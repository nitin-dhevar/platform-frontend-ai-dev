package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

const (
	maxScreenshotSize = 10 << 20 // 10 MB
	screenshotTag     = "screenshots"
	screenshotRelease = "Bot Screenshots"
)

type ghClient struct {
	token   string
	client  *http.Client
	baseURL string // override for tests; empty = api.github.com
}

func (c *ghClient) api(method, path string, body io.Reader, contentType string) (*http.Response, error) {
	var host string
	if c.baseURL != "" {
		host = c.baseURL
		path = strings.TrimPrefix(path, "/uploads")
	} else {
		host = "https://api.github.com"
		if strings.HasPrefix(path, "/uploads/") {
			host = "https://uploads.github.com"
			path = strings.TrimPrefix(path, "/uploads")
		}
	}

	req, err := http.NewRequest(method, host+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return c.client.Do(req)
}

func NewScreenshotUploader(ghToken string) http.Handler {
	gh := &ghClient{
		token:  ghToken,
		client: &http.Client{Timeout: 60 * time.Second},
	}
	return newScreenshotUploaderWithClient(gh, "")
}

func newScreenshotUploaderWithClient(gh *ghClient, baseURL string) http.Handler {
	if baseURL != "" {
		gh.baseURL = baseURL
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("POST /upload", screenshotUploadHandler(gh))
	return mux
}

func screenshotUploadHandler(gh *ghClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		repo := r.Header.Get("X-Repo")
		filename := r.Header.Get("X-Filename")
		if repo == "" || filename == "" {
			http.Error(w, `{"error":"X-Repo and X-Filename headers required"}`, http.StatusBadRequest)
			return
		}

		parts := strings.SplitN(repo, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			http.Error(w, `{"error":"X-Repo must be owner/repo"}`, http.StatusBadRequest)
			return
		}

		body := http.MaxBytesReader(w, r.Body, maxScreenshotSize)
		data, err := io.ReadAll(body)
		if err != nil {
			http.Error(w, `{"error":"file too large (max 10MB)"}`, http.StatusRequestEntityTooLarge)
			return
		}
		if len(data) == 0 {
			http.Error(w, `{"error":"empty body"}`, http.StatusBadRequest)
			return
		}

		contentType := r.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/octet-stream"
		}

		releaseID, err := ensureRelease(gh, repo)
		if err != nil {
			log.Printf("screenshot: ensure-release repo=%s err=%v", repo, err)
			http.Error(w, fmt.Sprintf(`{"error":"ensure release: %s"}`, err), http.StatusBadGateway)
			return
		}

		if err := deleteExistingAsset(gh, repo, releaseID, filename); err != nil {
			log.Printf("screenshot: delete-asset repo=%s file=%s err=%v", repo, filename, err)
		}

		assetURL, err := uploadAsset(gh, repo, releaseID, filename, contentType, data)
		if err != nil {
			log.Printf("screenshot: upload repo=%s file=%s err=%v", repo, filename, err)
			http.Error(w, fmt.Sprintf(`{"error":"upload: %s"}`, err), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"url": assetURL})

		log.Printf("screenshot: repo=%s file=%s size=%d dur=%s",
			repo, filename, len(data), time.Since(start).Round(time.Millisecond))
	}
}

func ensureRelease(gh *ghClient, repo string) (int64, error) {
	resp, err := gh.api("GET", fmt.Sprintf("/repos/%s/releases/tags/%s", repo, screenshotTag), nil, "")
	if err != nil {
		return 0, fmt.Errorf("get release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var rel struct {
			ID int64 `json:"id"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
			return 0, fmt.Errorf("decode release: %w", err)
		}
		return rel.ID, nil
	}

	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("get release: %d %s", resp.StatusCode, string(body)[:200])
	}

	createBody := fmt.Sprintf(`{"tag_name":%q,"name":%q,"body":"Asset store for bot screenshots","draft":false,"prerelease":false}`,
		screenshotTag, screenshotRelease)
	resp2, err := gh.api("POST", fmt.Sprintf("/repos/%s/releases", repo),
		strings.NewReader(createBody), "application/json")
	if err != nil {
		return 0, fmt.Errorf("create release: %w", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp2.Body)
		return 0, fmt.Errorf("create release: %d %s", resp2.StatusCode, string(body)[:200])
	}

	var rel struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&rel); err != nil {
		return 0, fmt.Errorf("decode created release: %w", err)
	}
	return rel.ID, nil
}

func deleteExistingAsset(gh *ghClient, repo string, releaseID int64, filename string) error {
	resp, err := gh.api("GET", fmt.Sprintf("/repos/%s/releases/%d/assets", repo, releaseID), nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var assets []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&assets); err != nil {
		return err
	}

	for _, a := range assets {
		if a.Name == filename {
			delResp, err := gh.api("DELETE", fmt.Sprintf("/repos/%s/releases/assets/%d", repo, a.ID), nil, "")
			if err != nil {
				return err
			}
			delResp.Body.Close()
			return nil
		}
	}
	return nil
}

func uploadAsset(gh *ghClient, repo string, releaseID int64, filename, contentType string, data []byte) (string, error) {
	path := fmt.Sprintf("/uploads/repos/%s/releases/%d/assets?name=%s", repo, releaseID, filename)
	resp, err := gh.api("POST", path, bytes.NewReader(data), contentType)
	if err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("upload: %d %s", resp.StatusCode, string(body)[:200])
	}

	downloadURL := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, screenshotTag, filename)
	return downloadURL, nil
}

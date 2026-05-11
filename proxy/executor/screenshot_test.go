package executor

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestScreenshotHealthz(t *testing.T) {
	handler := NewScreenshotUploader("test-token")
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("/healthz status = %d, want 200", w.Code)
	}
}

func TestScreenshotMissingRepo(t *testing.T) {
	handler := NewScreenshotUploader("test-token")
	req := httptest.NewRequest("POST", "/upload", strings.NewReader("png-data"))
	req.Header.Set("X-Filename", "test.png")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("missing X-Repo: status = %d, want 400", w.Code)
	}
}

func TestScreenshotMissingFilename(t *testing.T) {
	handler := NewScreenshotUploader("test-token")
	req := httptest.NewRequest("POST", "/upload", strings.NewReader("png-data"))
	req.Header.Set("X-Repo", "owner/repo")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("missing X-Filename: status = %d, want 400", w.Code)
	}
}

func TestScreenshotInvalidRepo(t *testing.T) {
	handler := NewScreenshotUploader("test-token")
	req := httptest.NewRequest("POST", "/upload", strings.NewReader("png-data"))
	req.Header.Set("X-Repo", "noslash")
	req.Header.Set("X-Filename", "test.png")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid repo: status = %d, want 400", w.Code)
	}
}

func TestScreenshotEmptyBody(t *testing.T) {
	handler := NewScreenshotUploader("test-token")
	req := httptest.NewRequest("POST", "/upload", strings.NewReader(""))
	req.Header.Set("X-Repo", "owner/repo")
	req.Header.Set("X-Filename", "test.png")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("empty body: status = %d, want 400", w.Code)
	}
}

func TestScreenshotUploadSuccess(t *testing.T) {
	var gotAuth string
	var gotUploadBody string
	var gotContentType string

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")

		switch {
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/releases/tags/"):
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"id": 42})

		case r.Method == "GET" && strings.Contains(r.URL.Path, "/assets"):
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode([]interface{}{})

		case r.Method == "POST" && strings.Contains(r.URL.Path, "/assets"):
			body, _ := io.ReadAll(r.Body)
			gotUploadBody = string(body)
			gotContentType = r.Header.Get("Content-Type")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{"id": 99})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mock.Close()

	gh := &ghClient{
		token:  "test-token-123",
		client: mock.Client(),
	}
	origAPI := gh.api
	_ = origAPI

	handler := newScreenshotUploaderWithClient(gh, mock.URL)

	req := httptest.NewRequest("POST", "/upload", strings.NewReader("fake-png-bytes"))
	req.Header.Set("X-Repo", "myorg/myrepo")
	req.Header.Set("X-Filename", "shot.png")
	req.Header.Set("Content-Type", "image/png")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("upload status = %d, want 200, body: %s", w.Code, w.Body.String())
	}

	var result map[string]string
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	wantURL := "https://github.com/myorg/myrepo/releases/download/screenshots/shot.png"
	if result["url"] != wantURL {
		t.Errorf("url = %q, want %q", result["url"], wantURL)
	}

	if gotAuth != "token test-token-123" {
		t.Errorf("auth = %q, want 'token test-token-123'", gotAuth)
	}

	if gotUploadBody != "fake-png-bytes" {
		t.Errorf("upload body = %q, want 'fake-png-bytes'", gotUploadBody)
	}

	if gotContentType != "image/png" {
		t.Errorf("upload content-type = %q, want 'image/png'", gotContentType)
	}
}

func TestScreenshotDeleteExistingAsset(t *testing.T) {
	var deleteCalled bool

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/releases/tags/"):
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"id": 42})

		case r.Method == "GET" && strings.Contains(r.URL.Path, "/assets"):
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode([]map[string]interface{}{
				{"id": 77, "name": "existing.png"},
			})

		case r.Method == "DELETE" && strings.Contains(r.URL.Path, "/assets/77"):
			deleteCalled = true
			w.WriteHeader(http.StatusNoContent)

		case r.Method == "POST" && strings.Contains(r.URL.Path, "/assets"):
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{"id": 99})

		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mock.Close()

	gh := &ghClient{token: "tok", client: mock.Client()}
	handler := newScreenshotUploaderWithClient(gh, mock.URL)

	req := httptest.NewRequest("POST", "/upload", strings.NewReader("new-data"))
	req.Header.Set("X-Repo", "org/repo")
	req.Header.Set("X-Filename", "existing.png")
	req.Header.Set("Content-Type", "image/png")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	if !deleteCalled {
		t.Error("expected DELETE call for existing asset")
	}
}

func TestScreenshotCreateRelease(t *testing.T) {
	var createCalled bool

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/releases/tags/"):
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)

		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/releases"):
			createCalled = true
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{"id": 55})

		case r.Method == "GET" && strings.Contains(r.URL.Path, "/assets"):
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode([]interface{}{})

		case r.Method == "POST" && strings.Contains(r.URL.Path, "/assets"):
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{"id": 99})

		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mock.Close()

	gh := &ghClient{token: "tok", client: mock.Client()}
	handler := newScreenshotUploaderWithClient(gh, mock.URL)

	req := httptest.NewRequest("POST", "/upload", strings.NewReader("data"))
	req.Header.Set("X-Repo", "org/repo")
	req.Header.Set("X-Filename", "new.png")
	req.Header.Set("Content-Type", "image/png")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	if !createCalled {
		t.Error("expected POST to create release")
	}
}

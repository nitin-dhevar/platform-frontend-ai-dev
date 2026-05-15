"""Shared path constants for skill scripts."""

from pathlib import Path

DATA_DIR = Path(__file__).resolve().parent.parent.parent / "data"
SLEEP_FILE = DATA_DIR / "cycle-sleep.json"

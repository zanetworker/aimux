package provider

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// newestFileModTime returns the modification time of the newest file matching
// the given glob pattern within dir. Returns zero time if no files match.
func newestFileModTime(dir, pattern string) time.Time {
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil || len(matches) == 0 {
		return time.Time{}
	}

	var newest time.Time
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil {
			continue
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest
}

// extractCodexCWD reads the first few lines of a Codex session JSONL file
// looking for a session_meta entry with a "cwd" field.
func extractCodexCWD(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Check the first 5 lines at most
	for i := 0; i < 5 && scanner.Scan(); i++ {
		var meta struct {
			Type string `json:"type"`
			CWD  string `json:"cwd"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &meta); err != nil {
			continue
		}
		if meta.CWD != "" {
			return meta.CWD
		}
	}
	return ""
}

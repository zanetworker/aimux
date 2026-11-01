package history

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/zanetworker/aimux/internal/jsonl"
)

// SessionCwd returns the first working directory a Claude session file
// records, reading only up to that line: continuing a session must not scan
// its whole transcript. It decodes each line as scanSession does (a
// sessionEntry), so it picks the same line, and the same Cwd the session
// list shows: a line that does not decode (not JSON, a bad timestamp) is
// skipped by both. A file that records no directory is an error.
func SessionCwd(path string) (string, error) {
	f, err := os.Open(path) // #nosec G304 -- a session file found under the projects dir
	if err != nil {
		return "", fmt.Errorf("open session file %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	s := jsonl.NewScanner(f)
	for s.Scan() {
		var e sessionEntry
		if json.Unmarshal(s.Bytes(), &e) == nil && e.CWD != "" {
			return e.CWD, nil
		}
	}
	if err := s.Err(); err != nil {
		return "", fmt.Errorf("read session file %s: %w", path, err)
	}
	return "", fmt.Errorf("session file %s records no working directory", path)
}

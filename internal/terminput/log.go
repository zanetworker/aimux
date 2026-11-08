package terminput

import (
	"io"
	"os"
	"path/filepath"
)

// OpenLog opens (creating it and its directory) the append-only attempt log
// with mode 0600.
func OpenLog(path string) (io.WriteCloser, error) {
	dir := filepath.Dir(path)
	_, statErr := os.Stat(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if os.IsNotExist(statErr) {
		_ = os.Chmod(dir, 0o700) //nolint:gosec // owner-only dir needs the x bit; umask may have loosened it; only for a directory we created
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- caller-chosen log path
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil { // an existing file keeps its old mode otherwise
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

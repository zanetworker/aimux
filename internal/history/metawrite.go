package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"
)

// UpdateMeta is the only writer of a session's sidecar .meta.json. It takes
// an exclusive lock, reads the file again, lets fn change the fields it owns,
// and atomically replaces the file, so concurrent writers (TUI, dashboard,
// remote serve, titler; in one process or several) never lose each other's
// changes. Keys this version of Meta does not know are kept as they are.
//
// The lock is a separate file next to the metadata (<id>.meta.json.lock):
// the metadata file itself is replaced by rename on every write, so a lock
// held on it would guard an inode that the next writer no longer opens.
// UpdatedAt is set on every write. A metadata file that is not valid JSON is
// an error and is left untouched. The file keeps its mode; a new one is 0600.
func UpdateMeta(sessionFile string, fn func(*Meta)) error {
	metaPath := MetaPath(sessionFile)
	unlock, err := lockMeta(MetaLockPath(sessionFile))
	if err != nil {
		return err
	}
	defer unlock()

	mode := fs.FileMode(0o600)
	raw := map[string]json.RawMessage{}
	var m Meta
	data, err := os.ReadFile(metaPath) // #nosec G304 G703 -- derived from a session file path
	switch {
	case err == nil:
		if info, statErr := os.Stat(metaPath); statErr == nil { // #nosec G703 -- derived from a session file path
			mode = info.Mode().Perm()
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("read session metadata %s (delete the file to reset): %w", metaPath, err)
		}
		if err := json.Unmarshal(data, &m); err != nil {
			return fmt.Errorf("read session metadata %s (delete the file to reset): %w", metaPath, err)
		}
		if raw == nil { // the file holds JSON null: no metadata
			raw = map[string]json.RawMessage{}
		}
	case errors.Is(err, fs.ErrNotExist):
	default:
		return fmt.Errorf("read session metadata %s: %w", metaPath, err)
	}

	fn(&m)
	m.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	known, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal session metadata %s: %w", metaPath, err)
	}
	var knownMap map[string]json.RawMessage
	if err := json.Unmarshal(known, &knownMap); err != nil {
		return fmt.Errorf("marshal session metadata %s: %w", metaPath, err)
	}
	for _, key := range metaKeys {
		delete(raw, key) // a field fn emptied is dropped (omitempty)
	}
	for k, v := range knownMap {
		raw[k] = v
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session metadata %s: %w", metaPath, err)
	}
	return replaceFile(metaPath, append(out, '\n'), mode)
}

// RemoveMeta deletes a session's metadata under the same lock UpdateMeta
// takes, then deletes the lock file last. Missing files are not an error.
func RemoveMeta(sessionFile string) error {
	lockPath := MetaLockPath(sessionFile)
	unlock, err := lockMeta(lockPath)
	if err != nil {
		return err
	}
	defer unlock()
	metaPath := MetaPath(sessionFile)
	if err := os.Remove(metaPath); err != nil && !errors.Is(err, fs.ErrNotExist) { // #nosec G703 -- derived from a session file path
		return fmt.Errorf("remove session metadata %s: %w", metaPath, err)
	}
	if err := os.Remove(lockPath); err != nil && !errors.Is(err, fs.ErrNotExist) { // #nosec G703 -- derived from a session file path
		return fmt.Errorf("remove session metadata lock %s: %w", lockPath, err)
	}
	return nil
}

// MetaLockPath returns the lock file UpdateMeta uses for a session file.
func MetaLockPath(sessionFile string) string {
	return MetaPath(sessionFile) + ".lock"
}

// metaKeys are the JSON names of Meta's fields.
var metaKeys = func() []string {
	t := reflect.TypeOf(Meta{})
	keys := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); name != "" && name != "-" {
			keys = append(keys, name)
		}
	}
	return keys
}()

// lockMeta takes an exclusive flock on path (created 0600 if missing) and
// returns the function that releases it.
func lockMeta(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 G703 -- derived from a session file path
	if err != nil {
		return nil, fmt.Errorf("open session metadata lock %s: %w", path, err)
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX) // #nosec G115 -- a file descriptor fits in an int
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock session metadata %s: %w", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) // #nosec G115 -- closing releases it anyway
		_ = f.Close()
	}, nil
}

// replaceFile writes data to a temp file in path's folder with mode, syncs
// it, and renames it over path.
func replaceFile(path string, data []byte, mode fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".meta.tmp.*")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	done := false
	defer func() {
		if !done {
			_ = tmp.Close()
			_ = os.Remove(tmpPath) // #nosec G703 -- our own temp file
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		return fmt.Errorf("set mode on temp file for %s: %w", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temp file for %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp file for %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file for %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil { // #nosec G703 -- path derived from a session file path
		return fmt.Errorf("replace %s: %w", path, err)
	}
	done = true
	return nil
}

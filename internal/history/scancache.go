package history

import (
	"encoding/gob"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// Parsing every session file on each Discover took ~11s for ~3,500 sessions.
// The parsed Session (before sidecar metadata, which is always read fresh) is
// cached per file, keyed by mtime and size, so only new or changed files are
// parsed, in parallel.

// scanCacheVersion must be bumped whenever scanSession's output changes, so
// stale cached parses are discarded.
const scanCacheVersion = 1

// scanSessionFn is swapped in tests to count parses.
var scanSessionFn = scanSession

// scanCacheFile returns the cache file for a projects dir, or "" for no
// cache. Only the default ~/.claude/projects is cached, so callers passing
// their own directory (tests, tools) never touch the user's cache.
var scanCacheFile = func(projectsDir string, isDefault bool) string {
	if !isDefault {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".aimux", "cache", "session-scan.gob")
}

type scanEntry struct {
	ModTime int64
	Size    int64
	Session Session
}

type scanCacheData struct {
	Version int
	Entries map[string]scanEntry // by session file path
}

func loadScanCache(path string) map[string]scanEntry {
	if path == "" {
		return nil
	}
	f, err := os.Open(path) // #nosec G304 -- path from scanCacheFile
	if err != nil {
		return map[string]scanEntry{}
	}
	defer func() { _ = f.Close() }()
	var d scanCacheData
	if gob.NewDecoder(f).Decode(&d) != nil || d.Version != scanCacheVersion || d.Entries == nil {
		return map[string]scanEntry{} // corrupt or outdated: rescan
	}
	return d.Entries
}

// saveScanCache writes atomically (temp file + rename) so a concurrent
// Discover never reads a half-written cache.
func saveScanCache(path string, entries map[string]scanEntry) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".session-scan-*")
	if err != nil {
		return err
	}
	if err := gob.NewEncoder(tmp).Encode(scanCacheData{Version: scanCacheVersion, Entries: entries}); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// scanJob is one session file Discover needs.
type scanJob struct {
	id, path, project string
	modTime, size     int64
}

// scanAll returns parsed sessions for jobs, from the cache when the file is
// unchanged and by parsing (in parallel) otherwise. It reports whether the
// cache was modified.
func scanAll(jobs []scanJob, cache map[string]scanEntry) ([]Session, bool) {
	out := make([]Session, len(jobs))
	ok := make([]bool, len(jobs))
	var todo []int
	for i, j := range jobs {
		if e, hit := cache[j.path]; hit && e.ModTime == j.modTime && e.Size == j.size {
			s := e.Session
			s.Project = j.project // path resolution can change; parsing can't
			out[i], ok[i] = s, true
			continue
		}
		todo = append(todo, i)
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())
	for _, i := range todo {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer func() { <-sem; wg.Done() }()
			j := jobs[i]
			if s, err := scanSessionFn(j.id, j.path, j.project); err == nil {
				out[i], ok[i] = s, true
			}
		}(i)
	}
	wg.Wait()

	changed := false
	for _, i := range todo {
		if ok[i] && cache != nil {
			cache[jobs[i].path] = scanEntry{ModTime: jobs[i].modTime, Size: jobs[i].size, Session: out[i]}
			changed = true
		}
	}
	var sessions []Session
	for i := range out {
		if ok[i] {
			sessions = append(sessions, out[i])
		}
	}
	return sessions, changed
}

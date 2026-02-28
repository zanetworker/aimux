package spawn

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// RecentDir represents a recently-used project directory.
type RecentDir struct {
	Path     string    // display key (dir-key for Claude, absolute path for Codex)
	LastUsed time.Time // mod time of the newest session file
	Provider string    // "claude", "codex", or "both"
}

// RecentDirs scans Claude and Codex session directories to find
// recently-used project directories, sorted by last activity.
// Returns at most maxResults entries.
//
// Uses default home-directory paths. For testable scanning, see
// ScanClaudeDirs and ScanCodexDirs.
func RecentDirs(maxResults int) []RecentDir {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	claudeDir := filepath.Join(home, ".claude", "projects")
	codexDir := filepath.Join(home, ".codex", "sessions")

	return recentDirs(maxResults, claudeDir, codexDir)
}

// recentDirs is the internal implementation that accepts explicit paths
// for testability.
func recentDirs(maxResults int, claudeDir, codexDir string) []RecentDir {
	byPath := make(map[string]*RecentDir)

	// Scan Claude projects
	for _, rd := range ScanClaudeDirs(claudeDir) {
		byPath[rd.Path] = &RecentDir{
			Path:     rd.Path,
			LastUsed: rd.LastUsed,
			Provider: "claude",
		}
	}

	// Scan Codex sessions
	for _, rd := range ScanCodexDirs(codexDir) {
		if existing, ok := byPath[rd.Path]; ok {
			existing.Provider = "both"
			if rd.LastUsed.After(existing.LastUsed) {
				existing.LastUsed = rd.LastUsed
			}
		} else {
			byPath[rd.Path] = &RecentDir{
				Path:     rd.Path,
				LastUsed: rd.LastUsed,
				Provider: "codex",
			}
		}
	}

	// Collect and sort by most recent first
	result := make([]RecentDir, 0, len(byPath))
	for _, rd := range byPath {
		result = append(result, *rd)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].LastUsed.After(result[j].LastUsed)
	})

	if maxResults > 0 && len(result) > maxResults {
		result = result[:maxResults]
	}
	return result
}

// ScanClaudeDirs scans the Claude projects directory for recently-used
// project directories. Each subdirectory of projectsDir is a dir-key
// (the encoded working directory path). The newest .jsonl file in each
// subdirectory determines LastUsed.
//
// The dir-key is used as-is for Path because decoding is lossy (hyphens
// could be original path separators or literal hyphens). The user
// recognizes the project from the key's basename pattern.
func ScanClaudeDirs(projectsDir string) []RecentDir {
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil
	}

	var dirs []RecentDir
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		subdir := filepath.Join(projectsDir, e.Name())
		newest := newestFileModTime(subdir, "*.jsonl")
		if newest.IsZero() {
			continue
		}
		dirs = append(dirs, RecentDir{
			Path:     e.Name(),
			LastUsed: newest,
			Provider: "claude",
		})
	}
	return dirs
}

// codexSessionMeta is the minimal structure for parsing the first line of
// a Codex session JSONL file, which contains the session metadata.
type codexSessionMeta struct {
	Type string `json:"type"`
	CWD  string `json:"cwd"`
}

// ScanCodexDirs scans the Codex sessions directory for recently-used
// project directories. Session files are organized as sessions/YYYY/MM/DD/*.jsonl.
// The first line of each file contains a session_meta entry with a "cwd" field.
//
// Only files modified within the last 30 days are considered.
func ScanCodexDirs(sessionsDir string) []RecentDir {
	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	byPath := make(map[string]*RecentDir)

	// Walk the sessions directory looking for .jsonl files
	_ = filepath.Walk(sessionsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip inaccessible paths
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".jsonl" {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			return nil
		}

		cwd := extractCodexCWD(path)
		if cwd == "" {
			return nil
		}

		if existing, ok := byPath[cwd]; ok {
			if info.ModTime().After(existing.LastUsed) {
				existing.LastUsed = info.ModTime()
			}
		} else {
			byPath[cwd] = &RecentDir{
				Path:     cwd,
				LastUsed: info.ModTime(),
				Provider: "codex",
			}
		}
		return nil
	})

	result := make([]RecentDir, 0, len(byPath))
	for _, rd := range byPath {
		result = append(result, *rd)
	}
	return result
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
		var meta codexSessionMeta
		if err := json.Unmarshal(scanner.Bytes(), &meta); err != nil {
			continue
		}
		if meta.CWD != "" {
			return meta.CWD
		}
	}
	return ""
}

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

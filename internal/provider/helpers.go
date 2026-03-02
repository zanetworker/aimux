package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

// getProcessPPID returns the parent PID for a given process, or 0 on error.
// It is a variable so tests can override it without calling external processes.
var getProcessPPID = getProcessPPIDImpl

func getProcessPPIDImpl(pid int) int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	ppid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0
	}
	return ppid
}

// findProcessRoots maps each PID to its root ancestor within the given PID set.
// Two processes sharing a root belong to the same session (process tree).
// A process whose parent is not in pidSet is its own root.
func findProcessRoots(pids []int) map[int]int {
	pidSet := make(map[int]bool, len(pids))
	for _, p := range pids {
		pidSet[p] = true
	}

	roots := make(map[int]int, len(pids))
	for _, pid := range pids {
		root := pid
		cur := pid
		seen := make(map[int]bool)
		for {
			seen[cur] = true
			ppid := getProcessPPID(cur)
			if ppid <= 0 || seen[ppid] {
				break
			}
			if pidSet[ppid] {
				root = ppid
				cur = ppid
			} else {
				break
			}
		}
		roots[pid] = root
	}
	return roots
}

// getProcessStartTime returns the start time for a process, or zero time on error.
// It is a variable so tests can override it.
var getProcessStartTime = getProcessStartTimeImpl

func getProcessStartTimeImpl(pid int) time.Time {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return time.Time{}
	}
	// Format: "Mon Mar  2 09:32:31 2026" (local time).
	raw := strings.TrimSpace(string(out))
	t, err := time.ParseInLocation("Mon Jan  2 15:04:05 2006", raw, time.Local)
	if err != nil {
		t, err = time.ParseInLocation("Mon Jan 2 15:04:05 2006", raw, time.Local)
		if err != nil {
			return time.Time{}
		}
	}
	return t.UTC()
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

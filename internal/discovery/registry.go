package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ClaudeRegistryEntry is one running Claude Code session as recorded by
// Claude Code itself in ~/.claude/sessions/<pid>.json. The format is not
// documented, so only the fields aimux needs are read and every field is
// treated as optional.
type ClaudeRegistryEntry struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	Status    string `json:"status"`    // "idle" or "busy" in observed files
	ProcStart string `json:"procStart"` // process start, UTC, ctime format
}

// StartedAt parses ProcStart. It reports false when the value is missing or
// in an unexpected format, so callers can skip the PID-reuse check.
func (e ClaudeRegistryEntry) StartedAt() (time.Time, bool) {
	t, err := time.ParseInLocation(time.ANSIC, e.ProcStart, time.UTC)
	return t, err == nil
}

// ReadClaudeRegistry returns registry entries keyed by PID. Unreadable or
// malformed files and entries without a PID or session ID are skipped, and a
// missing directory yields an empty map: the registry is an accuracy
// improvement, never a requirement.
func ReadClaudeRegistry(dir string) map[int]ClaudeRegistryEntry {
	reg, _ := readClaudeRegistry(dir)
	return reg
}

// readClaudeRegistry is the one registry read: every entry it can decode
// (never nil), and the first file, or the directory, it could not read or
// decode. ReadClaudeRegistry drops the error, ReadClaudeRegistryStrict the
// entries when there is one.
func readClaudeRegistry(dir string) (map[int]ClaudeRegistryEntry, error) {
	reg := make(map[int]ClaudeRegistryEntry)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return reg, fmt.Errorf("read Claude session registry %s: %w", dir, err)
	}
	var first error
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, de.Name())
		data, err := os.ReadFile(path) // #nosec G304 -- fixed registry dir
		if err != nil {
			if first == nil {
				first = fmt.Errorf("read Claude session registry entry %s: %w", path, err)
			}
			continue
		}
		var e ClaudeRegistryEntry
		if err := json.Unmarshal(data, &e); err != nil {
			if first == nil {
				first = fmt.Errorf("decode Claude session registry entry %s: %w", path, err)
			}
			continue
		}
		if e.PID > 0 && e.SessionID != "" {
			reg[e.PID] = e
		}
	}
	return reg, first
}

// ProcessStartTime returns when a process started, or the zero time if
// unknown. It is a variable so tests can override it. Only IsLiveHolder uses
// it, and nothing outside tests calls that: both are kept as the pinned
// reference for LiveCache's rule. LiveCache checks start times with one
// batched ps of its own, so overriding this hook does not reach it.
var ProcessStartTime = ReadProcessStartTime

// ReadProcessStartTime asks ps for pid's start time (UTC), or the zero time on
// any failure. The one `ps -o lstart=` reader; provider uses it too.
func ReadProcessStartTime(pid int) time.Time {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output() // #nosec G204 -- pid is an int
	if err != nil {
		return time.Time{}
	}
	raw := strings.Join(strings.Fields(string(out)), " ") // ps pads single-digit days
	t, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", raw, time.Local)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// IsLiveHolder reports whether the registry entry's process is still the one
// that wrote it: the PID is alive and, when both start times are known, it
// started when the entry says. A stale entry whose PID was reused by an
// unrelated process does not count. No production caller: it is kept as the
// pinned reference for the rule LiveCache applies to every entry at once
// (liveHolders; TestLiveHolders_SameRuleAsIsLiveHolder).
func IsLiveHolder(e ClaudeRegistryEntry) bool {
	if e.PID <= 0 || syscall.Kill(e.PID, 0) != nil {
		return false
	}
	recorded, ok := e.StartedAt()
	if !ok {
		return true
	}
	return startMatches(recorded, ProcessStartTime(e.PID))
}

// startMatches reports whether a process that started at actual can be the
// one recorded as starting at recorded: within 2 s, or actual unknown (zero),
// since an unknown start time must not hide a live holder.
func startMatches(recorded, actual time.Time) bool {
	if actual.IsZero() {
		return true
	}
	d := actual.Sub(recorded)
	return d <= 2*time.Second && d >= -2*time.Second
}

// ReadClaudeRegistryStrict is ReadClaudeRegistry for decisions that must not
// guess: any registry file it cannot read or decode is an error, since that
// file may be the live terminal holding a session.
func ReadClaudeRegistryStrict(dir string) (map[int]ClaudeRegistryEntry, error) {
	reg, err := readClaudeRegistry(dir)
	if err != nil {
		return nil, err
	}
	return reg, nil
}

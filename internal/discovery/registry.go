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
	reg := make(map[int]ClaudeRegistryEntry)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return reg
	}
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, de.Name())) // #nosec G304 -- fixed registry dir
		if err != nil {
			continue
		}
		var e ClaudeRegistryEntry
		if json.Unmarshal(data, &e) != nil || e.PID <= 0 || e.SessionID == "" {
			continue
		}
		reg[e.PID] = e
	}
	return reg
}

// ProcessStartTime returns when a process started, or the zero time if
// unknown. It is a variable so tests can override it.
var ProcessStartTime = processStartTimeImpl

func processStartTimeImpl(pid int) time.Time {
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
// unrelated process does not count.
func IsLiveHolder(e ClaudeRegistryEntry) bool {
	if e.PID <= 0 || syscall.Kill(e.PID, 0) != nil {
		return false
	}
	recorded, ok := e.StartedAt()
	if !ok {
		return true
	}
	actual := ProcessStartTime(e.PID)
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
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read Claude session registry %s: %w", dir, err)
	}
	reg := make(map[int]ClaudeRegistryEntry)
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, de.Name())
		data, err := os.ReadFile(path) // #nosec G304 -- fixed registry dir
		if err != nil {
			return nil, fmt.Errorf("read Claude session registry entry %s: %w", path, err)
		}
		var e ClaudeRegistryEntry
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, fmt.Errorf("decode Claude session registry entry %s: %w", path, err)
		}
		if e.PID > 0 && e.SessionID != "" {
			reg[e.PID] = e
		}
	}
	return reg, nil
}

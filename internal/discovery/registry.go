package discovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

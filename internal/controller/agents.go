package controller

import (
	"fmt"
	"os"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/history"
)

// FilterHidden removes agents whose session key is in the hidden set.
// The key is derived from SessionID, SessionFile, or PID (in that priority).
func FilterHidden(agents []agent.Agent, hidden map[string]bool) []agent.Agent {
	if len(hidden) == 0 {
		return agents
	}
	var result []agent.Agent
	for _, ag := range agents {
		key := ag.SessionID
		if key == "" && ag.SessionFile != "" {
			key = ag.SessionFile
		}
		if key == "" {
			key = fmt.Sprintf("pid-%d", ag.PID)
		}
		if !hidden[key] {
			result = append(result, ag)
		}
	}
	return result
}

// DeleteSession removes a session's JSONL file and its sidecar .meta.json.
func DeleteSession(s history.Session) error {
	if err := os.Remove(s.FilePath); err != nil {
		return fmt.Errorf("delete session file: %w", err)
	}
	metaPath := history.MetaPath(s.FilePath)
	os.Remove(metaPath) // ignore error — may not exist
	return nil
}

// BulkDeleteSessions removes multiple sessions, returning the count of
// successfully deleted sessions and the first error encountered.
func BulkDeleteSessions(sessions []history.Session) (int, error) {
	deleted := 0
	var firstErr error
	for _, s := range sessions {
		if err := DeleteSession(s); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		deleted++
	}
	return deleted, firstErr
}

package waiting

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

// The hook events and fields below are Claude Code's, as documented at
// https://code.claude.com/docs/en/hooks (formerly
// https://docs.claude.com/en/docs/claude-code/hooks). Every event's stdin
// JSON carries session_id, transcript_path and hook_event_name (and
// agent_id inside a subagent); Notification adds message, an optional title
// and notification_type, and its matcher filters on that type. A
// permission_prompt notification fires once the prompt has waited about six
// seconds, so a prompt answered at the Mac right away sends nothing.
// PermissionRequest fires before the dialog shows (and also in sessions that
// cannot show one), which would push for prompts the owner is already
// answering, so it is not used.
const (
	// SetEvent marks a session as waiting, with SetMatcher.
	SetEvent   = "Notification"
	SetMatcher = "permission_prompt"
)

// ClearEvents end the wait: a tool ran (allowed) or failed, the owner sent a
// prompt, the turn ended, or the session closed. A tool event from a
// subagent does not clear the main thread's prompt. A prompt denied with Esc
// fires none of these (an interrupt has no Stop); the server drops that
// marker once the session's transcript is written after the prompt
// (Tracker.Current).
var ClearEvents = []string{"PostToolUse", "PostToolUseFailure", "UserPromptSubmit", "Stop", "SessionEnd"}

type hookInput struct {
	SessionID        string `json:"session_id"`
	TranscriptPath   string `json:"transcript_path"`
	AgentID          string `json:"agent_id"`
	HookEventName    string `json:"hook_event_name"`
	Message          string `json:"message"`
	NotificationType string `json:"notification_type"`
}

// HandleHook applies one hook call, read from in, to the markers in dir.
// Events it does not know change nothing.
func HandleHook(dir string, in io.Reader, now time.Time) error {
	var h hookInput
	if err := json.NewDecoder(io.LimitReader(in, 1<<20)).Decode(&h); err != nil {
		return fmt.Errorf("read the hook input: %w", err)
	}
	if h.SessionID == "" {
		return errors.New("the hook input has no session_id")
	}
	switch {
	case h.HookEventName == SetEvent && isPermission(h):
		return writeMarker(dir, marker{SessionID: h.SessionID, TranscriptPath: h.TranscriptPath, AgentID: h.AgentID,
			Waiting: Waiting{Kind: KindPermission, Message: h.Message, Since: now}})
	case slices.Contains(ClearEvents, h.HookEventName):
		if h.AgentID != "" && isToolEvent(h.HookEventName) {
			if m, ok := readMarker(dir, h.SessionID); ok && m.AgentID == "" {
				return nil // a subagent's tool ran; the main thread still waits
			}
		}
		return Remove(dir, h.SessionID)
	}
	if !safeID.MatchString(h.SessionID) {
		return errors.New("not a session id")
	}
	return nil
}

func isToolEvent(event string) bool { return event == "PostToolUse" || event == "PostToolUseFailure" }

// isPermission: the notification type says so, or, from a Claude Code that
// sends no type, the message names permission.
func isPermission(h hookInput) bool {
	if h.NotificationType != "" {
		return h.NotificationType == SetMatcher
	}
	return strings.Contains(strings.ToLower(h.Message), "permission")
}

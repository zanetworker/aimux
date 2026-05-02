package statusdetect

import (
	"encoding/json"
	"io"
	"os"
	"strings"

	"github.com/zanetworker/aimux/internal/agent"
)

// jsonlEntry is a minimal representation of a JSONL session log entry.
type jsonlEntry struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	Content   json.RawMessage `json:"content"`
	Message   *messageEntry   `json:"message"`
	Operation string          `json:"operation"`
}

type messageEntry struct {
	StopReason string        `json:"stop_reason"`
	Content    []contentItem `json:"content"`
}

type contentItem struct {
	Type string `json:"type"`
}

// DetectFromJSONL reads the tail of a JSONL session file and returns
// the agent's status based on Claude Code's event model.
//
// tailBytes controls how many bytes from the end of the file to read
// (default recommendation: 8192). A value <= 0 reads the entire file.
func DetectFromJSONL(sessionFile string, tailBytes int64) agent.Status {
	f, err := os.Open(sessionFile)
	if err != nil {
		return agent.StatusUnknown
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return agent.StatusUnknown
	}
	fileSize := info.Size()
	if fileSize == 0 {
		return agent.StatusUnknown
	}

	// Determine seek offset.
	seeked := false
	if tailBytes > 0 && fileSize > tailBytes {
		if _, err := f.Seek(fileSize-tailBytes, io.SeekStart); err != nil {
			return agent.StatusUnknown
		}
		seeked = true
	}

	data, err := io.ReadAll(f)
	if err != nil {
		return agent.StatusUnknown
	}

	lines := strings.Split(string(data), "\n")

	// If we seeked past the start, the first line is likely partial — skip it.
	if seeked && len(lines) > 1 {
		lines = lines[1:]
	}

	// Parse all valid JSONL entries.
	var entries []jsonlEntry
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry jsonlEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}

	if len(entries) == 0 {
		return agent.StatusUnknown
	}

	// Walk backwards through entries to determine status.
	last := entries[len(entries)-1]

	// Check for system error.
	if last.Type == "system" {
		if last.Subtype == "error" {
			return agent.StatusError
		}
		// Check content for error keywords.
		if len(last.Content) > 0 {
			var contentStr string
			if err := json.Unmarshal(last.Content, &contentStr); err == nil {
				if strings.Contains(contentStr, "context_window_exceeded") ||
					strings.Contains(contentStr, "overloaded_error") {
					return agent.StatusError
				}
			}
		}
	}

	// Check for queue-operation enqueue.
	if last.Type == "queue-operation" && last.Operation == "enqueue" {
		return agent.StatusActive
	}

	// Check for assistant messages.
	if last.Type == "assistant" && last.Message != nil {
		if last.Message.StopReason == "end_turn" {
			return agent.StatusIdle
		}
		if last.Message.StopReason == "tool_use" {
			// Check if there's a following tool_result — there shouldn't be
			// since this is the last entry, so it's waiting for permission.
			return agent.StatusWaitingPermission
		}
	}

	// Check for user message with tool_result.
	if last.Type == "user" && last.Message != nil {
		for _, item := range last.Message.Content {
			if item.Type == "tool_result" {
				return agent.StatusActive
			}
		}
	}

	return agent.StatusIdle
}

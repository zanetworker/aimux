package otel

import (
	"encoding/json"
	"strings"
)

// ParseSessionReplies extracts assistant reply text from a Claude Code session
// JSONL file, keyed by promptId. Each assistant message's text blocks are
// concatenated with newlines. Non-text blocks (thinking, tool_use) are skipped.
//
// This is the primary source for model reply text in aimux's trace pane:
// Claude Code's OTEL telemetry does not emit assistant responses, but the
// session JSONL file (written to disk inside the sandbox) contains the full
// conversation transcript.
func ParseSessionReplies(data []byte) map[string]string {
	replies := make(map[string]string)
	if len(data) == 0 {
		return replies
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var entry sessionEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry.Type != "assistant" || entry.PromptID == "" {
			continue
		}
		if entry.Message.Role != "assistant" {
			continue
		}

		var texts []string
		for _, block := range entry.Message.Content {
			if block.Type == "text" && block.Text != "" {
				texts = append(texts, block.Text)
			}
		}
		if len(texts) > 0 {
			replies[entry.PromptID] = strings.Join(texts, "\n")
		}
	}

	return replies
}

type sessionEntry struct {
	Type     string         `json:"type"`
	PromptID string         `json:"promptId"`
	Message  sessionMessage `json:"message"`
}

type sessionMessage struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

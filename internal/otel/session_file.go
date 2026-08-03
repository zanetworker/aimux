package otel

import (
	"encoding/json"
	"strings"
)

// ParseSessionReplies extracts assistant reply text from a Claude Code session
// JSONL file, keyed by the promptId of the preceding user message. Claude Code
// puts promptId on user entries but NOT on assistant entries, so we track the
// last-seen user promptId and assign the next assistant's text to it.
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

	var lastUserPromptID string

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var entry sessionEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		if entry.Type == "user" && entry.PromptID != "" {
			lastUserPromptID = entry.PromptID
			continue
		}

		if entry.Type == "assistant" && lastUserPromptID != "" {
			if text := parseAssistantText(entry.Message); text != "" {
				replies[lastUserPromptID] = text
				lastUserPromptID = ""
			}
		}
	}

	return replies
}

type sessionEntry struct {
	Type     string          `json:"type"`
	PromptID string          `json:"promptId"`
	Message  json.RawMessage `json:"message"`
}

type sessionMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// parseAssistantText extracts text from an assistant message's content blocks.
func parseAssistantText(raw json.RawMessage) string {
	var msg sessionMessage
	if err := json.Unmarshal(raw, &msg); err != nil || msg.Role != "assistant" {
		return ""
	}
	var blocks []contentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return ""
	}
	var texts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			texts = append(texts, b.Text)
		}
	}
	return strings.Join(texts, "\n")
}

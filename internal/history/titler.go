package history

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// TitleConfig controls LLM-based session title generation.
type TitleConfig struct {
	Enabled bool   // generate titles automatically
	Model   string // "haiku" (default), "sonnet", "opus"
	APIKey  string // Anthropic API key (from env or config)
}

// DefaultTitleConfig returns sensible defaults for title generation.
func DefaultTitleConfig() TitleConfig {
	return TitleConfig{
		Enabled: false,
		Model:   "haiku",
	}
}

// resolveModel maps short model names to full Anthropic model IDs.
func resolveModel(short string) string {
	switch short {
	case "haiku":
		return "claude-haiku-4-5-20251001"
	case "sonnet":
		return "claude-sonnet-4-6-20250527"
	case "opus":
		return "claude-opus-4-6-20250527"
	default:
		// Allow full model ID passthrough
		return short
	}
}

// resolveAPIKey returns the API key from config or environment.
func resolveAPIKey(cfg TitleConfig) string {
	if cfg.APIKey != "" {
		return cfg.APIKey
	}
	return os.Getenv("ANTHROPIC_API_KEY")
}

// extractConversationSummary reads the first few turns of a session JSONL
// and builds a condensed conversation string for title generation.
// Captures up to 3 user messages and 2 assistant responses, keeping
// total text under ~800 chars to minimize token usage.
func extractConversationSummary(filePath string) string {
	if filePath == "" {
		return ""
	}

	f, err := os.Open(filePath)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	var parts []string
	userCount, assistantCount := 0, 0
	totalLen := 0

	for scanner.Scan() {
		if totalLen > 800 || (userCount >= 3 && assistantCount >= 2) {
			break
		}

		var entry struct {
			Message *struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil || entry.Message == nil {
			continue
		}

		role := entry.Message.Role
		if role != "user" && role != "assistant" {
			continue
		}

		text := extractTextFromContent(entry.Message.Content)
		if text == "" {
			continue
		}

		// Truncate individual messages
		if len(text) > 200 {
			text = text[:200] + "..."
		}

		if role == "user" {
			userCount++
			parts = append(parts, "User: "+text)
		} else {
			assistantCount++
			parts = append(parts, "Assistant: "+text)
		}
		totalLen += len(text)
	}

	return strings.Join(parts, "\n")
}

// extractTextFromContent pulls plain text from a message content field.
func extractTextFromContent(content json.RawMessage) string {
	if content == nil {
		return ""
	}
	// Try as array of blocks
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &blocks); err == nil {
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				return strings.TrimSpace(b.Text)
			}
		}
	}
	// Try as string
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		return strings.TrimSpace(text)
	}
	return ""
}

// GenerateTitle calls the Anthropic API to generate a concise session title
// from the first few turns of a session conversation. Returns the title
// string or an error. The title is 3-8 words summarizing the session topic.
func GenerateTitle(session Session, cfg TitleConfig) (string, error) {
	apiKey := resolveAPIKey(cfg)
	if apiKey == "" {
		return "", fmt.Errorf("no API key: set ANTHROPIC_API_KEY or configure sessions.api_key")
	}

	// Build a conversation summary from the session file
	conversationSummary := extractConversationSummary(session.FilePath)
	if conversationSummary == "" {
		if session.FirstPrompt != "" && session.FirstPrompt != "(no prompt)" {
			conversationSummary = "User: " + session.FirstPrompt
		} else {
			return "", fmt.Errorf("no conversation content to summarize")
		}
	}

	model := resolveModel(cfg.Model)

	reqBody := map[string]interface{}{
		"model":      model,
		"max_tokens": 30,
		"messages": []map[string]interface{}{
			{
				"role": "user",
				"content": fmt.Sprintf(
					"Generate a concise 3-8 word title for this AI assistant session. "+
						"Focus on what the user wanted to accomplish. "+
						"Return ONLY the title, no quotes, no punctuation at the end.\n\n"+
						"Conversation:\n%s", conversationSummary),
			},
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("API request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	if len(result.Content) == 0 || result.Content[0].Text == "" {
		return "", fmt.Errorf("empty response from API")
	}

	return result.Content[0].Text, nil
}

// GenerateTitles generates titles for all sessions that don't have one yet.
// Returns the number of titles generated and any error encountered.
// Stops on first API error to avoid burning through quota on failures.
func GenerateTitles(sessions []Session, cfg TitleConfig) (int, error) {
	count := 0
	for _, s := range sessions {
		if s.Title != "" {
			continue // already has a title
		}
		if s.FirstPrompt == "" || s.FirstPrompt == "(no prompt)" {
			continue
		}

		title, err := GenerateTitle(s, cfg)
		if err != nil {
			return count, fmt.Errorf("session %s: %w", s.ID, err)
		}

		// Save to meta
		meta := LoadMeta(s.FilePath)
		meta.Title = title
		if err := SaveMeta(s.FilePath, meta); err != nil {
			return count, fmt.Errorf("save meta for %s: %w", s.ID, err)
		}
		count++
	}
	return count, nil
}

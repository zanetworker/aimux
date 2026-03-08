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
	Enabled    bool   // generate titles automatically
	Model      string // "flash" (default), "haiku", "sonnet", "opus"
	APIKey     string // API key (from env or config)
	Regenerate bool   // regenerate titles even if they already exist
}

// DefaultTitleConfig returns sensible defaults for title generation.
func DefaultTitleConfig() TitleConfig {
	return TitleConfig{
		Enabled: false,
		Model:   "flash",
	}
}

// isGeminiModel returns true if the model name refers to a Gemini model.
func isGeminiModel(model string) bool {
	switch model {
	case "flash", "gemini-flash", "gemini-3-flash-preview", "gemini-3.1-flash-lite-preview":
		return true
	}
	return strings.HasPrefix(model, "gemini")
}

// resolveModel maps short model names to full model IDs.
func resolveModel(short string) string {
	switch short {
	case "flash", "gemini-flash":
		return "gemini-3.1-flash-lite-preview"
	case "haiku":
		return "claude-haiku-4-5-20251001"
	case "sonnet":
		return "claude-sonnet-4-6-20250527"
	case "opus":
		return "claude-opus-4-6-20250527"
	default:
		return short
	}
}

// resolveAPIKey returns the appropriate API key for the model.
func resolveAPIKey(cfg TitleConfig) string {
	if cfg.APIKey != "" {
		return cfg.APIKey
	}
	if isGeminiModel(cfg.Model) {
		if key := os.Getenv("GEMINI_API_KEY"); key != "" {
			return key
		}
		return os.Getenv("GOOGLE_API_KEY")
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

		// Strip XML tags and noise from the text
		text = stripXMLTags(text)
		text = strings.TrimSpace(text)
		if text == "" || len(text) < 5 {
			continue
		}

		// Truncate individual messages
		if len(text) > 200 {
			text = text[:200]
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

// stripXMLTags removes XML-like tags from text.
func stripXMLTags(text string) string {
	for strings.Contains(text, "<") && strings.Contains(text, ">") {
		start := strings.Index(text, "<")
		end := strings.Index(text[start:], ">")
		if end < 0 {
			break
		}
		text = text[:start] + text[start+end+1:]
	}
	return text
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

	prompt := fmt.Sprintf(
		"Your task: write a single descriptive title (5-10 words) summarizing this coding session.\n\n"+
			"Rules:\n"+
			"- Must be a COMPLETE phrase, never cut off mid-sentence\n"+
			"- Must describe the SPECIFIC task, not generic words\n"+
			"- BAD: 'Researching', 'Create', 'Optimize', 'Add dist directory to'\n"+
			"- GOOD: 'Add dist directory to gitignore', 'Fix markdown rendering in trace view', 'Research Claude session storage and access patterns'\n\n"+
			"Conversation:\n%s\n\n"+
			"Title:", conversationSummary)

	if isGeminiModel(cfg.Model) {
		return callGemini(prompt, model, apiKey)
	}
	return callAnthropic(prompt, model, apiKey)
}

func callGemini(prompt, model, apiKey string) (string, error) {
	reqBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]interface{}{
					{"text": prompt},
				},
			},
		},
		"generationConfig": map[string]interface{}{
			"maxOutputTokens": 256,
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent", model)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
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
		return "", fmt.Errorf("Gemini API error %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	if len(result.Candidates) == 0 || len(result.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("empty response from Gemini")
	}

	title := strings.TrimSpace(result.Candidates[0].Content.Parts[0].Text)
	// Take only the first line (Gemini sometimes adds explanation)
	if idx := strings.IndexAny(title, "\n\r"); idx > 0 {
		title = title[:idx]
	}
	// Strip quotes if wrapped
	title = strings.Trim(title, "\"'")
	return title, nil
}

func callAnthropic(prompt, model, apiKey string) (string, error) {
	reqBody := map[string]interface{}{
		"model":      model,
		"max_tokens": 30,
		"messages": []map[string]interface{}{
			{"role": "user", "content": prompt},
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

	client := &http.Client{Timeout: 30 * time.Second}
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
		return "", fmt.Errorf("Anthropic API error %d: %s", resp.StatusCode, string(respBody))
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
		return "", fmt.Errorf("empty response from Anthropic")
	}

	return strings.TrimSpace(result.Content[0].Text), nil
}

// GenerateTitles generates titles for all sessions that don't have one yet.
// Returns the number of titles generated and any error encountered.
// Stops on first API error to avoid burning through quota on failures.
func GenerateTitles(sessions []Session, cfg TitleConfig) (int, error) {
	total := 0
	for _, s := range sessions {
		if (s.Title == "" || cfg.Regenerate) && s.FirstPrompt != "" && s.FirstPrompt != "(no prompt)" {
			total++
		}
	}

	count := 0
	for _, s := range sessions {
		if s.Title != "" && !cfg.Regenerate {
			continue // already has a title
		}
		if s.FirstPrompt == "" || s.FirstPrompt == "(no prompt)" {
			continue
		}

		prompt := s.FirstPrompt
		if len(prompt) > 40 {
			prompt = prompt[:37] + "..."
		}
		fmt.Fprintf(os.Stderr, "  [%d/%d] %s %s... ", count+1, total, s.ID[:8], prompt)

		title, err := GenerateTitle(s, cfg)
		if err != nil {
			// Skip sessions that fail (safety filter, empty content, etc.)
			// but stop on auth/network errors
			errStr := err.Error()
			if strings.Contains(errStr, "API key") {
				fmt.Fprintln(os.Stderr, "FAILED (auth)")
				return count, fmt.Errorf("session %s: %w", s.ID, err)
			}
			fmt.Fprintf(os.Stderr, "skipped (%v)\n", err)
			continue
		}

		// Save to meta
		meta := LoadMeta(s.FilePath)
		meta.Title = title
		if err := SaveMeta(s.FilePath, meta); err != nil {
			return count, fmt.Errorf("save meta for %s: %w", s.ID, err)
		}
		count++
		fmt.Fprintf(os.Stderr, "→ %q\n", title)
	}
	return count, nil
}

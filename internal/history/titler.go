package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/cost"
	"github.com/zanetworker/aimux/internal/jsonl"
	"github.com/zanetworker/aimux/internal/llm"
)

// anthropicMessagesURL is the Messages API endpoint (a test points it at a fake).
var anthropicMessagesURL = llm.MessagesURL

// recordNamingUsage records the titler's calls under "naming" (a test
// replaces it).
var recordNamingUsage = func(u cost.Usage) error { return cost.AppendUsage(cost.DefaultUsagePath(), u) }

// titleTimeout bounds one title call.
const titleTimeout = 30 * time.Second

// errNoTitleKey is the one error that stops GenerateTitles: without an API
// key every session would fail the same way.
var errNoTitleKey = errors.New("no API key: set ANTHROPIC_API_KEY or configure sessions.api_key in config")

// TitleConfig controls LLM-based session title generation.
type TitleConfig struct {
	Enabled    bool      // generate titles automatically
	Model      string    // "haiku" (default), "sonnet", "opus"
	APIKey     string    // API key (from env or config)
	Regenerate bool      // regenerate titles even if they already exist
	Output     io.Writer // progress output destination (nil defaults to os.Stderr)
}

// DefaultTitleConfig returns sensible defaults for title generation.
func DefaultTitleConfig() TitleConfig {
	return TitleConfig{
		Enabled: false,
		Model:   "haiku",
	}
}

// extractConversationSummary reads the first few turns of a session JSONL
// and builds a condensed conversation string for title generation.
// Captures up to 3 user messages and 2 assistant responses, keeping
// total text under ~800 chars to minimize token usage.
func extractConversationSummary(filePath string) string {
	if filePath == "" {
		return ""
	}

	f, err := os.Open(filePath) // #nosec G304
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	scanner := jsonl.NewScanner(f)

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
	apiKey := llm.APIKey(cfg.APIKey)
	if apiKey == "" {
		return "", errNoTitleKey
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

	model, _ := ResolveModel(cfg.Model)

	prompt := fmt.Sprintf(
		"Your task: write a single descriptive title (5-10 words) summarizing this coding session.\n\n"+
			"Rules:\n"+
			"- Must be a COMPLETE phrase, never cut off mid-sentence\n"+
			"- Must describe the SPECIFIC task, not generic words\n"+
			"- BAD: 'Researching', 'Create', 'Optimize', 'Add dist directory to'\n"+
			"- GOOD: 'Add dist directory to gitignore', 'Fix markdown rendering in trace view', 'Research Claude session storage and access patterns'\n\n"+
			"Conversation:\n%s\n\n"+
			"Title:", conversationSummary)

	return callAnthropic(prompt, model, apiKey)
}

// titleClient is the naming engine as the titler uses it: the Messages API
// only (no claude -p fallback), titleTimeout per call.
func titleClient(apiKey string) *llm.Client {
	return namingClient(llm.NewAPI(apiKey, anthropicMessagesURL, titleTimeout), nil, recordNamingUsage, nil, log.Printf)
}

// callAnthropic asks for one title through the naming engine (namingClient):
// the Messages API only (the titler has no claude -p fallback), max_tokens
// 30, no system prompt, usage recorded under "naming".
func callAnthropic(prompt, model, apiKey string) (string, error) {
	reply, err := titleClient(apiKey).Complete(context.Background(), llm.Request{Model: model, Prompt: prompt, MaxTokens: 30})
	if err != nil {
		return "", err
	}
	if reply.Text == "" {
		return "", fmt.Errorf("empty response from Anthropic")
	}
	return strings.TrimSpace(reply.Text), nil
}

// GenerateTitles generates titles for all sessions that don't have one yet.
// Returns the number of titles generated and any error encountered.
// Stops on first API error to avoid burning through quota on failures.
func GenerateTitles(sessions []Session, cfg TitleConfig) (int, error) {
	out := cfg.Output
	if out == nil {
		out = os.Stderr
	}
	if _, known := ResolveModel(cfg.Model); !known {
		_, _ = fmt.Fprintf(out, "  unknown title model %q, using %s\n", cfg.Model, DefaultModel)
	}

	var todo []Session
	for _, s := range sessions {
		if (s.Title != "" && !cfg.Regenerate) || s.FilePath == "" {
			continue // already titled, or no session file to read
		}
		if LoadMeta(s.FilePath).TitleBy == "owner" {
			continue // the owner's title is never replaced
		}
		todo = append(todo, s)
	}
	total := len(todo)

	count := 0
	for _, s := range todo {
		prompt := s.FirstPrompt
		if prompt == "" || prompt == "(no prompt)" {
			prompt = "(from conversation)"
		}
		if len(prompt) > 40 {
			prompt = prompt[:37] + "..."
		}
		_, _ = fmt.Fprintf(out, "  [%d/%d] %s %s... ", count+1, total, s.ID[:8], prompt)

		title, err := GenerateTitle(s, cfg)
		if err != nil {
			// Skip sessions that fail (safety filter, empty content, etc.)
			// but stop on auth/network errors
			if errors.Is(err, errNoTitleKey) {
				_, _ = fmt.Fprintln(out, "FAILED (auth)")
				return count, fmt.Errorf("session %s: %w", s.ID, err)
			}
			_, _ = fmt.Fprintf(out, "skipped (%v)\n", err)
			continue
		}

		// Save to meta
		if err := UpdateMeta(s.FilePath, func(m *Meta) {
			if m.TitleBy != "owner" { // the owner may have named it during the call
				m.Title, m.TitleBy = title, "model"
			}
		}); err != nil {
			return count, fmt.Errorf("save meta for %s: %w", s.ID, err)
		}
		count++
		_, _ = fmt.Fprintf(out, "→ %q\n", title)
	}
	return count, nil
}

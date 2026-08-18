package insight

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type Config struct {
	Model  string // "haiku" (default), "sonnet", "opus"
	APIKey string // optional override; falls back to env vars
}

func resolveModel(short string) string {
	switch short {
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

func resolveAPIKey(cfg Config) string {
	if cfg.APIKey != "" {
		return cfg.APIKey
	}
	return os.Getenv("ANTHROPIC_API_KEY")
}

// Generate sends a prompt to the configured LLM and returns the response text.
func Generate(cfg Config, prompt string) (string, error) {
	apiKey := resolveAPIKey(cfg)
	if apiKey == "" {
		return "", fmt.Errorf("no API key configured")
	}
	model := resolveModel(cfg.Model)
	return callAnthropic(prompt, model, apiKey)
}

func callAnthropic(prompt, model, apiKey string) (string, error) {
	reqBody := map[string]interface{}{
		"model":      model,
		"max_tokens": 2048,
		"messages":   []map[string]interface{}{{"role": "user", "content": prompt}},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("anthropic API %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	if len(result.Content) == 0 {
		return "", fmt.Errorf("empty response")
	}
	return strings.TrimSpace(result.Content[0].Text), nil
}

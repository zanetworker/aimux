package insight

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/zanetworker/aimux/internal/cost"
	"github.com/zanetworker/aimux/internal/llm"
)

// messagesURL is the Messages API endpoint (a test points it at a fake).
var messagesURL = llm.MessagesURL

type Config struct {
	Model       string                 // "haiku" (default), "sonnet", "opus" (llm.ResolveModel; unknown names give haiku)
	APIKey      string                 // optional override; falls back to env vars
	RecordUsage func(cost.Usage) error // default: cost.AppendUsage to cost.DefaultUsagePath()
}

// logf logs (a test replaces it); warnedModels holds the configured model
// names already logged as unknown, so each is logged once.
var (
	logf         = log.Printf
	warnedModels sync.Map
)

// newClient is insight's model client: the Messages API only, 30 s per call,
// usage under "insight".
func newClient(cfg Config, apiKey string) *llm.Client {
	return &llm.Client{Primary: llm.NewAPI(apiKey, messagesURL, 30*time.Second), Label: "insight", Record: cfg.RecordUsage, Logf: logf}
}

// Generate sends a prompt to the configured model through the shared client
// (Messages API only, 30 s, max_tokens 2048) and returns the response text.
// Each successful call's usage is recorded under "insight".
func Generate(cfg Config, prompt string) (string, error) {
	apiKey := llm.APIKey(cfg.APIKey)
	if apiKey == "" {
		return "", fmt.Errorf("no API key configured")
	}
	model, known := llm.ResolveModel(cfg.Model) // the one model table; empty is haiku
	if !known {
		if _, seen := warnedModels.LoadOrStore(cfg.Model, true); !seen {
			logf("insight: unknown model %q, using %s", cfg.Model, model)
		}
	}
	reply, err := newClient(cfg, apiKey).Complete(context.Background(), llm.Request{Model: model, Prompt: prompt, MaxTokens: 2048})
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(reply.Text)
	if text == "" {
		return "", fmt.Errorf("empty response")
	}
	return text, nil
}

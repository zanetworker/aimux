// Package llm is aimux's one model client: the Anthropic Messages API when
// an API key is set, else `claude -p` in safe mode with the owner's Claude
// Code login; the short model names config accepts; and a usage line in
// ~/.aimux/data/usage.jsonl for every call, labelled by the caller
// ("naming", "insight"). API keys are never logged, and errors carry no
// prompt or reply text.
package llm

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/cost"
)

// MessagesURL is the Anthropic Messages API endpoint.
const MessagesURL = "https://api.anthropic.com/v1/messages"

// DefaultModel is the model "haiku" and an empty name mean.
const DefaultModel = "claude-haiku-5-5"

// modelIDs maps the short names config accepts to full model ids.
var modelIDs = map[string]string{
	"haiku":  DefaultModel,
	"sonnet": "claude-sonnet-5-5",
	"opus":   "claude-opus-5-5",
}

// ResolveModel maps a configured model name ("haiku", "sonnet", "opus", or
// one of their full ids; empty means haiku) to a full model id. Any other
// name gives DefaultModel and known=false, so the caller can log it once.
func ResolveModel(name string) (id string, known bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return DefaultModel, true
	}
	if id, ok := modelIDs[name]; ok {
		return id, true
	}
	for _, id := range modelIDs {
		if name == id {
			return id, true
		}
	}
	return DefaultModel, false
}

// APIKey is the key to use: override (sessions.api_key) when set, else
// ANTHROPIC_API_KEY. Returns the key; never log it.
func APIKey(override string) string {
	if override != "" {
		return override
	}
	return os.Getenv("ANTHROPIC_API_KEY")
}

// Request is one model call: an optional system prompt, a user prompt and an
// output cap. Model is a full id (or "haiku" for claude -p).
type Request struct {
	Model     string
	System    string
	Prompt    string
	MaxTokens int
}

// Reply is a model's answer with the tokens it cost.
type Reply struct {
	Text         string
	Model        string // the model the backend reports, "" when it does not
	InputTokens  int64
	OutputTokens int64
	CostUSD      *float64 // when the backend reports it; else from pricing, or unknown
	Estimated    bool     // tokens guessed from text length
}

// Backend makes one model call. Errors carry no prompt or reply text.
type Backend interface {
	Complete(ctx context.Context, req Request) (Reply, error)
}

var (
	// ErrModelNotFound is the API's answer for a model id it does not know.
	ErrModelNotFound = errors.New("model not found")
	ErrUnauthorized  = errors.New("API key rejected")
	ErrBadReply      = errors.New("unreadable model reply")
	ErrNoBackend     = errors.New("no API key and no claude CLI")
	ErrModelCall     = errors.New("model call failed")
	ErrTimeout       = errors.New("timed out")
)

// Backends picks how a caller reaches a model: the Messages API at url when
// an API key is set (apiKey, else ANTHROPIC_API_KEY), with claude -p as the
// fallback for "model not found"; otherwise claude -p alone. Both time out
// after 60 s.
func Backends(apiKey, url string) (primary, fallback Backend) {
	apiKey = APIKey(apiKey)
	cli := &CLI{Bin: "claude", Timeout: 60 * time.Second}
	if apiKey == "" {
		return cli, nil
	}
	return NewAPI(apiKey, url, 60*time.Second), cli
}

// Client calls Primary, once more through Fallback with FallbackModel when
// the API does not know the model, and records each successful call's usage
// under Label.
type Client struct {
	Primary       Backend
	Fallback      Backend                          // optional
	FallbackModel string                           // the model Fallback is asked for (default "haiku")
	Label         string                           // the usage line's label, e.g. "naming", "insight"
	Record        func(cost.Usage) error           // default: cost.AppendUsage to cost.DefaultUsagePath()
	Now           func() time.Time                 // default: time.Now
	Logf          func(format string, args ...any) // default: log.Printf
}

// Complete makes the call and records its usage. A failed call records
// nothing; a failed record is logged, not returned.
func (c *Client) Complete(ctx context.Context, req Request) (Reply, error) {
	if c.Primary == nil {
		return Reply{}, ErrNoBackend
	}
	reply, err := c.Primary.Complete(ctx, req)
	if errors.Is(err, ErrModelNotFound) && c.Fallback != nil {
		fb := c.FallbackModel
		if fb == "" {
			fb = "haiku"
		}
		c.logf("%s: model %s not found, retrying once with the fallback, model %s", c.Label, req.Model, fb)
		req.Model = fb
		reply, err = c.Fallback.Complete(ctx, req)
	}
	if err != nil {
		return Reply{}, err
	}
	c.record(reply, req.Model)
	return reply, nil
}

func (c *Client) record(reply Reply, model string) {
	if reply.Model != "" {
		model = reply.Model
	}
	if id, known := ResolveModel(model); known {
		model = id // "haiku" from the CLI fallback is recorded as the model it means
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	u := cost.Usage{At: now().UTC(), Label: c.Label, Model: model, InputTokens: reply.InputTokens,
		OutputTokens: reply.OutputTokens, CostUSD: reply.CostUSD, Estimated: reply.Estimated}
	rec := c.Record
	if rec == nil {
		rec = func(u cost.Usage) error { return cost.AppendUsage(cost.DefaultUsagePath(), u) }
	}
	if err := rec(u); err != nil {
		c.logf("%s: %v", c.Label, err)
	}
}

func (c *Client) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

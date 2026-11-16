package decide

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/zanetworker/aimux/internal/keyring"
)

// DecisionsURL is the OpenRouter Decisions API endpoint.
const DecisionsURL = "https://openrouter.ai/api/alpha/decisions"

// OpenRouter is a Backend on the OpenRouter Decisions API: one choice
// question per call, answered with per-option probabilities and a
// confidence.
type OpenRouter struct {
	Key   func(ctx context.Context) (string, error) // read on first use, then kept until OpenRouter rejects it; never logged
	Model string
	ZDR   bool   // ask OpenRouter to route only to zero-data-retention providers
	URL   string // default DecisionsURL
	HTTP  *http.Client

	mu  sync.Mutex
	key string // the key last read, "" until read or after a rejection
}

var (
	_ Preparer = (*OpenRouter)(nil)
	_ Warmer   = (*OpenRouter)(nil)
)

// Prepare reads the key (Decide then makes no read), so its time is not
// counted against the decision call.
func (b *OpenRouter) Prepare(ctx context.Context) error {
	_, err := b.apiKey(ctx)
	return err
}

// Warm reads the key and opens the HTTPS connection with a HEAD request on
// the API's host root, which asks no question and costs nothing, so the
// first decision skips the key read and the TLS handshake. A missing key
// skips the HEAD (no decision could be made anyway).
func (b *OpenRouter) Warm(ctx context.Context) error {
	if err := b.Prepare(ctx); err != nil {
		return err
	}
	u, err := url.Parse(b.URL)
	if err != nil {
		return fmt.Errorf("openrouter warm: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u.Scheme+"://"+u.Host+"/", nil)
	if err != nil {
		return fmt.Errorf("openrouter warm: %w", err)
	}
	resp, err := b.client().Do(req)
	if err != nil {
		return fmt.Errorf("openrouter warm: %w", ErrCall)
	}
	_, _ = io.Copy(io.Discard, resp.Body) // drained, so the connection is kept for the first call
	return resp.Body.Close()
}

func (b *OpenRouter) client() *http.Client {
	if b.HTTP != nil {
		return b.HTTP
	}
	return http.DefaultClient
}

// apiKey is the kept key, else a fresh read (kept when found). It returns
// an error wrapping ErrNoKey when there is none, ErrKeyRead when the read
// failed, or the context's error.
func (b *OpenRouter) apiKey(ctx context.Context) (string, error) {
	b.mu.Lock()
	k := b.key
	b.mu.Unlock()
	if k != "" {
		return k, nil
	}
	if b.Key != nil {
		got, err := b.Key(ctx)
		switch {
		case err == nil:
			k = got
		case errors.Is(err, keyring.ErrNotFound):
		case ctx.Err() != nil:
			return "", fmt.Errorf("openrouter key: %w", ctx.Err())
		default:
			return "", fmt.Errorf("openrouter: %w (%w)", ErrKeyRead, err) // keyring errors never hold the key
		}
	}
	if k == "" {
		return "", fmt.Errorf("openrouter: %w", ErrNoKey)
	}
	b.mu.Lock()
	b.key = k
	b.mu.Unlock()
	return k, nil
}

// forgetKey drops the kept key after OpenRouter rejected it, so the next
// call reads the key source again.
func (b *OpenRouter) forgetKey(rejected string) {
	b.mu.Lock()
	if b.key == rejected {
		b.key = ""
	}
	b.mu.Unlock()
}

// NewOpenRouter is the Decisions API backend for model, reading the API key
// from key on each call (a keyring.Chain fits). zdr restricts routing to
// zero-data-retention providers.
func NewOpenRouter(key func(ctx context.Context) (string, error), model string, zdr bool) *OpenRouter {
	return &OpenRouter{Key: key, Model: model, ZDR: zdr, URL: DecisionsURL,
		HTTP: &http.Client{Timeout: 30 * time.Second}} // a backstop: callers bound calls with ctx
}

// Decide sends r as {model, state, provider: {zdr}, questions: {<name>:
// {type: "choice", instructions, criteria}}}, state being the JSON of
// r.State as a string, and reads answers.<name>.{choice, probabilities,
// confidence} and usage.cost. Without a key (nil key func, empty key, or an
// error wrapping keyring.ErrNotFound) it makes no call and returns an Answer
// undecided "no_key" with an error wrapping ErrNoKey; any other key failure
// gives "key_error" and ErrKeyRead. Errors keep only the HTTP status: the
// API's message may echo the state.
func (b *OpenRouter) Decide(ctx context.Context, r Request) (Answer, error) {
	base := Answer{Backend: "openrouter", Model: b.Model}
	key, err := b.apiKey(ctx)
	if err != nil {
		switch {
		case errors.Is(err, ErrNoKey):
			base.Undecided = UndecidedNoKey
		case errors.Is(err, ErrKeyRead):
			base.Undecided = UndecidedKeyError
		}
		return base, err
	}
	state, err := json.Marshal(r.State)
	if err != nil {
		return base, fmt.Errorf("openrouter: state is not JSON: %w", ErrCall)
	}
	payload := map[string]any{
		"model":    b.Model,
		"state":    string(state),
		"provider": map[string]bool{"zdr": b.ZDR},
		"questions": map[string]any{r.Question.Name: map[string]any{
			"type":         "choice",
			"instructions": r.Question.Instructions,
			"criteria":     r.Question.Options,
		}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return base, fmt.Errorf("openrouter request: %w", err)
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.URL, bytes.NewReader(body))
	if err != nil {
		return base, fmt.Errorf("openrouter request: %w", err)
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Authorization", "Bearer "+key)
	resp, err := b.client().Do(hreq)
	if err != nil {
		if ctx.Err() != nil {
			return base, fmt.Errorf("openrouter: %w", ctx.Err())
		}
		return base, fmt.Errorf("openrouter: request failed: %w", ErrCall) // the URL error is dropped: harmless, but keep errors uniform
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return base, fmt.Errorf("openrouter: read reply: %w", ErrCall)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		b.forgetKey(key)
	}
	if resp.StatusCode != http.StatusOK {
		return base, fmt.Errorf("openrouter status %d: %w", resp.StatusCode, ErrCall)
	}
	var out struct {
		Model   string `json:"model"`
		Answers map[string]struct {
			Choice        string             `json:"choice"`
			Probabilities map[string]float64 `json:"probabilities"`
			Confidence    float64            `json:"confidence"`
		} `json:"answers"`
		Usage struct {
			Cost         *float64 `json:"cost"`
			InputTokens  int64    `json:"input_tokens"`
			OutputTokens int64    `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return base, fmt.Errorf("openrouter reply (%d bytes): %w", len(data), ErrBadReply)
	}
	ans, ok := out.Answers[r.Question.Name]
	if !ok || (ans.Choice == "" && len(ans.Probabilities) == 0) {
		return base, fmt.Errorf("openrouter reply has no answer for %q: %w", r.Question.Name, ErrBadReply)
	}
	a := base
	if out.Model != "" {
		a.Model = out.Model
	}
	a.Choice, a.Probabilities, a.Confidence = ans.Choice, ans.Probabilities, ans.Confidence
	a.CostUSD = out.Usage.Cost
	a.inputTokens, a.outputTokens = out.Usage.InputTokens, out.Usage.OutputTokens
	return a, nil
}

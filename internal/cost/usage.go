package cost

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Usage is one model call aimux itself made (not an agent session's), kept
// so its cost is visible. Label names the feature, e.g. "naming".
type Usage struct {
	At           time.Time `json:"at"`
	Label        string    `json:"label"`
	Model        string    `json:"model"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	CostUSD      *float64  `json:"cost_usd,omitempty"`     // nil when the price is unknown
	CostUnknown  bool      `json:"cost_unknown,omitempty"` // the model is not in the pricing table: only tokens are known
	Estimated    bool      `json:"estimated,omitempty"`    // token counts guessed from text length
}

// DefaultUsagePath is where aimux's own model usage is recorded.
func DefaultUsagePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".aimux", "data", "usage.jsonl")
}

// AppendUsage adds u as one JSON line to path (created 0600, folders 0700).
// A zero At becomes now. A nil CostUSD is filled from the pricing table, or,
// for a model the table does not price, left out with CostUnknown set: an
// unknown cost is never recorded as 0.
func AppendUsage(path string, u Usage) error {
	if u.Label == "" {
		return errors.New("record usage: missing label")
	}
	if u.At.IsZero() {
		u.At = time.Now().UTC()
	}
	if u.CostUSD == nil {
		if Priced(u.Model) {
			c := Calculate(u.Model, u.InputTokens, u.OutputTokens, 0, 0)
			u.CostUSD = &c
		} else {
			u.CostUnknown = true
		}
	}
	line, err := json.Marshal(u)
	if err != nil {
		return fmt.Errorf("record usage: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("record usage in %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- aimux's own data file
	if err != nil {
		return fmt.Errorf("record usage in %s: %w", path, err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("record usage in %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("record usage in %s: %w", path, err)
	}
	return nil
}

// Priced reports whether the pricing table knows model (after aliases).
func Priced(model string) bool {
	_, ok := pricing[normalizeModel(model)]
	return ok
}

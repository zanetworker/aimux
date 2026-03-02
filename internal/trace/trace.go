// Package trace defines shared types for conversation trace parsing.
// These types are produced by each provider's ParseTrace method and
// consumed by the TUI views for rendering.
package trace

import (
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/cost"
)

// Turn groups a user prompt with the assistant response into one logical
// unit -- the fundamental trace element for evaluation (input -> actions -> output).
type Turn struct {
	Number      int
	Timestamp   time.Time
	EndTime     time.Time // timestamp of last entry in this turn (for duration)
	UserLines   []string  // full user input text
	Actions     []ToolSpan
	OutputLines []string
	TokensIn    int64
	TokensOut   int64
	CostUSD     float64 // calculated from tokens + model
	Model       string  // model used for this turn
}

// Duration returns the wall-clock duration of this turn.
func (t Turn) Duration() time.Duration {
	if t.EndTime.IsZero() || t.Timestamp.IsZero() {
		return 0
	}
	d := t.EndTime.Sub(t.Timestamp)
	if d < 0 {
		return 0
	}
	return d
}

// ErrorCount returns the number of failed tool calls in this turn.
func (t Turn) ErrorCount() int {
	n := 0
	for _, a := range t.Actions {
		if !a.Success {
			n++
		}
	}
	return n
}

// ToolSpan represents a single tool call within a turn.
type ToolSpan struct {
	Name      string
	Snippet   string // short description of the input
	Success   bool   // true if tool succeeded
	ErrorMsg  string // error message if failed
	OldString string // for Edit: the old text
	NewString string // for Edit: the new text
	ToolUseID string // for matching tool_result entries
}

// EstimateTurnCost calculates the estimated cost for a turn based on
// model name and token counts, delegating to the cost package.
func EstimateTurnCost(model string, tokIn, tokOut int64) float64 {
	return cost.Calculate(model, tokIn, tokOut, 0, 0)
}

// EstimateTurnCostLegacy uses the original heuristic-based cost estimation
// from the view layer. It applies rough per-million-token rates based on
// model name substrings. Use EstimateTurnCost for more accurate pricing.
func EstimateTurnCostLegacy(model string, tokIn, tokOut int64) float64 {
	var inRate, outRate float64
	switch {
	case strings.Contains(model, "opus"):
		inRate, outRate = 15.0, 75.0
	case strings.Contains(model, "sonnet"):
		inRate, outRate = 3.0, 15.0
	case strings.Contains(model, "haiku"):
		inRate, outRate = 0.25, 1.25
	default:
		inRate, outRate = 3.0, 15.0
	}
	return (float64(tokIn)*inRate + float64(tokOut)*outRate) / 1_000_000
}

// Package controller provides UI-agnostic business logic for aimux.
// It orchestrates exports, session management, and agent coordination
// without depending on any TUI framework. Both the Bubble Tea TUI and
// a future web UI import this package.
package controller

import (
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/evaluation"
)

// Controller owns the business logic and state that is shared across
// all UI frontends. It does NOT import bubbletea or lipgloss.
type Controller struct {
	cfg config.Config
}

// New creates a new Controller with the given configuration.
func New(cfg config.Config) *Controller {
	return &Controller{cfg: cfg}
}

// ExportContext holds the runtime context needed for an export operation.
// The UI layer populates this from its active state (which trace is open,
// which session file is loaded, etc.).
type ExportContext struct {
	SessionID    string
	SessionFile  string // path to session JSONL (for loading .meta.json)
	ProviderName string
	Turns        []TraceInput
	EvalStore    *evaluation.Store
}

// TraceInput is the subset of trace.Turn fields needed for export,
// avoiding a direct dependency on the trace package's full type
// (which includes rendering-specific fields).
type TraceInput struct {
	Number     int
	Timestamp  string // RFC3339
	UserText   string
	OutputText string
	Actions    []ActionInput
	TokensIn   int64
	TokensOut  int64
	CostUSD    float64
	DurationMs int64
	Model      string
}

// ActionInput is a tool invocation within a trace turn.
type ActionInput struct {
	Tool    string
	Input   string
	Success bool
	Error   string
}

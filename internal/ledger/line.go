// Package ledger holds the ledger's line type and the rule a reader uses to
// count each action once. See docs/ledger-identity.md.
package ledger

import "time"

// Kind is what a ledger line records.
type Kind string

const (
	KindModelCall Kind = "model_call"
	KindToolCall  Kind = "tool_call"
	KindVerdict   Kind = "verdict"
	KindSwitch    Kind = "switch"
)

// Schema is the version of the line format. A reader skips a line of another schema and says how many it skipped;
// a writer refuses a file whose last line has one it does not know.
const Schema = "aimux.ledger/1"

// SemConv is the version of OpenTelemetry's semantic conventions the gen_ai.* field names follow: the first one
// that has gen_ai.usage.input_tokens and gen_ai.usage.output_tokens (1.26.0 still called them prompt_tokens and
// completion_tokens).
const SemConv = "1.27.0"

// OutcomeOK is the outcome of an action that succeeded. Every other outcome is a word from its writer's fixed
// list, never free text.
const OutcomeOK = "ok"

// Line is one ledger line: one JSON object per line in a writer's file. It says how an action went, never what
// it said: no prompt, no answer, no tool argument, no path.
type Line struct {
	Schema    string    `json:"schema"`
	SemConv   string    `json:"semconv,omitempty"`
	At        time.Time `json:"at"`
	Kind      Kind      `json:"kind"`
	Writer    string    `json:"writer"`             // "model-proxy", "mcp-proxy", "tool-gateway", "collector"
	Seq       uint64    `json:"seq"`                // per writer, only goes up
	EventID   string    `json:"event_id,omitempty"` // hook events: call id + hook event name
	Run       string    `json:"run,omitempty"`
	Session   string    `json:"session,omitempty"`
	ToSession string    `json:"to_session,omitempty"` // a switch that started a new session: the new one
	Turn      string    `json:"turn,omitempty"`
	Call      string    `json:"call,omitempty"`
	Secondary bool      `json:"secondary,omitempty"` // a cross-check, not the record

	Harness string `json:"aimux.harness,omitempty"` // "claude-code", "codex"
	Side    bool   `json:"aimux.side,omitempty"`    // off the session's main conversation: a subagent or background call
	Agent   string `json:"aimux.agent,omitempty"`   // the subagent's id, as the harness names it

	// A model call.
	RequestModel  string `json:"gen_ai.request.model,omitempty"`  // the model the client named
	ResponseModel string `json:"gen_ai.response.model,omitempty"` // the model that answered
	Engine        string `json:"aimux.engine,omitempty"`
	Effort        string `json:"aimux.effort,omitempty"`
	// InputTokens is the whole prompt, as OpenTelemetry defines the name: what was read from a cache is part of
	// it, and is also given on its own in CacheRead.
	InputTokens  int64 `json:"gen_ai.usage.input_tokens,omitempty"`
	OutputTokens int64 `json:"gen_ai.usage.output_tokens,omitempty"`
	CacheRead    int64 `json:"aimux.usage.cache_read_input_tokens,omitempty"`
	CacheWrite   int64 `json:"aimux.usage.cache_write_input_tokens,omitempty"`
	Estimated    bool  `json:"aimux.usage.estimated,omitempty"`
	// UsageUnknown: the answer carried no token figures, so the zeros above are not a measurement.
	UsageUnknown bool   `json:"aimux.usage.unknown,omitempty"`
	TTFBMillis   int64  `json:"aimux.ttfb_ms,omitempty"` // -1: no byte was sent
	DurationMs   int64  `json:"aimux.duration_ms,omitempty"`
	Outcome      string `json:"aimux.outcome,omitempty"`

	// A switch: the session's main conversation was answered by another engine than the time before.
	FromEngine string `json:"aimux.from_engine,omitempty"`
	ToEngine   string `json:"aimux.to_engine,omitempty"`
}

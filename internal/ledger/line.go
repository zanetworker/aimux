// Package ledger holds the ledger's line type and the rule a reader uses to
// count each action once. See docs/ledger-identity.md.
package ledger

import (
	"strings"
	"time"
)

// Kind is what a ledger line records.
type Kind string

const (
	KindModelCall Kind = "model_call"
	KindToolCall  Kind = "tool_call"
	KindVerdict   Kind = "verdict"
	KindSwitch    Kind = "switch"
	// KindSession says who and where a session is: its harness, where it runs, what started it. KindAgent says
	// the same of one agent of a session: its parent and its kind. Neither is a call. A fact that changes is
	// written again, with its time, and a reader takes the one in force at the time of a call.
	KindSession Kind = "session"
	KindAgent   Kind = "agent"
)

// AgentRoot is the agent id of a session's main conversation, said outright so that it is never confused with a
// call whose agent is not known, which carries none.
const AgentRoot = "root"

// How a line was learned. Seen: aimux carried the call itself. Reported: the harness said so. Verified: a
// supervisor outside the agent recorded it.
const (
	LearnedSeen     = "seen"
	LearnedReported = "reported"
	LearnedVerified = "verified"
)

// Where a session runs.
const (
	PlaceHost    = "host"
	PlaceSandbox = "sandbox"
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

// OutcomeError is the outcome of a tool call whose result the harness marked as failed. OutcomeNotReturned is a
// tool call the model asked for whose result never came back: asked, result unknown.
const (
	OutcomeError       = "error"
	OutcomeNotReturned = "not returned"
)

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

	// Learned says how the line was learned: LearnedSeen, LearnedReported or LearnedVerified. A line from before
	// this was recorded has none.
	Learned string `json:"aimux.learned,omitempty"`

	Harness string `json:"aimux.harness,omitempty"` // "claude-code", "codex"
	// A session line: its harness's version, where it runs (PlaceHost, PlaceSandbox, written only on evidence),
	// the sandbox's id, the session that started it when something says so, and, for a sandboxed session, the
	// sandbox's own id for a request (reserved).
	HarnessVersion string `json:"aimux.harness_version,omitempty"`
	Place          string `json:"aimux.place,omitempty"`
	Sandbox        string `json:"aimux.sandbox,omitempty"`
	PlaceRequest   string `json:"aimux.place_request,omitempty"`
	StartedBy      string `json:"aimux.started_by,omitempty"`
	// Role is the kind of work a model call was: "main" (the session's main conversation), "subagent" or
	// "background". A line written before roles were recorded has none.
	Role  string `json:"aimux.role,omitempty"`
	Side  bool   `json:"aimux.side,omitempty"`  // off the session's main conversation: a subagent or background call
	Agent string `json:"aimux.agent,omitempty"` // the subagent's id, as the harness names it; AgentRoot for the main conversation
	// An agent line: the agent that started this one, and its kind ("general-purpose", "code-reviewer").
	AgentParent string `json:"aimux.agent_parent,omitempty"`
	AgentType   string `json:"aimux.agent_type,omitempty"`
	// PromptID is the harness's own id for the turn, kept apart from Turn, which is the proxy's ordinal.
	PromptID string `json:"aimux.prompt_id,omitempty"`

	// A tool call: the tool, the MCP server it belongs to, the model call that asked for it (that call's Call),
	// and, for a failure, its class from a fixed list, never its text.
	ToolName   string `json:"gen_ai.tool.name,omitempty"`
	ToolServer string `json:"aimux.tool.server,omitempty"`
	AskedBy    string `json:"aimux.asked_by,omitempty"`
	ErrorType  string `json:"aimux.error_type,omitempty"`

	// A model call.
	RequestModel  string `json:"gen_ai.request.model,omitempty"`  // the model the client named
	ResponseModel string `json:"gen_ai.response.model,omitempty"` // the model that answered
	Engine        string `json:"aimux.engine,omitempty"`
	Effort        string `json:"aimux.effort,omitempty"`
	// Request is the upstream's own id for the request, the one id a harness's telemetry also carries.
	Request string `json:"aimux.request,omitempty"`
	// Reasoned says whether the answer held reasoning. Nil when the engine's answer does not show it: never
	// false on a guess.
	Reasoned *bool `json:"aimux.reasoned,omitempty"`
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
	// Status is the HTTP status the session was given for a call that failed: 429 for a rate limit, 401 for a
	// refused login. Absent for a call that succeeded, and for one that ended before any answer.
	Status int `json:"http.response.status_code,omitempty"`
	// Limit is the kind of limit a failed model call met: "short" (a rate limit of the moment, which the harness
	// retries) or "plan" (the plan's allowance is used up until its window starts again). Absent otherwise.
	Limit string `json:"aimux.limit,omitempty"`
	// OnLimit is the proxy's report for a call that met a plan limit, when it was told to report: "would:<engine>"
	// for a fallback that would have gone there, "would_not:<reason>" otherwise. Nothing was done either way.
	OnLimit string `json:"aimux.on_limit,omitempty"`

	// A switch: the session's main conversation was answered by another engine than the time before.
	FromEngine string `json:"aimux.from_engine,omitempty"`
	ToEngine   string `json:"aimux.to_engine,omitempty"`
}

// Name is s when it looks like a name (at most 128 characters of ASCII letters, digits, '-', '_', '.' and ':'),
// "other" when it does not, and empty when s is empty. A tool, a server or a kind of agent is written through it,
// so that nothing which is not a name reaches a line.
func Name(s string) string {
	if s == "" {
		return ""
	}
	if len(s) > 128 {
		return "other"
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == ':':
		default:
			return "other"
		}
	}
	return s
}

// SplitTool splits a tool's name as a harness gives it into its MCP server and the tool: "mcp__slack__search" is
// ("slack", "search"). A name that is not of that form has no server and is kept whole. Each part goes through
// Name. The first separator after the prefix ends the server's name, so a server whose own name holds a double
// underscore would be cut short; harnesses do not name servers so.
func SplitTool(name string) (server, tool string) {
	const prefix, sep = "mcp__", "__"
	rest, isMCP := strings.CutPrefix(name, prefix)
	if isMCP {
		if s, t, ok := strings.Cut(rest, sep); ok && s != "" && t != "" {
			return Name(s), Name(t)
		}
	}
	return "", Name(name)
}

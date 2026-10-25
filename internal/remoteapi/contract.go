package remoteapi

import (
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// The /v1 wire contract. api/remote-v1.openapi.yaml describes the same
// shapes for clients; the contract test checks live responses and events
// against it, so the two cannot drift apart.

// Capabilities a server may report in Health; clients use them instead of
// probing endpoints.
const (
	CapSessions    = "sessions"     // list, detail, send, events, cancel, approvals
	CapSearch      = "search"       // GET /v1/search
	CapNewSessions = "new_sessions" // GET /v1/projects, POST /v1/sessions
	CapPush        = "push"         // POST /v1/devices, DELETE /v1/devices/{token}: approval notifications over APNs
)

// Health is GET /v1/health: who the caller is and what this server offers.
type Health struct {
	Status  string `json:"status"`
	Login   string `json:"login"`
	Version string `json:"version"`
	// ACPProtocolVersion is the Agent Client Protocol version that `update`
	// events follow: their data is an ACP SessionUpdate, passed through.
	ACPProtocolVersion int      `json:"acpProtocolVersion"`
	Capabilities       []string `json:"capabilities"`
}

// SessionList is GET /v1/sessions.
type SessionList struct {
	Sessions []Session `json:"sessions"`
}

// Turn is one exchange in a session's history: the user's message, the
// reply text, and the tools it used.
type Turn struct {
	Number int        `json:"number"`
	Time   time.Time  `json:"time"`
	User   string     `json:"user"`
	Reply  string     `json:"reply"`
	Tools  []TurnTool `json:"tools"`
}

// TurnTool is a tool call in a turn: its name, a short target (a path, a
// command) and whether it succeeded.
type TurnTool struct {
	Name    string `json:"name"`
	Snippet string `json:"snippet,omitempty"`
	OK      bool   `json:"ok"`
}

// SearchResults is GET /v1/search.
type SearchResults struct {
	Results []SearchHit `json:"results"`
}

// ProjectList is GET /v1/projects.
type ProjectList struct {
	Projects []Project `json:"projects"`
}

// TurnAccepted answers POST /v1/sessions and POST /v1/sessions/{id}/messages:
// the session, the new turn, and where to follow it (Events is the events
// URL with ?after= set just before the turn).
type TurnAccepted struct {
	ID     string `json:"id"`
	Turn   string `json:"turn"`
	After  int64  `json:"after"`
	Events string `json:"events"`
}

// Status answers cancel and approval requests.
type Status struct {
	Status string `json:"status"`
}

// EventPage is GET /v1/sessions/{id}/events?wait=0: the events after the
// cursor and the log they belong to (pass it back as ?log=).
type EventPage struct {
	Events []Event `json:"events"`
	Log    string  `json:"log"`
}

// ErrorBody is every error response.
type ErrorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// Event data, by Event.Kind. `update` events carry an ACP SessionUpdate as
// is (see Health.ACPProtocolVersion).

// TurnStartedData is the data of a turn_started event.
type TurnStartedData struct {
	Text string `json:"text"`
}

// TurnDoneData is the data of a turn_done event; StopReason is ACP's
// (end_turn, cancelled, max_tokens, ...).
type TurnDoneData struct {
	StopReason string `json:"stopReason"`
}

// TurnErrorData is the data of a turn_error event.
type TurnErrorData struct {
	Error string `json:"error"`
}

// ApprovalData is the data of an approval event: answer it with one of
// Options' optionId at POST /v1/approvals/{approvalId}.
type ApprovalData struct {
	ApprovalID string                 `json:"approvalId"`
	Title      string                 `json:"title"`
	Options    []acp.PermissionOption `json:"options"`
	ToolCall   acp.ToolCallUpdate     `json:"toolCall"`
}

// Approval outcomes in ApprovalResolvedData.
const (
	OutcomeAllowed = "allowed"
	OutcomeDenied  = "denied"
	OutcomeExpired = "expired"
)

// ApprovalResolvedData is the data of an approval_resolved event.
type ApprovalResolvedData struct {
	ApprovalID string `json:"approvalId"`
	Outcome    string `json:"outcome"`
}

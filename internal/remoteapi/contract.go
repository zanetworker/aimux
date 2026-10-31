package remoteapi

import (
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/zanetworker/aimux/internal/terminput"
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
	// CapTerminalInput: POST /v1/sessions/{id}/terminal/messages and
	// /terminal/keys type into sessions open in a terminal on the Mac.
	CapTerminalInput = "terminal_input"
	// CapDeviceLogin: every route but health, the contract, /app and
	// /v1/auth/{pair,challenge,login} needs a paired device's session.
	CapDeviceLogin = "device_login"
	// CapQueue: /v1/sessions/{id}/queue holds messages the Mac sends one
	// turn at a time; SessionDetail.Queue and queue events (events?queue=1).
	CapQueue = "queue"
	// CapDescribe: PATCH /v1/sessions/{id}/meta (title, description, pin)
	// and POST /v1/sessions/{id}/describe ("Update summary"); Health.Naming.
	CapDescribe = "describe"
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
	// Links is present only when remote_access.links.jira_base is set.
	Links *Links `json:"links,omitempty"`
	// Session is present only with device login: "live" when the request
	// carries a live session, else "none". Reading it never counts as use.
	Session string `json:"session,omitempty"`
	// Naming is present when the server makes titles and descriptions.
	Naming *NamingHealth `json:"naming,omitempty"`
}

// NamingHealth is Health.Naming: today's automatic naming and its state.
type NamingHealth struct {
	Today   int    `json:"today"`            // sessions named automatically today
	Limit   int    `json:"limit"`            // sessions.naming_daily_limit
	Failing bool   `json:"failing"`          // the last calls failed
	Reason  string `json:"reason,omitempty"` // a fixed reason, while failing
	Paused  bool   `json:"paused,omitempty"` // automatic naming waits after repeated failures
}

// LastOnMac is SessionList.LastOnMac: the session last in front on the Mac
// and when. Clients hide it after 12 hours.
type LastOnMac struct {
	ID string    `json:"id"`
	At time.Time `json:"at"`
}

// MetaResult answers PATCH /v1/sessions/{id}/meta: what clients now show.
type MetaResult struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Pinned      bool   `json:"pinned"`
}

// DescribeResult answers POST /v1/sessions/{id}/describe: what clients now
// show, and which fields the model changed or left (owner-set, or nothing
// usable came back).
type DescribeResult struct {
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Changed     []string `json:"changed"`
	Kept        []string `json:"kept"`
}

// Links is Health.Links: where clients link Jira keys in a reply.
type Links struct {
	JiraBase     string   `json:"jiraBase"`
	JiraProjects []string `json:"jiraProjects"`
}

// Repo is SessionDetail.Repo: the session's git repository on the web, so
// clients can link commits, issues and files in a reply.
type Repo struct {
	Web    string `json:"web"`    // https address, no ".git", no credentials
	Branch string `json:"branch"` // checked out in the session's folder
	// Root is the repository's top folder on the Mac; clients make absolute
	// paths in a reply relative to it.
	Root string `json:"root"`
}

// SessionList is GET /v1/sessions.
type SessionList struct {
	Sessions []Session `json:"sessions"`
	// LastOnMac is absent when unknown, or when that session is gone.
	LastOnMac *LastOnMac `json:"lastOnMac,omitempty"`
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
	// Detail is the step's full text; omitted when there is none.
	Detail *ToolDetail `json:"detail,omitempty"`
}

// ToolDetail is what a client shows when a step is opened: the full input,
// the error and, for an edit or a write, the changed text. Capped, see
// maxDetailInput and friends.
type ToolDetail struct {
	Input string `json:"input,omitempty"`
	Error string `json:"error,omitempty"`
	Old   string `json:"old,omitempty"`
	New   string `json:"new,omitempty"`
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

// TerminalStatus is SessionDetail.Terminal: what a session open in a terminal
// on the Mac is doing and whether the phone may type into it.
type TerminalStatus struct {
	Status  string `json:"status"` // busy, idle or unknown, from Claude's registry
	CanType bool   `json:"canType"`
	// Working: Claude Code is working on a turn; a message is refused with
	// busy (CanType leaves it out), so clients offer the queue.
	Working bool            `json:"working"`
	Reason  string          `json:"reason,omitempty"` // why CanType is false
	Prompt  *TerminalPrompt `json:"prompt,omitempty"`
	// BoxHasText: the input box holds text (a draft, or Claude Code's grey
	// suggestion); a message with replace: true clears it first.
	BoxHasText bool `json:"boxHasText,omitempty"`
	// WorkingSeconds: while working, how long Claude Code's working line says
	// the turn has run; absent when it shows no time.
	WorkingSeconds int `json:"workingSeconds,omitempty"`
}

// QueueStatus is a session's chat queue (GET /v1/sessions/{id}/queue,
// SessionDetail.Queue, and the data of a queue event). Reason is set while
// paused; Note is the dispatcher's waiting line while it runs.
type QueueStatus struct {
	State  string      `json:"state"` // running or paused
	Reason string      `json:"reason,omitempty"`
	Note   string      `json:"note,omitempty"`
	Items  []QueueItem `json:"items"`
}

// QueueItem is one queued message. Its idempotency key stays on the Mac.
type QueueItem struct {
	ID      string    `json:"id"`
	Text    string    `json:"text"`
	State   string    `json:"state"` // waiting, sending, answering; done only in an add's answer
	Created time.Time `json:"created"`
}

// TerminalPrompt is a permission prompt read from the terminal's screen.
// PromptID stays the same while the same prompt shows; Detail is the
// prompt's body (the command, or the file and its preview).
type TerminalPrompt struct {
	PromptID string           `json:"promptId"`
	Question string           `json:"question"`
	Detail   string           `json:"detail,omitempty"` // what is being approved, e.g. the command; monospace
	Options  []TerminalOption `json:"options"`
}

// TerminalOption is one numbered choice; Key is what to press.
type TerminalOption struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// terminalStatusOf turns the service's status into its wire shape.
func terminalStatusOf(st terminput.Status) *TerminalStatus {
	out := &TerminalStatus{Status: st.State, CanType: st.CanType, Working: st.Working, Reason: st.Reason, BoxHasText: st.BoxHasText, WorkingSeconds: st.WorkingSeconds}
	if p := st.Prompt; p != nil {
		out.Prompt = &TerminalPrompt{PromptID: p.ID, Question: p.Question, Detail: p.Detail, Options: make([]TerminalOption, 0, len(p.Options))}
		for _, o := range p.Options {
			out.Prompt.Options = append(out.Prompt.Options, TerminalOption{Key: string(o.Key), Label: o.Label})
		}
	}
	return out
}

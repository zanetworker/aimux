package modelproxy

import (
	"cmp"
	"net/http"
	"strings"
)

// CallFacts is what the proxy needs to know about one model call to record it and to decide anything about it:
// whose it is, what it asked for, and where it stands in its conversation. A front fills it from the request it
// was sent, and nothing after that reads the request again. So a front that speaks another API than Messages
// supplies its own reading here and reuses the rest: the role, the turn, the switch, the ledger line.
//
// It holds ids and counts, never text.
type CallFacts struct {
	// Session is the session the call belongs to; empty when the request named none.
	Session string
	// SessionFromBody: the session id came in the request's own metadata, which the main conversation and a
	// subagent carry. A call that names its session only in a header is one the harness makes on the side.
	SessionFromBody bool
	// Agent is the subagent's id, as the harness names it; empty for any other call.
	Agent string
	// Model is the model the client named, and Effort the effort it asked for.
	Model  string
	Effort string
	// Harness is the client: "claude-code", or empty when it is not known.
	Harness string
	// StartsTurn: the call opens a new turn (its last user message is a prompt, not a tool's result). Prompts is
	// how many prompts the conversation holds, and LastFingerprint a one-way mark of the last user message, by
	// which the same request sent again is recognised.
	StartsTurn      bool
	Prompts         int
	LastFingerprint uint64
}

// The role of a call: which kind of work it is.
const (
	RoleMain       = "main"       // the session's main conversation
	RoleSubagent   = "subagent"   // a subagent's own conversation
	RoleBackground = "background" // a call the harness makes on the side, outside any conversation
)

// RoleOf is the role of a call, from its facts alone. A request does not say which kind of subagent sent it, so
// that is not part of the role. The model named plays no part: a session its owner moved to a small model is
// still its main conversation.
func RoleOf(f CallFacts) string {
	switch {
	case f.Agent != "":
		return RoleSubagent
	case !f.SessionFromBody:
		return RoleBackground
	}
	return RoleMain
}

// callFactsOfMessages reads the facts of a Messages request from Claude Code. It is the only place that reads
// such a request for them. effort is the effort the engine will be asked for, which each engine settles itself.
func callFactsOfMessages(r *http.Request, request messageRequest, effort string) CallFacts {
	// Read from the metadata whatever model was named: the proxy keeps a Codex thread only for requests that name
	// a codex: model, but every request belongs to its session.
	fromBody := ledgerID(metadataSessionID(request.Metadata))
	facts := CallFacts{
		Session:         cmp.Or(fromBody, ledgerID(r.Header.Get(sessionHeader))),
		SessionFromBody: fromBody != "",
		Agent:           ledgerID(r.Header.Get(agentHeader)),
		Model:           ledgerModel(request.Model),
		Effort:          ledgerID(effort),
	}
	if strings.HasPrefix(r.Header.Get("User-Agent"), "claude-cli") {
		facts.Harness = "claude-code"
	}
	facts.StartsTurn, facts.Prompts, facts.LastFingerprint = requestShape(request.Messages)
	return facts
}

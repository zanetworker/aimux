package history

import (
	"time"

	"github.com/zanetworker/aimux/internal/cost"
	"github.com/zanetworker/aimux/internal/llm"
)

// NamingRequest, NamingReply and NamingBackend are the shared model client's
// (internal/llm) types under the names the namer has always used.
type (
	NamingRequest = llm.Request
	NamingReply   = llm.Reply
	NamingBackend = llm.Backend
)

// NamingBackends picks how the namer calls a model (llm.Backends): the
// Messages API when an API key is set (apiKey from sessions.api_key, else
// ANTHROPIC_API_KEY), with claude -p as the once-only fallback for "model
// not found"; otherwise claude -p with the owner's Claude Code login. The
// key is never logged.
func NamingBackends(apiKey string) (primary, fallback NamingBackend) {
	return llm.Backends(apiKey, anthropicMessagesURL)
}

// namingClient is the one engine both naming paths call through: the Namer
// (remote serve) and GenerateTitles (TUI and dashboard). Every call's usage
// is recorded under "naming".
func namingClient(primary, fallback NamingBackend, record func(cost.Usage) error, now func() time.Time, logf func(string, ...any)) *llm.Client {
	return &llm.Client{Primary: primary, Fallback: fallback, Label: "naming", Record: record, Now: now, Logf: logf}
}

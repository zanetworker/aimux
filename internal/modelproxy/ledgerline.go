package modelproxy

import (
	"cmp"
	"crypto/rand"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/ledger"
)

// The headers Claude Code sends with every request, and with a subagent's.
const (
	sessionHeader = "X-Claude-Code-Session-Id"
	agentHeader   = "X-Claude-Code-Agent-Id"
)

// WithLedger makes the handler write one model_call line per request that reached the engine. earlier holds lines
// already in the writer's file, from which each session's turn number goes on. A nil writer writes nothing.
func WithLedger(w *ledger.Writer, earlier ...ledger.Line) HandlerOption {
	return func(c *handlerConfig) {
		c.ledger = w
		c.turns.seed(earlier)
	}
}

// WithLedgerOff records that the ledger could not be opened, so health says so instead of looking complete.
func WithLedgerOff() HandlerOption {
	return func(c *handlerConfig) { c.ledgerOff = true }
}

// LedgerName is the ledger writer's name for a proxy on port. Only one process may write a ledger file, so the
// stable proxy and a dev listener each have their own.
func LedgerName(port int) string {
	if port == StablePort {
		return "model-proxy"
	}
	return fmt.Sprintf("model-proxy-%d", port)
}

// startModelCall fills in what is known when a request arrives: who it belongs to and what was asked for. The
// turn is taken now, so calls are numbered in the order they arrived, not the order they finished.
func startModelCall(table *turns, r *http.Request, request messageRequest, turn TurnRequest) *ledger.Line {
	// Read from the metadata whatever model was named: the proxy keeps a Codex thread only for requests that name
	// a codex: model, but every request belongs to its session.
	metadataSession := metadataSessionID(request.Metadata)
	metadataSession = ledgerID(metadataSession)
	agent := ledgerID(r.Header.Get(agentHeader))
	session := cmp.Or(metadataSession, ledgerID(r.Header.Get(sessionHeader)))
	side := sideRequest(metadataSession != "", agent != "")
	startsTurn, prompts, last := requestShape(request.Messages)
	harness := ""
	if strings.HasPrefix(r.Header.Get("User-Agent"), "claude-cli") {
		harness = "claude-code"
	}
	return &ledger.Line{
		Kind: ledger.KindModelCall, Session: session, Side: side, Agent: agent,
		Turn: table.next(session, side, startsTurn, prompts, last), Call: "mc_" + rand.Text(), Harness: harness,
		RequestModel: ledgerModel(request.Model), ResponseModel: "codex:" + turn.Model, Engine: "codex", Effort: ledgerID(turn.Effort),
	}
}

// ledgerID lets an id from a request into the ledger only when it looks like one. A line holds ids, never text,
// and these come from whoever calls the proxy.
func ledgerID(id string) string {
	if len(id) == 0 || len(id) > 128 {
		return ""
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == ':':
		default:
			return ""
		}
	}
	return id
}

// ledgerModel lets a model name from a request into the ledger only when it looks like one: an id, with the
// slash a provider prefix has.
func ledgerModel(name string) string {
	if ledgerID(strings.ReplaceAll(name, "/", "-")) == "" {
		return ""
	}
	return name
}

// finishModelCall adds how the call went. failure is the request history's word for what went wrong, or empty.
func finishModelCall(call *ledger.Line, started time.Time, firstByte time.Duration, usage Usage, failure string) {
	call.At = started
	// The ledger's input tokens are the whole prompt; the proxy's usage keeps the cached part apart.
	call.InputTokens = int64(usage.InputTokens) + int64(usage.CacheReadInputTokens)
	call.CacheRead = int64(usage.CacheReadInputTokens)
	call.OutputTokens = int64(usage.OutputTokens)
	call.Estimated = usage.estimated
	call.TTFBMillis = firstByte.Milliseconds()
	call.DurationMs = time.Since(started).Milliseconds()
	call.Outcome = cmp.Or(failure, ledger.OutcomeOK)
}

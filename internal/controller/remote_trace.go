package controller

import (
	aimuxotel "github.com/zanetworker/aimux/internal/otel"
	"github.com/zanetworker/aimux/internal/trace"
)

// OTELLookup is the minimal interface over *otel.SpanStore needed by the
// remote trace parser. Both the TUI and web frontends satisfy this via the
// shared SpanStore.
type OTELLookup interface {
	HasData() bool
	GetByConversation(id string) *aimuxotel.Span
	ConversationIDs() []string
}

// RemoteTraceParser reads OTEL spans (or falls back to the sandbox session
// file) and returns trace turns for a remote agent session. This is the
// shared logic extracted from the TUI's parserForRemote; any frontend can
// call it directly.
func RemoteTraceParser(store OTELLookup, otelSessionID, sandboxName string) []trace.Turn {
	if store == nil || !store.HasData() {
		return aimuxotel.FetchSessionTurns(sandboxName, otelSessionID)
	}

	enrich := func(turns []trace.Turn) []trace.Turn {
		if len(turns) > 0 && sandboxName != "" {
			replies := aimuxotel.FetchSessionReplies(sandboxName, otelSessionID)
			aimuxotel.EnrichTurnsWithReplies(turns, replies)
		}
		return turns
	}

	if otelSessionID != "" {
		if root := store.GetByConversation(otelSessionID); root != nil {
			turns := aimuxotel.SpansToTurns(root)
			if len(turns) > 0 {
				return enrich(turns)
			}
		}
	}

	return aimuxotel.FetchSessionTurns(sandboxName, otelSessionID)
}

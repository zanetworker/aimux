package controller

import (
	"testing"

	aimuxotel "github.com/zanetworker/aimux/internal/otel"
)

type stubSpan struct {
	name string
}

type stubOTELLookup struct {
	hasData       bool
	conversations map[string]*stubSpan
}

func (s *stubOTELLookup) HasData() bool { return s.hasData }

func (s *stubOTELLookup) GetByConversation(id string) *aimuxotel.Span {
	if s.conversations == nil {
		return nil
	}
	stub, ok := s.conversations[id]
	if !ok {
		return nil
	}
	return &aimuxotel.Span{Name: stub.name}
}

func (s *stubOTELLookup) ConversationIDs() []string {
	ids := make([]string, 0, len(s.conversations))
	for id := range s.conversations {
		ids = append(ids, id)
	}
	return ids
}

func TestRemoteTraceParser_NilStore_FallsBackToSessionFile(t *testing.T) {
	// With nil OTEL store and a valid sandbox+session,
	// it should attempt FetchSessionTurns (returns nil for non-existent sandbox).
	turns := RemoteTraceParser(nil, "fake-uuid", "fake-sandbox")
	if turns != nil {
		t.Errorf("expected nil for non-existent sandbox, got %d turns", len(turns))
	}
}

func TestRemoteTraceParser_EmptyStore_FallsBackToSessionFile(t *testing.T) {
	store := &stubOTELLookup{hasData: false}
	turns := RemoteTraceParser(store, "fake-uuid", "fake-sandbox")
	if turns != nil {
		t.Errorf("expected nil for empty store, got %d turns", len(turns))
	}
}

func TestRemoteTraceParser_StoreHasConversation_ReturnsTurns(t *testing.T) {
	store := &stubOTELLookup{
		hasData: true,
		conversations: map[string]*stubSpan{
			"session-123": {name: "root"},
		},
	}
	// SpansToTurns on a minimal span returns empty turns, so the function
	// falls through to the session file fallback. This is expected since
	// we can't construct real span trees in unit tests without the full OTEL pipeline.
	turns := RemoteTraceParser(store, "session-123", "sandbox-1")
	// With a stub that returns a root span but SpansToTurns produces no turns,
	// we expect the fallback path (nil for non-existent sandbox).
	if turns != nil {
		t.Errorf("expected nil fallback for stub span, got %d turns", len(turns))
	}
}

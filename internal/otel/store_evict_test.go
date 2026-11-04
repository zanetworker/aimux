package otel

import (
	"testing"
	"time"
)

// clockStore returns a store whose Add timestamps come from *now.
func clockStore(now *time.Time) *SpanStore {
	s := NewSpanStore()
	s.now = func() time.Time { return *now }
	return s
}

func convSpan(id, conv, name string, extra map[string]any) *Span {
	attrs := map[string]any{"gen_ai.conversation.id": conv}
	for k, v := range extra {
		attrs[k] = v
	}
	return &Span{SpanID: id, TraceID: conv, Name: name, Attrs: attrs}
}

func TestSpanStore_TakeIdle_RemovesOnlyIdleSessions(t *testing.T) {
	t0 := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	now := t0
	s := clockStore(&now)

	s.Add(convSpan("a-root", "old", "user_prompt", nil))
	s.Add(convSpan("a-1", "old", "tool_result", map[string]any{"tool_name": "Read", "tool_use_id": "tu-a1"}))
	now = t0.Add(time.Hour)
	s.Add(convSpan("b-root", "fresh", "user_prompt", nil))

	taken := s.TakeIdle(t0.Add(30 * time.Minute))

	if len(taken) != 1 || taken["old"] == nil {
		t.Fatalf("TakeIdle = %v, want only session \"old\"", keys(taken))
	}
	if got := len(taken["old"].Children); got != 1 {
		t.Errorf("taken root has %d children, want 1", got)
	}
	if s.GetByConversation("old") != nil || s.GetSpans("old") != nil {
		t.Error("idle session still in store after TakeIdle")
	}
	if s.GetByConversation("fresh") == nil || len(s.GetSpans("fresh")) != 1 {
		t.Error("active session was removed")
	}
	if s.TraceCount() != 1 {
		t.Errorf("TraceCount = %d, want 1", s.TraceCount())
	}
}

func TestSpanStore_TakeIdle_ActivityKeepsSessionAlive(t *testing.T) {
	t0 := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	now := t0
	s := clockStore(&now)
	s.Add(convSpan("root", "s1", "user_prompt", nil))
	now = t0.Add(time.Hour)
	s.Add(convSpan("late", "s1", "api_request", nil))

	if taken := s.TakeIdle(t0.Add(30 * time.Minute)); len(taken) != 0 {
		t.Errorf("session with recent activity was taken: %v", keys(taken))
	}
}

func TestSpanStore_TakeIdle_CutoffIsExclusive(t *testing.T) {
	t0 := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	now := t0
	s := clockStore(&now)
	s.Add(convSpan("root", "s1", "user_prompt", nil))

	if taken := s.TakeIdle(t0); len(taken) != 0 {
		t.Errorf("session last seen exactly at cutoff was taken")
	}
	if taken := s.TakeIdle(t0.Add(time.Nanosecond)); len(taken) != 1 {
		t.Errorf("session last seen before cutoff was not taken")
	}
}

func TestSpanStore_TakeIdle_RemovesAliasAndOrphanTraces(t *testing.T) {
	t0 := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	now := t0
	s := clockStore(&now)
	s.Add(convSpan("root", "claude-id", "user_prompt", map[string]any{"aimux.session_id": "aimux-id"}))
	s.Add(convSpan("child", "claude-id", "api_request", map[string]any{"aimux.session_id": "aimux-id"}))
	// A span with no session ID at all only lives in byTraceID.
	s.Add(&Span{SpanID: "orphan", TraceID: "trace-x", Name: "chat", Attrs: map[string]any{}})

	taken := s.TakeIdle(t0.Add(time.Minute))

	if taken["claude-id"] == nil || taken["aimux-id"] != taken["claude-id"] {
		t.Errorf("want both IDs returned with the same root, got %v", keys(taken))
	}
	if s.HasData() || len(s.ConversationIDs()) != 0 {
		t.Errorf("store not empty: traces=%d convs=%v", s.TraceCount(), s.ConversationIDs())
	}
}

func TestSpanStore_TakeIdle_EmptyStore(t *testing.T) {
	if taken := NewSpanStore().TakeIdle(time.Now()); len(taken) != 0 {
		t.Errorf("empty store returned %d sessions", len(taken))
	}
}

func keys(m map[string]*Span) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestSpanStore_TakeIdle_ActivityViaAliasKeepsSessionAlive(t *testing.T) {
	t0 := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	now := t0
	s := clockStore(&now)
	s.Add(convSpan("root", "claude-id", "user_prompt", map[string]any{"aimux.session_id": "aimux-id"}))
	// Later events carry only the aimux alias.
	now = t0.Add(time.Hour)
	s.Add(&Span{SpanID: "late", TraceID: "aimux-id", Name: "api_request",
		Attrs: map[string]any{"aimux.session_id": "aimux-id"}})

	if taken := s.TakeIdle(t0.Add(30 * time.Minute)); len(taken) != 0 {
		t.Errorf("session active through its alias was taken: %v", keys(taken))
	}
	if s.GetByConversation("claude-id") == nil || s.GetByConversation("aimux-id") == nil {
		t.Error("session IDs removed while the alias is active")
	}
}

package otel

import (
	"testing"
	"time"
)

func TestSpanStore_AddAndRetrieve(t *testing.T) {
	store := NewSpanStore()

	span := &Span{
		SpanID:  "span-1",
		TraceID: "trace-1",
		Name:    "invoke_agent",
		Start:   time.Now(),
		Attrs: map[string]any{
			"gen_ai.conversation.id": "session-abc",
		},
	}

	store.Add(span)

	if !store.HasData() {
		t.Error("HasData() should be true after Add")
	}

	got := store.GetByConversation("session-abc")
	if got == nil {
		t.Fatal("GetByConversation returned nil")
	}
	if got.SpanID != "span-1" {
		t.Errorf("SpanID = %q, want %q", got.SpanID, "span-1")
	}
}

func TestSpanStore_AssembleTree(t *testing.T) {
	store := NewSpanStore()

	root := &Span{SpanID: "root", TraceID: "t1", Name: "session"}
	child1 := &Span{SpanID: "c1", TraceID: "t1", ParentID: "root", Name: "turn-1"}
	child2 := &Span{SpanID: "c2", TraceID: "t1", ParentID: "root", Name: "turn-2"}
	grandchild := &Span{SpanID: "gc1", TraceID: "t1", ParentID: "c1", Name: "execute_tool Read"}

	store.Add(root)
	store.Add(child1)
	store.Add(child2)
	store.Add(grandchild)

	tree := store.AssembleTree("t1")
	if tree == nil {
		t.Fatal("AssembleTree returned nil")
	}
	if tree.SpanID != "root" {
		t.Errorf("root SpanID = %q, want %q", tree.SpanID, "root")
	}
	if len(tree.Children) != 2 {
		t.Fatalf("root has %d children, want 2", len(tree.Children))
	}

	// Find turn-1 and check it has the grandchild
	var turn1 *Span
	for _, c := range tree.Children {
		if c.Name == "turn-1" {
			turn1 = c
		}
	}
	if turn1 == nil {
		t.Fatal("turn-1 not found in children")
	}
	if len(turn1.Children) != 1 {
		t.Fatalf("turn-1 has %d children, want 1", len(turn1.Children))
	}
	if turn1.Children[0].Name != "execute_tool Read" {
		t.Errorf("grandchild name = %q, want %q", turn1.Children[0].Name, "execute_tool Read")
	}
}

func TestSpan_AttrHelpers(t *testing.T) {
	s := &Span{
		Attrs: map[string]any{
			"gen_ai.request.model":       "claude-opus-4-6",
			"gen_ai.usage.input_tokens":  int64(5000),
			"gen_ai.usage.cost":          0.42,
		},
	}

	if got := s.AttrStr("gen_ai.request.model"); got != "claude-opus-4-6" {
		t.Errorf("AttrStr = %q, want %q", got, "claude-opus-4-6")
	}
	if got := s.AttrInt64("gen_ai.usage.input_tokens"); got != 5000 {
		t.Errorf("AttrInt64 = %d, want %d", got, 5000)
	}
	if got := s.AttrStr("missing"); got != "" {
		t.Errorf("AttrStr(missing) = %q, want empty", got)
	}
}

func TestSpanStore_Empty(t *testing.T) {
	store := NewSpanStore()
	if store.HasData() {
		t.Error("empty store should not HasData")
	}
	if got := store.GetByConversation("nope"); got != nil {
		t.Errorf("GetByConversation on empty = %v, want nil", got)
	}
}

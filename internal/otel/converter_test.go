package otel

import (
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/subagent"
)

func TestSpansToTurns_BasicTree(t *testing.T) {
	now := time.Now()
	root := &Span{
		SpanID: "root",
		Name:   "claude_code.user_prompt",
		Start:  now.Add(-5 * time.Minute),
		End:    now,
		Attrs: map[string]any{
			"gen_ai.input.messages": "fix the bug",
			"prompt.id":            "p1",
		},
		Children: []*Span{
			{
				SpanID: "api1",
				Name:   "claude_code.api_request",
				Start:  now.Add(-4 * time.Minute),
				End:    now.Add(-3 * time.Minute),
				Attrs: map[string]any{
					"gen_ai.request.model":       "claude-opus-4-6",
					"gen_ai.usage.input_tokens":  int64(5000),
					"gen_ai.usage.output_tokens": int64(200),
					"prompt.id":                 "p1",
				},
			},
			{
				SpanID: "tool1",
				Name:   "claude_code.tool_result",
				Start:  now.Add(-3 * time.Minute),
				Attrs: map[string]any{
					"tool_name": "Read",
					"success":   "true",
					"prompt.id": "p1",
				},
				Status: StatusOK,
			},
		},
	}

	turns := SpansToTurns(root)
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1", len(turns))
	}

	turn := turns[0]
	if turn.Number != 1 {
		t.Errorf("Number = %d, want 1", turn.Number)
	}
	if len(turn.UserLines) == 0 || turn.UserLines[0] != "fix the bug" {
		t.Errorf("UserLines = %v, want [\"fix the bug\"]", turn.UserLines)
	}
	if turn.Model != "claude-opus-4-6" {
		t.Errorf("Model = %q, want %q", turn.Model, "claude-opus-4-6")
	}
	if turn.TokensIn != 5000 {
		t.Errorf("TokensIn = %d, want 5000", turn.TokensIn)
	}
	if len(turn.Actions) != 1 {
		t.Fatalf("Actions has %d entries, want 1", len(turn.Actions))
	}
	if turn.Actions[0].Name != "Read" {
		t.Errorf("Action name = %q, want %q", turn.Actions[0].Name, "Read")
	}
}

func TestSpansToTurns_NilRoot(t *testing.T) {
	turns := SpansToTurns(nil)
	if turns != nil {
		t.Errorf("SpansToTurns(nil) = %v, want nil", turns)
	}
}

func TestSpansToTurns_EmptyRoot(t *testing.T) {
	root := &Span{SpanID: "root", Name: "session"}
	turns := SpansToTurns(root)
	if len(turns) != 0 {
		t.Errorf("SpansToTurns(empty root) = %d turns, want 0", len(turns))
	}
}

func TestSpansToTurns_ToolError(t *testing.T) {
	root := &Span{
		SpanID: "root",
		Name:   "claude_code.user_prompt",
		Attrs: map[string]any{
			"gen_ai.input.messages": "run tests",
			"prompt.id":            "p1",
		},
		Children: []*Span{
			{
				SpanID: "tool1",
				Name:   "claude_code.tool_result",
				Status: StatusError,
				Attrs: map[string]any{
					"tool_name": "Bash",
					"success":   "false",
					"error":     "exit code 1",
					"prompt.id": "p1",
				},
			},
		},
	}

	turns := SpansToTurns(root)
	if len(turns) != 1 || len(turns[0].Actions) != 1 {
		t.Fatalf("expected 1 turn with 1 action, got %d turns", len(turns))
	}
	if turns[0].Actions[0].Success {
		t.Error("expected Success = false for error span")
	}
	if turns[0].Actions[0].ErrorMsg != "exit code 1" {
		t.Errorf("ErrorMsg = %q, want %q", turns[0].Actions[0].ErrorMsg, "exit code 1")
	}
}

func TestSpansToTurns_MultiplePrompts(t *testing.T) {
	now := time.Now()
	root := &Span{
		SpanID: "root",
		Name:   "claude_code.user_prompt",
		Start:  now,
		Attrs: map[string]any{
			"gen_ai.input.messages": "hello",
			"prompt.id":            "p1",
		},
		Children: []*Span{
			{
				SpanID: "api1",
				Name:   "claude_code.api_request",
				Start:  now.Add(1 * time.Second),
				Attrs: map[string]any{
					"model":    "claude-opus-4-6",
					"prompt.id": "p1",
				},
			},
			// Second prompt
			{
				SpanID: "prompt2",
				Name:   "claude_code.user_prompt",
				Start:  now.Add(10 * time.Second),
				Attrs: map[string]any{
					"gen_ai.input.messages": "fix the bug",
					"prompt.id":            "p2",
				},
			},
			{
				SpanID: "api2",
				Name:   "claude_code.api_request",
				Start:  now.Add(11 * time.Second),
				Attrs: map[string]any{
					"model":    "claude-opus-4-6",
					"prompt.id": "p2",
				},
			},
			{
				SpanID: "tool2",
				Name:   "claude_code.tool_result",
				Start:  now.Add(12 * time.Second),
				Attrs: map[string]any{
					"tool_name": "Read",
					"prompt.id": "p2",
				},
			},
		},
	}

	turns := SpansToTurns(root)
	if len(turns) != 2 {
		t.Fatalf("got %d turns, want 2 (one per prompt.id)", len(turns))
	}
	if turns[0].UserLines[0] != "hello" {
		t.Errorf("turn[0] user = %v, want [hello]", turns[0].UserLines)
	}
	if turns[1].UserLines[0] != "fix the bug" {
		t.Errorf("turn[1] user = %v, want [fix the bug]", turns[1].UserLines)
	}
	if len(turns[1].Actions) != 1 {
		t.Errorf("turn[1] actions = %d, want 1", len(turns[1].Actions))
	}
}

func TestEventsToTurns_SubagentIdentity(t *testing.T) {
	root := &Span{
		SpanID: "root", Name: "user_prompt", Start: time.Now(),
		Attrs: map[string]any{
			"prompt.id": "p1", "gen_ai.conversation.id": "s1",
			"gen_ai.input.messages": "search the codebase",
		},
		Subagent: subagent.Info{ID: "sub-1", Type: "Explore"},
	}
	child := &Span{
		SpanID: "c1", Name: "tool_result", Start: time.Now(), ParentID: "root",
		Attrs:    map[string]any{"prompt.id": "p1", "gen_ai.tool.name": "Read"},
		Subagent: subagent.Info{ID: "sub-1", Type: "Explore"},
	}
	root.Children = append(root.Children, child)

	turns := SpansToTurns(root)
	if len(turns) == 0 {
		t.Fatal("expected at least 1 turn")
	}
	if turns[0].Subagent.Type != "Explore" {
		t.Errorf("Turn.Subagent.Type = %q, want %q", turns[0].Subagent.Type, "Explore")
	}
	if turns[0].Subagent.ID != "sub-1" {
		t.Errorf("Turn.Subagent.ID = %q, want %q", turns[0].Subagent.ID, "sub-1")
	}
}

package otel

import (
	"testing"
	"time"
)

func TestSpansToTurns_BasicTree(t *testing.T) {
	root := &Span{
		SpanID: "root",
		Name:   "invoke_agent",
		Start:  time.Now().Add(-5 * time.Minute),
		End:    time.Now(),
		Children: []*Span{
			{
				SpanID: "turn1",
				Name:   "chat turn-1",
				Start:  time.Now().Add(-5 * time.Minute),
				End:    time.Now().Add(-3 * time.Minute),
				Attrs: map[string]any{
					"gen_ai.input.messages":      "fix the bug",
					"gen_ai.output.messages":     "I'll look at it.",
					"gen_ai.request.model":       "claude-opus-4-6",
					"gen_ai.usage.input_tokens":  int64(5000),
					"gen_ai.usage.output_tokens": int64(200),
				},
				Children: []*Span{
					{
						SpanID: "tool1",
						Name:   "execute_tool Read",
						Attrs: map[string]any{
							"gen_ai.operation.name":      "execute_tool",
							"gen_ai.tool.name":           "Read",
							"gen_ai.tool.call.arguments": "main.go",
						},
						Status: StatusOK,
					},
				},
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
	if len(turn.OutputLines) == 0 || turn.OutputLines[0] != "I'll look at it." {
		t.Errorf("OutputLines = %v, want [\"I'll look at it.\"]", turn.OutputLines)
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
		Children: []*Span{
			{
				SpanID: "turn1",
				Name:   "chat",
				Attrs: map[string]any{
					"gen_ai.input.messages": "run tests",
				},
				Children: []*Span{
					{
						SpanID: "tool1",
						Name:   "execute_tool Bash",
						Status: StatusError,
						Attrs: map[string]any{
							"gen_ai.operation.name": "execute_tool",
							"gen_ai.tool.name":      "Bash",
							"error.type":            "exit code 1",
						},
					},
				},
			},
		},
	}

	turns := SpansToTurns(root)
	if len(turns) != 1 || len(turns[0].Actions) != 1 {
		t.Fatal("expected 1 turn with 1 action")
	}
	if turns[0].Actions[0].Success {
		t.Error("expected Success = false for error span")
	}
	if turns[0].Actions[0].ErrorMsg != "exit code 1" {
		t.Errorf("ErrorMsg = %q, want %q", turns[0].Actions[0].ErrorMsg, "exit code 1")
	}
}

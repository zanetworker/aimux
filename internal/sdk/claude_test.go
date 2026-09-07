package sdk

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadEvents_TextOnly(t *testing.T) {
	input := strings.NewReader(strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"sess-1","model":"haiku"}`,
		`{"type":"assistant","message":{"id":"msg_1","content":[{"type":"text","text":"Hello world"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}},"session_id":"sess-1"}`,
		`{"type":"result","is_error":false,"result":"Hello world","stop_reason":"end_turn","num_turns":1,"total_cost_usd":0.001,"session_id":"sess-1","usage":{"input_tokens":0,"output_tokens":0}}`,
	}, "\n"))

	result, events, err := ReadEvents(input)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}

	if result.IsError {
		t.Error("expected no error")
	}
	if result.Output != "Hello world" {
		t.Errorf("Output = %q, want %q", result.Output, "Hello world")
	}
	if result.NumTurns != 1 {
		t.Errorf("NumTurns = %d, want 1", result.NumTurns)
	}
	if result.CostUSD != 0.001 {
		t.Errorf("CostUSD = %f, want 0.001", result.CostUSD)
	}
	if result.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want %q", result.SessionID, "sess-1")
	}
	if result.TokensIn != 10 {
		t.Errorf("TokensIn = %d, want 10", result.TokensIn)
	}
	if result.TokensOut != 5 {
		t.Errorf("TokensOut = %d, want 5", result.TokensOut)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "text" {
		t.Errorf("event type = %q, want %q", events[0].Type, "text")
	}
	if events[0].SessionID != "sess-1" {
		t.Errorf("event session_id = %q, want %q", events[0].SessionID, "sess-1")
	}
}

func TestReadEvents_WithToolUse(t *testing.T) {
	input := strings.NewReader(strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"sess-2","model":"haiku"}`,
		`{"type":"assistant","message":{"id":"msg_1","content":[{"type":"text","text":"Let me check"},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"echo hi"}}],"stop_reason":"tool_use","usage":{"input_tokens":20,"output_tokens":15}},"session_id":"sess-2"}`,
		`{"type":"assistant","message":{"id":"msg_2","content":[{"type":"text","text":"Done"}],"stop_reason":"end_turn","usage":{"input_tokens":30,"output_tokens":10}},"session_id":"sess-2"}`,
		`{"type":"result","is_error":false,"result":"Done","stop_reason":"end_turn","num_turns":2,"total_cost_usd":0.005,"session_id":"sess-2","usage":{"input_tokens":0,"output_tokens":0}}`,
	}, "\n"))

	result, events, err := ReadEvents(input)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}

	if result.Output != "Done" {
		t.Errorf("Output = %q, want %q", result.Output, "Done")
	}
	if result.NumTurns != 2 {
		t.Errorf("NumTurns = %d, want 2", result.NumTurns)
	}
	if result.TokensIn != 50 {
		t.Errorf("TokensIn = %d, want 50", result.TokensIn)
	}
	if result.TokensOut != 25 {
		t.Errorf("TokensOut = %d, want 25", result.TokensOut)
	}

	hasToolCall := false
	hasText := false
	for _, e := range events {
		switch e.Type {
		case "tool_call":
			hasToolCall = true
			if !strings.Contains(e.Data, "Bash") {
				t.Errorf("tool_call data should contain 'Bash'; got %s", e.Data)
			}
			if !strings.Contains(e.Data, "toolu_1") {
				t.Errorf("tool_call data should contain tool ID; got %s", e.Data)
			}
		case "text":
			hasText = true
		}
	}
	if !hasToolCall {
		t.Error("expected at least one tool_call event")
	}
	if !hasText {
		t.Error("expected at least one text event")
	}
}

func TestReadEvents_ErrorResult(t *testing.T) {
	input := strings.NewReader(strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"sess-3","model":"haiku"}`,
		`{"type":"result","is_error":true,"result":"rate limit exceeded","stop_reason":"error","num_turns":0,"total_cost_usd":0,"session_id":"sess-3","usage":{"input_tokens":5,"output_tokens":0}}`,
	}, "\n"))

	result, _, err := ReadEvents(input)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}

	if !result.IsError {
		t.Error("expected IsError = true")
	}
	if result.Output != "rate limit exceeded" {
		t.Errorf("Output = %q, want %q", result.Output, "rate limit exceeded")
	}
}

func TestReadEvents_SkipsMalformedLines(t *testing.T) {
	input := strings.NewReader(strings.Join([]string{
		`not json at all`,
		`{"type":"system","subtype":"init","session_id":"sess-4"}`,
		`{"broken json`,
		`{"type":"result","is_error":false,"result":"ok","stop_reason":"end_turn","num_turns":1,"total_cost_usd":0.001,"session_id":"sess-4","usage":{"input_tokens":1,"output_tokens":1}}`,
	}, "\n"))

	result, _, err := ReadEvents(input)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}

	if result.Output != "ok" {
		t.Errorf("Output = %q, want %q", result.Output, "ok")
	}
}

func TestReadEvents_ThinkingBlocksIgnored(t *testing.T) {
	input := strings.NewReader(strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"sess-5"}`,
		`{"type":"assistant","message":{"id":"msg_1","content":[{"type":"thinking","thinking":"Let me analyze..."},{"type":"text","text":"The answer is 42"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":8}},"session_id":"sess-5"}`,
		`{"type":"result","is_error":false,"result":"The answer is 42","stop_reason":"end_turn","num_turns":1,"total_cost_usd":0.002,"session_id":"sess-5","usage":{"input_tokens":0,"output_tokens":0}}`,
	}, "\n"))

	_, events, err := ReadEvents(input)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}

	for _, e := range events {
		if e.Type == "thinking" {
			t.Error("thinking blocks should not be stored as events")
		}
	}
	if len(events) != 1 {
		t.Errorf("expected 1 event (text only), got %d", len(events))
	}
}

func TestReadEvents_MultipleTextBlocks(t *testing.T) {
	input := strings.NewReader(strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"sess-6"}`,
		`{"type":"assistant","message":{"id":"msg_1","content":[{"type":"text","text":"First part"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3}},"session_id":"sess-6"}`,
		`{"type":"assistant","message":{"id":"msg_2","content":[{"type":"text","text":"Second part"}],"stop_reason":"end_turn","usage":{"input_tokens":8,"output_tokens":4}},"session_id":"sess-6"}`,
		`{"type":"result","is_error":false,"result":"Second part","stop_reason":"end_turn","num_turns":2,"total_cost_usd":0.003,"session_id":"sess-6","usage":{"input_tokens":0,"output_tokens":0}}`,
	}, "\n"))

	result, events, err := ReadEvents(input)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}

	// Result should contain the final result text.
	if result.Output != "Second part" {
		t.Errorf("Output = %q, want %q", result.Output, "Second part")
	}

	if len(events) != 2 {
		t.Errorf("expected 2 text events, got %d", len(events))
	}
	if result.TokensIn != 13 {
		t.Errorf("TokensIn = %d, want 13", result.TokensIn)
	}
}

func TestReadEvents_EmptyInput(t *testing.T) {
	input := strings.NewReader("")

	result, events, err := ReadEvents(input)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}

	if result.Output != "" {
		t.Errorf("Output = %q, want empty", result.Output)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events, got %d", len(events))
	}
}

func TestClaudeSession_RealExecution(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not on PATH")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sess, err := NewClaudeSession(ctx, "haiku", "respond with just the word hello, do not use any tools", "")
	if err != nil {
		t.Fatalf("NewClaudeSession: %v", err)
	}
	defer sess.Stop()

	result, err := sess.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.IsError {
		t.Errorf("unexpected error: %s", result.Output)
	}
	if result.Output == "" {
		t.Error("expected non-empty output")
	}
	if result.CostUSD <= 0 {
		t.Error("expected positive cost")
	}
	t.Logf("Output: %s", result.Output)
	t.Logf("Cost: $%.6f, Turns: %d, TokensIn: %d, TokensOut: %d",
		result.CostUSD, result.NumTurns, result.TokensIn, result.TokensOut)
}

func TestClaudeSession_WithToolUse(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not on PATH")
	}

	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sess, err := NewClaudeSession(ctx, "haiku", "create a file called test.txt containing 'hello from aimux' in the current directory, then read it back and tell me what it says", dir)
	if err != nil {
		t.Fatalf("NewClaudeSession: %v", err)
	}
	defer sess.Stop()

	result, err := sess.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	events := sess.Events()
	hasToolCall := false
	for _, e := range events {
		if e.Type == "tool_call" {
			hasToolCall = true
			t.Logf("Tool call: %s", e.Data)
		}
	}
	if !hasToolCall {
		t.Error("expected at least one tool_call event")
	}

	content, err := os.ReadFile(filepath.Join(dir, "test.txt"))
	if err != nil {
		t.Logf("Warning: test.txt not found (model may have used different filename): %v", err)
	} else {
		t.Logf("File content: %s", string(content))
	}

	t.Logf("Output: %s", result.Output)
	t.Logf("Events: %d, Cost: $%.6f", len(events), result.CostUSD)
}

func TestClaudeSession_NotOnPath(t *testing.T) {
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", "/nonexistent")
	defer os.Setenv("PATH", origPath)

	ctx := context.Background()
	_, err := NewClaudeSession(ctx, "haiku", "hello", "")
	if err == nil {
		t.Fatal("expected error when claude not on PATH")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found'; got %q", err.Error())
	}
}

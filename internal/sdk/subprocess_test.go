package sdk

import (
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func buildMockSDK(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "mock-sdk")
	cmd := exec.Command("go", "build", "-o", binary, "../../cmd/mock-sdk")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build mock-sdk: %s\n%s", err, out)
	}
	return binary
}

func TestEventFromToolCall(t *testing.T) {
	tc := ToolCall{ID: "tc_1", Name: "bash", Input: map[string]any{"command": "ls"}}
	ev := EventFromToolCall(tc)

	if ev.Type != EventToolCall {
		t.Errorf("Type = %q, want %q", ev.Type, EventToolCall)
	}
	if ev.ID != "tc_1" {
		t.Errorf("ID = %q, want %q", ev.ID, "tc_1")
	}
	if ev.Name != "bash" {
		t.Errorf("Name = %q, want %q", ev.Name, "bash")
	}
	if ev.Input["command"] != "ls" {
		t.Errorf("Input[command] = %v, want %q", ev.Input["command"], "ls")
	}
}

func TestEventFromToolResult(t *testing.T) {
	tr := ToolResult{ID: "tc_1", Output: "hello", IsError: false}
	ev := EventFromToolResult(tr)

	if ev.Type != EventToolResult {
		t.Errorf("Type = %q, want %q", ev.Type, EventToolResult)
	}
	if ev.ID != "tc_1" {
		t.Errorf("ID = %q, want %q", ev.ID, "tc_1")
	}
	if ev.Output != "hello" {
		t.Errorf("Output = %q, want %q", ev.Output, "hello")
	}
	if ev.IsError {
		t.Error("IsError = true, want false")
	}
}

func TestEventFromToolResult_Error(t *testing.T) {
	tr := ToolResult{ID: "tc_2", Output: "fail", IsError: true}
	ev := EventFromToolResult(tr)

	if ev.Type != EventToolResult {
		t.Errorf("Type = %q, want %q", ev.Type, EventToolResult)
	}
	if !ev.IsError {
		t.Error("IsError = false, want true")
	}
}

func TestToToolCall_Roundtrip(t *testing.T) {
	original := ToolCall{ID: "tc_5", Name: "read", Input: map[string]any{"file_path": "/etc/hosts"}}
	ev := EventFromToolCall(original)
	got := ev.ToToolCall()

	if got.ID != original.ID {
		t.Errorf("ID = %q, want %q", got.ID, original.ID)
	}
	if got.Name != original.Name {
		t.Errorf("Name = %q, want %q", got.Name, original.Name)
	}
	if got.Input["file_path"] != original.Input["file_path"] {
		t.Errorf("Input[file_path] = %v, want %v", got.Input["file_path"], original.Input["file_path"])
	}
}

func TestSubprocess_StartStop(t *testing.T) {
	binary := buildMockSDK(t)
	ctx := context.Background()

	sub, err := StartSubprocess(ctx, binary)
	if err != nil {
		t.Fatalf("StartSubprocess: %v", err)
	}

	if err := sub.Stop(); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

func TestSubprocess_SendReceive(t *testing.T) {
	binary := buildMockSDK(t)
	ctx := context.Background()

	sub, err := StartSubprocess(ctx, binary)
	if err != nil {
		t.Fatalf("StartSubprocess: %v", err)
	}
	defer sub.Stop()

	// Send a prompt, expect a tool_call back.
	err = sub.Send(Event{Type: EventPrompt, Content: "Fix the bug"})
	if err != nil {
		t.Fatalf("Send prompt: %v", err)
	}

	ev, err := sub.Receive()
	if err != nil {
		t.Fatalf("Receive tool_call: %v", err)
	}
	if ev.Type != EventToolCall {
		t.Fatalf("expected tool_call, got %q", ev.Type)
	}
	if ev.ID != "tc_1" {
		t.Errorf("tool_call ID = %q, want %q", ev.ID, "tc_1")
	}
	if ev.Name != "bash" {
		t.Errorf("tool_call Name = %q, want %q", ev.Name, "bash")
	}

	// Send tool_result, expect text + completion.
	err = sub.Send(Event{Type: EventToolResult, ID: "tc_1", Output: "hello from mock"})
	if err != nil {
		t.Fatalf("Send tool_result: %v", err)
	}

	ev, err = sub.Receive()
	if err != nil {
		t.Fatalf("Receive text: %v", err)
	}
	if ev.Type != EventText {
		t.Fatalf("expected text, got %q", ev.Type)
	}
	if ev.Content != "Mock analysis: hello from mock" {
		t.Errorf("text content = %q, want %q", ev.Content, "Mock analysis: hello from mock")
	}

	ev, err = sub.Receive()
	if err != nil {
		t.Fatalf("Receive completion: %v", err)
	}
	if ev.Type != EventCompletion {
		t.Fatalf("expected completion, got %q", ev.Type)
	}
	if ev.StopReason != "end_turn" {
		t.Errorf("stop_reason = %q, want %q", ev.StopReason, "end_turn")
	}
}

func TestSubprocess_InvalidBinary(t *testing.T) {
	ctx := context.Background()
	_, err := StartSubprocess(ctx, "/nonexistent/binary")
	if err == nil {
		t.Fatal("expected error for nonexistent binary, got nil")
	}
}

func TestSubprocess_ContextCancel(t *testing.T) {
	binary := buildMockSDK(t)
	ctx, cancel := context.WithCancel(context.Background())

	sub, err := StartSubprocess(ctx, binary)
	if err != nil {
		t.Fatalf("StartSubprocess: %v", err)
	}

	cancel()

	// The process should exit. Wait with a deadline to avoid hanging.
	done := make(chan error, 1)
	go func() {
		done <- sub.Wait()
	}()

	select {
	case <-done:
		// Process exited as expected.
	case <-time.After(5 * time.Second):
		t.Fatal("subprocess did not exit after context cancel within 5s")
	}
}

func TestSubprocess_EOF_After_Close(t *testing.T) {
	binary := buildMockSDK(t)
	ctx := context.Background()

	sub, err := StartSubprocess(ctx, binary)
	if err != nil {
		t.Fatalf("StartSubprocess: %v", err)
	}

	sub.stdin.Close()

	// After stdin closes, the mock-sdk scanner loop ends and stdout closes.
	_, err = sub.Receive()
	if err != io.EOF {
		t.Errorf("expected io.EOF after stdin close, got %v", err)
	}
}

func TestSubprocess_Stderr(t *testing.T) {
	binary := buildMockSDK(t)
	ctx := context.Background()

	sub, err := StartSubprocess(ctx, binary)
	if err != nil {
		t.Fatalf("StartSubprocess: %v", err)
	}

	if err := sub.Stop(); err != nil {
		t.Errorf("Stop: %v", err)
	}

	// Mock SDK doesn't write to stderr, so it should be empty.
	if got := sub.Stderr(); got != "" {
		t.Errorf("Stderr = %q, want empty", got)
	}
}

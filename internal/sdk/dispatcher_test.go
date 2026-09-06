package sdk

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/environment"
)

type execCall struct {
	sandbox string
	command string
}

type mockEnv struct {
	execCalls []execCall
	execOut   string
	execErr   error
}

func (m *mockEnv) Name() string                            { return "mock" }
func (m *mockEnv) Type() string                            { return "mock" }
func (m *mockEnv) Discover() ([]agent.Agent, error)        { return nil, nil }
func (m *mockEnv) Kill(a agent.Agent) error                { return nil }
func (m *mockEnv) CreateSandbox(_ context.Context, _ environment.SandboxOpts) (string, error) {
	return "", nil
}
func (m *mockEnv) DeleteSandbox(_ context.Context, _ string) error { return nil }
func (m *mockEnv) ListSandboxes(_ context.Context) ([]environment.SandboxStatus, error) {
	return nil, nil
}
func (m *mockEnv) Exec(_ context.Context, sandbox, command string) (string, error) {
	m.execCalls = append(m.execCalls, execCall{sandbox: sandbox, command: command})
	return m.execOut, m.execErr
}

func TestDispatch_BashTool(t *testing.T) {
	env := &mockEnv{execOut: "hello world"}
	d := NewDispatcher(env, "test-sandbox")

	call := ToolCall{ID: "t1", Name: "bash", Input: map[string]any{"command": "echo hello"}}
	result, decision := d.Dispatch(context.Background(), call)

	if decision != Handled {
		t.Fatalf("expected Handled, got %d", decision)
	}
	if result.ID != "t1" {
		t.Errorf("expected ID t1, got %s", result.ID)
	}
	if result.Output != "hello world" {
		t.Errorf("expected 'hello world', got %q", result.Output)
	}
	if result.IsError {
		t.Error("expected IsError=false")
	}
	if len(env.execCalls) != 1 {
		t.Fatalf("expected 1 exec call, got %d", len(env.execCalls))
	}
	if env.execCalls[0].sandbox != "test-sandbox" {
		t.Errorf("expected sandbox 'test-sandbox', got %q", env.execCalls[0].sandbox)
	}
	if env.execCalls[0].command != "echo hello" {
		t.Errorf("expected command 'echo hello', got %q", env.execCalls[0].command)
	}
}

func TestDispatch_BashCapitalized(t *testing.T) {
	env := &mockEnv{execOut: "ok"}
	d := NewDispatcher(env, "sb")

	call := ToolCall{ID: "t2", Name: "Bash", Input: map[string]any{"command": "ls"}}
	result, decision := d.Dispatch(context.Background(), call)

	if decision != Handled {
		t.Fatalf("expected Handled, got %d", decision)
	}
	if result.Output != "ok" {
		t.Errorf("expected 'ok', got %q", result.Output)
	}
}

func TestDispatch_ReadTool(t *testing.T) {
	env := &mockEnv{execOut: "file contents"}
	d := NewDispatcher(env, "sb")

	call := ToolCall{ID: "r1", Name: "read", Input: map[string]any{"file_path": "/tmp/foo.txt"}}
	result, decision := d.Dispatch(context.Background(), call)

	if decision != Handled {
		t.Fatalf("expected Handled, got %d", decision)
	}
	if result.Output != "file contents" {
		t.Errorf("expected 'file contents', got %q", result.Output)
	}
	if len(env.execCalls) != 1 {
		t.Fatalf("expected 1 exec call, got %d", len(env.execCalls))
	}
	if !strings.Contains(env.execCalls[0].command, "cat") {
		t.Errorf("expected cat command, got %q", env.execCalls[0].command)
	}
	if !strings.Contains(env.execCalls[0].command, "/tmp/foo.txt") {
		t.Errorf("expected file path in command, got %q", env.execCalls[0].command)
	}
}

func TestDispatch_ReadCapitalized(t *testing.T) {
	env := &mockEnv{execOut: "data"}
	d := NewDispatcher(env, "sb")

	call := ToolCall{ID: "r2", Name: "Read", Input: map[string]any{"file_path": "/a/b.go"}}
	result, decision := d.Dispatch(context.Background(), call)

	if decision != Handled {
		t.Fatalf("expected Handled")
	}
	if result.Output != "data" {
		t.Errorf("expected 'data', got %q", result.Output)
	}
}

func TestDispatch_WriteTool(t *testing.T) {
	env := &mockEnv{execOut: ""}
	d := NewDispatcher(env, "sb")

	call := ToolCall{
		ID:   "w1",
		Name: "write",
		Input: map[string]any{
			"file_path": "/tmp/out.txt",
			"content":   "hello\nworld",
		},
	}
	result, decision := d.Dispatch(context.Background(), call)

	if decision != Handled {
		t.Fatalf("expected Handled")
	}
	if result.IsError {
		t.Errorf("expected no error, got: %s", result.Output)
	}
	if len(env.execCalls) != 1 {
		t.Fatalf("expected 1 exec call, got %d", len(env.execCalls))
	}
	cmd := env.execCalls[0].command
	if !strings.Contains(cmd, "AIMUX_EOF") {
		t.Errorf("expected heredoc delimiter in command, got %q", cmd)
	}
	if !strings.Contains(cmd, "/tmp/out.txt") {
		t.Errorf("expected file path in command, got %q", cmd)
	}
	if !strings.Contains(cmd, "hello\nworld") {
		t.Errorf("expected content in command, got %q", cmd)
	}
}

func TestDispatch_GrepTool(t *testing.T) {
	env := &mockEnv{execOut: "main.go:10:func main()"}
	d := NewDispatcher(env, "sb")

	call := ToolCall{
		ID:   "g1",
		Name: "grep",
		Input: map[string]any{
			"pattern": "func main",
			"path":    "/src",
		},
	}
	result, decision := d.Dispatch(context.Background(), call)

	if decision != Handled {
		t.Fatalf("expected Handled")
	}
	if result.Output != "main.go:10:func main()" {
		t.Errorf("unexpected output: %q", result.Output)
	}
	cmd := env.execCalls[0].command
	if !strings.Contains(cmd, "grep") {
		t.Errorf("expected grep in command, got %q", cmd)
	}
	if !strings.Contains(cmd, "func main") {
		t.Errorf("expected pattern in command, got %q", cmd)
	}
}

func TestDispatch_GrepToolDefaultPath(t *testing.T) {
	env := &mockEnv{execOut: ""}
	d := NewDispatcher(env, "sb")

	call := ToolCall{
		ID:    "g2",
		Name:  "Grep",
		Input: map[string]any{"pattern": "TODO"},
	}
	_, decision := d.Dispatch(context.Background(), call)

	if decision != Handled {
		t.Fatalf("expected Handled")
	}
	cmd := env.execCalls[0].command
	if !strings.Contains(cmd, `"."`) {
		t.Errorf("expected default path '.' in command, got %q", cmd)
	}
}

func TestDispatch_GlobTool(t *testing.T) {
	env := &mockEnv{execOut: "./main.go\n./app.go"}
	d := NewDispatcher(env, "sb")

	call := ToolCall{ID: "gl1", Name: "glob", Input: map[string]any{"pattern": "*.go"}}
	result, decision := d.Dispatch(context.Background(), call)

	if decision != Handled {
		t.Fatalf("expected Handled")
	}
	if result.Output != "./main.go\n./app.go" {
		t.Errorf("unexpected output: %q", result.Output)
	}
	if !strings.Contains(env.execCalls[0].command, "find") {
		t.Errorf("expected find command, got %q", env.execCalls[0].command)
	}
}

func TestDispatch_EditTool(t *testing.T) {
	env := &mockEnv{execOut: ""}
	d := NewDispatcher(env, "sb")

	call := ToolCall{
		ID:   "e1",
		Name: "edit",
		Input: map[string]any{
			"file_path":  "/tmp/f.go",
			"old_string": "foo",
			"new_string": "bar",
		},
	}
	result, decision := d.Dispatch(context.Background(), call)

	if decision != Handled {
		t.Fatalf("expected Handled")
	}
	if result.IsError {
		t.Errorf("expected no error, got: %s", result.Output)
	}
	cmd := env.execCalls[0].command
	if !strings.Contains(cmd, "sed") {
		t.Errorf("expected sed command, got %q", cmd)
	}
}

func TestDispatch_NonSandboxTool(t *testing.T) {
	env := &mockEnv{}
	d := NewDispatcher(env, "sb")

	call := ToolCall{ID: "ws1", Name: "WebSearch", Input: map[string]any{"query": "test"}}
	result, decision := d.Dispatch(context.Background(), call)

	if decision != Approve {
		t.Fatalf("expected Approve, got %d", decision)
	}
	if result.ID != "" {
		t.Errorf("expected empty result ID for Approve, got %q", result.ID)
	}
	if len(env.execCalls) != 0 {
		t.Errorf("expected no exec calls for non-sandbox tool, got %d", len(env.execCalls))
	}
}

func TestDispatch_UnknownTool(t *testing.T) {
	env := &mockEnv{}
	d := NewDispatcher(env, "sb")

	call := ToolCall{ID: "u1", Name: "SomethingElse", Input: map[string]any{}}
	_, decision := d.Dispatch(context.Background(), call)

	if decision != Approve {
		t.Fatalf("expected Approve for unknown tool, got %d", decision)
	}
	if len(env.execCalls) != 0 {
		t.Errorf("expected no exec calls, got %d", len(env.execCalls))
	}
}

func TestDispatch_ExecError(t *testing.T) {
	env := &mockEnv{execErr: errors.New("container crashed")}
	d := NewDispatcher(env, "sb")

	call := ToolCall{ID: "err1", Name: "bash", Input: map[string]any{"command": "exit 1"}}
	result, decision := d.Dispatch(context.Background(), call)

	if decision != Handled {
		t.Fatalf("expected Handled even on error, got %d", decision)
	}
	if !result.IsError {
		t.Error("expected IsError=true")
	}
	if result.ID != "err1" {
		t.Errorf("expected ID err1, got %s", result.ID)
	}
	if !strings.Contains(result.Output, "container crashed") {
		t.Errorf("expected error message in output, got %q", result.Output)
	}
}

func TestDispatch_EmptyToolName(t *testing.T) {
	env := &mockEnv{}
	d := NewDispatcher(env, "sb")

	call := ToolCall{ID: "emp1", Name: "", Input: map[string]any{}}
	_, decision := d.Dispatch(context.Background(), call)

	if decision != Approve {
		t.Fatalf("expected Approve for empty tool name, got %d", decision)
	}
	if len(env.execCalls) != 0 {
		t.Errorf("expected no exec calls, got %d", len(env.execCalls))
	}
}

func TestDispatch_BashMissingCommand(t *testing.T) {
	env := &mockEnv{}
	d := NewDispatcher(env, "sb")

	call := ToolCall{ID: "bad1", Name: "bash", Input: map[string]any{}}
	result, decision := d.Dispatch(context.Background(), call)

	if decision != Handled {
		t.Fatalf("expected Handled, got %d", decision)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing command input")
	}
	if !strings.Contains(result.Output, "missing") {
		t.Errorf("expected 'missing' in error, got %q", result.Output)
	}
	if len(env.execCalls) != 0 {
		t.Errorf("expected no exec calls when input is invalid, got %d", len(env.execCalls))
	}
}

func TestDispatch_ReadMissingFilePath(t *testing.T) {
	env := &mockEnv{}
	d := NewDispatcher(env, "sb")

	call := ToolCall{ID: "bad2", Name: "Read", Input: map[string]any{}}
	result, decision := d.Dispatch(context.Background(), call)

	if decision != Handled {
		t.Fatalf("expected Handled")
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing file_path")
	}
}

func TestDispatch_WriteMissingContent(t *testing.T) {
	env := &mockEnv{}
	d := NewDispatcher(env, "sb")

	call := ToolCall{ID: "bad3", Name: "write", Input: map[string]any{"file_path": "/tmp/x"}}
	result, decision := d.Dispatch(context.Background(), call)

	if decision != Handled {
		t.Fatalf("expected Handled")
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing content")
	}
}

func TestNewDispatcher_SandboxToolSet(t *testing.T) {
	d := NewDispatcher(&mockEnv{}, "sb")

	expected := []string{"bash", "Bash", "read", "Read", "write", "Write", "edit", "Edit", "glob", "Glob", "grep", "Grep"}
	for _, tool := range expected {
		if !d.sandboxTools[tool] {
			t.Errorf("expected %q in sandboxTools", tool)
		}
	}
	if d.sandboxTools["WebSearch"] {
		t.Error("WebSearch should not be in sandboxTools")
	}
}

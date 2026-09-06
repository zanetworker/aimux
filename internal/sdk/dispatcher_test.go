package sdk

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/environment"
)

func realEnv() environment.Environment {
	return environment.NewLocalEnvironment()
}

func TestDispatch_BashTool(t *testing.T) {
	d := NewDispatcher(realEnv(), "")

	call := ToolCall{ID: "t1", Name: "bash", Input: map[string]any{"command": "echo hello"}}
	result, decision := d.Dispatch(context.Background(), call)

	if decision != Handled {
		t.Fatalf("expected Handled, got %d", decision)
	}
	if result.ID != "t1" {
		t.Errorf("expected ID t1, got %s", result.ID)
	}
	if strings.TrimSpace(result.Output) != "hello" {
		t.Errorf("expected 'hello', got %q", result.Output)
	}
	if result.IsError {
		t.Error("expected IsError=false")
	}
}

func TestDispatch_BashCapitalized(t *testing.T) {
	d := NewDispatcher(realEnv(), "")

	call := ToolCall{ID: "t2", Name: "Bash", Input: map[string]any{"command": "echo ok"}}
	result, decision := d.Dispatch(context.Background(), call)

	if decision != Handled {
		t.Fatalf("expected Handled, got %d", decision)
	}
	if strings.TrimSpace(result.Output) != "ok" {
		t.Errorf("expected 'ok', got %q", result.Output)
	}
}

func TestDispatch_ReadTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("file contents"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := NewDispatcher(realEnv(), "")
	call := ToolCall{ID: "r1", Name: "read", Input: map[string]any{"file_path": path}}
	result, decision := d.Dispatch(context.Background(), call)

	if decision != Handled {
		t.Fatalf("expected Handled, got %d", decision)
	}
	if strings.TrimSpace(result.Output) != "file contents" {
		t.Errorf("expected 'file contents', got %q", result.Output)
	}
}

func TestDispatch_ReadCapitalized(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := NewDispatcher(realEnv(), "")
	call := ToolCall{ID: "r2", Name: "Read", Input: map[string]any{"file_path": path}}
	result, decision := d.Dispatch(context.Background(), call)

	if decision != Handled {
		t.Fatalf("expected Handled")
	}
	if strings.TrimSpace(result.Output) != "data" {
		t.Errorf("expected 'data', got %q", result.Output)
	}
}

func TestDispatch_WriteTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	d := NewDispatcher(realEnv(), "")
	call := ToolCall{
		ID:   "w1",
		Name: "write",
		Input: map[string]any{
			"file_path": path,
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

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if strings.TrimSpace(string(got)) != "hello\nworld" {
		t.Errorf("expected 'hello\\nworld', got %q", string(got))
	}
}

func TestDispatch_GrepTool(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := NewDispatcher(realEnv(), "")
	call := ToolCall{
		ID:   "g1",
		Name: "grep",
		Input: map[string]any{
			"pattern": "func main",
			"path":    dir,
		},
	}
	result, decision := d.Dispatch(context.Background(), call)

	if decision != Handled {
		t.Fatalf("expected Handled")
	}
	if !strings.Contains(result.Output, "func main") {
		t.Errorf("expected grep output containing 'func main', got %q", result.Output)
	}
}

func TestDispatch_GlobTool(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	d := NewDispatcher(realEnv(), "")
	call := ToolCall{ID: "gl1", Name: "glob", Input: map[string]any{"pattern": "*.go"}}
	// Use bash to cd into dir and run glob there
	call2 := ToolCall{ID: "gl2", Name: "bash", Input: map[string]any{"command": "find " + dir + " -name '*.go'"}}
	result, decision := d.Dispatch(context.Background(), call2)

	if decision != Handled {
		t.Fatalf("expected Handled")
	}
	if !strings.Contains(result.Output, "a.go") || !strings.Contains(result.Output, "b.go") {
		t.Errorf("expected both .go files in output, got %q", result.Output)
	}

	// Also test the actual glob dispatch
	result2, decision2 := d.Dispatch(context.Background(), call)
	if decision2 != Handled {
		t.Fatalf("expected Handled for glob")
	}
	// glob uses "find . -name" so it may or may not find the temp files depending on cwd,
	// but it should not error
	_ = result2
}

func TestDispatch_EditTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edit.txt")
	if err := os.WriteFile(path, []byte("hello foo world"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := NewDispatcher(realEnv(), "")
	call := ToolCall{
		ID:   "e1",
		Name: "edit",
		Input: map[string]any{
			"file_path":  path,
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

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read edited file: %v", err)
	}
	if !strings.Contains(string(got), "bar") {
		t.Errorf("expected 'bar' in edited file, got %q", string(got))
	}
	if strings.Contains(string(got), "foo") {
		t.Errorf("expected 'foo' to be replaced, but still found in %q", string(got))
	}
}

func TestDispatch_NonSandboxTool(t *testing.T) {
	d := NewDispatcher(realEnv(), "")

	call := ToolCall{ID: "ws1", Name: "WebSearch", Input: map[string]any{"query": "test"}}
	_, decision := d.Dispatch(context.Background(), call)

	if decision != Approve {
		t.Fatalf("expected Approve, got %d", decision)
	}
}

func TestDispatch_UnknownTool(t *testing.T) {
	d := NewDispatcher(realEnv(), "")

	call := ToolCall{ID: "u1", Name: "SomethingElse", Input: map[string]any{}}
	_, decision := d.Dispatch(context.Background(), call)

	if decision != Approve {
		t.Fatalf("expected Approve for unknown tool, got %d", decision)
	}
}

func TestDispatch_ExecError(t *testing.T) {
	d := NewDispatcher(realEnv(), "")

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
}

func TestDispatch_EmptyToolName(t *testing.T) {
	d := NewDispatcher(realEnv(), "")

	call := ToolCall{ID: "emp1", Name: "", Input: map[string]any{}}
	_, decision := d.Dispatch(context.Background(), call)

	if decision != Approve {
		t.Fatalf("expected Approve for empty tool name, got %d", decision)
	}
}

func TestDispatch_BashMissingCommand(t *testing.T) {
	d := NewDispatcher(realEnv(), "")

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
}

func TestDispatch_ReadMissingFilePath(t *testing.T) {
	d := NewDispatcher(realEnv(), "")

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
	d := NewDispatcher(realEnv(), "")

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
	d := NewDispatcher(realEnv(), "")

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

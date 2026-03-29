package views

import (
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/trace"
)

func TestTraceSnippet(t *testing.T) {
	turns := []trace.Turn{
		{
			UserLines: []string{"fix the bug"},
		},
		{
			OutputLines: []string{"I'll read the file first."},
			Actions: []trace.ToolSpan{
				{Name: "Read", Snippet: "config.go", Success: true},
				{Name: "Edit", Snippet: "config.go:45", Success: true},
			},
		},
		{
			OutputLines: []string{"Done. All tests pass."},
			Actions: []trace.ToolSpan{
				{Name: "Bash", Snippet: "go test ./...", Success: true},
			},
		},
	}

	result := TraceSnippet(turns, 4)
	lines := strings.Split(result, "\n")

	if len(lines) > 4 {
		t.Errorf("TraceSnippet(4) returned %d lines, want <= 4", len(lines))
	}
	if !strings.Contains(result, "Bash") {
		t.Error("TraceSnippet should contain most recent tool call (Bash)")
	}
}

func TestTraceSnippet_Empty(t *testing.T) {
	result := TraceSnippet(nil, 5)
	if result != "" {
		t.Errorf("TraceSnippet(nil) = %q, want empty", result)
	}
}

func TestTraceSnippet_MaxLines(t *testing.T) {
	turns := []trace.Turn{
		{
			Actions: []trace.ToolSpan{
				{Name: "Read", Snippet: "a.go", Success: true},
				{Name: "Read", Snippet: "b.go", Success: true},
				{Name: "Read", Snippet: "c.go", Success: true},
				{Name: "Read", Snippet: "d.go", Success: true},
				{Name: "Read", Snippet: "e.go", Success: true},
				{Name: "Read", Snippet: "f.go", Success: true},
			},
		},
	}
	result := TraceSnippet(turns, 3)
	lines := strings.Split(strings.TrimRight(result, "\n"), "\n")
	if len(lines) > 3 {
		t.Errorf("TraceSnippet(3) returned %d lines, want <= 3", len(lines))
	}
}

func TestTraceSnippet_ZeroMaxLines(t *testing.T) {
	turns := []trace.Turn{{OutputLines: []string{"hello"}}}
	result := TraceSnippet(turns, 0)
	if result != "" {
		t.Errorf("TraceSnippet(0) = %q, want empty", result)
	}
}

func TestTraceSnippet_FailedTool(t *testing.T) {
	turns := []trace.Turn{
		{
			Actions: []trace.ToolSpan{
				{Name: "Bash", Snippet: "go build", Success: false},
			},
		},
	}
	result := TraceSnippet(turns, 5)
	if !strings.Contains(result, "✗") {
		t.Error("failed tool should show ✗ icon")
	}
}

func TestTraceSnippet_LongSnippetTruncated(t *testing.T) {
	turns := []trace.Turn{
		{
			Actions: []trace.ToolSpan{
				{Name: "Read", Snippet: strings.Repeat("x", 60), Success: true},
			},
		},
	}
	result := TraceSnippet(turns, 5)
	if !strings.Contains(result, "...") {
		t.Error("long snippet should be truncated with ...")
	}
}

func TestTraceSnippet_LongOutputTruncated(t *testing.T) {
	turns := []trace.Turn{
		{
			OutputLines: []string{strings.Repeat("y", 60)},
		},
	}
	result := TraceSnippet(turns, 5)
	if !strings.Contains(result, "...") {
		t.Error("long output should be truncated with ...")
	}
}

func TestTraceSnippet_ChronologicalOrder(t *testing.T) {
	turns := []trace.Turn{
		{
			Actions: []trace.ToolSpan{
				{Name: "Read", Snippet: "first.go", Success: true},
			},
		},
		{
			Actions: []trace.ToolSpan{
				{Name: "Edit", Snippet: "second.go", Success: true},
			},
		},
	}
	result := TraceSnippet(turns, 10)
	readIdx := strings.Index(result, "Read")
	editIdx := strings.Index(result, "Edit")
	if readIdx >= editIdx {
		t.Error("output should be in chronological order (Read before Edit)")
	}
}

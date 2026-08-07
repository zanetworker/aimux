package otel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFlushTracker_EmptyStore(t *testing.T) {
	store := NewSpanStore()
	dir := t.TempDir()

	if err := FlushTracker(store, dir); err != nil {
		t.Fatalf("FlushTracker error: %v", err)
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("expected no files for empty store, got %d", len(entries))
	}
}

func TestFlushTracker_WritesJSON(t *testing.T) {
	store := NewSpanStore()
	now := time.Now()

	root := &Span{
		SpanID: "root", TraceID: "session-1", Name: "invoke_agent",
		Start: now,
		Attrs: map[string]any{"gen_ai.conversation.id": "session-1"},
	}
	store.Add(root)

	store.Add(&Span{
		SpanID: "t1", TraceID: "session-1", Name: "tool_decision",
		Start: now, Attrs: map[string]any{
			"gen_ai.conversation.id": "session-1",
			"tool_name":             "Read",
			"tool_use_id":           "tu1",
		},
	})

	store.Add(&Span{
		SpanID: "a1", TraceID: "session-1", Name: "api_request",
		Start: now, Attrs: map[string]any{
			"gen_ai.conversation.id":      "session-1",
			"gen_ai.usage.input_tokens":   int64(5000),
			"gen_ai.usage.output_tokens":  int64(1000),
			"tool_name":                   "Read",
		},
	})

	dir := t.TempDir()
	if err := FlushTracker(store, dir); err != nil {
		t.Fatalf("FlushTracker error: %v", err)
	}

	path := filepath.Join(dir, "session-1.json")
	b, err := os.ReadFile(path) // #nosec G304
	if err != nil {
		t.Fatalf("read tracker file: %v", err)
	}

	var data TrackerData
	if err := json.Unmarshal(b, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if data.Calls != 1 {
		t.Errorf("Calls = %d, want 1", data.Calls)
	}
	if data.Last != "Read" {
		t.Errorf("Last = %q, want %q", data.Last, "Read")
	}
	if detail, ok := data.ByTool["Read"]; !ok {
		t.Error("missing ByTool[Read]")
	} else {
		if detail.Calls != 1 {
			t.Errorf("Read.Calls = %d, want 1", detail.Calls)
		}
		if detail.Tokens == 0 {
			t.Error("Read.Tokens should be > 0")
		}
	}
}

func TestFlushTracker_AggregatesByTool(t *testing.T) {
	store := NewSpanStore()
	now := time.Now()

	root := &Span{
		SpanID: "root", TraceID: "s2", Name: "invoke_agent",
		Start: now,
		Attrs: map[string]any{"gen_ai.conversation.id": "s2"},
	}
	store.Add(root)

	tools := []string{"Read", "Read", "Bash", "Edit", "Edit", "Edit"}
	for i, name := range tools {
		store.Add(&Span{
			SpanID: fmt.Sprintf("td%d", i), TraceID: "s2", Name: "tool_decision",
			Start: now, Attrs: map[string]any{
				"gen_ai.conversation.id": "s2",
				"tool_name":             name,
				"tool_use_id":           fmt.Sprintf("tu%d", i),
			},
		})
	}

	store.Add(&Span{
		SpanID: "api1", TraceID: "s2", Name: "api_request",
		Start: now, Attrs: map[string]any{
			"gen_ai.conversation.id":     "s2",
			"gen_ai.usage.input_tokens":  int64(12000),
			"gen_ai.usage.output_tokens": int64(6000),
		},
	})

	dir := t.TempDir()
	_ = FlushTracker(store, dir)

	b, _ := os.ReadFile(filepath.Join(dir, "s2.json")) // #nosec G304
	var data TrackerData
	_ = json.Unmarshal(b, &data)

	if data.Calls != 6 {
		t.Errorf("Calls = %d, want 6", data.Calls)
	}

	if data.ByTool["Read"].Calls != 2 {
		t.Errorf("Read.Calls = %d, want 2", data.ByTool["Read"].Calls)
	}
	if data.ByTool["Edit"].Calls != 3 {
		t.Errorf("Edit.Calls = %d, want 3", data.ByTool["Edit"].Calls)
	}
	if data.ByTool["Bash"].Calls != 1 {
		t.Errorf("Bash.Calls = %d, want 1", data.ByTool["Bash"].Calls)
	}

	totalTokens := 0
	for _, d := range data.ByTool {
		totalTokens += d.Tokens
	}
	if totalTokens != 18000 {
		t.Errorf("total distributed tokens = %d, want 18000", totalTokens)
	}
}

func TestFlushTracker_MCPCategorization(t *testing.T) {
	store := NewSpanStore()
	now := time.Now()

	root := &Span{
		SpanID: "root", TraceID: "s3", Name: "invoke_agent",
		Start: now,
		Attrs: map[string]any{"gen_ai.conversation.id": "s3"},
	}
	store.Add(root)

	store.Add(&Span{
		SpanID: "td1", TraceID: "s3", Name: "tool_decision",
		Start: now, Attrs: map[string]any{
			"gen_ai.conversation.id": "s3",
			"tool_name":             "mcp__JirAIa__search",
			"tool_use_id":           "tu1",
		},
	})
	store.Add(&Span{
		SpanID: "td2", TraceID: "s3", Name: "tool_decision",
		Start: now, Attrs: map[string]any{
			"gen_ai.conversation.id": "s3",
			"tool_name":             "Read",
			"tool_use_id":           "tu2",
		},
	})
	store.Add(&Span{
		SpanID: "td3", TraceID: "s3", Name: "tool_decision",
		Start: now, Attrs: map[string]any{
			"gen_ai.conversation.id": "s3",
			"tool_name":             "Agent",
			"tool_use_id":           "tu3",
		},
	})

	store.Add(&Span{
		SpanID: "api1", TraceID: "s3", Name: "api_request",
		Start: now, Attrs: map[string]any{
			"gen_ai.conversation.id":     "s3",
			"gen_ai.usage.input_tokens":  int64(9000),
			"gen_ai.usage.output_tokens": int64(0),
		},
	})

	dir := t.TempDir()
	_ = FlushTracker(store, dir)

	b, _ := os.ReadFile(filepath.Join(dir, "s3.json")) // #nosec G304
	var data TrackerData
	_ = json.Unmarshal(b, &data)

	if data.ByTool["mcp__JirAIa__search"].Calls != 1 {
		t.Error("MCP tool not tracked")
	}
	if data.ByTool["Agent"].Calls != 1 {
		t.Error("Agent tool not tracked")
	}

	if data.MCP == 0 {
		t.Error("MCP token count should be > 0")
	}
	if data.Agents == 0 {
		t.Error("Agents token count should be > 0")
	}
	if data.Tools == 0 {
		t.Error("Tools token count should be > 0")
	}
}

func TestFlushTracker_NoCallsSkipsFile(t *testing.T) {
	store := NewSpanStore()
	now := time.Now()

	root := &Span{
		SpanID: "root", TraceID: "empty-session", Name: "invoke_agent",
		Start: now,
		Attrs: map[string]any{"gen_ai.conversation.id": "empty-session"},
	}
	store.Add(root)

	dir := t.TempDir()
	_ = FlushTracker(store, dir)

	_, err := os.Stat(filepath.Join(dir, "empty-session.json"))
	if !os.IsNotExist(err) {
		t.Error("should not write file for session with zero tool calls")
	}
}

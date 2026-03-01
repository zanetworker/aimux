package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zanetworker/agentmux/internal/agent"
	agentmuxotel "github.com/zanetworker/agentmux/internal/otel"
	"github.com/zanetworker/agentmux/internal/provider"
	"github.com/zanetworker/agentmux/internal/trace"
	"github.com/zanetworker/agentmux/internal/tui/views"
)

// TestParserForProvider_FallsBackToFile verifies that the parser uses
// file-based parsing when the OTEL store is empty.
func TestParserForProvider_FallsBackToFile(t *testing.T) {
	app := App{
		otelStore: agentmuxotel.NewSpanStore(),
	}
	p := &provider.Claude{}

	parser := app.parserForProvider(p)

	// Create a minimal JSONL file
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "session.jsonl")
	data := `{"type":"user","timestamp":"2026-01-01T10:00:00Z","message":{"role":"user","content":"hello"}}
{"type":"assistant","timestamp":"2026-01-01T10:00:05Z","message":{"role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":100,"output_tokens":50}}}`
	os.WriteFile(path, []byte(data), 0o644)

	turns, err := parser(path)
	if err != nil {
		t.Fatalf("parser error: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn from file, got %d", len(turns))
	}
	if turns[0].UserLines[0] != "hello" {
		t.Errorf("UserLines = %v, want [hello]", turns[0].UserLines)
	}
}

// TestParserForProvider_PrefersOTEL verifies that when the OTEL store has
// data for the session, it is used instead of file parsing.
func TestParserForProvider_PrefersOTEL(t *testing.T) {
	store := agentmuxotel.NewSpanStore()

	// Add OTEL data for a session
	root := &agentmuxotel.Span{
		SpanID:  "root-1",
		TraceID: "session-test-otel",
		Name:    "invoke_agent",
		Attrs: map[string]any{
			"gen_ai.conversation.id": "session-test-otel",
		},
		Children: []*agentmuxotel.Span{
			{
				SpanID: "turn-1",
				Name:   "chat",
				Attrs: map[string]any{
					"gen_ai.input.messages":  "from otel",
					"gen_ai.output.messages": "otel response",
				},
			},
		},
	}
	store.Add(root)

	// Create app with the OTEL store and a session view agent
	sessionView := views.NewSessionView()
	sessionAgent := &agent.Agent{
		SessionID:    "session-test-otel",
		ProviderName: "claude",
	}

	app := App{
		otelStore:   store,
		agentsView:  views.NewAgentsView(),
		sessionView: sessionView,
	}

	// Simulate the session view having an agent
	// We can't call Open() without a real backend, but we can test
	// the parser function directly by passing the session ID through
	// the store
	_ = sessionAgent // used conceptually

	p := &provider.Claude{}
	parser := app.parserForProvider(p)

	// The parser should find OTEL data even with an empty file path
	// (because the OTEL store has data and the agentsView.Selected
	// might return nil, but the store.HasData() is true)
	// However, without a selected agent or session view agent, it
	// won't know which session to look up. Let's test with agents view.
	app.agentsView.SetAgents([]agent.Agent{
		{SessionID: "session-test-otel", ProviderName: "claude"},
	})

	turns, err := parser("")
	if err != nil {
		t.Fatalf("parser error: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn from OTEL, got %d", len(turns))
	}
	if turns[0].UserLines[0] != "from otel" {
		t.Errorf("UserLines = %v, want [from otel]", turns[0].UserLines)
	}
}

// TestParserForProvider_OTELEmptyFallsBackToFile verifies that when the
// OTEL store has data but not for this session, file parsing is used.
func TestParserForProvider_OTELEmptyFallsBackToFile(t *testing.T) {
	store := agentmuxotel.NewSpanStore()

	// Add OTEL data for a DIFFERENT session
	store.Add(&agentmuxotel.Span{
		SpanID:  "other-root",
		TraceID: "other-session",
		Name:    "invoke_agent",
		Attrs: map[string]any{
			"gen_ai.conversation.id": "other-session",
		},
	})

	app := App{
		otelStore:  store,
		agentsView: views.NewAgentsView(),
	}

	// Set selected agent to a different session
	app.agentsView.SetAgents([]agent.Agent{
		{SessionID: "my-session", ProviderName: "claude"},
	})

	p := &provider.Claude{}
	parser := app.parserForProvider(p)

	// Create a file for this session
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "session.jsonl")
	data := `{"type":"user","timestamp":"2026-01-01T10:00:00Z","message":{"role":"user","content":"from file"}}
{"type":"assistant","timestamp":"2026-01-01T10:00:05Z","message":{"role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"file response"}],"usage":{"input_tokens":50,"output_tokens":25}}}`
	os.WriteFile(path, []byte(data), 0o644)

	turns, err := parser(path)
	if err != nil {
		t.Fatalf("parser error: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn from file fallback, got %d", len(turns))
	}
	if turns[0].UserLines[0] != "from file" {
		t.Errorf("UserLines = %v, want [from file]", turns[0].UserLines)
	}
}

// TestOTELStoreLogEvents verifies that Claude-style log events get
// stored and can be converted to turns.
func TestOTELStoreLogEvents(t *testing.T) {
	store := agentmuxotel.NewSpanStore()

	// Simulate Claude log events
	userPrompt := &agentmuxotel.Span{
		SpanID:  "log-1",
		TraceID: "sess-abc",
		Name:    "claude_code.user_prompt",
		Attrs: map[string]any{
			"gen_ai.conversation.id": "sess-abc",
			"gen_ai.operation.name":  "invoke_agent",
			"gen_ai.input.messages":  "fix the bug",
			"session.id":            "sess-abc",
		},
	}
	store.Add(userPrompt)

	apiRequest := &agentmuxotel.Span{
		SpanID:  "log-2",
		TraceID: "sess-abc",
		Name:    "claude_code.api_request",
		Attrs: map[string]any{
			"gen_ai.conversation.id":    "sess-abc",
			"gen_ai.operation.name":     "chat",
			"gen_ai.request.model":      "claude-opus-4-6",
			"gen_ai.usage.input_tokens": int64(5000),
		},
	}
	store.Add(apiRequest)

	toolResult := &agentmuxotel.Span{
		SpanID:  "log-3",
		TraceID: "sess-abc",
		Name:    "claude_code.tool_result",
		Attrs: map[string]any{
			"gen_ai.conversation.id": "sess-abc",
			"gen_ai.operation.name":  "execute_tool",
			"gen_ai.tool.name":       "Read",
		},
	}
	store.Add(toolResult)

	// Verify the root span has children
	root := store.GetByConversation("sess-abc")
	if root == nil {
		t.Fatal("GetByConversation returned nil")
	}
	if len(root.Children) != 2 {
		t.Fatalf("root has %d children, want 2 (api_request + tool_result)", len(root.Children))
	}

	// Convert to turns
	turns := agentmuxotel.SpansToTurns(root)
	if len(turns) != 2 {
		t.Fatalf("SpansToTurns returned %d turns, want 2", len(turns))
	}
}

// TestLogsViewSetFilePath verifies that SetFilePath + Reload works
// for late-discovered session files.
func TestLogsViewSetFilePath(t *testing.T) {
	// Create a parser that reads Claude JSONL
	p := &provider.Claude{}
	parser := func(path string) ([]trace.Turn, error) {
		return p.ParseTrace(path)
	}

	// Create LogsView with empty path
	lv := views.NewLogsView(0, "", parser)
	if len(lv.Turns()) != 0 {
		t.Fatalf("expected 0 turns with empty path, got %d", len(lv.Turns()))
	}

	// Create a session file
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "session.jsonl")
	data := `{"type":"user","timestamp":"2026-01-01T10:00:00Z","message":{"role":"user","content":"late discovery"}}
{"type":"assistant","timestamp":"2026-01-01T10:00:05Z","message":{"role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"found it"}],"usage":{"input_tokens":50,"output_tokens":25}}}`
	os.WriteFile(path, []byte(data), 0o644)

	// Set the file path and reload
	lv.SetFilePath(path)
	lv.Reload()

	if len(lv.Turns()) != 1 {
		t.Fatalf("expected 1 turn after SetFilePath+Reload, got %d", len(lv.Turns()))
	}
	if lv.Turns()[0].UserLines[0] != "late discovery" {
		t.Errorf("UserLines = %v, want [late discovery]", lv.Turns()[0].UserLines)
	}
}

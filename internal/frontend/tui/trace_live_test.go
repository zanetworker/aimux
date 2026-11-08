package tui

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/frontend/tui/views"
	aimuxotel "github.com/zanetworker/aimux/internal/otel"
	"github.com/zanetworker/aimux/internal/provider"
)

// A transcript with no usable turns falls back to OTEL; those turns must be
// marked live so the views do not cache them against the file's stamp.
func TestParserForProvider_OTELFallbackIsLive(t *testing.T) {
	store := aimuxotel.NewSpanStore()
	store.Add(&aimuxotel.Span{
		SpanID: "root-1", TraceID: "s-live", Name: "claude_code.user_prompt",
		Attrs: map[string]any{"gen_ai.conversation.id": "s-live", "gen_ai.input.messages": "from otel", "prompt.id": "p1"},
		Children: []*aimuxotel.Span{{SpanID: "t1", Name: "claude_code.api_request",
			Attrs: map[string]any{"gen_ai.request.model": "claude-opus-4-6", "prompt.id": "p1"}}},
	})
	app := App{otelStore: store, agentsView: views.NewAgentsView()}
	app.agentsView.SetAgents([]agent.Agent{{SessionID: "s-live", ProviderName: "claude"}})
	parser := app.parserForProvider(&provider.Claude{})

	empty := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(empty, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	turns, err := parser(empty)
	if len(turns) != 1 || !errors.Is(err, views.ErrLiveTrace) {
		t.Fatalf("OTEL fallback: %d turns, err %v; want 1 turn and ErrLiveTrace", len(turns), err)
	}

	full := filepath.Join(t.TempDir(), "full.jsonl")
	data := `{"type":"user","timestamp":"2026-01-01T10:00:00Z","message":{"role":"user","content":"hello"}}
{"type":"assistant","timestamp":"2026-01-01T10:00:05Z","message":{"role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1,"output_tokens":1}}}`
	if err := os.WriteFile(full, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if turns, err := parser(full); err != nil || len(turns) != 1 {
		t.Errorf("file-backed: %d turns, err %v; want 1 turn and no error", len(turns), err)
	}

	// The remote parser reads OTEL only: always live.
	remote := app.parserForRemote("s-live", "")
	if turns, err := remote(full); len(turns) == 0 || !errors.Is(err, views.ErrLiveTrace) {
		t.Errorf("remote: %d turns, err %v; want turns and ErrLiveTrace", len(turns), err)
	}
}

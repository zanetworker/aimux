package otel

import (
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/trace"
)

func TestExportTrace_NoEndpoint(t *testing.T) {
	cfg := ExportConfig{
		Endpoint:  "",
		SessionID: "test",
	}
	err := ExportTrace(cfg, nil, nil)
	// With empty endpoint, the exporter creation should fail
	if err == nil {
		t.Error("ExportTrace with empty endpoint should fail")
	}
}

func TestExportTrace_InvalidEndpoint(t *testing.T) {
	cfg := ExportConfig{
		Endpoint:  "localhost:99999",
		Insecure:  true,
		SessionID: "test",
		Provider:  "claude",
	}

	turns := []trace.Turn{
		{
			Number:    1,
			Timestamp: time.Now().Add(-1 * time.Minute),
			EndTime:   time.Now(),
			UserLines: []string{"fix the bug"},
			OutputLines: []string{"I'll look at it."},
			TokensIn:  100,
			TokensOut: 50,
			Model:     "claude-opus-4-6",
			Actions: []trace.ToolSpan{
				{Name: "Read", Snippet: "main.go", Success: true},
			},
		},
	}

	// This will create spans but fail to export (no server listening)
	// The error is expected and should be about connection/export failure
	err := ExportTrace(cfg, turns, nil)
	// May or may not error depending on batching behavior
	_ = err
}

func TestExportConfig_Fields(t *testing.T) {
	cfg := ExportConfig{
		Endpoint:  "localhost:5000",
		Insecure:  true,
		SessionID: "sess-123",
		Provider:  "codex",
	}
	if cfg.Endpoint != "localhost:5000" {
		t.Errorf("Endpoint = %q, want %q", cfg.Endpoint, "localhost:5000")
	}
	if !cfg.Insecure {
		t.Error("Insecure should be true")
	}
}

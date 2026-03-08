package controller

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/evaluation"
	"github.com/zanetworker/aimux/internal/history"
)

func TestExportJSONL_Success(t *testing.T) {
	dir := t.TempDir()

	// Create a session file with metadata
	sessionFile := filepath.Join(dir, "test-session.jsonl")
	os.WriteFile(sessionFile, []byte("{}"), 0o644)
	history.SaveMeta(sessionFile, history.Meta{
		Annotation: "failed",
		Tags:       []string{"loop-on-error"},
		Note:       "agent looped",
		Title:      "Fix markdown rendering",
	})

	// Override export path by setting session ID
	ctrl := New(config.Default())
	ctx := ExportContext{
		SessionID:   "test-export-123",
		SessionFile: sessionFile,
		Turns: []TraceInput{
			{
				Number:    1,
				Timestamp: "2026-03-08T10:00:00Z",
				UserText:  "fix the bug",
				OutputText: "I'll fix that.",
				TokensIn:  1000,
				TokensOut: 500,
				CostUSD:   0.05,
				Actions: []ActionInput{
					{Tool: "Read", Input: "main.go", Success: true},
				},
			},
		},
	}

	result, err := ctrl.ExportJSONL(ctx)
	if err != nil {
		t.Fatalf("ExportJSONL: %v", err)
	}
	if result.Count != 1 {
		t.Errorf("Count = %d, want 1", result.Count)
	}
	if result.Path == "" {
		t.Error("expected non-empty path")
	}

	// Read the export file and verify session_meta line
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines (meta + turn), got %d", len(lines))
	}

	// First line: session_meta
	var meta evaluation.ExportSessionMeta
	if err := json.Unmarshal([]byte(lines[0]), &meta); err != nil {
		t.Fatalf("unmarshal session_meta: %v", err)
	}
	if meta.Type != "session_meta" {
		t.Errorf("meta.Type = %q, want session_meta", meta.Type)
	}
	if meta.Annotation != "failed" {
		t.Errorf("meta.Annotation = %q, want failed", meta.Annotation)
	}
	if len(meta.FailureModes) != 1 || meta.FailureModes[0] != "loop-on-error" {
		t.Errorf("meta.FailureModes = %v, want [loop-on-error]", meta.FailureModes)
	}

	// Second line: turn data
	var turn evaluation.ExportTurn
	if err := json.Unmarshal([]byte(lines[1]), &turn); err != nil {
		t.Fatalf("unmarshal turn: %v", err)
	}
	if turn.Turn != 1 {
		t.Errorf("turn.Turn = %d, want 1", turn.Turn)
	}
	if turn.Input != "fix the bug" {
		t.Errorf("turn.Input = %q, want 'fix the bug'", turn.Input)
	}

	// Cleanup
	os.Remove(result.Path)
}

func TestExportJSONL_NoTurns(t *testing.T) {
	ctrl := New(config.Default())
	_, err := ctrl.ExportJSONL(ExportContext{SessionID: "empty"})
	if err == nil {
		t.Error("expected error for empty turns")
	}
}

func TestExportJSONL_NoSessionID(t *testing.T) {
	ctrl := New(config.Default())
	_, err := ctrl.ExportJSONL(ExportContext{
		Turns: []TraceInput{{Number: 1, Timestamp: "2026-03-08T10:00:00Z"}},
	})
	if err == nil {
		t.Error("expected error for empty session ID")
	}
}

func TestExportJSONL_NoMeta(t *testing.T) {
	ctrl := New(config.Default())
	ctx := ExportContext{
		SessionID: "no-meta-test",
		Turns: []TraceInput{
			{Number: 1, Timestamp: "2026-03-08T10:00:00Z", UserText: "hello"},
		},
	}

	result, err := ctrl.ExportJSONL(ctx)
	if err != nil {
		t.Fatalf("ExportJSONL: %v", err)
	}

	// Without session file, no session_meta line
	data, _ := os.ReadFile(result.Path)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Errorf("expected 1 line (turn only, no meta), got %d", len(lines))
	}

	os.Remove(result.Path)
}

func TestExportOTEL_NoEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.Export.Endpoint = ""
	ctrl := New(cfg)

	_, err := ctrl.ExportOTEL(ExportContext{
		SessionID: "test",
		Turns:     []TraceInput{{Number: 1, Timestamp: "2026-03-08T10:00:00Z"}},
	})
	if err == nil {
		t.Error("expected error for missing endpoint")
	}
	if !strings.Contains(err.Error(), "endpoint") {
		t.Errorf("error should mention endpoint, got: %v", err)
	}
}

func TestBuildExportTurns(t *testing.T) {
	ctx := ExportContext{
		Turns: []TraceInput{
			{
				Number:     1,
				Timestamp:  "2026-03-08T10:00:00Z",
				UserText:   "input",
				OutputText: "output",
				TokensIn:   100,
				TokensOut:  50,
				CostUSD:    0.01,
				DurationMs: 5000,
				Actions: []ActionInput{
					{Tool: "Read", Input: "file.go", Success: true},
					{Tool: "Edit", Input: "file.go", Success: false, Error: "conflict"},
				},
			},
		},
	}

	turns := buildExportTurns(ctx)
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}

	et := turns[0]
	if et.Turn != 1 {
		t.Errorf("Turn = %d, want 1", et.Turn)
	}
	if et.Input != "input" {
		t.Errorf("Input = %q, want input", et.Input)
	}
	if len(et.Actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(et.Actions))
	}
	if et.Actions[1].Error != "conflict" {
		t.Errorf("Actions[1].Error = %q, want conflict", et.Actions[1].Error)
	}
}

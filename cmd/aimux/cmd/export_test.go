package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/trace"
)

func TestExportCmd_InvalidFormat(t *testing.T) {
	discover := func(_ history.DiscoverOpts, _ string) ([]history.Session, error) {
		return nil, nil
	}
	c := newExportCmd(discover, nil)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"export", "abc123", "--type", "csv"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for invalid format")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("invalid export type")) {
		t.Errorf("error should mention invalid format, got: %s", err.Error())
	}
}

func TestExportCmd_SessionNotFound(t *testing.T) {
	sessions := []history.Session{
		{ID: "deadbeef-1234", Provider: "claude", FilePath: "/tmp/test.jsonl"},
	}
	discover := func(_ history.DiscoverOpts, _ string) ([]history.Session, error) {
		return sessions, nil
	}
	c := newExportCmd(discover, nil)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"export", "zzz-no-match"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for no matching session")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("no session found")) {
		t.Errorf("error should mention no session found, got: %s", err.Error())
	}
}

func TestExportCmd_AmbiguousPrefix(t *testing.T) {
	sessions := []history.Session{
		{ID: "abc-001", Provider: "claude", FilePath: "/tmp/a.jsonl"},
		{ID: "abc-002", Provider: "claude", FilePath: "/tmp/b.jsonl"},
	}
	discover := func(_ history.DiscoverOpts, _ string) ([]history.Session, error) {
		return sessions, nil
	}
	c := newExportCmd(discover, nil)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"export", "abc"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for ambiguous prefix")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("ambiguous")) {
		t.Errorf("error should mention ambiguous, got: %s", err.Error())
	}
}

func TestExportCmd_JSONL_JSON(t *testing.T) {
	// Create a temp file to serve as the session file (not parsed, but needed for path).
	tmpDir := t.TempDir()
	sessionFile := filepath.Join(tmpDir, "test-session.jsonl")
	if err := os.WriteFile(sessionFile, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	sessions := []history.Session{
		{ID: "test-export-session-001", Provider: "claude", FilePath: sessionFile, Project: "/tmp/proj"},
	}
	discover := func(_ history.DiscoverOpts, _ string) ([]history.Session, error) {
		return sessions, nil
	}

	now := time.Now()
	parsers := map[string]traceParserFn{
		"claude": func(_ string) ([]trace.Turn, error) {
			return []trace.Turn{
				{
					Number:    1,
					Timestamp: now,
					EndTime:   now.Add(5 * time.Second),
					UserLines: []string{"hello"},
					OutputLines: []string{"world"},
					TokensIn:  100,
					TokensOut: 200,
					CostUSD:   0.01,
					Model:     "claude-sonnet-4-20250514",
				},
			}, nil
		},
	}

	var stdout bytes.Buffer
	c := newExportCmd(discover, parsers)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"export", "test-export-session-001"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if result["session_id"] != "test-export-session-001" {
		t.Errorf("session_id=%q, want %q", result["session_id"], "test-export-session-001")
	}
	if result["type"] != "jsonl" {
		t.Errorf("type=%q, want %q", result["type"], "jsonl")
	}
	if turns, ok := result["turns"].(float64); !ok || turns != 1 {
		t.Errorf("turns=%v, want 1", result["turns"])
	}
	// Clean up the export file.
	if path, ok := result["path"].(string); ok {
		_ = os.Remove(path)
	}
}

func TestExportCmd_MissingArgs(t *testing.T) {
	discover := func(_ history.DiscoverOpts, _ string) ([]history.Session, error) {
		return nil, nil
	}
	c := newExportCmd(discover, nil)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"export"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing session ID argument")
	}
}

func TestExportCmd_NoParser(t *testing.T) {
	sessions := []history.Session{
		{ID: "sess-no-parser", Provider: "unknown-provider", FilePath: "/tmp/test.jsonl"},
	}
	discover := func(_ history.DiscoverOpts, _ string) ([]history.Session, error) {
		return sessions, nil
	}
	c := newExportCmd(discover, map[string]traceParserFn{})
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"export", "sess-no-parser"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing parser")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("no trace parser")) {
		t.Errorf("error should mention no trace parser, got: %s", err.Error())
	}
}

func TestTurnsToInputs(t *testing.T) {
	now := time.Now()
	turns := []trace.Turn{
		{
			Number:      1,
			Timestamp:   now,
			EndTime:     now.Add(3 * time.Second),
			UserLines:   []string{"first", "second"},
			OutputLines: []string{"response"},
			TokensIn:    50,
			TokensOut:   100,
			CostUSD:     0.005,
			Model:       "claude-sonnet-4-20250514",
			Actions: []trace.ToolSpan{
				{Name: "Read", Snippet: "file.go", Success: true},
				{Name: "Bash", Snippet: "go test", Success: false, ErrorMsg: "exit 1"},
			},
		},
	}

	inputs := turnsToInputs(turns)
	if len(inputs) != 1 {
		t.Fatalf("len(inputs)=%d, want 1", len(inputs))
	}
	in := inputs[0]
	if in.Number != 1 {
		t.Errorf("Number=%d, want 1", in.Number)
	}
	if in.UserText != "first\nsecond" {
		t.Errorf("UserText=%q, want %q", in.UserText, "first\nsecond")
	}
	if in.OutputText != "response" {
		t.Errorf("OutputText=%q, want %q", in.OutputText, "response")
	}
	if in.DurationMs != 3000 {
		t.Errorf("DurationMs=%d, want 3000", in.DurationMs)
	}
	if len(in.Actions) != 2 {
		t.Fatalf("len(Actions)=%d, want 2", len(in.Actions))
	}
	if in.Actions[0].Tool != "Read" || !in.Actions[0].Success {
		t.Errorf("Actions[0] mismatch: %+v", in.Actions[0])
	}
	if in.Actions[1].Tool != "Bash" || in.Actions[1].Success || in.Actions[1].Error != "exit 1" {
		t.Errorf("Actions[1] mismatch: %+v", in.Actions[1])
	}
}

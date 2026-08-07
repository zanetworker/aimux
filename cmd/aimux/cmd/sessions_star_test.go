package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/zanetworker/aimux/internal/history"
)

func TestSessionsStarCmd_ToggleOn(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(sessionFile, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	sessions := []history.Session{
		{ID: "abc-123", FilePath: sessionFile},
	}
	discover := func(_ history.DiscoverOpts, _ string) ([]history.Session, error) {
		return sessions, nil
	}

	parent := newSessionsCmd(discover, nil, nil, nil)
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"sessions", "star", "abc-123", "--json"})
	rootCmd.AddCommand(parent)
	defer rootCmd.RemoveCommand(parent)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, buf.String())
	}
	if result["starred"] != true {
		t.Errorf("expected starred=true, got %v", result["starred"])
	}
	if result["session_id"] != "abc-123" {
		t.Errorf("expected session_id=abc-123, got %v", result["session_id"])
	}
}

func TestSessionsStarCmd_ToggleOff(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(sessionFile, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := history.SaveMeta(sessionFile, history.Meta{Starred: true}); err != nil {
		t.Fatal(err)
	}

	sessions := []history.Session{
		{ID: "abc-123", FilePath: sessionFile},
	}
	discover := func(_ history.DiscoverOpts, _ string) ([]history.Session, error) {
		return sessions, nil
	}

	parent := newSessionsCmd(discover, nil, nil, nil)
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"sessions", "star", "abc-123", "--json"})
	rootCmd.AddCommand(parent)
	defer rootCmd.RemoveCommand(parent)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}
	if result["starred"] != false {
		t.Errorf("expected starred=false, got %v", result["starred"])
	}
}

func TestSessionsStarCmd_NotFound(t *testing.T) {
	discover := func(_ history.DiscoverOpts, _ string) ([]history.Session, error) {
		return []history.Session{{ID: "deadbeef"}}, nil
	}

	parent := newSessionsCmd(discover, nil, nil, nil)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"sessions", "star", "zzz-no-match"})
	rootCmd.AddCommand(parent)
	defer rootCmd.RemoveCommand(parent)

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for no matching session")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("no session found")) {
		t.Errorf("error should mention no session found, got: %s", err.Error())
	}
}

func TestSessionsStarCmd_AmbiguousPrefix(t *testing.T) {
	sessions := []history.Session{
		{ID: "abc-001"},
		{ID: "abc-002"},
	}
	discover := func(_ history.DiscoverOpts, _ string) ([]history.Session, error) {
		return sessions, nil
	}

	parent := newSessionsCmd(discover, nil, nil, nil)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"sessions", "star", "abc"})
	rootCmd.AddCommand(parent)
	defer rootCmd.RemoveCommand(parent)

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for ambiguous prefix")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("ambiguous")) {
		t.Errorf("error should mention ambiguous, got: %s", err.Error())
	}
}

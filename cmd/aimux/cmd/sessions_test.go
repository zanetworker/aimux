package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/history"
)

func fakeSessions() []history.Session {
	return []history.Session{
		{
			ID:          "sess-001",
			Provider:    "claude",
			Project:     "/home/user/project-a",
			TurnCount:   15,
			CostUSD:     1.23,
			LastActive:  time.Now().Add(-1 * time.Hour),
			FirstPrompt: "fix the tests",
			Title:       "Test fixes",
		},
		{
			ID:          "sess-002",
			Provider:    "codex",
			Project:     "/home/user/project-b",
			TurnCount:   8,
			CostUSD:     0.45,
			LastActive:  time.Now().Add(-2 * time.Hour),
			FirstPrompt: "add logging",
			Title:       "Logging feature",
		},
	}
}

func TestSessionsCmd_JSON(t *testing.T) {
	var stdout bytes.Buffer
	sessions := fakeSessions()
	c := newSessionsCmd(
		func(opts history.DiscoverOpts, dir string) ([]history.Session, error) { return sessions, nil },
		nil, nil,
	)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"sessions", "--list"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result struct {
		Sessions []struct {
			ID string `json:"id"`
		} `json:"sessions"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v\nraw: %s", err, stdout.String())
	}
	if result.Count != 2 {
		t.Errorf("count=%d, want 2", result.Count)
	}
}

func TestSessionsCmd_Limit(t *testing.T) {
	var stdout bytes.Buffer
	sessions := fakeSessions()
	c := newSessionsCmd(
		func(opts history.DiscoverOpts, dir string) ([]history.Session, error) { return sessions, nil },
		nil, nil,
	)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"sessions", "--list", "--limit", "1"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result struct {
		Sessions  []any `json:"sessions"`
		Count     int   `json:"count"`
		Total     int   `json:"total"`
		Truncated bool  `json:"truncated"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if result.Count != 1 {
		t.Errorf("count=%d, want 1", result.Count)
	}
	if result.Total != 2 {
		t.Errorf("total=%d, want 2", result.Total)
	}
	if !result.Truncated {
		t.Error("expected truncated=true")
	}
}

func TestSessionsCmd_AllFlag(t *testing.T) {
	sessions := fakeSessions()
	var capturedDir string
	c := newSessionsCmd(
		func(opts history.DiscoverOpts, dir string) ([]history.Session, error) {
			capturedDir = opts.Dir
			return sessions, nil
		},
		nil, nil,
	)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"sessions", "--list", "--all"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedDir != "" {
		t.Errorf("--all should set dir to empty, got %q", capturedDir)
	}
}

func TestSessionsCmd_DefaultScopesToCWD(t *testing.T) {
	sessions := fakeSessions()
	var capturedDir string
	c := newSessionsCmd(
		func(opts history.DiscoverOpts, dir string) ([]history.Session, error) {
			capturedDir = opts.Dir
			return sessions, nil
		},
		nil, nil,
	)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"sessions", "--list"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedDir == "" {
		t.Error("without --all, dir should default to CWD, got empty")
	}
}

func TestSessionsCmd_Export(t *testing.T) {
	var stdout bytes.Buffer
	sessions := fakeSessions()
	c := newSessionsCmd(
		func(opts history.DiscoverOpts, dir string) ([]history.Session, error) { return sessions, nil },
		nil, nil,
	)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"sessions", "--export"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lines := bytes.Split(bytes.TrimSpace(stdout.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Errorf("expected 2 JSONL lines, got %d", len(lines))
	}
}

func TestSessionsList_HidesAutomatedUnlessAsked(t *testing.T) {
	sessions := fakeSessions()
	sessions[1].Automated = true
	run := func(args ...string) string {
		var stdout bytes.Buffer
		c := newSessionsCmd(func(history.DiscoverOpts, string) ([]history.Session, error) { return sessions, nil }, nil, nil)
		rootCmd.SetOut(&stdout)
		rootCmd.SetErr(&bytes.Buffer{})
		rootCmd.SetArgs(append([]string{"sessions", "--list", "--all"}, args...))
		rootCmd.AddCommand(c)
		defer rootCmd.RemoveCommand(c)
		if err := rootCmd.Execute(); err != nil {
			t.Fatal(err)
		}
		return stdout.String()
	}
	jsonOutput = true
	defer func() { jsonOutput = false }()
	if out := run(); strings.Contains(out, "sess-002") || !strings.Contains(out, "sess-001") {
		t.Errorf("default list should hide the automated session:\n%s", out)
	}
	if out := run("--include-automated"); !strings.Contains(out, "sess-002") {
		t.Errorf("--include-automated should show it:\n%s", out)
	}
}

func TestSessionsExport_KeepsAutomatedSessions(t *testing.T) {
	sessions := fakeSessions()
	sessions[1].Automated = true
	var stdout bytes.Buffer
	c := newSessionsCmd(func(history.DiscoverOpts, string) ([]history.Session, error) { return sessions, nil }, nil, nil)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"sessions", "--export", "--all"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)
	if err := rootCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "sess-002") {
		t.Errorf("--export must keep automated sessions (evaluation pipelines rely on the full set):\n%s", stdout.String())
	}
}

package cmd

import (
	"bytes"
	"encoding/json"
	"testing"
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/spawn"
)

func testDeps() Deps {
	return Deps{
		Discover:         func() ([]agent.Agent, error) { return nil, nil },
		DiscoverSessions: func(_ history.DiscoverOpts, _ string) ([]history.Session, error) { return fakeSessions(), nil },
		SearchContent:    func(_, _ string) ([]history.ContentMatch, error) { return nil, nil },
		PickSession:      func(_ []history.Session) (history.Session, error) { return history.Session{}, nil },
		ResumeBuilder:    func(_ string, _ bool) (string, string, error) { return "", "", nil },
		ResumeExec:       func(_ string, _ bool) {},
		SpawnAgent:       func(_ spawn.LaunchOpts) (int, string, error) { return 42, "aimux-test", nil },
		Providers:        []string{"claude", "codex"},
		AgentConfigs: []config.AgentConfig{
			{Name: "reviewer", Harness: "claude", Model: "opus", Prompt: "review code"},
		},
		Environments: map[string]config.EnvironmentConfig{
			"local": {Type: "local"},
		},
	}
}

func TestSessionsStart_DryRun_JSON(t *testing.T) {
	var stdout bytes.Buffer
	d := testDeps()
	cmd := newSessionsParentCmd(d)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"sessions", "start", "claude", "--dry-run"})
	rootCmd.AddCommand(cmd)
	defer rootCmd.RemoveCommand(cmd)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v\nraw: %s", err, stdout.String())
	}
	if result["dry_run"] != true {
		t.Error("expected dry_run=true")
	}
	if result["provider"] != "claude" {
		t.Errorf("provider=%q, want %q", result["provider"], "claude")
	}
}

func TestSessionsStart_NamedConfig_DryRun(t *testing.T) {
	var stdout bytes.Buffer
	d := testDeps()
	cmd := newSessionsParentCmd(d)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"sessions", "start", "reviewer", "--dry-run"})
	rootCmd.AddCommand(cmd)
	defer rootCmd.RemoveCommand(cmd)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v\nraw: %s", err, stdout.String())
	}
	if result["model"] != "opus" {
		t.Errorf("model=%q, want %q (should inherit from agent config)", result["model"], "opus")
	}
}

func TestSessionsStart_WithEnv_DryRun(t *testing.T) {
	var stdout bytes.Buffer
	d := testDeps()
	cmd := newSessionsParentCmd(d)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"sessions", "start", "reviewer", "--environment", "local", "--dry-run"})
	rootCmd.AddCommand(cmd)
	defer rootCmd.RemoveCommand(cmd)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v\nraw: %s", err, stdout.String())
	}
	if result["dry_run"] != true {
		t.Error("expected dry_run=true")
	}
}

func TestSessionsList_JSON(t *testing.T) {
	var stdout bytes.Buffer
	d := testDeps()
	cmd := newSessionsParentCmd(d)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"sessions", "list", "--list"})
	rootCmd.AddCommand(cmd)
	defer rootCmd.RemoveCommand(cmd)

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

func TestSessionsList_BareCommand_JSON(t *testing.T) {
	var stdout bytes.Buffer
	d := testDeps()
	cmd := newSessionsParentCmd(d)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"sessions", "--list"})
	rootCmd.AddCommand(cmd)
	defer rootCmd.RemoveCommand(cmd)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v\nraw: %s", err, stdout.String())
	}
	if result.Count != 2 {
		t.Errorf("count=%d, want 2", result.Count)
	}
}

func TestSessionsStop_NonexistentPID(t *testing.T) {
	d := testDeps()
	cmd := newSessionsParentCmd(d)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"sessions", "stop", "999999999"})
	rootCmd.AddCommand(cmd)
	defer rootCmd.RemoveCommand(cmd)

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for nonexistent PID")
	}
}

func TestSessionsStop_InvalidPID(t *testing.T) {
	d := testDeps()
	cmd := newSessionsParentCmd(d)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"sessions", "stop", "not-a-pid"})
	rootCmd.AddCommand(cmd)
	defer rootCmd.RemoveCommand(cmd)

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for non-numeric PID")
	}
}

func TestSessionsArchive_Found(t *testing.T) {
	var stdout bytes.Buffer
	d := testDeps()
	cmd := newSessionsParentCmd(d)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"sessions", "archive", "sess-001"})
	rootCmd.AddCommand(cmd)
	defer rootCmd.RemoveCommand(cmd)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v\nraw: %s", err, stdout.String())
	}
	if result["archived"] != true {
		t.Error("expected archived=true")
	}
	if result["session_id"] != "sess-001" {
		t.Errorf("session_id=%q, want %q", result["session_id"], "sess-001")
	}
}

func TestSessionsArchive_NotFound(t *testing.T) {
	d := testDeps()
	cmd := newSessionsParentCmd(d)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"sessions", "archive", "nonexistent"})
	rootCmd.AddCommand(cmd)
	defer rootCmd.RemoveCommand(cmd)

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestSpawnAlias_DryRun(t *testing.T) {
	var stdout bytes.Buffer
	d := testDeps()
	alias := newHiddenSpawnAlias(d)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"spawn", "reviewer", "--dry-run"})
	rootCmd.AddCommand(alias)
	defer rootCmd.RemoveCommand(alias)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v\nraw: %s", err, stdout.String())
	}
	if result["dry_run"] != true {
		t.Error("expected dry_run=true in spawn alias output")
	}
}

func TestResumeAlias_DryRun(t *testing.T) {
	var stdout bytes.Buffer
	alias := newHiddenResumeAlias(func(id string, danger bool) (string, string, error) {
		return "claude --resume " + id, "/tmp/project", nil
	}, nil, false)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"resume", "abc-123", "--dry-run"})
	rootCmd.AddCommand(alias)
	defer rootCmd.RemoveCommand(alias)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v\nraw: %s", err, stdout.String())
	}
	if result["dry_run"] != true {
		t.Error("expected dry_run=true in resume alias output")
	}
}

func TestSessionsResume_DryRun(t *testing.T) {
	var stdout bytes.Buffer
	d := testDeps()
	d.ResumeBuilder = func(id string, danger bool) (string, string, error) {
		return "claude --resume " + id, "/tmp/project", nil
	}
	cmd := newSessionsParentCmd(d)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"sessions", "resume", "abc-123", "--dry-run"})
	rootCmd.AddCommand(cmd)
	defer rootCmd.RemoveCommand(cmd)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v\nraw: %s", err, stdout.String())
	}
	if result["dry_run"] != true {
		t.Error("expected dry_run=true")
	}
}

// fakeSessions is used by sessions_test.go; verify the import works here too.
func TestFakeSessionsAvailable(t *testing.T) {
	sessions := fakeSessions()
	if len(sessions) != 2 {
		t.Fatalf("expected 2 fake sessions, got %d", len(sessions))
	}
}

// Ensure sessions returned from discover have expected fields.
func TestSessionsParent_Subcommands(t *testing.T) {
	d := testDeps()
	cmd := newSessionsParentCmd(d)

	expected := map[string]bool{
		"start":   false,
		"stop":    false,
		"resume":  false,
		"list":    false,
		"archive": false,
		"star":    false,
	}
	for _, sub := range cmd.Commands() {
		name := sub.Name()
		if _, ok := expected[name]; ok {
			expected[name] = true
		}
	}
	for name, found := range expected {
		if !found {
			t.Errorf("missing subcommand %q on sessions parent", name)
		}
	}
}

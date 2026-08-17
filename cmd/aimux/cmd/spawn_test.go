package cmd

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/zanetworker/aimux/internal/spawn"
)

func TestSpawnCmd_DryRun_JSON(t *testing.T) {
	var stdout bytes.Buffer
	c := newSpawnCmd(
		[]string{"claude", "codex"},
		func(opts spawn.LaunchOpts) (int, string, error) {
			return 0, "", nil
		},
		"",
	)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"spawn", "claude", "--dry-run"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if result["dry_run"] != true {
		t.Error("expected dry_run=true")
	}
	if result["provider"] != "claude" {
		t.Errorf("provider=%q, want %q", result["provider"], "claude")
	}
}

func TestSpawnCmd_InvalidProvider(t *testing.T) {
	c := newSpawnCmd(
		[]string{"claude", "codex"},
		nil,
		"",
	)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"spawn", "gpt"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for invalid provider")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("claude")) {
		t.Errorf("error should list valid providers, got: %s", err.Error())
	}
}

func TestSpawnCmd_DryRun_Wait(t *testing.T) {
	var stdout bytes.Buffer
	c := newSpawnCmd(
		[]string{"claude", "codex"},
		func(opts spawn.LaunchOpts) (int, string, error) {
			return 0, "", nil
		},
		"",
	)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"spawn", "claude", "--dry-run", "--wait"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if result["wait"] != true {
		t.Error("expected wait=true in dry-run output")
	}
}

func TestSpawnCmd_DefaultMode(t *testing.T) {
	var stdout bytes.Buffer
	c := newSpawnCmd(
		[]string{"claude"},
		func(opts spawn.LaunchOpts) (int, string, error) {
			return 0, "", nil
		},
		"bypass",
	)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"spawn", "claude", "--dry-run"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if result["mode"] != "bypass" {
		t.Errorf("mode=%q, want %q (should use default_mode from config)", result["mode"], "bypass")
	}
}

func TestSpawnCmd_ExplicitModeOverridesDefault(t *testing.T) {
	var stdout bytes.Buffer
	c := newSpawnCmd(
		[]string{"claude"},
		func(opts spawn.LaunchOpts) (int, string, error) {
			return 0, "", nil
		},
		"bypass",
	)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"spawn", "claude", "--dry-run", "--mode", "plan"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if result["mode"] != "plan" {
		t.Errorf("mode=%q, want %q (explicit --mode should override default)", result["mode"], "plan")
	}
}

func TestSpawnCmd_MissingProvider(t *testing.T) {
	c := newSpawnCmd([]string{"claude", "codex"}, nil, "")
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"spawn"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing provider arg")
	}
}

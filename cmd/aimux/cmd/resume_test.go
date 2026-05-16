package cmd

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestResumeCmd_DryRun_JSON(t *testing.T) {
	var stdout bytes.Buffer
	c := newResumeCmd(func(id string, danger bool) (string, string, error) {
		return "claude --resume " + id, "/tmp/project", nil
	}, nil, false)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"resume", "abc-123", "--dry-run"})
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
	if result["command"] != "claude --resume abc-123" {
		t.Errorf("command=%q, want %q", result["command"], "claude --resume abc-123")
	}
}

func TestResumeCmd_MissingID(t *testing.T) {
	c := newResumeCmd(nil, nil, false)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"resume"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing session ID")
	}
}

func TestResumeCmd_DryRun_Text(t *testing.T) {
	var stdout bytes.Buffer
	c := newResumeCmd(func(id string, danger bool) (string, string, error) {
		return "claude --resume " + id, "/tmp/proj", nil
	}, nil, false)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = false
	rootCmd.SetArgs([]string{"resume", "xyz-789", "--dry-run"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := stdout.String()
	if !bytes.Contains([]byte(out), []byte("Would run:")) {
		t.Errorf("expected 'Would run:' in output, got %q", out)
	}
}

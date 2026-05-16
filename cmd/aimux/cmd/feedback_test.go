package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFeedbackCmd_WritesJSONL(t *testing.T) {
	var stdout bytes.Buffer
	dir := t.TempDir()
	path := filepath.Join(dir, "feedback.jsonl")

	c := newFeedbackCmd(path)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"feedback", "test feedback message"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(path) // #nosec G304
	if err != nil {
		t.Fatalf("read feedback file: %v", err)
	}

	var entry feedbackEntry
	if err := json.Unmarshal(bytes.TrimSpace(data), &entry); err != nil {
		t.Fatalf("not valid JSON: %v\nraw: %s", err, data)
	}
	if entry.Text != "test feedback message" {
		t.Errorf("text=%q, want %q", entry.Text, "test feedback message")
	}
	if entry.Timestamp == "" {
		t.Error("timestamp should not be empty")
	}
	if entry.OS == "" {
		t.Error("os should not be empty")
	}
}

func TestFeedbackCmd_JSON(t *testing.T) {
	var stdout bytes.Buffer
	dir := t.TempDir()
	path := filepath.Join(dir, "feedback.jsonl")

	c := newFeedbackCmd(path)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"feedback", "json test"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if result["status"] != "recorded" {
		t.Errorf("status=%v, want 'recorded'", result["status"])
	}
}

func TestFeedbackCmd_Appends(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "feedback.jsonl")

	for i, msg := range []string{"first", "second"} {
		c := newFeedbackCmd(path)
		rootCmd.SetOut(&bytes.Buffer{})
		rootCmd.SetErr(&bytes.Buffer{})
		rootCmd.SetArgs([]string{"feedback", msg})
		rootCmd.AddCommand(c)
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		rootCmd.RemoveCommand(c)
	}

	data, _ := os.ReadFile(path) // #nosec G304
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 lines, got %d", len(lines))
	}
}

func TestFeedbackCmd_MissingText(t *testing.T) {
	c := newFeedbackCmd("")
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"feedback"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing feedback text")
	}
}

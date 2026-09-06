package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateAgentName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"reviewer", false},
		{"my-agent", false},
		{"agent1", false},
		{"A1-test", false},
		{"", true},
		{"-bad", true},
		{"has space", true},
		{"has_underscore", true},
		{"has.dot", true},
	}
	for _, tt := range tests {
		err := ValidateAgentName(tt.name)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateAgentName(%q) err=%v, wantErr=%v", tt.name, err, tt.wantErr)
		}
	}
}

func TestLoadAgentDir_MultipleFiles(t *testing.T) {
	dir := t.TempDir()

	writeAgentFile(t, dir, "reviewer.agent.yaml", `
name: reviewer
harness: claude
model: opus
prompt: "Review code"
`)
	writeAgentFile(t, dir, "writer.agent.yaml", `
name: writer
harness: codex
model: o3
`)
	// Non-agent file should be ignored
	writeAgentFile(t, dir, "notes.yaml", `some: data`)

	agents, err := LoadAgentDir(dir)
	if err != nil {
		t.Fatalf("LoadAgentDir: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(agents))
	}
	if agents[0].Name != "reviewer" {
		t.Errorf("agents[0].Name = %q, want reviewer", agents[0].Name)
	}
	if agents[0].Harness != "claude" {
		t.Errorf("agents[0].Harness = %q, want claude", agents[0].Harness)
	}
	if agents[1].Name != "writer" {
		t.Errorf("agents[1].Name = %q, want writer", agents[1].Name)
	}
}

func TestLoadAgentDir_NonexistentDir(t *testing.T) {
	agents, err := LoadAgentDir("/nonexistent/dir")
	if err != nil {
		t.Errorf("expected nil error for missing dir, got %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected empty, got %d", len(agents))
	}
}

func TestLoadAgentDir_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	agents, err := LoadAgentDir(dir)
	if err != nil {
		t.Fatalf("LoadAgentDir: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected empty, got %d", len(agents))
	}
}

func TestLoadAgentDir_InfersNameFromFilename(t *testing.T) {
	dir := t.TempDir()
	writeAgentFile(t, dir, "my-bot.agent.yaml", `
harness: claude
model: sonnet
`)
	agents, err := LoadAgentDir(dir)
	if err != nil {
		t.Fatalf("LoadAgentDir: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].Name != "my-bot" {
		t.Errorf("Name = %q, want my-bot (inferred from filename)", agents[0].Name)
	}
}

func TestSaveAgent_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "agents")

	ac := AgentConfig{
		Name:    "test-agent",
		Harness: "claude-sdk",
		Model:   "opus",
		Prompt:  "Do things.",
	}
	if err := SaveAgent(agentDir, ac); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	// Verify file exists
	path := filepath.Join(agentDir, "test-agent.agent.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	if len(data) == 0 {
		t.Error("saved file is empty")
	}

	// Verify roundtrip
	agents, err := LoadAgentDir(agentDir)
	if err != nil {
		t.Fatalf("LoadAgentDir: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].Name != "test-agent" {
		t.Errorf("Name = %q, want test-agent", agents[0].Name)
	}
	if agents[0].Harness != "claude-sdk" {
		t.Errorf("Harness = %q, want claude-sdk", agents[0].Harness)
	}
	if agents[0].Model != "opus" {
		t.Errorf("Model = %q, want opus", agents[0].Model)
	}
}

func TestSaveAgent_EmptyNameFails(t *testing.T) {
	dir := t.TempDir()
	err := SaveAgent(dir, AgentConfig{Name: ""})
	if err == nil {
		t.Error("expected error for empty name")
	}
}

func TestSaveAgent_InvalidNameFails(t *testing.T) {
	dir := t.TempDir()
	err := SaveAgent(dir, AgentConfig{Name: "has space"})
	if err == nil {
		t.Error("expected error for invalid name")
	}
}

func TestDeleteAgent_Success(t *testing.T) {
	dir := t.TempDir()
	ac := AgentConfig{Name: "doomed", Harness: "claude"}
	if err := SaveAgent(dir, ac); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	if err := DeleteAgent(dir, "doomed"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}

	path := filepath.Join(dir, "doomed.agent.yaml")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file should be deleted")
	}
}

func TestDeleteAgent_Nonexistent(t *testing.T) {
	dir := t.TempDir()
	err := DeleteAgent(dir, "ghost")
	if err == nil {
		t.Error("expected error for nonexistent agent")
	}
}

func TestDeleteAgent_InvalidName(t *testing.T) {
	dir := t.TempDir()
	err := DeleteAgent(dir, "")
	if err == nil {
		t.Error("expected error for empty name")
	}
}

func writeAgentFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

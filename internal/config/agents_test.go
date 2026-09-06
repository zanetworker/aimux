package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAgents_ValidYAML(t *testing.T) {
	dir := t.TempDir()
	content := `
- name: reviewer
  runtime: claude
  model: opus
  prompt: "Review code for bugs"
  mcp:
    - github
  skills:
    - code-review
  policy: strict
- name: writer
  runtime: codex
  inference: openai
  model: o3
  prompt: "Write tests"
`
	if err := os.WriteFile(filepath.Join(dir, "agents.yaml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	agents, err := LoadAgents(filepath.Join(dir, "agents.yaml"))
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(agents))
	}
	if agents[0].Name != "reviewer" {
		t.Errorf("agents[0].Name = %q, want reviewer", agents[0].Name)
	}
	if agents[0].Runtime != "claude" {
		t.Errorf("agents[0].Runtime = %q, want claude", agents[0].Runtime)
	}
	if agents[0].Model != "opus" {
		t.Errorf("agents[0].Model = %q, want opus", agents[0].Model)
	}
	if len(agents[0].MCP) != 1 || agents[0].MCP[0] != "github" {
		t.Errorf("agents[0].MCP = %v, want [github]", agents[0].MCP)
	}
	if agents[1].Name != "writer" {
		t.Errorf("agents[1].Name = %q, want writer", agents[1].Name)
	}
	if agents[1].Inference != "openai" {
		t.Errorf("agents[1].Inference = %q, want openai", agents[1].Inference)
	}
}

func TestLoadAgents_FileNotFound(t *testing.T) {
	agents, err := LoadAgents("/nonexistent/agents.yaml")
	if err != nil {
		t.Errorf("expected nil error for missing file, got %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected empty list, got %d agents", len(agents))
	}
}

func TestLoadAgents_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agents.yaml"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	agents, err := LoadAgents(filepath.Join(dir, "agents.yaml"))
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected empty list for empty file, got %d", len(agents))
	}
}

func TestLoadAgents_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agents.yaml"), []byte("{{invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadAgents(filepath.Join(dir, "agents.yaml"))
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestLoadAgentsMerged_GlobalAndProject(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	globalContent := `
- name: reviewer
  runtime: claude
  model: opus
`
	projectContent := `
- name: local-helper
  runtime: codex
  model: o3
`
	if err := os.WriteFile(filepath.Join(globalDir, "agents.yaml"), []byte(globalContent), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectDir, ".aimux"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".aimux", "agents.yaml"), []byte(projectContent), 0o600); err != nil {
		t.Fatal(err)
	}

	agents := LoadAgentsMerged(
		filepath.Join(globalDir, "agents.yaml"),
		filepath.Join(projectDir, ".aimux", "agents.yaml"),
	)
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents (global + project), got %d", len(agents))
	}

	names := map[string]bool{}
	for _, a := range agents {
		names[a.Name] = true
	}
	if !names["reviewer"] || !names["local-helper"] {
		t.Errorf("expected reviewer and local-helper, got %v", names)
	}
}

func TestLoadAgentsMerged_ProjectOverridesGlobal(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	globalContent := `
- name: reviewer
  runtime: claude
  model: opus
`
	projectContent := `
- name: reviewer
  runtime: claude
  model: sonnet
`
	if err := os.WriteFile(filepath.Join(globalDir, "agents.yaml"), []byte(globalContent), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectDir, ".aimux"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".aimux", "agents.yaml"), []byte(projectContent), 0o600); err != nil {
		t.Fatal(err)
	}

	agents := LoadAgentsMerged(
		filepath.Join(globalDir, "agents.yaml"),
		filepath.Join(projectDir, ".aimux", "agents.yaml"),
	)
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent (project overrides global), got %d", len(agents))
	}
	if agents[0].Model != "sonnet" {
		t.Errorf("model = %q, want sonnet (project override)", agents[0].Model)
	}
}

func TestAgentConfigNames(t *testing.T) {
	agents := []AgentConfig{
		{Name: "writer"},
		{Name: "reviewer"},
	}
	names := AgentConfigNames(agents)
	if len(names) != 2 || names[0] != "reviewer" || names[1] != "writer" {
		t.Errorf("expected sorted [reviewer, writer], got %v", names)
	}
}

func TestAgentConfigNames_Empty(t *testing.T) {
	names := AgentConfigNames(nil)
	if len(names) != 0 {
		t.Errorf("expected empty, got %v", names)
	}
}

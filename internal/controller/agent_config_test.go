package controller

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zanetworker/aimux/internal/config"
)

func TestCreateAgent_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, "agents")

	ac := config.AgentConfig{
		Name:    "test-agent",
		Harness: "claude-sdk",
		Model:   "opus",
		Prompt:  "Review code.",
	}
	if err := CreateAgent(agentsDir, ac); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	agents, err := ListAgents(agentsDir, "")
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
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
}

func TestCreateAgent_EmptyNameFails(t *testing.T) {
	dir := t.TempDir()
	err := CreateAgent(dir, config.AgentConfig{Name: ""})
	if err == nil {
		t.Error("expected error for empty name")
	}
}

func TestCreateAgent_InvalidNameFails(t *testing.T) {
	dir := t.TempDir()
	err := CreateAgent(dir, config.AgentConfig{Name: "bad name"})
	if err == nil {
		t.Error("expected error for invalid name")
	}
}

func TestUpdateAgent_MergesFields(t *testing.T) {
	dir := t.TempDir()

	ac := config.AgentConfig{
		Name:    "my-agent",
		Harness: "claude-native",
		Model:   "opus",
		Prompt:  "Original prompt.",
	}
	if err := CreateAgent(dir, ac); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	updates := config.AgentConfig{
		Model:  "sonnet",
		Prompt: "Updated prompt.",
	}
	if err := UpdateAgent(dir, "my-agent", updates); err != nil {
		t.Fatalf("UpdateAgent: %v", err)
	}

	agents, err := ListAgents(dir, "")
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1, got %d", len(agents))
	}
	if agents[0].Model != "sonnet" {
		t.Errorf("Model = %q, want sonnet", agents[0].Model)
	}
	if agents[0].Prompt != "Updated prompt." {
		t.Errorf("Prompt = %q, want Updated prompt.", agents[0].Prompt)
	}
	if agents[0].Harness != "claude-native" {
		t.Errorf("Harness = %q, want claude-native (unchanged)", agents[0].Harness)
	}
}

func TestUpdateAgent_NotFound(t *testing.T) {
	dir := t.TempDir()
	err := UpdateAgent(dir, "ghost", config.AgentConfig{Model: "opus"})
	if err == nil {
		t.Error("expected error for nonexistent agent")
	}
}

func TestRemoveAgent_Success(t *testing.T) {
	dir := t.TempDir()

	if err := CreateAgent(dir, config.AgentConfig{Name: "doomed", Harness: "claude"}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if err := RemoveAgent(dir, "doomed"); err != nil {
		t.Fatalf("RemoveAgent: %v", err)
	}

	agents, err := ListAgents(dir, "")
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected 0 agents after remove, got %d", len(agents))
	}
}

func TestRemoveAgent_Nonexistent(t *testing.T) {
	dir := t.TempDir()
	err := RemoveAgent(dir, "ghost")
	if err == nil {
		t.Error("expected error for nonexistent agent")
	}
}

func TestListAgents_MergesGlobalAndProject(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	if err := CreateAgent(globalDir, config.AgentConfig{Name: "global-agent", Harness: "claude", Model: "opus"}); err != nil {
		t.Fatalf("CreateAgent global: %v", err)
	}
	if err := CreateAgent(projectDir, config.AgentConfig{Name: "project-agent", Harness: "codex", Model: "o3"}); err != nil {
		t.Fatalf("CreateAgent project: %v", err)
	}

	agents, err := ListAgents(globalDir, projectDir)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(agents))
	}
	if agents[0].Name != "global-agent" {
		t.Errorf("agents[0].Name = %q, want global-agent", agents[0].Name)
	}
	if agents[1].Name != "project-agent" {
		t.Errorf("agents[1].Name = %q, want project-agent", agents[1].Name)
	}
}

func TestListAgents_ProjectOverridesGlobal(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	if err := CreateAgent(globalDir, config.AgentConfig{Name: "shared", Harness: "claude", Model: "opus"}); err != nil {
		t.Fatalf("CreateAgent global: %v", err)
	}
	if err := CreateAgent(projectDir, config.AgentConfig{Name: "shared", Harness: "claude", Model: "sonnet"}); err != nil {
		t.Fatalf("CreateAgent project: %v", err)
	}

	agents, err := ListAgents(globalDir, projectDir)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent (project overrides), got %d", len(agents))
	}
	if agents[0].Model != "sonnet" {
		t.Errorf("Model = %q, want sonnet (project override)", agents[0].Model)
	}
}

func TestListAgents_EmptyDirs(t *testing.T) {
	agents, err := ListAgents("/nonexistent/global", "/nonexistent/project")
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected 0, got %d", len(agents))
	}
}

func TestCreateUpdateRemove_FullCycle(t *testing.T) {
	dir := t.TempDir()

	// Create
	if err := CreateAgent(dir, config.AgentConfig{Name: "cycle", Harness: "claude", Model: "opus"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Verify exists
	path := filepath.Join(dir, "cycle.agent.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file should exist: %v", err)
	}

	// Update
	if err := UpdateAgent(dir, "cycle", config.AgentConfig{Model: "haiku"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	agents, _ := ListAgents(dir, "")
	if agents[0].Model != "haiku" {
		t.Errorf("after update Model = %q, want haiku", agents[0].Model)
	}

	// Remove
	if err := RemoveAgent(dir, "cycle"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	agents, _ = ListAgents(dir, "")
	if len(agents) != 0 {
		t.Errorf("after remove expected 0, got %d", len(agents))
	}
}

package cmd

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
)

func TestAgentsCmd_JSON_Empty(t *testing.T) {
	var stdout bytes.Buffer
	c := newAgentsCmd(func() ([]agent.Agent, error) { return nil, nil })
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"agents"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if result["count"].(float64) != 0 {
		t.Errorf("count=%v, want 0", result["count"])
	}
}

func TestAgentsCmd_JSON_WithAgents(t *testing.T) {
	var stdout bytes.Buffer
	agents := []agent.Agent{
		{PID: 1234, ProviderName: "claude", Name: "aimux", Status: agent.StatusActive, WorkingDir: "/tmp/test"},
	}
	c := newAgentsCmd(func() ([]agent.Agent, error) { return agents, nil })
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"agents"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result struct {
		Agents []struct {
			PID      int    `json:"pid"`
			Provider string `json:"provider"`
			Status   string `json:"status"`
		} `json:"agents"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if result.Count != 1 {
		t.Errorf("count=%d, want 1", result.Count)
	}
	if result.Agents[0].Provider != "claude" {
		t.Errorf("provider=%q, want %q", result.Agents[0].Provider, "claude")
	}
}

func TestAgentsCmd_Limit(t *testing.T) {
	var stdout bytes.Buffer
	agents := []agent.Agent{
		{PID: 1, ProviderName: "claude", Name: "a", Status: agent.StatusActive},
		{PID: 2, ProviderName: "codex", Name: "b", Status: agent.StatusActive},
		{PID: 3, ProviderName: "gemini", Name: "c", Status: agent.StatusActive},
	}
	c := newAgentsCmd(func() ([]agent.Agent, error) { return agents, nil })
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"agents", "--limit", "2"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result struct {
		Agents []any `json:"agents"`
		Count  int   `json:"count"`
		Total  int   `json:"total"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if result.Count != 2 {
		t.Errorf("count=%d, want 2", result.Count)
	}
	if result.Total != 3 {
		t.Errorf("total=%d, want 3", result.Total)
	}
}

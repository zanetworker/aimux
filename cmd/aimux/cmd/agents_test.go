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

func TestAgentsCmd_SortByName(t *testing.T) {
	var stdout bytes.Buffer
	agents := []agent.Agent{
		{PID: 1, ProviderName: "claude", Name: "zebra", Status: agent.StatusActive, WorkingDir: "/tmp/zebra"},
		{PID: 2, ProviderName: "codex", Name: "alpha", Status: agent.StatusActive, WorkingDir: "/tmp/alpha"},
	}
	c := newAgentsCmd(func() ([]agent.Agent, error) { return agents, nil })
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"agents", "--sort", "name"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result struct {
		Agents []struct {
			Project string `json:"project"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if len(result.Agents) != 2 {
		t.Fatalf("agents count=%d, want 2", len(result.Agents))
	}
	// "alpha" should come before "zebra" when sorted by name.
	if result.Agents[0].Project != "alpha" {
		t.Errorf("first agent=%q, want %q (sorted by name)", result.Agents[0].Project, "alpha")
	}
}

func TestAgentsCmd_SortInvalid(t *testing.T) {
	c := newAgentsCmd(func() ([]agent.Agent, error) {
		return []agent.Agent{{PID: 1, ProviderName: "claude", Name: "test"}}, nil
	})
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"agents", "--sort", "invalid_field"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for invalid sort field")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("invalid sort field")) {
		t.Errorf("error should mention invalid sort field, got: %s", err.Error())
	}
}

func TestAgentsCmd_Filter(t *testing.T) {
	var stdout bytes.Buffer
	agents := []agent.Agent{
		{PID: 1, ProviderName: "claude", Name: "aimux", Status: agent.StatusActive, WorkingDir: "/tmp/aimux"},
		{PID: 2, ProviderName: "codex", Name: "showtime", Status: agent.StatusIdle, WorkingDir: "/tmp/showtime"},
		{PID: 3, ProviderName: "gemini", Name: "other", Status: agent.StatusActive, WorkingDir: "/tmp/other"},
	}
	c := newAgentsCmd(func() ([]agent.Agent, error) { return agents, nil })
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"agents", "--filter", "claude"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result struct {
		Agents []struct {
			Provider string `json:"provider"`
		} `json:"agents"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if result.Count != 1 {
		t.Errorf("count=%d, want 1 (only claude should match)", result.Count)
	}
	if len(result.Agents) > 0 && result.Agents[0].Provider != "claude" {
		t.Errorf("provider=%q, want %q", result.Agents[0].Provider, "claude")
	}
}

func TestAgentsCmd_FilterNoMatch(t *testing.T) {
	var stdout bytes.Buffer
	agents := []agent.Agent{
		{PID: 1, ProviderName: "claude", Name: "test", Status: agent.StatusActive},
	}
	c := newAgentsCmd(func() ([]agent.Agent, error) { return agents, nil })
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"agents", "--filter", "nonexistent"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if result.Count != 0 {
		t.Errorf("count=%d, want 0", result.Count)
	}
}

func TestAgentsCmd_SortAndFilter(t *testing.T) {
	var stdout bytes.Buffer
	agents := []agent.Agent{
		{PID: 1, ProviderName: "claude", Name: "zebra", Status: agent.StatusActive, WorkingDir: "/tmp/z"},
		{PID: 2, ProviderName: "claude", Name: "alpha", Status: agent.StatusIdle, WorkingDir: "/tmp/a"},
		{PID: 3, ProviderName: "codex", Name: "beta", Status: agent.StatusActive, WorkingDir: "/tmp/b"},
	}
	c := newAgentsCmd(func() ([]agent.Agent, error) { return agents, nil })
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"agents", "--filter", "claude", "--sort", "name"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result struct {
		Agents []struct {
			Project string `json:"project"`
		} `json:"agents"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if result.Count != 2 {
		t.Errorf("count=%d, want 2 (two claude agents)", result.Count)
	}
	if len(result.Agents) >= 2 && result.Agents[0].Project != "alpha" {
		t.Errorf("first agent=%q, want %q (sorted by name after filter)", result.Agents[0].Project, "alpha")
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

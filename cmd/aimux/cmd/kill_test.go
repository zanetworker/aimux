package cmd

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
)

func TestKillCmd_InvalidPID(t *testing.T) {
	c := newKillCmd(func() ([]agent.Agent, error) { return nil, nil }, nil)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"kill", "notanumber"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for non-integer PID")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("invalid PID")) {
		t.Errorf("error should mention invalid PID, got: %s", err.Error())
	}
}

func TestKillCmd_AgentNotFound(t *testing.T) {
	agents := []agent.Agent{
		{PID: 1000, ProviderName: "claude", Name: "test"},
	}
	c := newKillCmd(func() ([]agent.Agent, error) { return agents, nil }, nil)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"kill", "9999"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when PID not found")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("no agent found")) {
		t.Errorf("error should mention no agent found, got: %s", err.Error())
	}
}

func TestKillCmd_RemoveOnly_JSON(t *testing.T) {
	// PID=0 with no pod prefix triggers KillRemoveOnly, which succeeds
	// without actually sending a signal.
	agents := []agent.Agent{
		{PID: 0, ProviderName: "claude", Name: "stale-session", SessionID: "abc123"},
	}
	var stdout bytes.Buffer
	c := newKillCmd(func() ([]agent.Agent, error) { return agents, nil }, nil)
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	jsonOutput = true
	defer func() { jsonOutput = false }()
	rootCmd.SetArgs([]string{"kill", "0"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if result["action"] != "remove_only" {
		t.Errorf("action=%q, want %q", result["action"], "remove_only")
	}
	if result["killed"] != true {
		t.Error("expected killed=true")
	}
}

func TestKillCmd_PodKill_Error(t *testing.T) {
	// Pod kill is not implemented in CLI, should return an error.
	agents := []agent.Agent{
		{PID: 5555, ProviderName: "claude", Name: "k8s-agent", SessionID: "pod-my-agent-pod"},
	}
	c := newKillCmd(func() ([]agent.Agent, error) { return agents, nil }, nil)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"kill", "5555"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for pod kill")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("pod kill not implemented")) {
		t.Errorf("error should mention pod kill not implemented, got: %s", err.Error())
	}
}

func TestKillCmd_MissingArgs(t *testing.T) {
	c := newKillCmd(func() ([]agent.Agent, error) { return nil, nil }, nil)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"kill"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing PID argument")
	}
}

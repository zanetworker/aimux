package discovery

import (
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
)

func TestParseSandboxAgents_Ready(t *testing.T) {
	output := "NAME              CREATED             PHASE\nhappy-fox         2026-06-21 10:00:00  Ready\nsad-cat           2026-06-21 10:01:00  Error\n"
	agents := parseSandboxAgents(output)
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(agents))
	}
	if agents[0].Name != "happy-fox" {
		t.Errorf("name: got %q", agents[0].Name)
	}
	if agents[0].Location != "remote" {
		t.Errorf("location: got %q", agents[0].Location)
	}
	if agents[0].Status != agent.StatusActive {
		t.Errorf("status: got %v, want Active", agents[0].Status)
	}
	if agents[1].Status != agent.StatusError {
		t.Errorf("status: got %v, want Error", agents[1].Status)
	}
}

func TestParseSandboxAgents_Empty(t *testing.T) {
	agents := parseSandboxAgents("No sandboxes found.\n")
	if len(agents) != 0 {
		t.Errorf("expected 0, got %d", len(agents))
	}
}

func TestParseSandboxAgents_WithAnsi(t *testing.T) {
	output := "\x1b[1mNAME\x1b[0m  \x1b[1mPHASE\x1b[0m\ntest-box  \x1b[32mReady\x1b[39m\n"
	agents := parseSandboxAgents(output)
	if len(agents) != 1 {
		t.Fatalf("expected 1, got %d", len(agents))
	}
	if agents[0].Name != "test-box" {
		t.Errorf("name: got %q", agents[0].Name)
	}
}

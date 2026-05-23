package controller

import (
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
)

func TestDetermineKillAction_LocalProcess(t *testing.T) {
	ag := agent.Agent{PID: 12345, SessionID: "abc-123"}
	action := DetermineKillAction(ag)

	if action.Type != KillProcess {
		t.Errorf("expected KillProcess, got %d", action.Type)
	}
	if action.PodName != "" {
		t.Errorf("expected empty PodName, got %q", action.PodName)
	}
}

func TestDetermineKillAction_K8sPod(t *testing.T) {
	ag := agent.Agent{
		SessionID:  "pod-my-agent-0",
		WorkingDir: "k8s://custom-ns/repo",
		PID:        0,
	}
	action := DetermineKillAction(ag)

	if action.Type != KillPod {
		t.Errorf("expected KillPod, got %d", action.Type)
	}
	if action.PodName != "my-agent-0" {
		t.Errorf("expected PodName %q, got %q", "my-agent-0", action.PodName)
	}
	if action.Namespace != "custom-ns" {
		t.Errorf("expected Namespace %q, got %q", "custom-ns", action.Namespace)
	}
}

func TestDetermineKillAction_K8sPod_DefaultNamespace(t *testing.T) {
	ag := agent.Agent{
		SessionID:  "pod-my-agent-1",
		WorkingDir: "/some/local/path",
		PID:        0,
	}
	action := DetermineKillAction(ag)

	if action.Type != KillPod {
		t.Errorf("expected KillPod, got %d", action.Type)
	}
	if action.PodName != "my-agent-1" {
		t.Errorf("expected PodName %q, got %q", "my-agent-1", action.PodName)
	}
	if action.Namespace != "agents" {
		t.Errorf("expected default namespace %q, got %q", "agents", action.Namespace)
	}
}

func TestDetermineKillAction_SessionOnly(t *testing.T) {
	ag := agent.Agent{PID: 0, SessionID: "some-session-id"}
	action := DetermineKillAction(ag)

	if action.Type != KillRemoveOnly {
		t.Errorf("expected KillRemoveOnly, got %d", action.Type)
	}
}

func TestDetermineKillAction_K8sPod_WithPID(t *testing.T) {
	// Pod prefix takes priority even if PID is non-zero
	ag := agent.Agent{
		SessionID:  "pod-worker-5",
		WorkingDir: "k8s://production/app",
		PID:        99999,
	}
	action := DetermineKillAction(ag)

	if action.Type != KillPod {
		t.Errorf("expected KillPod (pod prefix should take priority), got %d", action.Type)
	}
	if action.PodName != "worker-5" {
		t.Errorf("expected PodName %q, got %q", "worker-5", action.PodName)
	}
	if action.Namespace != "production" {
		t.Errorf("expected Namespace %q, got %q", "production", action.Namespace)
	}
}

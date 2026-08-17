package mcpserver

import (
	"context"
	"testing"
)

func TestK8sBackend_ImplementsBackend(t *testing.T) {
	var _ Backend = (*K8sBackend)(nil)
}

func TestK8sConfig_Defaults(t *testing.T) {
	cfg := K8sConfig{}.withDefaults()
	if cfg.Namespace != "agents" {
		t.Errorf("Namespace: got %q, want 'agents'", cfg.Namespace)
	}
	if cfg.TeamID != "default" {
		t.Errorf("TeamID: got %q, want 'default'", cfg.TeamID)
	}
	if cfg.MaxAgents != 20 {
		t.Errorf("MaxAgents: got %d, want 20", cfg.MaxAgents)
	}
}

func TestK8sConfig_PreservesExplicit(t *testing.T) {
	cfg := K8sConfig{
		Namespace: "prod",
		TeamID:    "alpha",
		MaxAgents: 5,
	}.withDefaults()
	if cfg.Namespace != "prod" {
		t.Errorf("Namespace: got %q", cfg.Namespace)
	}
	if cfg.TeamID != "alpha" {
		t.Errorf("TeamID: got %q", cfg.TeamID)
	}
	if cfg.MaxAgents != 5 {
		t.Errorf("MaxAgents: got %d", cfg.MaxAgents)
	}
}

func TestNewK8sBackend_InvalidRedisURL(t *testing.T) {
	_, err := NewK8sBackend(K8sConfig{RedisURL: "://invalid"})
	if err == nil {
		t.Fatal("expected error for invalid Redis URL")
	}
}

func TestK8sBackend_TeamKey(t *testing.T) {
	b := &K8sBackend{teamID: "myteam"}
	got := b.teamKey("heartbeat")
	if got != "team:myteam:heartbeat" {
		t.Errorf("got %q, want 'team:myteam:heartbeat'", got)
	}
}

func TestK8sBackend_RedisAccessor(t *testing.T) {
	b := &K8sBackend{teamID: "t1"}
	if b.TeamID() != "t1" {
		t.Errorf("TeamID: got %q", b.TeamID())
	}
	// Redis() returns nil when not initialized; that's expected in unit tests
	if b.Redis() != nil {
		t.Error("expected nil Redis client in uninitialized backend")
	}
}

func TestDeploymentNameFromPod(t *testing.T) {
	tests := []struct {
		pod    string
		deploy string
	}{
		{"agent-claude-coder-78564fdf75-4rxlk", "agent-claude-coder"},
		{"agent-claude-researcher-abc123-xyz", "agent-claude-researcher"},
		// 3-segment deployment name: must pass through unchanged (not treated as RS+pod suffix)
		{"agent-claude-coder", "agent-claude-coder"},
		{"simple-pod", "simple-pod"},
		{"two-parts", "two-parts"},
	}
	for _, tt := range tests {
		got := deploymentNameFromPod(tt.pod)
		if got != tt.deploy {
			t.Errorf("deploymentNameFromPod(%q) = %q, want %q", tt.pod, got, tt.deploy)
		}
	}
}

func TestK8sBackend_ExecStream_NotSupported(t *testing.T) {
	b := &K8sBackend{}
	_, err := b.ExecStream(context.Background(), "agent-1", []string{"echo"})
	if err == nil {
		t.Fatal("expected error: ExecStream not supported on K8s backend")
	}
}

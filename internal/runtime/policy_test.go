package runtime

import "testing"

// mockPolicyEnforcer verifies the PolicyEnforcer interface can be satisfied.
type mockPolicyEnforcer struct {
	policy SandboxConfig
}

// Compile-time interface check.
var _ PolicyEnforcer = (*mockPolicyEnforcer)(nil)

func (m *mockPolicyEnforcer) ApplyPolicy(cfg SandboxConfig) error  { m.policy = cfg; return nil }
func (m *mockPolicyEnforcer) UpdatePolicy(cfg SandboxConfig) error { m.policy = cfg; return nil }
func (m *mockPolicyEnforcer) CurrentPolicy() SandboxConfig         { return m.policy }

func TestSandboxConfig_HasNetworkPolicy_DenyAll(t *testing.T) {
	cfg := SandboxConfig{
		Network: NetworkPolicy{DenyAll: true},
	}
	if !cfg.HasNetworkPolicy() {
		t.Error("HasNetworkPolicy() = false, want true when DenyAll is set")
	}
}

func TestSandboxConfig_HasNetworkPolicy_WithRules(t *testing.T) {
	cfg := SandboxConfig{
		Network: NetworkPolicy{
			Rules: []NetworkRule{
				{Name: "allow-github", Hosts: []string{"github.com"}, Access: "allow"},
			},
		},
	}
	if !cfg.HasNetworkPolicy() {
		t.Error("HasNetworkPolicy() = false, want true when Rules are present")
	}
}

func TestSandboxConfig_HasNetworkPolicy_WithGroups(t *testing.T) {
	cfg := SandboxConfig{
		Network: NetworkPolicy{
			Groups: []string{"web-browsing"},
		},
	}
	if !cfg.HasNetworkPolicy() {
		t.Error("HasNetworkPolicy() = false, want true when Groups are present")
	}
}

func TestSandboxConfig_HasNetworkPolicy_Empty(t *testing.T) {
	cfg := SandboxConfig{}
	if cfg.HasNetworkPolicy() {
		t.Error("HasNetworkPolicy() = true, want false for empty config")
	}
}

func TestNetworkRule_Fields(t *testing.T) {
	rule := NetworkRule{
		Name:     "allow-api",
		Hosts:    []string{"api.example.com"},
		Ports:    []int{443, 8080},
		Binaries: []string{"curl", "wget"},
		Access:   "allow",
	}

	if rule.Name != "allow-api" {
		t.Errorf("Name = %q, want %q", rule.Name, "allow-api")
	}
	if len(rule.Hosts) != 1 {
		t.Errorf("Hosts len = %d, want 1", len(rule.Hosts))
	}
	if len(rule.Ports) != 2 {
		t.Errorf("Ports len = %d, want 2", len(rule.Ports))
	}
	if len(rule.Binaries) != 2 {
		t.Errorf("Binaries len = %d, want 2", len(rule.Binaries))
	}
	if rule.Access != "allow" {
		t.Errorf("Access = %q, want %q", rule.Access, "allow")
	}
}

package provider

import (
	"strings"
	"testing"
)

func TestGatherHealth_LocalProviders(t *testing.T) {
	providers := []Provider{&Claude{}, &Codex{}, &Gemini{}}
	counts := map[string]int{"claude": 3, "codex": 0, "gemini": 1}

	sh := GatherHealth(providers, nil, counts)

	if len(sh.Providers) != 3 {
		t.Fatalf("GatherHealth() returned %d providers, want 3", len(sh.Providers))
	}

	// Claude should have agents counted.
	for _, p := range sh.Providers {
		if p.Name == "claude" {
			if p.Agents != 3 {
				t.Errorf("claude agents = %d, want 3", p.Agents)
			}
			if p.Kind != "local" {
				t.Errorf("claude kind = %q, want %q", p.Kind, "local")
			}
		}
	}
}

func TestGatherHealth_NilInfra(t *testing.T) {
	sh := GatherHealth([]Provider{&Claude{}}, nil, nil)
	if len(sh.Providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(sh.Providers))
	}
	if sh.Providers[0].Infra != nil {
		t.Error("expected nil Infra for local provider")
	}
}

func TestFormatHealth_LocalOnly(t *testing.T) {
	sh := SystemHealth{
		Providers: []ProviderHealth{
			{Name: "claude", Kind: "local", BinaryOK: true, BinaryPath: "/usr/bin/claude", Version: "v2.1.72", Agents: 2},
			{Name: "codex", Kind: "local", BinaryOK: false},
		},
	}
	out := FormatHealth(sh)
	if !strings.Contains(out, "Local Providers") {
		t.Error("missing 'Local Providers' header")
	}
	if !strings.Contains(out, "OK") {
		t.Error("missing OK for claude")
	}
	if !strings.Contains(out, "not installed") {
		t.Error("missing 'not installed' for codex")
	}
	if !strings.Contains(out, "2 agents") {
		t.Error("missing agent count for claude")
	}
}

func TestFormatHealth_WithInfra(t *testing.T) {
	sh := SystemHealth{
		Providers: []ProviderHealth{
			{Name: "claude", Kind: "local", BinaryOK: true, BinaryPath: "/usr/bin/claude", Version: "v2.1.72"},
			{Name: "k8s", Kind: "infra", Infra: &HealthStatus{
				Configured: true,
				CoordOK:    true,
				ComputeOK:  true,
				Workloads:  []string{"agent-claude-session", "agent-claude-task"},
			}},
		},
	}
	out := FormatHealth(sh)
	if !strings.Contains(out, "Infrastructure (k8s)") {
		t.Error("missing infrastructure header")
	}
	if !strings.Contains(out, "2 workloads") {
		t.Error("missing workload count")
	}
}

func TestFormatHealth_InfraNotConfigured(t *testing.T) {
	sh := SystemHealth{
		Providers: []ProviderHealth{
			{Name: "k8s", Kind: "infra", Infra: &HealthStatus{Configured: false}},
		},
	}
	out := FormatHealth(sh)
	if !strings.Contains(out, "not configured") {
		t.Error("expected 'not configured' for unconfigured infra")
	}
}

func TestFormatHealth_InfraCoordFail(t *testing.T) {
	sh := SystemHealth{
		Providers: []ProviderHealth{
			{Name: "k8s", Kind: "infra", Infra: &HealthStatus{
				Configured: true,
				CoordOK:    false,
				CoordErr:   "Redis timeout",
				ComputeOK:  true,
				Workloads:  []string{"agent-claude-session"},
			}},
		},
	}
	out := FormatHealth(sh)
	if !strings.Contains(out, "FAIL") {
		t.Error("expected FAIL for coord failure")
	}
	if !strings.Contains(out, "Redis timeout") {
		t.Error("expected error message in output")
	}
}

func TestGetBinaryVersion_NotFound(t *testing.T) {
	v := getBinaryVersion("/nonexistent/binary")
	if v != "" {
		t.Errorf("getBinaryVersion() for missing binary = %q, want empty", v)
	}
}

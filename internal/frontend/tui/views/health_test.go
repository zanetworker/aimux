package views

import (
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/provider"
)

func TestHealthView_RenderLocal(t *testing.T) {
	v := NewHealthView()
	v.SetSize(80, 24)
	v.SetHealth(provider.SystemHealth{
		Providers: []provider.ProviderHealth{
			{Name: "claude", Kind: "local", BinaryOK: true, BinaryPath: "/usr/bin/claude", Version: "v2.1.72", Agents: 3},
			{Name: "codex", Kind: "local", BinaryOK: false},
		},
	})
	out := v.View()
	if !strings.Contains(out, "System Health") {
		t.Error("missing title")
	}
	if !strings.Contains(out, "claude") {
		t.Error("missing claude provider")
	}
	if !strings.Contains(out, "not installed") {
		t.Error("missing 'not installed' for codex")
	}
}

func TestHealthView_RenderInfra(t *testing.T) {
	v := NewHealthView()
	v.SetSize(80, 24)
	v.SetHealth(provider.SystemHealth{
		Providers: []provider.ProviderHealth{
			{Name: "k8s", Kind: "infra", Agents: 2, Infra: &provider.HealthStatus{
				Configured: true,
				CoordOK:    true,
				ComputeOK:  true,
				Workloads:  []string{"agent-claude-session"},
			}},
		},
	})
	out := v.View()
	if !strings.Contains(out, "Infrastructure") {
		t.Error("missing infra section")
	}
	if !strings.Contains(out, "connected") {
		t.Error("missing connected status")
	}
	if !strings.Contains(out, "agent-claude-session") {
		t.Error("missing workload name")
	}
}

func TestHealthView_RenderEmpty(t *testing.T) {
	v := NewHealthView()
	v.SetSize(80, 24)
	v.SetHealth(provider.SystemHealth{})
	out := v.View()
	if !strings.Contains(out, "System Health") {
		t.Error("should render title even with no providers")
	}
}

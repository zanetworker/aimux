package controller

import (
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
)

func TestCostsByProject(t *testing.T) {
	agents := []agent.Agent{
		{WorkingDir: "/p/cheap", ProviderName: "codex", Model: "o4-mini", TokensIn: 1, TokensOut: 2, EstCostUSD: 0.10},
		{WorkingDir: "/p/dear", ProviderName: "claude", Model: "claude-opus-4-6", TokensIn: 10, TokensOut: 20, EstCostUSD: 1.00},
		{WorkingDir: "/p/dear", ProviderName: "gemini", Model: "other", TokensIn: 5, TokensOut: 6, EstCostUSD: 0.50},
		{EstCostUSD: 0.20},
	}
	got := CostsByProject(agents)
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	d := got[0]
	// provider and model come from the first agent seen for the project
	if d.Project != "dear" || d.Provider != "claude" || d.Model != agents[1].ShortModel() ||
		d.TokensIn != 15 || d.TokensOut != 26 || d.CostUSD != 1.50 || d.AgentCount != 2 {
		t.Errorf("dear = %+v", d)
	}
	if got[1].Project != "(unknown)" || got[1].Provider != "" || got[1].AgentCount != 1 {
		t.Errorf("unknown = %+v", got[1])
	}
	if got[2].Project != "cheap" {
		t.Errorf("cheap = %+v", got[2])
	}
}

func TestCostsByProject_Empty(t *testing.T) {
	got := CostsByProject(nil)
	if got == nil || len(got) != 0 {
		t.Errorf("got %#v, want empty non-nil", got)
	}
}

func TestCostsByProject_EqualCostOrderedByProject(t *testing.T) {
	got := CostsByProject([]agent.Agent{
		{WorkingDir: "/p/b", EstCostUSD: 1},
		{WorkingDir: "/p/a", EstCostUSD: 1},
	})
	if got[0].Project != "a" || got[1].Project != "b" {
		t.Errorf("got %+v", got)
	}
}

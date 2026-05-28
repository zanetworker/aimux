package views

import (
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
)

func TestAgentsView_BranchInRow(t *testing.T) {
	v := NewAgentsView()
	v.SetSize(160, 30)
	a := agent.Agent{
		PID:          12345,
		Name:         "test-agent",
		ProviderName: "claude",
		Model:        "claude-sonnet-4-6",
		GitBranch:    "feat/auth",
		Status:       agent.StatusIdle,
	}
	v.SetAgents([]agent.Agent{a})
	output := v.View()
	if !strings.Contains(output, "feat/auth") {
		t.Errorf("expected 'feat/auth' branch in agents view, got:\n%s", output)
	}
	if !strings.Contains(output, "BRANCH") {
		t.Errorf("expected 'BRANCH' header in agents view")
	}
}

func TestAgentsView_BranchMainDimmed(t *testing.T) {
	v := NewAgentsView()
	v.SetSize(160, 30)
	a := agent.Agent{
		PID:          12345,
		Name:         "test-agent",
		ProviderName: "claude",
		Model:        "claude-sonnet-4-6",
		GitBranch:    "main",
		Status:       agent.StatusIdle,
	}
	v.SetAgents([]agent.Agent{a})
	output := v.View()
	// "main" should appear in the output (rendered with dim styling).
	if !strings.Contains(output, "main") {
		t.Errorf("expected 'main' branch in agents view, got:\n%s", output)
	}
}

func TestAgentsView_BranchEmpty(t *testing.T) {
	v := NewAgentsView()
	v.SetSize(160, 30)
	a := agent.Agent{
		PID:          12345,
		Name:         "test-agent",
		ProviderName: "claude",
		Model:        "claude-sonnet-4-6",
		GitBranch:    "",
		Status:       agent.StatusIdle,
	}
	v.SetAgents([]agent.Agent{a})
	output := v.View()
	// Header should still show BRANCH even with no branch value.
	if !strings.Contains(output, "BRANCH") {
		t.Errorf("expected 'BRANCH' header in agents view even when branch is empty")
	}
}

func TestAgentsView_BranchMasterDimmed(t *testing.T) {
	v := NewAgentsView()
	v.SetSize(160, 30)
	a := agent.Agent{
		PID:          12345,
		Name:         "test-agent",
		ProviderName: "claude",
		Model:        "claude-sonnet-4-6",
		GitBranch:    "master",
		Status:       agent.StatusIdle,
	}
	v.SetAgents([]agent.Agent{a})
	output := v.View()
	if !strings.Contains(output, "master") {
		t.Errorf("expected 'master' branch in agents view, got:\n%s", output)
	}
}

func TestAgentsView_SetFilterRebuildsRows(t *testing.T) {
	v := NewAgentsView()
	v.SetSize(160, 30)
	v.SetAgents([]agent.Agent{
		{PID: 1, Name: "alpha", ProviderName: "claude", WorkingDir: "/src/alpha"},
		{PID: 2, Name: "beta", ProviderName: "gemini", WorkingDir: "/src/beta"},
		{PID: 3, Name: "gamma", ProviderName: "claude", WorkingDir: "/src/gamma"},
	})

	// Before filter: all 3 agents visible
	output := v.View()
	if !strings.Contains(output, "alpha") {
		t.Error("expected alpha before filter")
	}
	if !strings.Contains(output, "beta") {
		t.Error("expected beta before filter")
	}

	// Apply filter: only "alpha" should remain immediately (no tick needed)
	v.SetFilter("alpha")
	output = v.View()
	if !strings.Contains(output, "alpha") {
		t.Error("expected alpha after filter")
	}
	if strings.Contains(output, "beta") {
		t.Error("beta should be hidden after filter for 'alpha'")
	}
	if strings.Contains(output, "gamma") {
		t.Error("gamma should be hidden after filter for 'alpha'")
	}

	// Clear filter: all agents should reappear immediately
	v.SetFilter("")
	output = v.View()
	if !strings.Contains(output, "alpha") {
		t.Error("expected alpha after clearing filter")
	}
	if !strings.Contains(output, "beta") {
		t.Error("expected beta after clearing filter")
	}
	if !strings.Contains(output, "gamma") {
		t.Error("expected gamma after clearing filter")
	}
}

func TestAgentsView_SetFilterNoMatch(t *testing.T) {
	v := NewAgentsView()
	v.SetSize(160, 30)
	v.SetAgents([]agent.Agent{
		{PID: 1, Name: "alpha", ProviderName: "claude", WorkingDir: "/src/alpha"},
	})

	v.SetFilter("nonexistent")
	output := v.View()
	if strings.Contains(output, "alpha") {
		t.Error("alpha should not appear when filter matches nothing")
	}
	if !strings.Contains(output, "No agents found") {
		t.Error("expected 'No agents found' when filter matches nothing")
	}
}

func TestAgentsView_SetFilterByProvider(t *testing.T) {
	v := NewAgentsView()
	v.SetSize(160, 30)
	v.SetAgents([]agent.Agent{
		{PID: 1, Name: "proj-a", ProviderName: "claude", WorkingDir: "/src/a"},
		{PID: 2, Name: "proj-b", ProviderName: "gemini", WorkingDir: "/src/b"},
	})

	v.SetFilter("gemini")
	output := v.View()
	if strings.Contains(output, "proj-a") {
		t.Error("proj-a (claude) should be hidden when filtering by 'gemini'")
	}
	if !strings.Contains(output, "proj-b") {
		t.Error("expected proj-b (gemini) to be visible when filtering by 'gemini'")
	}
}

func TestAgentsView_BranchTruncatesLongName(t *testing.T) {
	v := NewAgentsView()
	v.SetSize(200, 30)
	a := agent.Agent{
		PID:          12345,
		Name:         "test-agent",
		ProviderName: "claude",
		Model:        "claude-sonnet-4-6",
		GitBranch:    "feature/very-long-branch-name-that-exceeds-column",
		Status:       agent.StatusIdle,
	}
	v.SetAgents([]agent.Agent{a})
	output := v.View()
	// The branch name should be truncated (colBranch=14) so the full name
	// should NOT appear, but a truncated version with "..." should.
	if strings.Contains(output, "feature/very-long-branch-name-that-exceeds-column") {
		t.Errorf("expected branch to be truncated, but full name appeared")
	}
	if !strings.Contains(output, "...") {
		t.Errorf("expected truncation indicator '...' in branch column")
	}
}

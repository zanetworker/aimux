package controller

import (
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
)

func TestSortAgents_Default_ActiveFirst(t *testing.T) {
	agents := []agent.Agent{
		{WorkingDir: "/src/beta", Status: agent.StatusIdle},
		{WorkingDir: "/src/alpha", Status: agent.StatusActive},
		{WorkingDir: "/src/gamma", Status: agent.StatusActive},
	}
	SortAgents(agents, "")
	// Active agents first, then alphabetical within group.
	if agents[0].ShortProject() != "alpha" {
		t.Errorf("agents[0] = %q, want alpha", agents[0].ShortProject())
	}
	if agents[1].ShortProject() != "gamma" {
		t.Errorf("agents[1] = %q, want gamma", agents[1].ShortProject())
	}
	if agents[2].ShortProject() != "beta" {
		t.Errorf("agents[2] = %q, want beta (idle)", agents[2].ShortProject())
	}
}

func TestSortAgents_Default_AlphabeticalWithinStatus(t *testing.T) {
	agents := []agent.Agent{
		{WorkingDir: "/src/zebra", Status: agent.StatusIdle},
		{WorkingDir: "/src/apple", Status: agent.StatusIdle},
		{WorkingDir: "/src/mango", Status: agent.StatusIdle},
	}
	SortAgents(agents, "")
	want := []string{"apple", "mango", "zebra"}
	for i, w := range want {
		if agents[i].ShortProject() != w {
			t.Errorf("agents[%d] = %q, want %q", i, agents[i].ShortProject(), w)
		}
	}
}

func TestSortAgents_ByName(t *testing.T) {
	agents := []agent.Agent{
		{WorkingDir: "/src/Zebra"},
		{WorkingDir: "/src/apple"},
		{WorkingDir: "/src/Mango"},
	}
	SortAgents(agents, "name")
	want := []string{"apple", "Mango", "Zebra"}
	for i, w := range want {
		if agents[i].ShortProject() != w {
			t.Errorf("agents[%d] = %q, want %q", i, agents[i].ShortProject(), w)
		}
	}
}

func TestSortAgents_ByCost(t *testing.T) {
	agents := []agent.Agent{
		{WorkingDir: "/src/cheap", EstCostUSD: 1.0},
		{WorkingDir: "/src/expensive", EstCostUSD: 50.0},
		{WorkingDir: "/src/mid", EstCostUSD: 10.0},
	}
	SortAgents(agents, "cost")
	if agents[0].ShortProject() != "expensive" {
		t.Errorf("agents[0] = %q, want expensive (highest cost)", agents[0].ShortProject())
	}
	if agents[1].ShortProject() != "mid" {
		t.Errorf("agents[1] = %q, want mid", agents[1].ShortProject())
	}
	if agents[2].ShortProject() != "cheap" {
		t.Errorf("agents[2] = %q, want cheap (lowest cost)", agents[2].ShortProject())
	}
}

func TestSortAgents_ByAge(t *testing.T) {
	now := time.Now()
	agents := []agent.Agent{
		{WorkingDir: "/src/new", StartTime: now.Add(-1 * time.Minute)},
		{WorkingDir: "/src/old", StartTime: now.Add(-1 * time.Hour)},
		{WorkingDir: "/src/mid", StartTime: now.Add(-30 * time.Minute)},
	}
	SortAgents(agents, "age")
	// Oldest first (earliest AgeTime).
	if agents[0].ShortProject() != "old" {
		t.Errorf("agents[0] = %q, want old (oldest)", agents[0].ShortProject())
	}
	if agents[1].ShortProject() != "mid" {
		t.Errorf("agents[1] = %q, want mid", agents[1].ShortProject())
	}
	if agents[2].ShortProject() != "new" {
		t.Errorf("agents[2] = %q, want new (newest)", agents[2].ShortProject())
	}
}

func TestSortAgents_ByCPU(t *testing.T) {
	agents := []agent.Agent{
		{WorkingDir: "/src/low", CPUPercent: 5.0},
		{WorkingDir: "/src/high", CPUPercent: 80.0},
		{WorkingDir: "/src/mid", CPUPercent: 45.0},
	}
	SortAgents(agents, "cpu")
	if agents[0].ShortProject() != "high" {
		t.Errorf("agents[0] = %q, want high (highest CPU)", agents[0].ShortProject())
	}
}

func TestSortAgents_ByMem(t *testing.T) {
	agents := []agent.Agent{
		{WorkingDir: "/src/low", MemoryMB: 100},
		{WorkingDir: "/src/high", MemoryMB: 2000},
		{WorkingDir: "/src/mid", MemoryMB: 800},
	}
	SortAgents(agents, "mem")
	if agents[0].ShortProject() != "high" {
		t.Errorf("agents[0] = %q, want high (highest mem)", agents[0].ShortProject())
	}
}

func TestSortAgents_ByModel(t *testing.T) {
	agents := []agent.Agent{
		{WorkingDir: "/src/z", Model: "claude-sonnet-4-5"},
		{WorkingDir: "/src/a", Model: "claude-haiku-3-5"},
		{WorkingDir: "/src/m", Model: "claude-opus-4-6"},
	}
	SortAgents(agents, "model")
	// ShortModel: haiku-3.5, opus-4.6, sonnet-4.5
	want := []string{"a", "m", "z"}
	for i, w := range want {
		if agents[i].ShortProject() != w {
			t.Errorf("agents[%d] = %q, want %q", i, agents[i].ShortProject(), w)
		}
	}
}

func TestSortAgents_StableOrder(t *testing.T) {
	// All agents have the same cost. Stable sort must preserve insertion order.
	agents := []agent.Agent{
		{WorkingDir: "/src/first", EstCostUSD: 10.0, PID: 1},
		{WorkingDir: "/src/second", EstCostUSD: 10.0, PID: 2},
		{WorkingDir: "/src/third", EstCostUSD: 10.0, PID: 3},
	}
	SortAgents(agents, "cost")
	if agents[0].PID != 1 || agents[1].PID != 2 || agents[2].PID != 3 {
		t.Errorf("stable sort violated: PIDs = [%d, %d, %d], want [1, 2, 3]",
			agents[0].PID, agents[1].PID, agents[2].PID)
	}
}

func TestSortAgents_EmptySlice(t *testing.T) {
	var agents []agent.Agent
	SortAgents(agents, "name") // must not panic
	if len(agents) != 0 {
		t.Errorf("len = %d, want 0", len(agents))
	}
}

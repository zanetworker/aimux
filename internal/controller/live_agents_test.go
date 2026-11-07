package controller

import (
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
)

func pidsOf(agents []agent.Agent) []int {
	var out []int
	for _, a := range agents {
		out = append(out, a.PID)
	}
	return out
}

func samePIDs(t *testing.T, got []agent.Agent, want ...int) {
	t.Helper()
	g := pidsOf(got)
	if len(g) != len(want) {
		t.Fatalf("pids = %v, want %v", g, want)
	}
	for i := range want {
		if g[i] != want[i] {
			t.Fatalf("pids = %v, want %v", g, want)
		}
	}
}

func TestDropEphemeral(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	agents := []agent.Agent{
		{PID: 1, Model: "claude-sonnet-4-5", StartTime: now},
		{PID: 2, StartTime: now, Location: "remote"},
		{PID: 3, Model: "claude-opus-4-6", StartTime: now},
		{PID: 4, StartTime: now, TMuxSession: "aimux-x"},
		{PID: 5, StartTime: now.Add(-5 * time.Minute)},             // exactly 5 min: kept
		{PID: 6, StartTime: now.Add(-5*time.Minute + time.Second)}, // just under: dropped
		{PID: 7, StartTime: now, TokensIn: 999},                    // under threshold: dropped
		{PID: 8, StartTime: now, TokensIn: 1000},                   // at threshold: kept
		{PID: 9, StartTime: now, EstCostUSD: 0.01},
		{PID: 10, StartTime: now, TMuxSession: "other-aimux-"}, // prefix only: dropped
	}
	samePIDs(t, DropEphemeral(agents, now), 2, 3, 4, 5, 8, 9)
}

func TestDropEphemeral_Empty(t *testing.T) {
	if got := DropEphemeral(nil, time.Now()); len(got) != 0 {
		t.Errorf("got %v", got)
	}
}

func TestDedupBySession(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	agents := []agent.Agent{
		{PID: 1, SessionID: "a", StartTime: t0},
		{PID: 2},
		{PID: 3, SessionID: "a", StartTime: t0.Add(-time.Minute)}, // earlier: takes slot of 1
		{PID: 4},
		{PID: 5, SessionID: "a", StartTime: t0.Add(time.Minute)}, // later: dropped
		{PID: 6, SessionID: "b", StartTime: t0},
		{PID: 7, SessionID: "b", StartTime: t0}, // equal start: first kept
	}
	samePIDs(t, DedupBySession(agents), 3, 2, 4, 6)
}

package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
)

// firstSSEAgents reads the first agents event from /api/events.
func firstSSEAgents(t *testing.T, agents []agent.Agent) []agent.Agent {
	t.Helper()
	s := NewServer(0)
	s.SetDiscoverFunc(func() ([]agent.Agent, error) { return agents, nil })
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	// Bounded, so a stream that never sends agents fails fast instead of hanging.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL()+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE connect failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var payload struct {
			Agents []agent.Agent `json:"agents"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload); err != nil {
			t.Fatalf("unmarshal SSE data: %v", err)
		}
		return payload.Agents
	}
	t.Fatal("never received agents event")
	return nil
}

// Pins the dashboard stream's rule: young cheap agents are dropped unless
// remote, opus, or launched by aimux; agents sharing a session id collapse
// to the earliest started one; agents with no session id are all kept.
func TestSSEDropsEphemeralAndDedupsBySession(t *testing.T) {
	now := time.Now()
	agents := []agent.Agent{
		{PID: 1, Model: "claude-sonnet-4-5", StartTime: now},                         // ephemeral: dropped
		{PID: 2, Model: "claude-sonnet-4-5", StartTime: now, Location: "remote"},     // remote: kept
		{PID: 3, Model: "claude-opus-4-6", StartTime: now},                           // opus: kept
		{PID: 4, Model: "claude-sonnet-4-5", StartTime: now, TMuxSession: "aimux-x"}, // aimux: kept
		{PID: 5, Model: "m", StartTime: now.Add(-10 * time.Minute), SessionID: "s1"}, // old: kept
		{PID: 6, Model: "m", StartTime: now.Add(-20 * time.Minute), SessionID: "s1"}, // earlier same session: replaces 5
		{PID: 7, Model: "m", StartTime: now.Add(-6 * time.Minute), SessionID: "s1"},  // later same session: dropped
		{PID: 8, Model: "m", StartTime: now, TokensIn: 1000},                         // tokens at threshold: kept
		{PID: 9, Model: "m", StartTime: now, EstCostUSD: 0.01},                       // has cost: kept
	}
	got := firstSSEAgents(t, agents)
	var pids []int
	for _, a := range got {
		pids = append(pids, a.PID)
	}
	want := []int{2, 3, 4, 6, 8, 9}
	if len(pids) != len(want) {
		t.Fatalf("pids = %v, want %v", pids, want)
	}
	for i := range want {
		if pids[i] != want[i] {
			t.Fatalf("pids = %v, want %v", pids, want)
		}
	}
}

package web

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/config"
)

func TestSSEStreamsAgentState(t *testing.T) {
	s := NewServer(0)
	s.SetDiscoverFunc(func() ([]agent.Agent, error) {
		return []agent.Agent{
			{PID: 123, Name: "test-repo", ProviderName: "claude", Status: agent.StatusActive, TokensIn: 5000},
		}, nil
	})

	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/events")
	if err != nil {
		t.Fatalf("SSE connect failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %q", ct)
	}

	scanner := bufio.NewScanner(resp.Body)
	var gotEvent bool
	deadline := time.After(5 * time.Second)

	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for SSE event")
		default:
		}
		if !scanner.Scan() {
			break
		}
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			var payload struct {
				Agents []agent.Agent `json:"agents"`
			}
			if err := json.Unmarshal([]byte(data), &payload); err != nil {
				t.Fatalf("unmarshal SSE data: %v", err)
			}
			if len(payload.Agents) != 1 || payload.Agents[0].PID != 123 {
				t.Fatalf("unexpected agents: %+v", payload.Agents)
			}
			gotEvent = true
			break
		}
	}
	if !gotEvent {
		t.Fatal("never received agents event")
	}
}

func TestSSEIncludesBadges(t *testing.T) {
	// Create a temp dir with a badge-readable file
	workDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(workDir, "VERSION"), []byte("1.2.3\n"), 0o600)

	s := NewServer(0)
	s.SetDiscoverFunc(func() ([]agent.Agent, error) {
		return []agent.Agent{
			{PID: 456, Name: "badge-test", ProviderName: "claude", Status: agent.StatusActive,
				TokensIn: 5000, WorkingDir: workDir},
		}, nil
	})
	s.SetConfig(config.Config{
		Badges: []config.BadgeRule{
			{Path: "VERSION", Label: "ver"},
		},
	})

	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/events")
	if err != nil {
		t.Fatalf("SSE connect failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	scanner := bufio.NewScanner(resp.Body)
	deadline := time.After(5 * time.Second)

	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for SSE event with badges")
		default:
		}
		if !scanner.Scan() {
			t.Fatal("scanner ended before receiving event")
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		var payload struct {
			Agents []struct {
				PID    int               `json:"PID"`
				Badges []agent.BadgeValue `json:"Badges"`
			} `json:"agents"`
		}
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			t.Fatalf("unmarshal SSE data: %v", err)
		}
		if len(payload.Agents) != 1 {
			t.Fatalf("expected 1 agent, got %d", len(payload.Agents))
		}
		if len(payload.Agents[0].Badges) != 1 {
			t.Fatalf("expected 1 badge, got %d", len(payload.Agents[0].Badges))
		}
		if payload.Agents[0].Badges[0].Label != "ver" {
			t.Errorf("badge label = %q, want %q", payload.Agents[0].Badges[0].Label, "ver")
		}
		if payload.Agents[0].Badges[0].Value != "1.2.3" {
			t.Errorf("badge value = %q, want %q", payload.Agents[0].Badges[0].Value, "1.2.3")
		}
		break
	}
}

func TestSSENoBadgesWhenNoBadgeRules(t *testing.T) {
	workDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(workDir, "VERSION"), []byte("1.0.0\n"), 0o600)

	s := NewServer(0)
	s.SetDiscoverFunc(func() ([]agent.Agent, error) {
		return []agent.Agent{
			{PID: 789, Name: "no-badge", ProviderName: "claude", Status: agent.StatusActive,
				TokensIn: 5000, WorkingDir: workDir},
		}, nil
	})
	// No badge rules configured

	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/events")
	if err != nil {
		t.Fatalf("SSE connect failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	scanner := bufio.NewScanner(resp.Body)
	deadline := time.After(5 * time.Second)

	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for SSE event")
		default:
		}
		if !scanner.Scan() {
			t.Fatal("scanner ended before receiving event")
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		var payload struct {
			Agents []struct {
				Badges []agent.BadgeValue `json:"Badges"`
			} `json:"agents"`
		}
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			t.Fatalf("unmarshal SSE data: %v", err)
		}
		if len(payload.Agents) != 1 {
			t.Fatalf("expected 1 agent, got %d", len(payload.Agents))
		}
		if len(payload.Agents[0].Badges) != 0 {
			t.Errorf("expected 0 badges without rules, got %d", len(payload.Agents[0].Badges))
		}
		break
	}
}

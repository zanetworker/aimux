package web

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
)

func TestSSEStreamsAgentState(t *testing.T) {
	s := NewServer(0)
	s.SetDiscoverFunc(func() ([]agent.Agent, error) {
		return []agent.Agent{
			{PID: 123, Name: "test-repo", ProviderName: "claude", Status: agent.StatusActive},
		}, nil
	})

	go s.Start()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/events")
	if err != nil {
		t.Fatalf("SSE connect failed: %v", err)
	}
	defer resp.Body.Close()

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

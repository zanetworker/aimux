package web

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/config"
)

func TestAPISmoke_AllGETEndpoints(t *testing.T) {
	s := NewServer(0)
	cfg := config.Default()
	s.SetConfig(cfg)
	s.SetDiscoverFunc(func() ([]agent.Agent, error) {
		return []agent.Agent{
			{PID: 1, Name: "test-agent", ProviderName: "claude", Status: agent.StatusActive, WorkingDir: "/tmp/test"},
		}, nil
	})

	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(200 * time.Millisecond)

	addr := s.URL()

	endpoints := []struct {
		path   string
		status int
	}{
		{"/api/health", 200},
		{"/api/costs", 200},
		{"/api/agents", 200},
		{"/api/agents?sort=name", 200},
		{"/api/agents?filter=claude", 200},
		{"/api/teams", 200},
		{"/api/health/providers", 200},
		{"/api/history", 200},
	}

	for _, ep := range endpoints {
		t.Run(ep.path, func(t *testing.T) {
			resp, err := http.Get(addr + ep.path) // #nosec G107
			if err != nil {
				t.Fatalf("GET %s: %v", ep.path, err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != ep.status {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("GET %s: status %d, want %d\n%s", ep.path, resp.StatusCode, ep.status, body)
				return
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("GET %s: read body: %v", ep.path, err)
			}

			if !json.Valid(body) {
				t.Errorf("GET %s: response is not valid JSON: %s", ep.path, body)
			}
		})
	}
}

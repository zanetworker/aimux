package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/config"
)

// TestAPISmoke_AllEndpoints boots a real server and hits every registered endpoint.
// This is the single test that catches regressions across the entire API surface.
// If this test passes, every endpoint responds without panicking or 500-ing.
//
// Run with: go test ./internal/frontend/web/ -run Smoke -timeout 60s -v
func TestAPISmoke_AllEndpoints(t *testing.T) {
	s := NewServer(0)
	cfg := config.Default()
	s.SetConfig(cfg)
	s.SetDiscoverFunc(func() ([]agent.Agent, error) {
		return []agent.Agent{
			{PID: 1, Name: "test-agent", ProviderName: "claude", Status: agent.StatusActive, WorkingDir: "/tmp/test", SessionID: "test-session-1"},
		}, nil
	})
	s.SetRecentDirsFunc(func(max int) []RecentDirInfo {
		return []RecentDirInfo{{Path: "/tmp/test", Display: "test", Age: "1m"}}
	})

	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(200 * time.Millisecond)

	addr := s.URL()

	// --- GET endpoints: expect 200 + valid JSON ---
	gets := []string{
		"/api/health",
		"/api/agents",
		"/api/agents?sort=name",
		"/api/agents?filter=claude",
		"/api/costs",
		"/api/teams",
		"/api/health/providers",
		"/api/history",
		"/api/search?q=test",
		"/api/plugins",
		"/api/directories/recent",
		"/api/quick-launch",
		"/api/sessions/nonexistent/annotations",
		"/api/sessions/diffs?file=/nonexistent",
	}

	for _, path := range gets {
		t.Run("GET "+path, func(t *testing.T) {
			resp, err := http.Get(addr + path) // #nosec G107
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(resp.Body)

			// 500 = real server error (bug). 503 = not configured. 404 = not found.
			// Only 500 is a test failure.
			if resp.StatusCode == 500 {
				t.Errorf("GET %s: status 500 (server error)\n%s", path, body)
				return
			}
			if resp.StatusCode == 200 && !json.Valid(body) && len(body) > 0 {
				t.Errorf("GET %s: 200 but not valid JSON: %.100s", path, body)
			}
		})
	}

	// --- SSE endpoint: check content type ---
	t.Run("GET /api/events (SSE)", func(t *testing.T) {
		client := &http.Client{Timeout: 1 * time.Second}
		resp, err := client.Get(addr + "/api/events") // #nosec G107
		if err != nil && !strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "deadline") {
			t.Fatalf("GET /api/events: %v", err)
		}
		if resp != nil {
			defer func() { _ = resp.Body.Close() }()
			if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
				t.Errorf("SSE Content-Type = %q, want text/event-stream", ct)
			}
		}
	})

	// --- GET endpoints that may return non-200 (graceful error handling) ---
	gracefulGets := []string{
		"/api/trace?file=/nonexistent/path.jsonl",
		"/api/agents/nonexistent/trace",
		"/api/agents/nonexistent/diff",
		"/api/tasks/lists",
		"/api/tasks?list=default",
	}

	for _, path := range gracefulGets {
		t.Run("GET "+path+" (graceful)", func(t *testing.T) {
			resp, err := http.Get(addr + path) // #nosec G107
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode == 500 {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("GET %s: status 500 (server error)\n%s", path, body)
			}
		})
	}

	// --- POST endpoints: test with minimal/empty bodies ---
	posts := []struct {
		path string
		body interface{}
	}{
		{"/api/agents/1/archive", nil},
		{"/api/agents/1/annotate", map[string]interface{}{"turn": 1, "label": "GOOD"}},
		{"/api/sessions/nonexistent/export/jsonl", nil},
		{"/api/sessions/nonexistent/export/otel", nil},
		{"/api/sessions/meta", map[string]interface{}{"file": "/nonexistent", "starred": true}},
		{"/api/insight", map[string]string{"query": "test"}},
		{"/api/tasks/test-id/complete", nil},
		{"/api/tasks/test-id/reopen", nil},
		{"/api/trace/subscribe/test-session", nil},
		{"/api/trace/unsubscribe/test-session", nil},
	}

	for _, p := range posts {
		t.Run("POST "+p.path, func(t *testing.T) {
			var bodyReader *bytes.Reader
			if p.body != nil {
				data, _ := json.Marshal(p.body)
				bodyReader = bytes.NewReader(data)
			} else {
				bodyReader = bytes.NewReader([]byte("{}"))
			}
			resp, err := http.Post(addr+p.path, "application/json", bodyReader) // #nosec G107
			if err != nil {
				t.Fatalf("POST %s: %v", p.path, err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode == 500 {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("POST %s: status 500 (server error)\n%s", p.path, body)
			}
		})
	}

	// --- DELETE endpoint ---
	t.Run("DELETE /api/sessions/nonexistent", func(t *testing.T) {
		req, _ := http.NewRequest("DELETE", addr+"/api/sessions/nonexistent", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("DELETE: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == 500 {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("DELETE: status 500 (server error)\n%s", body)
		}
	})

	// --- Skipped endpoints ---
	// POST /api/agents/launch — requires LaunchFunc (tested separately in handlers_test.go)
	// POST /api/sessions/generate-titles — calls LLM API (requires API key)
	// /api/terminal/* — WebSocket (requires WS client)
	// GET /api/sessions/meta — requires file query param with real file
}

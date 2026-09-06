package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/tasks"
	"github.com/zanetworker/aimux/internal/trace"
)

// stubParser returns empty turns for any file (avoids 503 on trace endpoints).
type stubParser struct{}

func (p *stubParser) ParseTrace(_ string) ([]trace.Turn, error) {
	return nil, nil
}

// TestAPISmoke_AllEndpoints boots a real server and hits every registered endpoint.
// This is the single test that catches regressions across the entire API surface.
//
// Status code semantics:
//   500 = FAIL (real server error / panic)
//   503 = LOG  (feature not configured — blind spot warning)
//   404 = OK   (resource not found, expected for nonexistent IDs)
//   200 = OK   (success, validate JSON)
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
	s.SetProviderLookup(func(name string) interface{ ParseTrace(string) ([]trace.Turn, error) } {
		return &stubParser{}
	})
	s.SetKillFunc(func(pid int, tmuxSession string) error { return nil })
	s.SetController(controller.New(cfg))
	s.SetTaskProvider(&mockTaskProvider{
		lists: []tasks.TaskList{{ID: "test-list", Name: "Test"}},
		items: []tasks.Task{{ID: "task-1", Title: "Test task", Status: "needsAction"}},
	})

	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(200 * time.Millisecond)

	addr := s.URL()

	var unconfigured []string

	checkResponse := func(t *testing.T, method, path string, status int, body []byte) {
		t.Helper()
		switch {
		case status == 500:
			t.Errorf("%s %s: status 500 (SERVER ERROR — this is a bug)\n%s", method, path, body)
		case status == 503:
			unconfigured = append(unconfigured, fmt.Sprintf("%s %s: 503 — %s", method, path, strings.TrimSpace(string(body))))
		case status == 200 && !json.Valid(body) && len(body) > 0:
			t.Errorf("%s %s: 200 but response is not valid JSON: %.100s", method, path, body)
		}
	}

	// --- GET endpoints ---
	gets := []string{
		"/api/health",
		"/api/agents",
		"/api/agents?sort=name",
		"/api/agents?filter=claude",
		"/api/costs",
		"/api/teams",
		"/api/health/providers",
		"/api/health/remote",
		"/api/history",
		"/api/search?q=test",
		"/api/plugins",
		"/api/directories/recent",
		"/api/quick-launch",
		"/api/sessions/nonexistent/annotations",
		"/api/sessions/diffs?file=/nonexistent",
		"/api/trace?file=/nonexistent/path.jsonl",
		"/api/agents/nonexistent/trace",
		"/api/agents/nonexistent/diff",
		"/api/tasks/lists",
		"/api/tasks?list=default",
		"/api/agent-configs",
		"/api/environments",
	}

	for _, path := range gets {
		t.Run("GET "+path, func(t *testing.T) {
			resp, err := http.Get(addr + path) // #nosec G107
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(resp.Body)
			checkResponse(t, "GET", path, resp.StatusCode, body)
		})
	}

	// --- SSE endpoint ---
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

	// --- POST endpoints ---
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
			body, _ := io.ReadAll(resp.Body)
			checkResponse(t, "POST", p.path, resp.StatusCode, body)
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
		body, _ := io.ReadAll(resp.Body)
		checkResponse(t, "DELETE", "/api/sessions/nonexistent", resp.StatusCode, body)
	})

	// --- Report unconfigured endpoints (blind spots) ---
	if len(unconfigured) > 0 {
		t.Logf("\n=== UNCONFIGURED ENDPOINTS (503 — blind spots) ===")
		for _, msg := range unconfigured {
			t.Logf("  %s", msg)
		}
		t.Logf("These endpoints return 503 because their backing service is not wired in the test server.")
		t.Logf("To fix: add SetXxxFunc/SetXxxProvider calls in the smoke test setup.")
		t.Logf("=== %d endpoint(s) unconfigured ===\n", len(unconfigured))
	}

	// --- Skipped endpoints (documented) ---
	// POST /api/agents/launch — requires LaunchFunc (tested in handlers_test.go)
	// POST /api/sessions/generate-titles — calls LLM API (requires API key)
	// /api/terminal/* — WebSocket (requires WS client)
	// GET /api/sessions/meta — requires real file path
}

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

func startTestServer(t *testing.T) *Server {
	t.Helper()
	s := NewServer(0)
	s.SetConfig(config.Default())
	s.SetDiscoverFunc(func() ([]agent.Agent, error) {
		return []agent.Agent{
			{PID: 100, Name: "test-agent", ProviderName: "claude", Status: agent.StatusActive, WorkingDir: "/tmp/test"},
		}, nil
	})
	go func() { _ = s.Start() }()
	t.Cleanup(func() { s.Stop() })
	time.Sleep(150 * time.Millisecond)
	return s
}

func getJSON(t *testing.T, url string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(url) // #nosec G107
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func postJSON(t *testing.T, url string, payload interface{}) (int, []byte) {
	t.Helper()
	data, _ := json.Marshal(payload)
	resp, err := http.Post(url, "application/json", bytes.NewReader(data)) // #nosec G107
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// --- GET endpoints ---

func TestSSE_ContentType(t *testing.T) {
	s := startTestServer(t)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(s.URL() + "/api/events") // #nosec G107
	if err != nil && !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("GET /api/events: %v", err)
	}
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
		ct := resp.Header.Get("Content-Type")
		if !strings.Contains(ct, "text/event-stream") {
			t.Errorf("Content-Type = %q, want text/event-stream", ct)
		}
	}
}

func TestRecentDirs(t *testing.T) {
	s := startTestServer(t)
	s.SetRecentDirsFunc(func(max int) []RecentDirInfo {
		return []RecentDirInfo{{Path: "/tmp/test", Display: "test", Age: "1m"}}
	})
	status, body := getJSON(t, s.URL()+"/api/directories/recent")
	if status != 200 {
		t.Errorf("status = %d, want 200", status)
	}
	if !json.Valid(body) {
		t.Error("response not valid JSON")
	}
}

func TestQuickLaunch(t *testing.T) {
	s := startTestServer(t)
	status, body := getJSON(t, s.URL()+"/api/quick-launch")
	if status != 200 {
		t.Errorf("status = %d, want 200", status)
	}
	if !json.Valid(body) {
		t.Error("response not valid JSON")
	}
}

func TestPlugins(t *testing.T) {
	s := startTestServer(t)
	status, body := getJSON(t, s.URL()+"/api/plugins")
	if status != 200 {
		t.Errorf("status = %d, want 200", status)
	}
	if !json.Valid(body) {
		t.Error("response not valid JSON")
	}
}

func TestSearch_Empty(t *testing.T) {
	s := startTestServer(t)
	status, body := getJSON(t, s.URL()+"/api/search?q=nonexistent_query_xyz")
	if status != 200 {
		t.Errorf("status = %d, want 200", status)
	}
	if !json.Valid(body) {
		t.Error("response not valid JSON")
	}
}

func TestFastTrace_MissingFile(t *testing.T) {
	s := startTestServer(t)
	status, _ := getJSON(t, s.URL()+"/api/trace?file=/nonexistent/path.jsonl")
	if status == 500 {
		t.Error("should handle missing file gracefully, not 500")
	}
}

func TestAnnotations_MissingSession(t *testing.T) {
	s := startTestServer(t)
	status, body := getJSON(t, s.URL()+"/api/sessions/nonexistent-id/annotations")
	if status == 500 {
		t.Error("should handle missing session gracefully")
	}
	if !json.Valid(body) {
		t.Error("response not valid JSON")
	}
}

func TestTaskLists_NoProvider(t *testing.T) {
	s := startTestServer(t)
	status, _ := getJSON(t, s.URL()+"/api/tasks/lists")
	if status == 500 {
		t.Error("should handle missing task provider gracefully")
	}
}

func TestTasks_NoProvider(t *testing.T) {
	s := startTestServer(t)
	status, _ := getJSON(t, s.URL()+"/api/tasks?list=default")
	if status == 500 {
		t.Error("should handle missing task provider gracefully")
	}
}

// --- POST endpoints ---

func TestArchive_NotFound(t *testing.T) {
	s := startTestServer(t)
	status, _ := postJSON(t, s.URL()+"/api/agents/999/archive", nil)
	if status == 500 {
		t.Error("archive of nonexistent agent should not 500")
	}
}

// TestGenerateTitles skipped — handler calls LLM API (requires API key + network)

func TestExportJSONL_NotConfigured(t *testing.T) {
	s := startTestServer(t)
	status, _ := postJSON(t, s.URL()+"/api/sessions/nonexistent/export/jsonl", nil)
	if status == 500 {
		t.Error("export jsonl should handle missing session gracefully")
	}
}

func TestExportOTEL_NotConfigured(t *testing.T) {
	s := startTestServer(t)
	status, _ := postJSON(t, s.URL()+"/api/sessions/nonexistent/export/otel", nil)
	if status == 500 {
		t.Error("export otel should handle missing session gracefully")
	}
}

func TestInsight_InvalidBody(t *testing.T) {
	s := startTestServer(t)
	status, _ := postJSON(t, s.URL()+"/api/insight", map[string]string{"query": "test"})
	if status == 500 {
		t.Error("insight should not panic on request")
	}
}

func TestTaskComplete_NoProvider(t *testing.T) {
	s := startTestServer(t)
	status, _ := postJSON(t, s.URL()+"/api/tasks/test-id/complete", nil)
	if status == 500 {
		t.Error("task complete without provider should not 500")
	}
}

func TestTaskReopen_NoProvider(t *testing.T) {
	s := startTestServer(t)
	status, _ := postJSON(t, s.URL()+"/api/tasks/test-id/reopen", nil)
	if status == 500 {
		t.Error("task reopen without provider should not 500")
	}
}

func TestTraceSubscribe(t *testing.T) {
	s := startTestServer(t)
	status, _ := postJSON(t, s.URL()+"/api/trace/subscribe/test-session", nil)
	if status != 200 {
		t.Errorf("trace subscribe status = %d, want 200", status)
	}
}

func TestTraceUnsubscribe(t *testing.T) {
	s := startTestServer(t)
	status, _ := postJSON(t, s.URL()+"/api/trace/unsubscribe/test-session", nil)
	if status != 200 {
		t.Errorf("trace unsubscribe status = %d, want 200", status)
	}
}

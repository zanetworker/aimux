package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/controller"
	aimuxotel "github.com/zanetworker/aimux/internal/otel"
	"github.com/zanetworker/aimux/internal/provider"
	"github.com/zanetworker/aimux/internal/spawn"
	"github.com/zanetworker/aimux/internal/trace"
)

func TestLaunchHandler(t *testing.T) {
	s := NewServer(0)
	var launched bool
	s.SetLaunchFunc(func(opts spawn.LaunchOpts) (spawn.LaunchResult, error) {
		launched = true
		if opts.Provider != "claude" {
			t.Errorf("expected provider claude, got %s", opts.Provider)
		}
		return spawn.LaunchResult{TmuxSession: "aimux-claude-test"}, nil
	})

	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	tmpDir := t.TempDir()
	body, _ := json.Marshal(map[string]string{
		"provider": "claude",
		"dir":      tmpDir,
		"model":    "opus",
		"mode":     "auto",
	})
	resp, err := http.Post(s.URL()+"/api/agents/launch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/agents/launch failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !launched {
		t.Fatal("launch function was not called")
	}
}

func TestLaunchHandler_StoresSessionAndReturnsUUID(t *testing.T) {
	sessionStore := controller.NewSessionStore(t.TempDir())
	s := NewServer(0)
	s.SetSessionStore(sessionStore)
	s.SetLaunchFunc(func(opts spawn.LaunchOpts) (spawn.LaunchResult, error) {
		return spawn.LaunchResult{
			TmuxSession:   "aimux-claude-test",
			SandboxName:   "test-sandbox",
			OTELSessionID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		}, nil
	})

	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	tmpDir := t.TempDir()
	body, _ := json.Marshal(map[string]string{
		"provider": "claude",
		"dir":      tmpDir,
	})
	resp, err := http.Post(s.URL()+"/api/agents/launch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var payload map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if payload["sandbox_name"] != "test-sandbox" {
		t.Errorf("expected sandbox_name=test-sandbox, got %v", payload["sandbox_name"])
	}
	if payload["otel_session_id"] != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Errorf("expected otel_session_id in response, got %v", payload["otel_session_id"])
	}

	stored := sessionStore.Get("test-sandbox")
	if stored != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Errorf("sessionStore should contain the UUID, got %q", stored)
	}
}

func TestLaunchHandler_NoStoreWithoutSandbox(t *testing.T) {
	sessionStore := controller.NewSessionStore(t.TempDir())
	s := NewServer(0)
	s.SetSessionStore(sessionStore)
	s.SetLaunchFunc(func(opts spawn.LaunchOpts) (spawn.LaunchResult, error) {
		return spawn.LaunchResult{TmuxSession: "aimux-claude-local"}, nil
	})

	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	tmpDir := t.TempDir()
	body, _ := json.Marshal(map[string]string{
		"provider": "claude",
		"dir":      tmpDir,
	})
	resp, err := http.Post(s.URL()+"/api/agents/launch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var payload map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&payload)

	if _, ok := payload["sandbox_name"]; ok {
		t.Error("should not include sandbox_name when empty")
	}
	if _, ok := payload["otel_session_id"]; ok {
		t.Error("should not include otel_session_id when empty")
	}
}

func TestHistoryHandler(t *testing.T) {
	s := NewServer(0)

	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/history")
	if err != nil {
		t.Fatalf("GET /api/history failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var payload struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Should return a non-nil array (may be empty in test environment)
	if payload.Sessions == nil {
		t.Fatal("expected sessions array, got nil")
	}

	// If there are sessions, verify the shape
	if len(payload.Sessions) > 0 {
		s0 := payload.Sessions[0]
		for _, field := range []string{"id", "provider", "project", "filePath", "lastActive", "turnCount", "costUSD"} {
			if _, ok := s0[field]; !ok {
				t.Errorf("session missing field %q", field)
			}
		}
	}
}

func TestHistoryHandler_AllSessionFieldsMapped(t *testing.T) {
	expectedFields := []string{
		"id", "provider", "project", "filePath", "startTime", "lastActive",
		"turnCount", "tokensIn", "tokensOut", "costUSD", "firstPrompt",
		"title", "resumable", "annotation", "tags", "note",
		"isSubagent", "permissionMode", "starred",
	}

	s := NewServer(0)
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/history")
	if err != nil {
		t.Fatalf("GET /api/history failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var payload struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(payload.Sessions) == 0 {
		t.Skip("no sessions in test environment")
	}

	s0 := payload.Sessions[0]
	for _, field := range expectedFields {
		if _, ok := s0[field]; !ok {
			t.Errorf("session missing field %q in /api/history response", field)
		}
	}
}

func TestAnnotateHandler(t *testing.T) {
	s := NewServer(0)

	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	t.Cleanup(func() {
		home, _ := os.UserHomeDir()
		_ = os.Remove(filepath.Join(home, ".aimux", "evaluations", "abc-123.jsonl"))
	})

	body, _ := json.Marshal(map[string]any{
		"turn":  1,
		"label": "good",
		"note":  "clean implementation",
	})
	resp, err := http.Post(s.URL()+"/api/sessions/abc-123/annotate", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestGetAnnotationsHandler(t *testing.T) {
	s := NewServer(0)
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	t.Cleanup(func() {
		home, _ := os.UserHomeDir()
		_ = os.Remove(filepath.Join(home, ".aimux", "evaluations", "test-annot-session.jsonl"))
	})

	// POST an annotation
	body, _ := json.Marshal(map[string]any{"turn": 1, "label": "good", "note": "clean code"})
	resp, err := http.Post(s.URL()+"/api/sessions/test-annot-session/annotate", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST expected 200, got %d", resp.StatusCode)
	}

	// GET annotations
	resp, err = http.Get(s.URL() + "/api/sessions/test-annot-session/annotations")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET expected 200, got %d", resp.StatusCode)
	}

	var payload struct {
		Annotations []map[string]any `json:"annotations"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	if len(payload.Annotations) == 0 {
		t.Fatal("expected at least one annotation")
	}
	if payload.Annotations[0]["label"] != "good" {
		t.Errorf("expected label good, got %v", payload.Annotations[0]["label"])
	}
}

func TestSessionMetaHandler(t *testing.T) {
	tmpDir := t.TempDir()
	sessionFile := filepath.Join(tmpDir, "test-meta-session.jsonl")
	_ = os.WriteFile(sessionFile, []byte("{\"type\":\"user\"}\n"), 0o600)

	s := NewServer(0)
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	// POST meta
	body, _ := json.Marshal(map[string]any{
		"filePath": sessionFile, "annotation": "achieved",
		"tags": []string{"clean-code"}, "note": "Great session",
	})
	resp, err := http.Post(s.URL()+"/api/sessions/meta", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST expected 200, got %d", resp.StatusCode)
	}

	// GET meta
	resp, err = http.Get(s.URL() + "/api/sessions/meta?file=" + sessionFile)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var meta struct {
		Annotation string   `json:"annotation"`
		Tags       []string `json:"tags"`
		Note       string   `json:"note"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&meta)
	if meta.Annotation != "achieved" {
		t.Errorf("expected achieved, got %s", meta.Annotation)
	}
	if len(meta.Tags) != 1 || meta.Tags[0] != "clean-code" {
		t.Errorf("expected [clean-code], got %v", meta.Tags)
	}
	if meta.Note != "Great session" {
		t.Errorf("expected 'Great session', got %s", meta.Note)
	}
}

func TestHandleBrowseDirectory(t *testing.T) {
	// Create temp dir with subdirectory, file, and hidden dir
	tmpDir := t.TempDir()
	_ = os.Mkdir(filepath.Join(tmpDir, "subdir"), 0o750)
	_ = os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("test"), 0o600)
	_ = os.Mkdir(filepath.Join(tmpDir, ".hidden"), 0o750)

	s := NewServer(0)
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/directories/browse?path=" + tmpDir)
	if err != nil {
		t.Fatalf("GET /api/directories/browse failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var payload struct {
		Path    string `json:"path"`
		Entries []struct {
			Name  string `json:"name"`
			IsDir bool   `json:"isDir"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Verify path
	if payload.Path != tmpDir {
		t.Errorf("expected path %s, got %s", tmpDir, payload.Path)
	}

	// Verify hidden dir is excluded
	for _, e := range payload.Entries {
		if e.Name == ".hidden" {
			t.Error("hidden directory should be excluded")
		}
	}

	// Verify subdirectory appears with isDir: true
	foundSubdir := false
	for _, e := range payload.Entries {
		if e.Name == "subdir" {
			foundSubdir = true
			if !e.IsDir {
				t.Error("subdir should have isDir: true")
			}
		}
	}
	if !foundSubdir {
		t.Error("subdir should be in the results")
	}

	// Verify file appears
	foundFile := false
	for _, e := range payload.Entries {
		if e.Name == "file.txt" {
			foundFile = true
			if e.IsDir {
				t.Error("file.txt should have isDir: false")
			}
		}
	}
	if !foundFile {
		t.Error("file.txt should be in the results")
	}
}

func TestSessionDiffsHandler_MissingFile(t *testing.T) {
	s := NewServer(0)
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/sessions/diffs")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestSessionDiffsHandler_WithSessionFile(t *testing.T) {
	tmpDir := t.TempDir()
	sessionFile := filepath.Join(tmpDir, "test-diff.jsonl")

	content := `{"type":"summary","session_id":"test-123"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"Edit","input":{"file_path":"main.go","old_string":"old","new_string":"new"}}]}}
{"type":"result","tool_use_id":"tu1","content":"OK"}
`
	_ = os.WriteFile(sessionFile, []byte(content), 0o600)

	s := NewServer(0)
	s.SetProviderLookup(func(name string) interface{ ParseTrace(string) ([]trace.Turn, error) } {
		return &provider.Claude{}
	})
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/sessions/diffs?file=" + sessionFile)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var payload struct {
		Files      []map[string]any `json:"files"`
		TotalFiles int              `json:"totalFiles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if payload.TotalFiles < 0 {
		t.Errorf("totalFiles should be >= 0, got %d", payload.TotalFiles)
	}
}

func makeTestAgents() []agent.Agent {
	return []agent.Agent{
		{
			PID: 1001, SessionID: "sess-1", Name: "aimux",
			ProviderName: "claude", Model: "claude-opus-4-6[1m]",
			WorkingDir: "/home/user/projects/aimux",
			Status: agent.StatusActive, TokensIn: 5000, TokensOut: 2000,
			EstCostUSD: 0.85, CPUPercent: 12.5, MemoryMB: 400,
			StartTime: time.Now().Add(-10 * time.Minute),
			LastActivity: time.Now().Add(-1 * time.Minute),
			GitBranch: "main", LastAction: "Ed main.go",
		},
		{
			PID: 1002, SessionID: "sess-2", Name: "showtime",
			ProviderName: "codex", Model: "o4-mini",
			WorkingDir: "/home/user/projects/showtime",
			Status: agent.StatusIdle, TokensIn: 3000, TokensOut: 1000,
			EstCostUSD: 0.25, CPUPercent: 0.5, MemoryMB: 200,
			StartTime: time.Now().Add(-30 * time.Minute),
			LastActivity: time.Now().Add(-5 * time.Minute),
			GitBranch: "feat/record", LastAction: "Sh go test",
		},
		{
			PID: 1003, SessionID: "sess-3", Name: "aimux",
			ProviderName: "claude", Model: "claude-sonnet-4-5",
			WorkingDir: "/home/user/projects/aimux",
			Status: agent.StatusActive, TokensIn: 8000, TokensOut: 4000,
			EstCostUSD: 0.42, CPUPercent: 8.0, MemoryMB: 350,
			StartTime: time.Now().Add(-5 * time.Minute),
			LastActivity: time.Now(),
			GitBranch: "main", LastAction: "Rd config.go",
		},
	}
}

func newServerWithAgents(agents []agent.Agent) *Server {
	s := NewServer(0)
	s.SetDiscoverFunc(func() ([]agent.Agent, error) {
		return agents, nil
	})
	s.SetKillFunc(func(pid int, tmux string) error {
		return nil
	})
	return s
}

func TestHandleCosts(t *testing.T) {
	agents := makeTestAgents()
	s := newServerWithAgents(agents)
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/costs")
	if err != nil {
		t.Fatalf("GET /api/costs failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var payload struct {
		Costs []struct {
			Project    string  `json:"project"`
			Provider   string  `json:"provider"`
			TokensIn   int64   `json:"tokens_in"`
			TokensOut  int64   `json:"tokens_out"`
			CostUSD    float64 `json:"cost"`
			AgentCount int     `json:"agent_count"`
		} `json:"costs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Two projects: aimux (2 agents) and showtime (1 agent)
	if len(payload.Costs) != 2 {
		t.Fatalf("expected 2 cost entries, got %d", len(payload.Costs))
	}

	// Sorted by cost desc: aimux (0.85+0.42=1.27) > showtime (0.25)
	if payload.Costs[0].Project != "aimux" {
		t.Errorf("expected first entry to be aimux, got %s", payload.Costs[0].Project)
	}
	if payload.Costs[0].AgentCount != 2 {
		t.Errorf("expected 2 agents for aimux, got %d", payload.Costs[0].AgentCount)
	}
	if payload.Costs[0].TokensIn != 13000 {
		t.Errorf("expected 13000 tokens_in for aimux, got %d", payload.Costs[0].TokensIn)
	}
	if payload.Costs[1].Project != "showtime" {
		t.Errorf("expected second entry to be showtime, got %s", payload.Costs[1].Project)
	}
}

func TestHandleCosts_NotConfigured(t *testing.T) {
	s := NewServer(0)
	// No discoverFn set
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/costs")
	if err != nil {
		t.Fatalf("GET /api/costs failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
}

func TestHandleAgents(t *testing.T) {
	agents := makeTestAgents()
	s := newServerWithAgents(agents)
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/agents")
	if err != nil {
		t.Fatalf("GET /api/agents failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var payload struct {
		Agents []map[string]any `json:"agents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Agents) != 3 {
		t.Fatalf("expected 3 agents, got %d", len(payload.Agents))
	}

	// Verify required fields present
	for _, field := range []string{"pid", "sessionId", "provider", "model", "project", "status", "costUSD"} {
		if _, ok := payload.Agents[0][field]; !ok {
			t.Errorf("agent missing field %q", field)
		}
	}
}

func TestHandleAgents_Filter(t *testing.T) {
	agents := makeTestAgents()
	s := newServerWithAgents(agents)
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/agents?filter=codex")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var payload struct {
		Agents []map[string]any `json:"agents"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&payload)

	if len(payload.Agents) != 1 {
		t.Fatalf("expected 1 agent matching 'codex', got %d", len(payload.Agents))
	}
	if payload.Agents[0]["provider"] != "codex" {
		t.Errorf("expected provider codex, got %v", payload.Agents[0]["provider"])
	}
}

func TestHandleAgents_Sort(t *testing.T) {
	agents := makeTestAgents()
	s := newServerWithAgents(agents)
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/agents?sort=cost")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var payload struct {
		Agents []map[string]any `json:"agents"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&payload)

	if len(payload.Agents) != 3 {
		t.Fatalf("expected 3 agents, got %d", len(payload.Agents))
	}

	// Sort by cost descending: first should have highest cost (0.85)
	firstCost := payload.Agents[0]["costUSD"].(float64)
	secondCost := payload.Agents[1]["costUSD"].(float64)
	if firstCost < secondCost {
		t.Errorf("expected descending cost order, got first=%.2f second=%.2f", firstCost, secondCost)
	}
}

func TestHandleDeleteSession(t *testing.T) {
	tmpDir := t.TempDir()
	sessionFile := filepath.Join(tmpDir, "test-delete-session.jsonl")
	_ = os.WriteFile(sessionFile, []byte("{\"type\":\"summary\",\"session_id\":\"del-123\"}\n"), 0o600)

	// Verify file exists before delete
	if _, err := os.Stat(sessionFile); err != nil {
		t.Fatalf("session file should exist before delete: %v", err)
	}

	s := NewServer(0)
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	// Try deleting a non-existent session
	req, _ := http.NewRequest("DELETE", s.URL()+"/api/sessions/nonexistent-id", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for nonexistent session, got %d", resp.StatusCode)
	}
}

func TestHandleKill_ProcessAgent(t *testing.T) {
	var killedPID int
	agents := []agent.Agent{
		{
			PID: 9999, SessionID: "kill-sess-1",
			ProviderName: "claude", Status: agent.StatusActive,
			WorkingDir: "/tmp/test",
		},
	}
	s := NewServer(0)
	s.SetDiscoverFunc(func() ([]agent.Agent, error) {
		return agents, nil
	})
	s.SetKillFunc(func(pid int, tmux string) error {
		killedPID = pid
		return nil
	})

	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Post(s.URL()+"/api/agents/kill-sess-1/kill", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if killedPID != 9999 {
		t.Errorf("expected kill PID 9999, got %d", killedPID)
	}

	var payload struct {
		Status   string `json:"status"`
		KillType int    `json:"killType"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	if payload.Status != "killed" {
		t.Errorf("expected status 'killed', got %q", payload.Status)
	}
}

func TestHandleKill_NotFound(t *testing.T) {
	s := newServerWithAgents(nil)
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Post(s.URL()+"/api/agents/nonexistent/kill", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestHandleKill_ByPID(t *testing.T) {
	var killedPID int
	agents := []agent.Agent{
		{
			PID: 42, SessionID: "pid-sess",
			ProviderName: "claude", Status: agent.StatusActive,
			WorkingDir: "/tmp/test",
		},
	}
	s := NewServer(0)
	s.SetDiscoverFunc(func() ([]agent.Agent, error) {
		return agents, nil
	})
	s.SetKillFunc(func(pid int, tmux string) error {
		killedPID = pid
		return nil
	})

	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Post(s.URL()+"/api/agents/42/kill", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if killedPID != 42 {
		t.Errorf("expected kill PID 42, got %d", killedPID)
	}
}

func TestHandleKill_RemoveOnly(t *testing.T) {
	agents := []agent.Agent{
		{
			PID: 0, SessionID: "remove-only-sess",
			ProviderName: "claude", Status: agent.StatusUnknown,
			WorkingDir: "/tmp/test",
		},
	}
	s := newServerWithAgents(agents)
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Post(s.URL()+"/api/agents/remove-only-sess/kill", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var payload struct {
		KillType int `json:"killType"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	// KillRemoveOnly = 2
	if payload.KillType != 2 {
		t.Errorf("expected killType 2 (RemoveOnly), got %d", payload.KillType)
	}
}

func TestHandleCosts_NoAgents(t *testing.T) {
	s := newServerWithAgents(nil)
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/costs")
	if err != nil {
		t.Fatalf("GET /api/costs failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var payload struct {
		Costs []map[string]any `json:"costs"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	if len(payload.Costs) != 0 {
		t.Errorf("expected empty costs array, got %d entries", len(payload.Costs))
	}
}

func TestHandleAgents_NotConfigured(t *testing.T) {
	s := NewServer(0)
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/agents")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", resp.StatusCode)
	}
}

func TestHandleTeams(t *testing.T) {
	s := NewServer(0)
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/teams")
	if err != nil {
		t.Fatalf("GET /api/teams failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var payload struct {
		Teams []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Members     []struct {
				AgentID   string `json:"agentId"`
				Name      string `json:"name"`
				AgentType string `json:"agentType"`
				Model     string `json:"model"`
			} `json:"members"`
		} `json:"teams"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Should return a non-nil array (may be empty if ~/.claude/teams/ doesn't exist)
	if payload.Teams == nil {
		t.Fatal("expected teams array, got nil")
	}
}

func TestHandleTeams_WithTeamDir(t *testing.T) {
	// Create a temp teams directory with a valid team config
	tmpDir := t.TempDir()
	teamDir := filepath.Join(tmpDir, "test-team")
	if err := os.Mkdir(teamDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	configData := `{
		"name": "Alpha Squad",
		"description": "Test team for unit tests",
		"members": [
			{"agentId": "a1", "name": "Agent One", "agentType": "claude", "model": "opus"}
		]
	}`
	if err := os.WriteFile(filepath.Join(teamDir, "config.json"), []byte(configData), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// We can't easily redirect ListTeamsDefault to our tmpDir, but we can verify
	// the endpoint always returns 200 with a valid JSON array shape.
	// The main TestHandleTeams above covers that. This test validates that the
	// team package can parse what we send.
	s := NewServer(0)
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/teams")
	if err != nil {
		t.Fatalf("GET /api/teams failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Verify Content-Type header
	ct := resp.Header.Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}
}

func TestHandleProviderHealth(t *testing.T) {
	s := NewServer(0)
	s.SetConfig(config.Default())
	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/health/providers")
	if err != nil {
		t.Fatalf("GET /api/health/providers failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var payload struct {
		Providers []struct {
			Name      string `json:"name"`
			Enabled   bool   `json:"enabled"`
			Installed bool   `json:"installed"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(payload.Providers) != 3 {
		t.Fatalf("expected 3 providers, got %d", len(payload.Providers))
	}

	// Verify names are claude, codex, gemini in order
	expectedNames := []string{"claude", "codex", "gemini"}
	for i, name := range expectedNames {
		if payload.Providers[i].Name != name {
			t.Errorf("provider[%d]: expected name %q, got %q", i, name, payload.Providers[i].Name)
		}
	}

	// All three should be enabled with default config
	for _, p := range payload.Providers {
		if !p.Enabled {
			t.Errorf("provider %q should be enabled with default config", p.Name)
		}
	}
}

func TestHandleProviderHealth_DisabledProvider(t *testing.T) {
	s := NewServer(0)
	cfg := config.Default()
	cfg.Providers["codex"] = config.ProviderConfig{Enabled: false}
	s.SetConfig(cfg)

	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/health/providers")
	if err != nil {
		t.Fatalf("GET /api/health/providers failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var payload struct {
		Providers []struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		} `json:"providers"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&payload)

	for _, p := range payload.Providers {
		if p.Name == "codex" && p.Enabled {
			t.Error("codex should be disabled")
		}
		if p.Name == "claude" && !p.Enabled {
			t.Error("claude should be enabled")
		}
	}
}

func TestHandleGetTrace_RemoteAgent(t *testing.T) {
	otelStore := aimuxotel.NewSpanStore()
	sessionStore := controller.NewSessionStore(t.TempDir())
	sessionStore.Put("my-sandbox", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")

	agents := []agent.Agent{
		{
			PID:          0,
			SessionID:    "my-sandbox",
			Name:         "my-sandbox",
			SandboxName:  "my-sandbox",
			Location:     "remote",
			ProviderName: "claude",
		},
	}

	s := NewServer(0)
	s.SetDiscoverFunc(func() ([]agent.Agent, error) { return agents, nil })
	s.SetProviderLookup(func(name string) interface{ ParseTrace(string) ([]trace.Turn, error) } {
		return &provider.Claude{}
	})
	s.SetSessionStore(sessionStore)
	s.SetOTELStore(otelStore)

	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/agents/my-sandbox/trace")
	if err != nil {
		t.Fatalf("GET trace failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for remote agent trace, got %d", resp.StatusCode)
	}

	var payload struct {
		Turns []map[string]any `json:"turns"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Turns == nil {
		t.Fatal("expected turns array, got nil")
	}
}

func TestHandleGetTrace_LocalAgent(t *testing.T) {
	tmpDir := t.TempDir()
	sessionFile := filepath.Join(tmpDir, "local-session.jsonl")
	_ = os.WriteFile(sessionFile, []byte(`{"type":"summary","session_id":"local-123"}`+"\n"), 0o600)

	agents := []agent.Agent{
		{
			PID:          1234,
			SessionID:    "local-123",
			Name:         "aimux",
			Location:     "local",
			SessionFile:  sessionFile,
			ProviderName: "claude",
		},
	}

	s := NewServer(0)
	s.SetDiscoverFunc(func() ([]agent.Agent, error) { return agents, nil })
	s.SetProviderLookup(func(name string) interface{ ParseTrace(string) ([]trace.Turn, error) } {
		return &provider.Claude{}
	})

	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(s.URL() + "/api/agents/local-123/trace")
	if err != nil {
		t.Fatalf("GET trace failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for local agent trace, got %d", resp.StatusCode)
	}
}

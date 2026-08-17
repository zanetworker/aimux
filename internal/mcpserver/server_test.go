package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestOptionsWithDefaults(t *testing.T) {
	tests := []struct {
		name     string
		input    Options
		checkFn  func(t *testing.T, o Options)
	}{
		{
			name:  "all zeros get defaults",
			input: Options{},
			checkFn: func(t *testing.T, o Options) {
				if o.RedisURL != "redis://localhost:6379" {
					t.Errorf("RedisURL: got %q, want %q", o.RedisURL, "redis://localhost:6379")
				}
				if o.Namespace != "agents" {
					t.Errorf("Namespace: got %q, want %q", o.Namespace, "agents")
				}
				if o.TeamID != "default" {
					t.Errorf("TeamID: got %q, want %q", o.TeamID, "default")
				}
				if o.MaxAgents != 20 {
					t.Errorf("MaxAgents: got %d, want %d", o.MaxAgents, 20)
				}
				if o.MaxCost != 100 {
					t.Errorf("MaxCost: got %f, want %f", o.MaxCost, float64(100))
				}
			},
		},
		{
			name: "explicit values preserved",
			input: Options{
				RedisURL:  "redis://custom:9999",
				Namespace: "myns",
				TeamID:    "team42",
				MaxAgents: 5,
				MaxCost:   50.5,
			},
			checkFn: func(t *testing.T, o Options) {
				if o.RedisURL != "redis://custom:9999" {
					t.Errorf("RedisURL: got %q, want %q", o.RedisURL, "redis://custom:9999")
				}
				if o.Namespace != "myns" {
					t.Errorf("Namespace: got %q, want %q", o.Namespace, "myns")
				}
				if o.TeamID != "team42" {
					t.Errorf("TeamID: got %q, want %q", o.TeamID, "team42")
				}
				if o.MaxAgents != 5 {
					t.Errorf("MaxAgents: got %d, want %d", o.MaxAgents, 5)
				}
				if o.MaxCost != 50.5 {
					t.Errorf("MaxCost: got %f, want %f", o.MaxCost, 50.5)
				}
			},
		},
		{
			name:  "partial defaults fill gaps",
			input: Options{TeamID: "custom-team"},
			checkFn: func(t *testing.T, o Options) {
				if o.TeamID != "custom-team" {
					t.Errorf("TeamID: got %q, want %q", o.TeamID, "custom-team")
				}
				if o.RedisURL != "redis://localhost:6379" {
					t.Errorf("RedisURL should default, got %q", o.RedisURL)
				}
				if o.MaxAgents != 20 {
					t.Errorf("MaxAgents should default, got %d", o.MaxAgents)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.input.withDefaults()
			tt.checkFn(t, got)
		})
	}
}

func TestNewServer_InvalidRedisURL(t *testing.T) {
	opts := Options{RedisURL: "://invalid"}
	_, err := NewServer(opts)
	if err == nil {
		t.Fatal("expected error for invalid Redis URL, got nil")
	}
}

func TestNewServer_NoPanic(t *testing.T) {
	// NewServer with default options will fail (no Redis, no K8s) but must not panic
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewServer panicked: %v", r)
		}
	}()

	opts := Options{
		RedisURL:   "redis://localhost:6379",
		Kubeconfig: "/nonexistent/kubeconfig",
	}
	_, _ = NewServer(opts)
}

func TestNewServer_OpenShellBackend(t *testing.T) {
	opts := Options{
		Backend:         "openshell",
		ExternalBackend: &fakeBackend{},
		GatewayEndpoint: "http://localhost:8090",
		Image:           "agent-worker:latest",
		MaxAgents:       10,
	}
	s, err := NewServer(opts)
	if err != nil {
		t.Fatalf("NewServer with openshell backend failed: %v", err)
	}
	if s == nil {
		t.Fatal("NewServer returned nil")
	}
	if s.backend == nil {
		t.Fatal("backend is nil")
	}
	if s.rdb != nil {
		t.Error("rdb should be nil for OpenShell backend")
	}
}

func TestNewServer_UnknownBackend(t *testing.T) {
	_, err := NewServer(Options{Backend: "magic"})
	if err == nil {
		t.Fatal("expected error for unknown backend")
	}
}

func TestTeamKey(t *testing.T) {
	s := &Server{teamID: "myteam"}
	got := s.teamKey("heartbeat")
	if got != "team:myteam:heartbeat" {
		t.Errorf("teamKey: got %q, want %q", got, "team:myteam:heartbeat")
	}

	got = s.teamKey("task:abc123")
	if got != "team:myteam:task:abc123" {
		t.Errorf("teamKey: got %q, want %q", got, "team:myteam:task:abc123")
	}
}

func TestJoinLines(t *testing.T) {
	got := joinLines([]string{"a", "b", "c"})
	if got != "a\nb\nc" {
		t.Errorf("joinLines: got %q, want %q", got, "a\nb\nc")
	}
}

func TestSplitComma(t *testing.T) {
	got := splitComma("a,b,c")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("splitComma: got %v, want [a b c]", got)
	}
}

func TestValidTaskID(t *testing.T) {
	valid := []string{"abc123", "a3f2bc", "abc-def", "0123456789abcdef"}
	for _, id := range valid {
		if !validTaskID.MatchString(id) {
			t.Errorf("expected %q to be valid", id)
		}
	}

	invalid := []string{"release/foo", "../main", "abc def", "abc;rm", "$(whoami)", "abc`id`", ""}
	for _, id := range invalid {
		if validTaskID.MatchString(id) {
			t.Errorf("expected %q to be invalid", id)
		}
	}
}

// --- cleanup_branches handler tests (moved from cmd/mcp/main_test.go) ---

func TestCleanupBranchesTool_Definition(t *testing.T) {
	s := &Server{}
	tool := s.cleanupBranchesTool()

	if tool.Name != "cleanup_branches" {
		t.Errorf("expected tool name 'cleanup_branches', got %q", tool.Name)
	}

	desc := tool.Description
	if desc == "" {
		t.Fatal("tool description must not be empty")
	}
	if !strings.Contains(desc, "task-{id}") {
		t.Error("description should mention task-{id} branch naming convention")
	}

	schema := tool.InputSchema
	props, ok := schema.Properties["task_ids"]
	if !ok {
		t.Fatal("tool must have a 'task_ids' property")
	}
	propMap, ok := props.(map[string]interface{})
	if !ok {
		t.Fatal("task_ids property should be a map")
	}
	if propMap["type"] != "string" {
		t.Errorf("task_ids should be type string, got %v", propMap["type"])
	}

	required := schema.Required
	found := false
	for _, r := range required {
		if r == "task_ids" {
			found = true
			break
		}
	}
	if !found {
		t.Error("task_ids must be in required list")
	}
}

func TestBranchNameConstruction(t *testing.T) {
	tests := []struct {
		taskID   string
		expected string
	}{
		{"a3f2bc", "task-a3f2bc"},
		{"b7d1ef", "task-b7d1ef"},
		{"123", "task-123"},
		{"abc-def", "task-abc-def"},
	}
	for _, tt := range tests {
		branch := "task-" + tt.taskID
		if branch != tt.expected {
			t.Errorf("taskID %q: expected branch %q, got %q", tt.taskID, tt.expected, branch)
		}
	}
}

func TestHandleCleanupBranches_MissingEnvVars(t *testing.T) {
	s := &Server{githubToken: "", githubRepo: ""}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		"task_ids": "abc123",
	}

	result, err := s.handleCleanupBranches(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := result.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "GITHUB_TOKEN") || !strings.Contains(text, "GITHUB_REPO") {
		t.Errorf("expected error mentioning GITHUB_TOKEN and GITHUB_REPO, got: %s", text)
	}

	// Also test with only token set
	s.githubToken = "tok"
	s.githubRepo = ""
	result, err = s.handleCleanupBranches(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text = result.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "GITHUB_REPO") {
		t.Errorf("expected error about GITHUB_REPO, got: %s", text)
	}
}

func TestHandleCleanupBranches_BranchNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	origTransport := http.DefaultClient.Transport
	http.DefaultClient.Transport = &testTransport{server: ts}
	defer func() { http.DefaultClient.Transport = origTransport }()

	s := &Server{githubToken: "test-token", githubRepo: "owner/repo"}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		"task_ids": "abc123,def456",
	}

	result, err := s.handleCleanupBranches(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := result.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "task-abc123 (not found)") {
		t.Errorf("expected 'task-abc123 (not found)' in output, got: %s", text)
	}
	if !strings.Contains(text, "task-def456 (not found)") {
		t.Errorf("expected 'task-def456 (not found)' in output, got: %s", text)
	}
	firstLine := strings.SplitN(text, "\n", 2)[0]
	if firstLine != "Deleted: " {
		t.Errorf("expected empty Deleted list, got: %q", firstLine)
	}
}

func TestHandleCleanupBranches_SuccessfulDelete(t *testing.T) {
	var requestLog []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestLog = append(requestLog, r.Method+" "+r.URL.Path)

		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-token" {
			t.Errorf("expected Bearer test-token, got %q", auth)
		}

		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"ref":"refs/heads/task-abc123"}`)
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer ts.Close()

	origTransport := http.DefaultClient.Transport
	http.DefaultClient.Transport = &testTransport{server: ts}
	defer func() { http.DefaultClient.Transport = origTransport }()

	s := &Server{githubToken: "test-token", githubRepo: "owner/repo"}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		"task_ids": "abc123",
	}

	result, err := s.handleCleanupBranches(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := result.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "task-abc123") {
		t.Errorf("expected 'task-abc123' in Deleted list, got: %s", text)
	}
	if strings.Contains(text, "not found") || strings.Contains(text, "delete failed") {
		t.Errorf("expected no skipped branches, got: %s", text)
	}

	if len(requestLog) != 2 {
		t.Fatalf("expected 2 HTTP requests (GET + DELETE), got %d: %v", len(requestLog), requestLog)
	}
	if !strings.HasPrefix(requestLog[0], "GET") {
		t.Errorf("first request should be GET, got: %s", requestLog[0])
	}
	if !strings.HasPrefix(requestLog[1], "DELETE") {
		t.Errorf("second request should be DELETE, got: %s", requestLog[1])
	}
}

func TestHandleCleanupBranches_EmptyIDs(t *testing.T) {
	s := &Server{githubToken: "test-token", githubRepo: "owner/repo"}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		"task_ids": ",,,",
	}

	result, err := s.handleCleanupBranches(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := result.Content[0].(mcp.TextContent).Text
	if strings.Contains(text, "task-") {
		t.Errorf("expected no branches processed for empty IDs, got: %s", text)
	}
}

func TestHandleCleanupBranches_DeleteFailed(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"ref":"refs/heads/task-fail1"}`)
		case http.MethodDelete:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer ts.Close()

	origTransport := http.DefaultClient.Transport
	http.DefaultClient.Transport = &testTransport{server: ts}
	defer func() { http.DefaultClient.Transport = origTransport }()

	s := &Server{githubToken: "test-token", githubRepo: "owner/repo"}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		"task_ids": "fail1",
	}

	result, err := s.handleCleanupBranches(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := result.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "task-fail1 (delete failed)") {
		t.Errorf("expected 'task-fail1 (delete failed)' in Skipped, got: %s", text)
	}
}

func TestHandleCleanupBranches_URLConstruction(t *testing.T) {
	var capturedURLs []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURLs = append(capturedURLs, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	origTransport := http.DefaultClient.Transport
	http.DefaultClient.Transport = &testTransport{server: ts}
	defer func() { http.DefaultClient.Transport = origTransport }()

	s := &Server{githubToken: "test-token", githubRepo: "myorg/myrepo"}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		"task_ids": "x1y2z3",
	}

	_, err := s.handleCleanupBranches(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(capturedURLs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(capturedURLs))
	}

	expectedPath := "/repos/myorg/myrepo/git/ref/heads/task-x1y2z3"
	if capturedURLs[0] != expectedPath {
		t.Errorf("expected URL path %q, got %q", expectedPath, capturedURLs[0])
	}
}

func TestHandleCleanupBranches_InvalidTaskIDs(t *testing.T) {
	var requestLog []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestLog = append(requestLog, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	origTransport := http.DefaultClient.Transport
	http.DefaultClient.Transport = &testTransport{server: ts}
	defer func() { http.DefaultClient.Transport = origTransport }()

	s := &Server{githubToken: "test-token", githubRepo: "owner/repo"}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		"task_ids": "release/foo,../main,abc123",
	}

	result, err := s.handleCleanupBranches(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := result.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "release/foo (invalid task ID)") {
		t.Errorf("expected 'release/foo (invalid task ID)' in Skipped, got: %s", text)
	}
	if !strings.Contains(text, "../main (invalid task ID)") {
		t.Errorf("expected '../main (invalid task ID)' in Skipped, got: %s", text)
	}
	if len(requestLog) != 1 {
		t.Errorf("expected 1 request (only valid abc123), got %d: %v", len(requestLog), requestLog)
	}
}

// testTransport redirects all requests to the test server.
type testTransport struct {
	server *httptest.Server
}

func (t *testTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(t.server.URL, "http://")
	return http.DefaultTransport.RoundTrip(req)
}

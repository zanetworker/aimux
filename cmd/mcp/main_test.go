package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestCleanupBranchesTool_Definition(t *testing.T) {
	tool := cleanupBranchesTool()

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

	// Verify task_ids is a required parameter
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
	// Save and clear globals
	origToken, origRepo := githubToken, githubRepo
	defer func() { githubToken, githubRepo = origToken, origRepo }()

	githubToken = ""
	githubRepo = ""

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		"task_ids": "abc123",
	}

	result, err := handleCleanupBranches(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := result.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "GITHUB_TOKEN") || !strings.Contains(text, "GITHUB_REPO") {
		t.Errorf("expected error mentioning GITHUB_TOKEN and GITHUB_REPO, got: %s", text)
	}

	// Also test with only token set
	githubToken = "tok"
	githubRepo = ""
	result, err = handleCleanupBranches(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text = result.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "GITHUB_REPO") {
		t.Errorf("expected error about GITHUB_REPO, got: %s", text)
	}
}

func TestHandleCleanupBranches_BranchNotFound(t *testing.T) {
	// Mock GitHub API returning 404 for all branches
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	// Point githubRepo to use the test server URL — we override the URL construction
	// by setting githubRepo and githubToken, then using a custom transport
	origToken, origRepo := githubToken, githubRepo
	defer func() { githubToken, githubRepo = origToken, origRepo }()

	githubToken = "test-token"
	// We need the handler to hit our test server. Since the code uses hardcoded
	// api.github.com URLs, we use a custom RoundTripper to intercept.
	origTransport := http.DefaultClient.Transport
	http.DefaultClient.Transport = &testTransport{server: server}
	defer func() { http.DefaultClient.Transport = origTransport }()

	githubRepo = "owner/repo"

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		"task_ids": "abc123,def456",
	}

	result, err := handleCleanupBranches(context.Background(), req)
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
	if !strings.Contains(text, "Deleted: \n") || !strings.HasPrefix(text, "Deleted: \n") {
		// Deleted list should be empty
		if !strings.HasPrefix(text, "Deleted: \n") && !strings.Contains(text, "Deleted: \nSkipped:") {
			// Just verify no branches appear in Deleted
			lines := strings.Split(text, "\n")
			if len(lines) > 0 && strings.TrimPrefix(lines[0], "Deleted: ") != "" {
				t.Errorf("expected empty Deleted list, got: %s", lines[0])
			}
		}
	}
}

func TestHandleCleanupBranches_SuccessfulDelete(t *testing.T) {
	var requestLog []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestLog = append(requestLog, r.Method+" "+r.URL.Path)

		// Verify auth header
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-token" {
			t.Errorf("expected Bearer test-token, got %q", auth)
		}

		if r.Method == http.MethodGet {
			// Branch exists
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"ref":"refs/heads/task-abc123"}`)
		} else if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()

	origToken, origRepo := githubToken, githubRepo
	origTransport := http.DefaultClient.Transport
	defer func() {
		githubToken, githubRepo = origToken, origRepo
		http.DefaultClient.Transport = origTransport
	}()

	githubToken = "test-token"
	githubRepo = "owner/repo"
	http.DefaultClient.Transport = &testTransport{server: server}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		"task_ids": "abc123",
	}

	result, err := handleCleanupBranches(context.Background(), req)
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

	// Verify we made both a GET (check) and DELETE request
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
	origToken, origRepo := githubToken, githubRepo
	defer func() { githubToken, githubRepo = origToken, origRepo }()

	githubToken = "test-token"
	githubRepo = "owner/repo"

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		"task_ids": ",,,",
	}

	result, err := handleCleanupBranches(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := result.Content[0].(mcp.TextContent).Text
	// With all empty IDs, both lists should be empty
	if strings.Contains(text, "task-") {
		t.Errorf("expected no branches processed for empty IDs, got: %s", text)
	}
}

func TestHandleCleanupBranches_DeleteFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"ref":"refs/heads/task-fail1"}`)
		} else if r.Method == http.MethodDelete {
			// Simulate a permission error
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer server.Close()

	origToken, origRepo := githubToken, githubRepo
	origTransport := http.DefaultClient.Transport
	defer func() {
		githubToken, githubRepo = origToken, origRepo
		http.DefaultClient.Transport = origTransport
	}()

	githubToken = "test-token"
	githubRepo = "owner/repo"
	http.DefaultClient.Transport = &testTransport{server: server}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		"task_ids": "fail1",
	}

	result, err := handleCleanupBranches(context.Background(), req)
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURLs = append(capturedURLs, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	origToken, origRepo := githubToken, githubRepo
	origTransport := http.DefaultClient.Transport
	defer func() {
		githubToken, githubRepo = origToken, origRepo
		http.DefaultClient.Transport = origTransport
	}()

	githubToken = "test-token"
	githubRepo = "myorg/myrepo"
	http.DefaultClient.Transport = &testTransport{server: server}

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		"task_ids": "x1y2z3",
	}

	_, err := handleCleanupBranches(context.Background(), req)
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

// testTransport redirects all requests to the test server.
type testTransport struct {
	server *httptest.Server
}

func (t *testTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Rewrite the URL to point to our test server, preserving the path
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(t.server.URL, "http://")
	return http.DefaultTransport.RoundTrip(req)
}

# Launch Modes & Google Tasks Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add three agent launch modes (quick launch, directory browser, Google Tasks) and a toggleable tasks panel to the web UI.

**Architecture:** Core `tasks.Provider` interface in `internal/tasks/` with two backends (`gws` CLI, MCP HTTP). Config gains `QuickLaunch` and `Tasks` structs. Web backend exposes directory browse + tasks API endpoints. React frontend gets a refactored launch dialog (quick launch pills, directory browser), a new tasks panel (toggleable right sidebar), and a task launch dialog.

**Tech Stack:** Go (core + HTTP handlers), React/TypeScript (web frontend), `gws` CLI (Google Tasks API), MCP JSON-RPC over HTTP (fallback backend).

---

### Task 1: Config — QuickLaunch and Tasks structs

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go` (create if missing)

- [ ] **Step 1: Write failing test for QuickLaunch config parsing**

```go
// internal/config/config_test.go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadQuickLaunchDirectories(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgPath, []byte(`
quick_launch:
  directories:
    - /tmp/project-a
    - /tmp/project-b
`), 0644)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.QuickLaunch.Directories) != 2 {
		t.Fatalf("got %d directories, want 2", len(cfg.QuickLaunch.Directories))
	}
	if cfg.QuickLaunch.Directories[0] != "/tmp/project-a" {
		t.Errorf("got %q, want /tmp/project-a", cfg.QuickLaunch.Directories[0])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestLoadQuickLaunchDirectories -v`
Expected: FAIL — `cfg.QuickLaunch` field does not exist.

- [ ] **Step 3: Add QuickLaunch and Tasks structs to config**

Add these structs and fields to `internal/config/config.go`:

```go
// QuickLaunchConfig holds favorite directories for one-click launch.
type QuickLaunchConfig struct {
	Directories []string `yaml:"directories"`
}

// TasksConfig holds Google Tasks integration settings.
type TasksConfig struct {
	Backend        string `yaml:"backend"`         // "gws", "mcp", or "auto"
	MCPEndpoint    string `yaml:"mcp_endpoint"`    // MCP server URL for remote backend
	DefaultList    string `yaml:"default_list"`    // task list name or ID
	PromptTemplate string `yaml:"prompt_template"` // template with {title}, {notes}, {user_prompt}
}
```

Add to the `Config` struct:

```go
QuickLaunch QuickLaunchConfig `yaml:"quick_launch"`
Tasks       TasksConfig       `yaml:"tasks"`
```

Add merge logic in `Load()`:

```go
if len(fileCfg.QuickLaunch.Directories) > 0 {
	cfg.QuickLaunch = fileCfg.QuickLaunch
}
if fileCfg.Tasks.Backend != "" || fileCfg.Tasks.DefaultList != "" || fileCfg.Tasks.PromptTemplate != "" {
	cfg.Tasks = fileCfg.Tasks
}
```

Add default prompt template in `Default()`:

```go
Tasks: TasksConfig{
	Backend: "auto",
	PromptTemplate: "Work on the following task: {title}\n\nDetails: {notes}\n\nAdditional instructions: {user_prompt}\n\nWhen done, summarize what you did.",
},
```

- [ ] **Step 4: Write test for Tasks config parsing**

```go
func TestLoadTasksConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgPath, []byte(`
tasks:
  backend: gws
  default_list: "Work"
  prompt_template: "Do this: {title}"
`), 0644)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tasks.Backend != "gws" {
		t.Errorf("backend = %q, want gws", cfg.Tasks.Backend)
	}
	if cfg.Tasks.DefaultList != "Work" {
		t.Errorf("default_list = %q, want Work", cfg.Tasks.DefaultList)
	}
	if cfg.Tasks.PromptTemplate != "Do this: {title}" {
		t.Errorf("prompt_template = %q", cfg.Tasks.PromptTemplate)
	}
}

func TestDefaultPromptTemplate(t *testing.T) {
	cfg := Default()
	if cfg.Tasks.PromptTemplate == "" {
		t.Error("default prompt template should not be empty")
	}
	if cfg.Tasks.Backend != "auto" {
		t.Errorf("default backend = %q, want auto", cfg.Tasks.Backend)
	}
}
```

- [ ] **Step 5: Run all config tests**

Run: `go test ./internal/config/ -v`
Expected: ALL PASS

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add QuickLaunch and Tasks config structs"
```

---

### Task 2: Core tasks package — types, interface, prompt template

**Files:**
- Create: `internal/tasks/tasks.go`
- Create: `internal/tasks/tasks_test.go`

- [ ] **Step 1: Write test for Task types and RenderPrompt**

```go
// internal/tasks/tasks_test.go
package tasks

import "testing"

func TestRenderPrompt(t *testing.T) {
	tmpl := "Task: {title}\nDetails: {notes}\nInstructions: {user_prompt}"
	result := RenderPrompt(tmpl, "Fix auth bug", "JWT validation broken", "Focus on expiry")
	want := "Task: Fix auth bug\nDetails: JWT validation broken\nInstructions: Focus on expiry"
	if result != want {
		t.Errorf("got %q, want %q", result, want)
	}
}

func TestRenderPromptEmptyUserPrompt(t *testing.T) {
	tmpl := "Task: {title}\nDetails: {notes}\nInstructions: {user_prompt}"
	result := RenderPrompt(tmpl, "Fix auth bug", "JWT broken", "")
	want := "Task: Fix auth bug\nDetails: JWT broken\nInstructions: "
	if result != want {
		t.Errorf("got %q, want %q", result, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tasks/ -run TestRenderPrompt -v`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Create tasks.go with types and RenderPrompt**

```go
// internal/tasks/tasks.go
package tasks

import "strings"

type Task struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Notes    string `json:"notes"`
	Due      string `json:"due"`
	Status   string `json:"status"`
	ListID   string `json:"listID"`
	ListName string `json:"listName"`
	Updated  string `json:"updated"`
}

type TaskList struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Provider interface {
	ListTaskLists() ([]TaskList, error)
	ListTasks(listID string) ([]Task, error)
	CompleteTask(listID, taskID string) error
	ReopenTask(listID, taskID string) error
	AddNote(listID, taskID, note string) error
}

func RenderPrompt(template, title, notes, userPrompt string) string {
	r := strings.NewReplacer(
		"{title}", title,
		"{notes}", notes,
		"{user_prompt}", userPrompt,
	)
	return r.Replace(template)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/tasks/ -v`
Expected: ALL PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tasks/tasks.go internal/tasks/tasks_test.go
git commit -m "feat(tasks): add core types, Provider interface, and RenderPrompt"
```

---

### Task 3: GWS backend — gws CLI provider

**Files:**
- Create: `internal/tasks/gws.go`
- Modify: `internal/tasks/tasks_test.go`

- [ ] **Step 1: Write test for GWS provider parseTaskLists**

```go
// Add to internal/tasks/tasks_test.go

func TestGWSParseTaskLists(t *testing.T) {
	raw := `{
  "items": [
    {"id": "abc123", "title": "Work", "updated": "2026-05-01T20:09:31.719Z"},
    {"id": "def456", "title": "Personal", "updated": "2026-04-03T10:56:36.550Z"}
  ]
}`
	lists, err := parseTaskListsJSON([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(lists) != 2 {
		t.Fatalf("got %d lists, want 2", len(lists))
	}
	if lists[0].ID != "abc123" || lists[0].Name != "Work" {
		t.Errorf("list[0] = %+v", lists[0])
	}
}

func TestGWSParseTasks(t *testing.T) {
	raw := `{
  "items": [
    {
      "id": "task1",
      "title": "Fix auth bug",
      "notes": "JWT validation broken",
      "due": "2026-05-10T00:00:00.000Z",
      "status": "needsAction",
      "updated": "2026-05-07T09:36:30.253Z"
    },
    {
      "id": "task2",
      "title": "Deploy fix",
      "status": "completed",
      "completed": "2026-05-05T12:44:30.000Z",
      "updated": "2026-05-06T13:05:15.439Z"
    }
  ]
}`
	tasks, err := parseTasksJSON([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2", len(tasks))
	}
	if tasks[0].Title != "Fix auth bug" || tasks[0].Status != "needsAction" {
		t.Errorf("task[0] = %+v", tasks[0])
	}
	if tasks[1].Status != "completed" {
		t.Errorf("task[1].Status = %q, want completed", tasks[1].Status)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tasks/ -run TestGWS -v`
Expected: FAIL — `parseTaskListsJSON` not defined.

- [ ] **Step 3: Implement GWS provider**

```go
// internal/tasks/gws.go
package tasks

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

type GWSProvider struct{}

func NewGWSProvider() (*GWSProvider, error) {
	if _, err := exec.LookPath("gws"); err != nil {
		return nil, fmt.Errorf("gws CLI not found: %w", err)
	}
	return &GWSProvider{}, nil
}

func (g *GWSProvider) ListTaskLists() ([]TaskList, error) {
	out, err := exec.Command("gws", "tasks", "tasklists", "list", "--params", "{}").Output()
	if err != nil {
		return nil, fmt.Errorf("gws tasklists list: %w", err)
	}
	return parseTaskListsJSON(out)
}

func (g *GWSProvider) ListTasks(listID string) ([]Task, error) {
	params := fmt.Sprintf(`{"tasklist":"%s","showCompleted":true,"showHidden":true}`, listID)
	out, err := exec.Command("gws", "tasks", "tasks", "list", "--params", params).Output()
	if err != nil {
		return nil, fmt.Errorf("gws tasks list: %w", err)
	}
	tasks, err := parseTasksJSON(out)
	if err != nil {
		return nil, err
	}
	for i := range tasks {
		tasks[i].ListID = listID
	}
	return tasks, nil
}

func (g *GWSProvider) CompleteTask(listID, taskID string) error {
	params := fmt.Sprintf(`{"tasklist":"%s","task":"%s"}`, listID, taskID)
	body := `{"status":"completed"}`
	cmd := exec.Command("gws", "tasks", "tasks", "patch", "--params", params, "--body", body)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gws tasks patch (complete): %w: %s", err, out)
	}
	return nil
}

func (g *GWSProvider) ReopenTask(listID, taskID string) error {
	params := fmt.Sprintf(`{"tasklist":"%s","task":"%s"}`, listID, taskID)
	body := `{"status":"needsAction","completed":null}`
	cmd := exec.Command("gws", "tasks", "tasks", "patch", "--params", params, "--body", body)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gws tasks patch (reopen): %w: %s", err, out)
	}
	return nil
}

func (g *GWSProvider) AddNote(listID, taskID, note string) error {
	// Fetch current task to append to existing notes
	params := fmt.Sprintf(`{"tasklist":"%s","task":"%s"}`, listID, taskID)
	out, err := exec.Command("gws", "tasks", "tasks", "get", "--params", params).Output()
	if err != nil {
		return fmt.Errorf("gws tasks get: %w", err)
	}

	var raw struct {
		Notes string `json:"notes"`
	}
	json.Unmarshal(out, &raw)

	newNotes := strings.TrimSpace(raw.Notes)
	if newNotes != "" {
		newNotes += "\n\n"
	}
	newNotes += note

	bodyBytes, _ := json.Marshal(map[string]string{"notes": newNotes})
	cmd := exec.Command("gws", "tasks", "tasks", "patch", "--params", params, "--body", string(bodyBytes))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gws tasks patch (note): %w: %s", err, out)
	}
	return nil
}

// GWSAvailable checks if gws binary exists and has auth configured.
func GWSAvailable() bool {
	_, err := exec.LookPath("gws")
	return err == nil
}

// --- JSON parsing (exported for testing) ---

type taskListsResponse struct {
	Items []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"items"`
}

type tasksResponse struct {
	Items []struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Notes     string `json:"notes"`
		Due       string `json:"due"`
		Status    string `json:"status"`
		Updated   string `json:"updated"`
		Completed string `json:"completed"`
	} `json:"items"`
}

func parseTaskListsJSON(data []byte) ([]TaskList, error) {
	var resp taskListsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse task lists: %w", err)
	}
	lists := make([]TaskList, len(resp.Items))
	for i, item := range resp.Items {
		lists[i] = TaskList{ID: item.ID, Name: item.Title}
	}
	return lists, nil
}

func parseTasksJSON(data []byte) ([]Task, error) {
	var resp tasksResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse tasks: %w", err)
	}
	result := make([]Task, len(resp.Items))
	for i, item := range resp.Items {
		result[i] = Task{
			ID:      item.ID,
			Title:   item.Title,
			Notes:   item.Notes,
			Due:     item.Due,
			Status:  item.Status,
			Updated: item.Updated,
		}
	}
	return result, nil
}
```

- [ ] **Step 4: Add compile-time interface check**

Add to `gws.go`:

```go
var _ Provider = (*GWSProvider)(nil)
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/tasks/ -v`
Expected: ALL PASS

- [ ] **Step 6: Commit**

```bash
git add internal/tasks/gws.go internal/tasks/tasks_test.go
git commit -m "feat(tasks): add GWS CLI backend with JSON parsing"
```

---

### Task 4: MCP backend — lightweight JSON-RPC client

**Files:**
- Create: `internal/tasks/mcp.go`
- Modify: `internal/tasks/tasks_test.go`

- [ ] **Step 1: Write test for MCP response parsing**

```go
// Add to internal/tasks/tasks_test.go

func TestMCPParseListTaskListsResult(t *testing.T) {
	raw := `{"task_lists":[{"id":"abc","title":"Work"},{"id":"def","title":"Personal"}]}`
	lists, err := parseMCPTaskLists([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(lists) != 2 {
		t.Fatalf("got %d, want 2", len(lists))
	}
	if lists[0].Name != "Work" {
		t.Errorf("name = %q, want Work", lists[0].Name)
	}
}

func TestMCPParseListTasksResult(t *testing.T) {
	raw := `{"tasks":[{"id":"t1","title":"Fix bug","notes":"broken","status":"needsAction","due":"2026-05-10"}]}`
	tasks, err := parseMCPTasks([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d, want 1", len(tasks))
	}
	if tasks[0].Title != "Fix bug" {
		t.Errorf("title = %q", tasks[0].Title)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tasks/ -run TestMCP -v`
Expected: FAIL — `parseMCPTaskLists` not defined.

- [ ] **Step 3: Implement MCP provider**

```go
// internal/tasks/mcp.go
package tasks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type MCPProvider struct {
	endpoint string
	client   *http.Client
}

func NewMCPProvider(endpoint string) (*MCPProvider, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("MCP endpoint is required")
	}
	return &MCPProvider{
		endpoint: endpoint,
		client:   &http.Client{},
	}, nil
}

func (m *MCPProvider) callTool(toolName string, args map[string]interface{}) (json.RawMessage, error) {
	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name":      toolName,
			"arguments": args,
		},
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	resp, err := m.client.Post(m.endpoint, "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("MCP call %s: %w", toolName, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var rpcResp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &rpcResp); err != nil {
		return nil, fmt.Errorf("MCP parse response: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("MCP error: %s", rpcResp.Error.Message)
	}
	if len(rpcResp.Result.Content) == 0 {
		return nil, fmt.Errorf("MCP empty result for %s", toolName)
	}

	return json.RawMessage(rpcResp.Result.Content[0].Text), nil
}

func (m *MCPProvider) ListTaskLists() ([]TaskList, error) {
	result, err := m.callTool("list_task_lists", map[string]interface{}{
		"user_google_email": "azaalouk@redhat.com",
	})
	if err != nil {
		return nil, err
	}
	return parseMCPTaskLists(result)
}

func (m *MCPProvider) ListTasks(listID string) ([]Task, error) {
	result, err := m.callTool("list_tasks", map[string]interface{}{
		"user_google_email": "azaalouk@redhat.com",
		"task_list_id":      listID,
	})
	if err != nil {
		return nil, err
	}
	tasks, err := parseMCPTasks(result)
	if err != nil {
		return nil, err
	}
	for i := range tasks {
		tasks[i].ListID = listID
	}
	return tasks, nil
}

func (m *MCPProvider) CompleteTask(listID, taskID string) error {
	_, err := m.callTool("manage_task", map[string]interface{}{
		"user_google_email": "azaalouk@redhat.com",
		"task_list_id":      listID,
		"task_id":           taskID,
		"action":            "update",
		"status":            "completed",
	})
	return err
}

func (m *MCPProvider) ReopenTask(listID, taskID string) error {
	_, err := m.callTool("manage_task", map[string]interface{}{
		"user_google_email": "azaalouk@redhat.com",
		"task_list_id":      listID,
		"task_id":           taskID,
		"action":            "update",
		"status":            "needsAction",
	})
	return err
}

func (m *MCPProvider) AddNote(listID, taskID, note string) error {
	_, err := m.callTool("manage_task", map[string]interface{}{
		"user_google_email": "azaalouk@redhat.com",
		"task_list_id":      listID,
		"task_id":           taskID,
		"action":            "update",
		"notes":             note,
	})
	return err
}

// --- Parsing helpers ---

type mcpTaskListsResult struct {
	TaskLists []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"task_lists"`
}

type mcpTasksResult struct {
	Tasks []struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Notes  string `json:"notes"`
		Due    string `json:"due"`
		Status string `json:"status"`
	} `json:"tasks"`
}

func parseMCPTaskLists(data []byte) ([]TaskList, error) {
	var resp mcpTaskListsResult
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	lists := make([]TaskList, len(resp.TaskLists))
	for i, item := range resp.TaskLists {
		lists[i] = TaskList{ID: item.ID, Name: item.Title}
	}
	return lists, nil
}

func parseMCPTasks(data []byte) ([]Task, error) {
	var resp mcpTasksResult
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	result := make([]Task, len(resp.Tasks))
	for i, item := range resp.Tasks {
		result[i] = Task{
			ID:     item.ID,
			Title:  item.Title,
			Notes:  item.Notes,
			Due:    item.Due,
			Status: item.Status,
		}
	}
	return result, nil
}

var _ Provider = (*MCPProvider)(nil)
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/tasks/ -v`
Expected: ALL PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tasks/mcp.go internal/tasks/tasks_test.go
git commit -m "feat(tasks): add MCP backend with JSON-RPC client"
```

---

### Task 5: Provider resolver — auto-detection logic

**Files:**
- Create: `internal/tasks/resolve.go`
- Modify: `internal/tasks/tasks_test.go`

- [ ] **Step 1: Write test for NewProvider**

```go
// Add to internal/tasks/tasks_test.go

func TestNewProviderGWS(t *testing.T) {
	p, err := NewProvider("gws", "")
	if !GWSAvailable() {
		if err == nil {
			t.Error("expected error when gws not available")
		}
		t.Skip("gws not installed")
	}
	if err != nil {
		t.Fatalf("NewProvider(gws): %v", err)
	}
	if _, ok := p.(*GWSProvider); !ok {
		t.Errorf("expected *GWSProvider, got %T", p)
	}
}

func TestNewProviderMCPRequiresEndpoint(t *testing.T) {
	_, err := NewProvider("mcp", "")
	if err == nil {
		t.Error("expected error for mcp with empty endpoint")
	}
}

func TestNewProviderUnknown(t *testing.T) {
	_, err := NewProvider("invalid", "")
	if err == nil {
		t.Error("expected error for unknown backend")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tasks/ -run TestNewProvider -v`
Expected: FAIL — `NewProvider` not defined.

- [ ] **Step 3: Implement resolve.go**

```go
// internal/tasks/resolve.go
package tasks

import "fmt"

func NewProvider(backend, mcpEndpoint string) (Provider, error) {
	switch backend {
	case "gws":
		return NewGWSProvider()
	case "mcp":
		return NewMCPProvider(mcpEndpoint)
	case "auto", "":
		if GWSAvailable() {
			return NewGWSProvider()
		}
		if mcpEndpoint != "" {
			return NewMCPProvider(mcpEndpoint)
		}
		return nil, fmt.Errorf("tasks: no backend available (gws not found, no mcp_endpoint configured)")
	default:
		return nil, fmt.Errorf("tasks: unknown backend %q", backend)
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/tasks/ -v`
Expected: ALL PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tasks/resolve.go internal/tasks/tasks_test.go
git commit -m "feat(tasks): add provider resolver with auto-detection"
```

---

### Task 6: Web API — directory browse endpoints

**Files:**
- Modify: `internal/frontend/web/server.go`
- Modify: `internal/frontend/web/handlers.go`
- Modify: `internal/frontend/web/handlers_test.go`

- [ ] **Step 1: Write test for browse endpoint**

```go
// Add to internal/frontend/web/handlers_test.go

func TestHandleBrowseDirectory(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "subdir"), 0755)
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hi"), 0644)
	os.MkdirAll(filepath.Join(dir, ".hidden"), 0755)

	s := NewServer(0)
	req := httptest.NewRequest("GET", "/api/directories/browse?path="+dir, nil)
	w := httptest.NewRecorder()
	s.handleBrowseDir(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var resp struct {
		Path    string `json:"path"`
		Entries []struct {
			Name  string `json:"name"`
			IsDir bool   `json:"isDir"`
		} `json:"entries"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp.Path != dir {
		t.Errorf("path = %q, want %q", resp.Path, dir)
	}
	// Should have subdir and file.txt, but NOT .hidden
	for _, e := range resp.Entries {
		if e.Name == ".hidden" {
			t.Error("should not include hidden directories")
		}
	}
	found := false
	for _, e := range resp.Entries {
		if e.Name == "subdir" && e.IsDir {
			found = true
		}
	}
	if !found {
		t.Error("expected subdir entry")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/frontend/web/ -run TestHandleBrowseDirectory -v`
Expected: FAIL — `handleBrowseDir` not defined.

- [ ] **Step 3: Implement directory browse handlers**

Add to `internal/frontend/web/handlers.go`:

```go
func (s *Server) handleBrowseDir(w http.ResponseWriter, r *http.Request) {
	dirPath := r.URL.Query().Get("path")
	if dirPath == "" {
		home, _ := os.UserHomeDir()
		dirPath = home
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	type entry struct {
		Name  string `json:"name"`
		IsDir bool   `json:"isDir"`
	}

	var dirs, files []entry
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		item := entry{Name: e.Name(), IsDir: e.IsDir()}
		if e.IsDir() {
			dirs = append(dirs, item)
		} else {
			files = append(files, item)
		}
	}

	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name) })
	sort.Slice(files, func(i, j int) bool { return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name) })

	result := append(dirs, files...)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"path":    dirPath,
		"entries": result,
	})
}

func (s *Server) handleRecentDirs(w http.ResponseWriter, r *http.Request) {
	if s.ctrl == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"directories": []interface{}{}})
		return
	}

	dirs := s.ctrl.RecentDirs()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"directories": dirs,
	})
}
```

Add required imports to `handlers.go`: `"os"`, `"sort"`, `"strings"` (verify which are already imported).

- [ ] **Step 4: Register routes in server.go**

Add to `Start()` in `server.go`:

```go
mux.HandleFunc("GET /api/directories/browse", s.handleBrowseDir)
mux.HandleFunc("GET /api/directories/recent", s.handleRecentDirs)
```

- [ ] **Step 5: Add RecentDirs method to controller**

Check if `controller.Controller` already has a `RecentDirs()` method. If not, add one that delegates to the discovery layer (same logic as `app.go:buildRecentDirs()`). This is a core package method, no UI imports.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/frontend/web/ -run TestHandleBrowseDirectory -v`
Expected: PASS

- [ ] **Step 7: Run full suite**

Run: `go test ./... -timeout 30s`
Expected: ALL PASS

- [ ] **Step 8: Commit**

```bash
git add internal/frontend/web/handlers.go internal/frontend/web/server.go internal/frontend/web/handlers_test.go
git commit -m "feat(web): add directory browse and recent dirs API endpoints"
```

---

### Task 7: Web API — tasks endpoints

**Files:**
- Modify: `internal/frontend/web/server.go`
- Modify: `internal/frontend/web/handlers.go`
- Modify: `internal/frontend/web/handlers_test.go`

- [ ] **Step 1: Add tasks provider to Server**

Add to `Server` struct in `server.go`:

```go
taskProvider tasks.Provider
```

Add setter:

```go
func (s *Server) SetTaskProvider(tp tasks.Provider) {
	s.taskProvider = tp
}
```

Add import: `"github.com/zanetworker/aimux/internal/tasks"`

- [ ] **Step 2: Write test for task lists endpoint**

```go
// Add to internal/frontend/web/handlers_test.go

type mockTaskProvider struct {
	lists []tasks.TaskList
	tasks []tasks.Task
}

func (m *mockTaskProvider) ListTaskLists() ([]tasks.TaskList, error) {
	return m.lists, nil
}
func (m *mockTaskProvider) ListTasks(listID string) ([]tasks.Task, error) {
	return m.tasks, nil
}
func (m *mockTaskProvider) CompleteTask(listID, taskID string) error { return nil }
func (m *mockTaskProvider) ReopenTask(listID, taskID string) error  { return nil }
func (m *mockTaskProvider) AddNote(listID, taskID, note string) error { return nil }

func TestHandleTaskLists(t *testing.T) {
	s := NewServer(0)
	s.SetTaskProvider(&mockTaskProvider{
		lists: []tasks.TaskList{
			{ID: "abc", Name: "Work"},
			{ID: "def", Name: "Personal"},
		},
	})

	req := httptest.NewRequest("GET", "/api/tasks/lists", nil)
	w := httptest.NewRecorder()
	s.handleTaskLists(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}

	var resp struct {
		Lists []tasks.TaskList `json:"lists"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)

	if len(resp.Lists) != 2 {
		t.Fatalf("got %d lists, want 2", len(resp.Lists))
	}
}
```

- [ ] **Step 3: Implement task handlers**

Add to `internal/frontend/web/handlers.go`:

```go
func (s *Server) handleTaskLists(w http.ResponseWriter, r *http.Request) {
	if s.taskProvider == nil {
		http.Error(w, "tasks not configured", http.StatusServiceUnavailable)
		return
	}
	lists, err := s.taskProvider.ListTaskLists()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"lists": lists})
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if s.taskProvider == nil {
		http.Error(w, "tasks not configured", http.StatusServiceUnavailable)
		return
	}
	listID := r.URL.Query().Get("list")
	if listID == "" {
		http.Error(w, "list parameter required", http.StatusBadRequest)
		return
	}
	items, err := s.taskProvider.ListTasks(listID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"tasks": items})
}

func (s *Server) handleTaskComplete(w http.ResponseWriter, r *http.Request) {
	if s.taskProvider == nil {
		http.Error(w, "tasks not configured", http.StatusServiceUnavailable)
		return
	}
	taskID := r.PathValue("id")
	var req struct {
		ListID string `json:"listId"`
		Note   string `json:"note"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	if err := s.taskProvider.CompleteTask(req.ListID, taskID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if req.Note != "" {
		s.taskProvider.AddNote(req.ListID, taskID, req.Note)
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "completed"})
}

func (s *Server) handleTaskReopen(w http.ResponseWriter, r *http.Request) {
	if s.taskProvider == nil {
		http.Error(w, "tasks not configured", http.StatusServiceUnavailable)
		return
	}
	taskID := r.PathValue("id")
	var req struct {
		ListID string `json:"listId"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	if err := s.taskProvider.ReopenTask(req.ListID, taskID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "reopened"})
}
```

- [ ] **Step 4: Register routes in server.go**

Add to `Start()`:

```go
mux.HandleFunc("GET /api/tasks/lists", s.handleTaskLists)
mux.HandleFunc("GET /api/tasks", s.handleTasks)
mux.HandleFunc("POST /api/tasks/{id}/complete", s.handleTaskComplete)
mux.HandleFunc("POST /api/tasks/{id}/reopen", s.handleTaskReopen)
```

- [ ] **Step 5: Extend launchRequest with task fields**

Modify the `launchRequest` struct in `handlers.go`:

```go
type launchRequest struct {
	Provider   string `json:"provider"`
	Dir        string `json:"dir"`
	Model      string `json:"model"`
	Mode       string `json:"mode"`
	TaskID     string `json:"task_id,omitempty"`
	TaskListID string `json:"task_list_id,omitempty"`
	UserPrompt string `json:"user_prompt,omitempty"`
}
```

- [ ] **Step 6: Run tests**

Run: `go test ./internal/frontend/web/ -v -timeout 30s`
Expected: ALL PASS

- [ ] **Step 7: Run full suite**

Run: `go test ./... -timeout 30s`
Expected: ALL PASS

- [ ] **Step 8: Commit**

```bash
git add internal/frontend/web/handlers.go internal/frontend/web/server.go internal/frontend/web/handlers_test.go
git commit -m "feat(web): add tasks API endpoints (lists, complete, reopen)"
```

---

### Task 8: Web API — quick launch config endpoint

**Files:**
- Modify: `internal/frontend/web/handlers.go`
- Modify: `internal/frontend/web/server.go`

- [ ] **Step 1: Add quick launch config endpoint**

Add to `handlers.go`:

```go
func (s *Server) handleQuickLaunchDirs(w http.ResponseWriter, r *http.Request) {
	dirs := s.cfg.QuickLaunch.Directories
	if dirs == nil {
		dirs = []string{}
	}

	// Expand ~ and validate
	type dirEntry struct {
		Path     string `json:"path"`
		Basename string `json:"basename"`
		Exists   bool   `json:"exists"`
	}

	var entries []dirEntry
	for _, d := range dirs {
		expanded := expandHome(d)
		_, err := os.Stat(expanded)
		entries = append(entries, dirEntry{
			Path:     expanded,
			Basename: filepath.Base(expanded),
			Exists:   err == nil,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"directories": entries})
}
```

Add the helper (if not already present):

```go
func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}
```

- [ ] **Step 2: Register route**

Add to `Start()`:

```go
mux.HandleFunc("GET /api/quick-launch", s.handleQuickLaunchDirs)
```

- [ ] **Step 3: Run full suite**

Run: `go test ./... -timeout 30s`
Expected: ALL PASS

- [ ] **Step 4: Commit**

```bash
git add internal/frontend/web/handlers.go internal/frontend/web/server.go
git commit -m "feat(web): add quick launch directories endpoint"
```

---

### Task 9: Wire tasks provider in main startup

**Files:**
- Modify: `cmd/aimux/main.go` (or wherever the web server is initialized)

- [ ] **Step 1: Find where web server is created**

Search for `web.NewServer` or `SetLaunchFunc` calls to find the startup wiring.

- [ ] **Step 2: Add tasks provider initialization**

After config is loaded and before the server starts, add:

```go
import "github.com/zanetworker/aimux/internal/tasks"

// Initialize tasks provider (best-effort; nil is OK, endpoints return 503)
taskProvider, taskErr := tasks.NewProvider(cfg.Tasks.Backend, cfg.Tasks.MCPEndpoint)
if taskErr != nil {
	log.Printf("tasks: %v (tasks panel will be unavailable)", taskErr)
}
if taskProvider != nil {
	webServer.SetTaskProvider(taskProvider)
}
```

- [ ] **Step 3: Build and verify**

Run: `go build ./cmd/aimux`
Expected: Compiles without errors.

- [ ] **Step 4: Commit**

```bash
git add cmd/aimux/main.go
git commit -m "feat: wire tasks provider into web server startup"
```

---

### Task 10: React — refactor LaunchDialog with directory browser and quick launch

**Files:**
- Modify: `web/src/components/LaunchDialog.tsx`

- [ ] **Step 1: Add directory browser state and API calls**

Replace the current raw text input with a tabbed interface. Add state for tabs (quick/recent/browse), fetched directories, and browse path:

```tsx
const [dirTab, setDirTab] = useState<'quick' | 'recent' | 'browse'>('quick');
const [quickDirs, setQuickDirs] = useState<{ path: string; basename: string; exists: boolean }[]>([]);
const [recentDirs, setRecentDirs] = useState<{ path: string; display: string; age: string }[]>([]);
const [browseEntries, setBrowseEntries] = useState<{ name: string; isDir: boolean }[]>([]);
const [browsePath, setBrowsePath] = useState('');
const [filterText, setFilterText] = useState('');
```

- [ ] **Step 2: Fetch quick launch dirs on mount**

```tsx
useEffect(() => {
  if (!open) return;
  fetch('/api/quick-launch')
    .then(r => r.json())
    .then(d => {
      setQuickDirs(d.directories || []);
      const firstValid = (d.directories || []).find((d: any) => d.exists);
      if (firstValid && !dir) setDir(firstValid.path);
    })
    .catch(() => {});
  fetch('/api/directories/recent')
    .then(r => r.json())
    .then(d => setRecentDirs(d.directories || []))
    .catch(() => {});
}, [open]);
```

- [ ] **Step 3: Implement Quick tab — directory pills**

```tsx
{dirTab === 'quick' && (
  <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
    {quickDirs.filter(d => d.exists).map(d => (
      <button
        key={d.path}
        onClick={() => setDir(d.path)}
        title={d.path}
        style={{
          padding: '6px 12px', borderRadius: 4, fontSize: 11,
          fontWeight: 600, cursor: 'pointer',
          border: dir === d.path ? '1px solid var(--accent)' : '1px solid var(--border)',
          background: dir === d.path ? 'var(--accent-dim)' : 'var(--bg-3)',
          color: dir === d.path ? 'var(--accent)' : 'var(--fg-3)',
        }}
      >
        {d.basename}
      </button>
    ))}
  </div>
)}
```

- [ ] **Step 4: Implement Recent tab — clickable list**

```tsx
{dirTab === 'recent' && (
  <div style={{ maxHeight: 200, overflowY: 'auto' }}>
    {recentDirs.map(d => (
      <div
        key={d.path}
        onClick={() => setDir(d.path)}
        style={{
          padding: '6px 8px', cursor: 'pointer', borderRadius: 4,
          background: dir === d.path ? 'var(--accent-dim)' : 'transparent',
          color: dir === d.path ? 'var(--accent)' : 'var(--fg-2)',
          fontSize: 12, display: 'flex', justifyContent: 'space-between',
        }}
      >
        <span>{d.display}</span>
        <span style={{ color: 'var(--fg-4)', fontSize: 10 }}>{d.age}</span>
      </div>
    ))}
  </div>
)}
```

- [ ] **Step 5: Implement Browse tab — directory navigation**

```tsx
const fetchBrowse = (path: string) => {
  fetch(`/api/directories/browse?path=${encodeURIComponent(path)}`)
    .then(r => r.json())
    .then(d => { setBrowseEntries(d.entries || []); setBrowsePath(d.path); })
    .catch(() => {});
};

{dirTab === 'browse' && (
  <div>
    <div style={{ fontSize: 11, color: 'var(--teal)', marginBottom: 8, wordBreak: 'break-all' }}>
      {browsePath}
    </div>
    <div style={{ maxHeight: 200, overflowY: 'auto' }}>
      <div onClick={() => fetchBrowse(browsePath.split('/').slice(0, -1).join('/') || '/')}
        style={{ padding: '4px 8px', cursor: 'pointer', color: 'var(--fg-3)', fontSize: 12 }}>
        ..
      </div>
      {browseEntries.map(e => (
        <div key={e.name}
          onClick={() => e.isDir ? fetchBrowse(browsePath + '/' + e.name) : null}
          style={{
            padding: '4px 8px', cursor: e.isDir ? 'pointer' : 'default',
            color: e.isDir ? 'var(--fg-2)' : 'var(--fg-4)', fontSize: 12,
          }}
        >
          {e.isDir ? '📁 ' : '  '}{e.name}
        </div>
      ))}
    </div>
    <button onClick={() => setDir(browsePath)}
      style={{
        marginTop: 8, padding: '4px 12px', borderRadius: 4, fontSize: 11,
        border: '1px solid var(--accent)', background: 'var(--accent-dim)',
        color: 'var(--accent)', cursor: 'pointer',
      }}
    >
      Select this directory
    </button>
  </div>
)}
```

- [ ] **Step 6: Add tab row above the directory section**

```tsx
<div style={{ display: 'flex', gap: 0, marginBottom: 8 }}>
  {(['quick', 'recent', 'browse'] as const).map(tab => (
    <button key={tab} onClick={() => { setDirTab(tab); if (tab === 'browse' && !browsePath) fetchBrowse(dir || '~'); }}
      style={{
        padding: '4px 10px', fontSize: 10, fontWeight: dirTab === tab ? 600 : 400,
        textTransform: 'uppercase', cursor: 'pointer', border: 'none',
        borderBottom: dirTab === tab ? '2px solid var(--accent)' : '2px solid transparent',
        background: 'transparent', color: dirTab === tab ? 'var(--fg)' : 'var(--fg-3)',
      }}
    >{tab}</button>
  ))}
</div>
```

- [ ] **Step 7: Build frontend and verify**

Run: `cd web && npm run build`
Expected: Builds without errors.

- [ ] **Step 8: Commit**

```bash
git add web/src/components/LaunchDialog.tsx
git commit -m "feat(web): refactor LaunchDialog with quick launch, recent, and browse tabs"
```

---

### Task 11: React — TasksPanel component

**Files:**
- Create: `web/src/components/TasksPanel.tsx`

- [ ] **Step 1: Create TasksPanel component**

```tsx
// web/src/components/TasksPanel.tsx
import { useState, useEffect } from 'react';

interface TaskItem {
  id: string;
  title: string;
  notes: string;
  due: string;
  status: string;
  listID: string;
}

interface TaskListItem {
  id: string;
  name: string;
}

interface Props {
  open: boolean;
  onClose: () => void;
  onLaunchFromTask: (task: TaskItem) => void;
}

export function TasksPanel({ open, onClose, onLaunchFromTask }: Props) {
  const [lists, setLists] = useState<TaskListItem[]>([]);
  const [selectedList, setSelectedList] = useState('');
  const [tasks, setTasks] = useState<TaskItem[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    if (!open) return;
    fetch('/api/tasks/lists')
      .then(r => {
        if (!r.ok) throw new Error('Tasks not configured');
        return r.json();
      })
      .then(d => {
        setLists(d.lists || []);
        if (d.lists?.length && !selectedList) {
          setSelectedList(d.lists[0].id);
        }
      })
      .catch(e => setError(e.message));
  }, [open]);

  useEffect(() => {
    if (!selectedList) return;
    setLoading(true);
    fetch(`/api/tasks?list=${encodeURIComponent(selectedList)}`)
      .then(r => r.json())
      .then(d => {
        const items = (d.tasks || []).map((t: any) => ({ ...t, listID: selectedList }));
        setTasks(items);
        setError('');
      })
      .catch(e => setError(e.message))
      .finally(() => setLoading(false));
  }, [selectedList]);

  const handleComplete = async (task: TaskItem) => {
    await fetch(`/api/tasks/${task.id}/complete`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ listId: task.listID }),
    });
    setTasks(prev => prev.map(t => t.id === task.id ? { ...t, status: 'completed' } : t));
  };

  const handleReopen = async (task: TaskItem) => {
    await fetch(`/api/tasks/${task.id}/reopen`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ listId: task.listID }),
    });
    setTasks(prev => prev.map(t => t.id === task.id ? { ...t, status: 'needsAction' } : t));
  };

  if (!open) return null;

  const pending = tasks.filter(t => t.status === 'needsAction');
  const completed = tasks.filter(t => t.status === 'completed');

  const formatDue = (due: string) => {
    if (!due) return '';
    const d = new Date(due);
    return d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
  };

  return (
    <div style={{
      width: 260, borderLeft: '1px solid var(--border)', background: 'var(--bg-0)',
      display: 'flex', flexDirection: 'column', flexShrink: 0, overflow: 'hidden',
    }}>
      {/* Header */}
      <div style={{
        display: 'flex', justifyContent: 'space-between', alignItems: 'center',
        padding: '12px 14px', borderBottom: '1px solid var(--border)',
      }}>
        <span style={{ fontSize: 13, fontWeight: 600, color: 'var(--fg)' }}>
          Tasks ({pending.length})
        </span>
        <span onClick={onClose}
          style={{ cursor: 'pointer', color: 'var(--fg-3)', fontSize: 16 }}>x</span>
      </div>

      {/* List selector */}
      <div style={{ padding: '8px 14px' }}>
        <select value={selectedList} onChange={e => setSelectedList(e.target.value)}
          style={{
            width: '100%', background: 'var(--bg-2)', border: '1px solid var(--border)',
            borderRadius: 4, color: 'var(--fg)', padding: '4px 8px', fontSize: 12,
          }}
        >
          {lists.map(l => <option key={l.id} value={l.id}>{l.name}</option>)}
        </select>
      </div>

      {/* Error */}
      {error && (
        <div style={{ padding: '8px 14px', color: 'var(--accent)', fontSize: 11 }}>{error}</div>
      )}

      {/* Tasks list */}
      <div style={{ flex: 1, overflowY: 'auto', padding: '0 14px' }}>
        {loading && <div style={{ color: 'var(--fg-3)', fontSize: 11, padding: 8 }}>Loading...</div>}

        {pending.length > 0 && (
          <>
            <div style={{ fontSize: 9, textTransform: 'uppercase', color: 'var(--fg-4)',
              letterSpacing: '0.06em', marginTop: 8, marginBottom: 4 }}>Pending</div>
            {pending.map(t => (
              <div key={t.id} style={{
                padding: '6px 0', borderBottom: '1px solid var(--border)',
              }}>
                <div style={{ fontSize: 12, color: 'var(--fg)', lineHeight: 1.4 }}>{t.title}</div>
                <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 4 }}>
                  <span style={{ fontSize: 10, color: 'var(--fg-4)' }}>{formatDue(t.due)}</span>
                  <span onClick={() => onLaunchFromTask(t)}
                    style={{ fontSize: 10, color: 'var(--accent)', cursor: 'pointer', fontWeight: 600 }}>
                    Launch
                  </span>
                </div>
              </div>
            ))}
          </>
        )}

        {completed.length > 0 && (
          <>
            <div style={{ fontSize: 9, textTransform: 'uppercase', color: 'var(--fg-4)',
              letterSpacing: '0.06em', marginTop: 12, marginBottom: 4 }}>Completed</div>
            {completed.map(t => (
              <div key={t.id} style={{
                padding: '6px 0', borderBottom: '1px solid var(--border)',
              }}>
                <div style={{ fontSize: 12, color: 'var(--fg-4)', textDecoration: 'line-through' }}>{t.title}</div>
                <div style={{ marginTop: 4 }}>
                  <span onClick={() => handleReopen(t)}
                    style={{ fontSize: 10, color: 'var(--fg-3)', cursor: 'pointer' }}>
                    Reopen
                  </span>
                </div>
              </div>
            ))}
          </>
        )}
      </div>
    </div>
  );
}
```

- [ ] **Step 2: Build frontend**

Run: `cd web && npm run build`
Expected: Compiles without errors.

- [ ] **Step 3: Commit**

```bash
git add web/src/components/TasksPanel.tsx
git commit -m "feat(web): add TasksPanel component with list selector and launch button"
```

---

### Task 12: React — TaskLaunchDialog component

**Files:**
- Create: `web/src/components/TaskLaunchDialog.tsx`

- [ ] **Step 1: Create the task launch dialog**

```tsx
// web/src/components/TaskLaunchDialog.tsx
import { useState, useEffect } from 'react';

interface TaskItem {
  id: string;
  title: string;
  notes: string;
  due: string;
  status: string;
  listID: string;
}

interface Props {
  open: boolean;
  task: TaskItem | null;
  onClose: () => void;
}

export function TaskLaunchDialog({ open, task, onClose }: Props) {
  const [provider, setProvider] = useState('claude');
  const [dir, setDir] = useState('');
  const [userPrompt, setUserPrompt] = useState('');
  const [quickDirs, setQuickDirs] = useState<{ path: string; basename: string; exists: boolean }[]>([]);
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (!open) return;
    fetch('/api/quick-launch')
      .then(r => r.json())
      .then(d => {
        const dirs = (d.directories || []).filter((d: any) => d.exists);
        setQuickDirs(dirs);
        if (dirs.length && !dir) setDir(dirs[0].path);
      })
      .catch(() => {});
  }, [open]);

  useEffect(() => {
    const handleKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    if (open) window.addEventListener('keydown', handleKey);
    return () => window.removeEventListener('keydown', handleKey);
  }, [open, onClose]);

  if (!open || !task) return null;

  const handleSubmit = async () => {
    if (!dir) return;
    setSubmitting(true);
    try {
      await fetch('/api/agents/launch', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          provider,
          dir,
          task_id: task.id,
          task_list_id: task.listID,
          user_prompt: userPrompt,
        }),
      });
      onClose();
      setUserPrompt('');
    } finally {
      setSubmitting(false);
    }
  };

  const providers = ['claude', 'codex', 'gemini'] as const;

  return (
    <div style={{
      position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.7)', zIndex: 1000,
      display: 'flex', alignItems: 'center', justifyContent: 'center',
    }} onClick={e => { if (e.target === e.currentTarget) onClose(); }}>
      <div style={{
        background: 'var(--bg-1)', border: '1px solid var(--border)',
        borderRadius: 8, padding: 24, width: 440,
      }} onClick={e => e.stopPropagation()}>
        <h2 style={{ fontSize: 16, fontWeight: 600, marginBottom: 16, color: 'var(--fg)' }}>
          Launch from Task
        </h2>

        {/* Task info */}
        <div style={{
          background: 'var(--bg-2)', borderRadius: 4, padding: 12, marginBottom: 16,
          border: '1px solid var(--border)',
        }}>
          <div style={{ fontSize: 13, fontWeight: 600, color: 'var(--fg)' }}>{task.title}</div>
          {task.notes && (
            <div style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 6, lineHeight: 1.5,
              maxHeight: 60, overflow: 'auto' }}>
              {task.notes}
            </div>
          )}
          {task.due && (
            <div style={{ fontSize: 10, color: 'var(--fg-4)', marginTop: 4 }}>
              Due: {new Date(task.due).toLocaleDateString()}
            </div>
          )}
        </div>

        {/* User prompt */}
        <div style={{ marginBottom: 16 }}>
          <label style={{
            display: 'block', fontSize: 11, textTransform: 'uppercase',
            letterSpacing: '0.06em', color: 'var(--fg-3)', marginBottom: 6,
          }}>Your instructions (optional)</label>
          <textarea
            value={userPrompt}
            onChange={e => setUserPrompt(e.target.value)}
            placeholder="Add specific instructions for the agent..."
            rows={3}
            style={{
              background: 'var(--bg-2)', border: '1px solid var(--border)',
              borderRadius: 4, color: 'var(--fg)', padding: '8px 12px',
              width: '100%', fontSize: 13, outline: 'none', resize: 'vertical',
              fontFamily: 'inherit',
            }}
          />
        </div>

        {/* Agent */}
        <div style={{ marginBottom: 16 }}>
          <label style={{
            display: 'block', fontSize: 11, textTransform: 'uppercase',
            letterSpacing: '0.06em', color: 'var(--fg-3)', marginBottom: 6,
          }}>Agent</label>
          <div style={{ display: 'flex', gap: 8 }}>
            {providers.map(p => (
              <button key={p} onClick={() => setProvider(p)} style={{
                padding: '6px 12px', borderRadius: 4, fontSize: 11, fontWeight: 600,
                textTransform: 'uppercase', cursor: 'pointer',
                border: provider === p ? '1px solid var(--accent)' : '1px solid var(--border)',
                background: provider === p ? 'var(--accent-dim)' : 'var(--bg-3)',
                color: provider === p ? 'var(--accent)' : 'var(--fg-3)',
              }}>{p}</button>
            ))}
          </div>
        </div>

        {/* Directory */}
        <div style={{ marginBottom: 16 }}>
          <label style={{
            display: 'block', fontSize: 11, textTransform: 'uppercase',
            letterSpacing: '0.06em', color: 'var(--fg-3)', marginBottom: 6,
          }}>Directory</label>
          <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
            {quickDirs.map(d => (
              <button key={d.path} onClick={() => setDir(d.path)} title={d.path} style={{
                padding: '6px 12px', borderRadius: 4, fontSize: 11, fontWeight: 600,
                cursor: 'pointer',
                border: dir === d.path ? '1px solid var(--accent)' : '1px solid var(--border)',
                background: dir === d.path ? 'var(--accent-dim)' : 'var(--bg-3)',
                color: dir === d.path ? 'var(--accent)' : 'var(--fg-3)',
              }}>{d.basename}</button>
            ))}
          </div>
        </div>

        {/* Submit */}
        <button onClick={handleSubmit} disabled={!dir || submitting} style={{
          background: !dir || submitting ? 'var(--bg-3)' : 'var(--accent)',
          color: !dir || submitting ? 'var(--fg-3)' : '#fff',
          border: 'none', borderRadius: 4, padding: '8px 16px',
          fontWeight: 600, cursor: !dir || submitting ? 'not-allowed' : 'pointer',
          width: '100%', fontSize: 13,
        }}>
          {submitting ? 'Launching...' : 'Launch Agent'}
        </button>
      </div>
    </div>
  );
}
```

- [ ] **Step 2: Build frontend**

Run: `cd web && npm run build`
Expected: Compiles without errors.

- [ ] **Step 3: Commit**

```bash
git add web/src/components/TaskLaunchDialog.tsx
git commit -m "feat(web): add TaskLaunchDialog with task context and user prompt"
```

---

### Task 13: React — wire TasksPanel and TaskLaunchDialog into App.tsx

**Files:**
- Modify: `web/src/App.tsx`
- Modify: `web/src/components/StatsBar.tsx`

- [ ] **Step 1: Add tasks state to App.tsx**

Add imports:

```tsx
import { TasksPanel } from './components/TasksPanel';
import { TaskLaunchDialog } from './components/TaskLaunchDialog';
```

Add state:

```tsx
const [showTasks, setShowTasks] = useState(false);
const [taskLaunchTarget, setTaskLaunchTarget] = useState<any>(null);
const [pendingTaskCount, setPendingTaskCount] = useState(0);
```

- [ ] **Step 2: Fetch task count for badge**

```tsx
useEffect(() => {
  fetch('/api/tasks/lists')
    .then(r => r.ok ? r.json() : null)
    .then(d => {
      if (!d?.lists?.length) return;
      return fetch(`/api/tasks?list=${d.lists[0].id}`);
    })
    .then(r => r?.ok ? r.json() : null)
    .then(d => {
      if (d?.tasks) {
        setPendingTaskCount(d.tasks.filter((t: any) => t.status === 'needsAction').length);
      }
    })
    .catch(() => {});
}, []);
```

- [ ] **Step 3: Add tasks toggle to StatsBar**

Modify `StatsBar.tsx` props:

```tsx
interface Props {
  agents: Agent[];
  onLaunch: () => void;
  onHome?: () => void;
  onToggleTasks?: () => void;
  taskCount?: number;
  tasksOpen?: boolean;
}
```

Add the tasks button next to Launch in `StatsBar.tsx`, before the `+ Launch` button:

```tsx
{onToggleTasks && (
  <button
    onClick={onToggleTasks}
    style={{
      padding: '5px 14px', borderRadius: 4,
      border: tasksOpen ? '1px solid var(--accent)' : '1px solid var(--border)',
      background: tasksOpen ? 'var(--accent-dim)' : 'transparent',
      color: tasksOpen ? 'var(--accent)' : 'var(--fg-3)',
      fontSize: 11, fontWeight: 600, cursor: 'pointer', marginRight: 8,
    }}
  >
    Tasks{taskCount ? ` (${taskCount})` : ''}
  </button>
)}
```

- [ ] **Step 4: Wire into App.tsx layout**

Pass new props to StatsBar:

```tsx
<StatsBar
  agents={agents}
  onLaunch={() => setShowLaunch(true)}
  onHome={() => { setActiveTab('agents'); setSelectedId(null); setSessionAgent(null); setPanelFullscreen(false); }}
  onToggleTasks={() => setShowTasks(v => !v)}
  taskCount={pendingTaskCount}
  tasksOpen={showTasks}
/>
```

Add TasksPanel to the main content flex container, after the RightPanel:

```tsx
<TasksPanel
  open={showTasks}
  onClose={() => setShowTasks(false)}
  onLaunchFromTask={(task) => setTaskLaunchTarget(task)}
/>
```

Add TaskLaunchDialog after LaunchDialog:

```tsx
<TaskLaunchDialog
  open={!!taskLaunchTarget}
  task={taskLaunchTarget}
  onClose={() => setTaskLaunchTarget(null)}
/>
```

- [ ] **Step 5: Build frontend**

Run: `cd web && npm run build`
Expected: Compiles without errors.

- [ ] **Step 6: Commit**

```bash
git add web/src/App.tsx web/src/components/StatsBar.tsx
git commit -m "feat(web): wire TasksPanel and TaskLaunchDialog into App layout"
```

---

### Task 14: TUI — add Quick tab to launcher

**Files:**
- Modify: `internal/frontend/tui/views/launcher.go`
- Modify: `internal/frontend/tui/views/launcher_test.go`

- [ ] **Step 1: Write test for Quick tab**

```go
// Add to internal/frontend/tui/views/launcher_test.go

func TestLauncherQuickTab(t *testing.T) {
	quickDirs := []string{"/tmp/project-a", "/tmp/project-b"}
	recent := []RecentDirEntry{{Path: "/tmp/other", Display: "other"}}
	opts := map[string]ProviderOptions{"claude": {Models: []string{"opus"}, Modes: []string{"auto"}}}

	lv := NewLauncherView(recent, opts, false)
	lv.SetQuickDirs(quickDirs)
	lv.SetSize(80, 24)

	// Skip provider step
	lv.Update(tea.KeyMsg{Type: tea.KeyEnter})

	// Should start on Quick tab if quickDirs are set
	view := lv.View()
	if !strings.Contains(view, "Quick") {
		t.Error("expected Quick tab in view")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/frontend/tui/views/ -run TestLauncherQuickTab -v`
Expected: FAIL — `SetQuickDirs` not defined.

- [ ] **Step 3: Add quick dirs support to LauncherView**

Add field to `LauncherView`:

```go
quickDirs []string // configured quick launch directories
```

Add setter:

```go
func (l *LauncherView) SetQuickDirs(dirs []string) {
	l.quickDirs = dirs
}
```

Update `viewDirectory()` to show three tabs when `quickDirs` is non-empty:

Add "Quick" tab rendering that shows directory pills (basename only), selectable with j/k and Enter.

Update `updateDirectory()` to handle a third tab state.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/frontend/tui/views/ -v -timeout 30s`
Expected: ALL PASS

- [ ] **Step 5: Wire quick dirs from config in app.go**

In `internal/frontend/tui/app.go`, where `NewLauncherView` is called, add:

```go
if len(a.cfg.QuickLaunch.Directories) > 0 {
	a.launcherView.SetQuickDirs(a.cfg.QuickLaunch.Directories)
}
```

- [ ] **Step 6: Build and run full suite**

Run: `go build ./... && go test ./... -timeout 30s`
Expected: ALL PASS

- [ ] **Step 7: Commit**

```bash
git add internal/frontend/tui/views/launcher.go internal/frontend/tui/views/launcher_test.go internal/frontend/tui/app.go
git commit -m "feat(tui): add Quick tab to launcher with configurable directories"
```

---

### Task 15: Integration — task-aware launch and session completion

**Files:**
- Modify: `internal/frontend/web/handlers.go`
- Modify: `internal/frontend/web/server.go`

- [ ] **Step 1: Handle task context in launch handler**

Modify `handleLaunch` in `handlers.go` to assemble the prompt when `TaskID` is set:

```go
func (s *Server) handleLaunch(w http.ResponseWriter, r *http.Request) {
	var req launchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if s.launchFn == nil {
		http.Error(w, "launch not configured", http.StatusServiceUnavailable)
		return
	}

	// If launching from a task, assemble the prompt
	prompt := ""
	if req.TaskID != "" && s.taskProvider != nil {
		taskItems, err := s.taskProvider.ListTasks(req.TaskListID)
		if err == nil {
			for _, t := range taskItems {
				if t.ID == req.TaskID {
					prompt = tasks.RenderPrompt(
						s.cfg.Tasks.PromptTemplate,
						t.Title, t.Notes, req.UserPrompt,
					)
					// Add "started" note
					s.taskProvider.AddNote(req.TaskListID, req.TaskID,
						fmt.Sprintf("Session started by aimux at %s", time.Now().Format(time.RFC3339)))
					break
				}
			}
		}
	}

	// Pass prompt through to the launch function
	// The launchFn signature needs extending to accept an optional prompt
	if err := s.launchFn(req.Provider, req.Dir, req.Model, req.Mode); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "launched"})
}
```

Note: The `launchFn` signature will need extending to pass the prompt. Update `SetLaunchFunc` and the caller to accept an optional prompt parameter. The prompt gets passed to the agent via the provider's `SpawnArgs`.

- [ ] **Step 2: Build and test**

Run: `go build ./... && go test ./... -timeout 30s`
Expected: ALL PASS

- [ ] **Step 3: Commit**

```bash
git add internal/frontend/web/handlers.go internal/frontend/web/server.go
git commit -m "feat(web): task-aware launch with prompt assembly and start note"
```

---

### Task 16: Build, test, and verify end-to-end

- [ ] **Step 1: Build Go backend**

Run: `go build -o aimux ./cmd/aimux`
Expected: Compiles without errors.

- [ ] **Step 2: Run full Go test suite**

Run: `go test ./... -timeout 30s`
Expected: ALL PASS

- [ ] **Step 3: Build React frontend**

Run: `cd web && npm run build`
Expected: No errors.

- [ ] **Step 4: Run go vet**

Run: `go vet ./...`
Expected: No issues.

- [ ] **Step 5: Manual smoke test**

1. Start aimux with `quick_launch` and `tasks` in config
2. Open web UI
3. Verify Quick Launch pills appear in launch dialog
4. Verify directory browse tabs work (Recent/Browse)
5. Click Tasks button in top bar, verify panel opens with task lists
6. Select a task, click Launch, verify TaskLaunchDialog opens
7. Launch an agent from a task, verify session starts

- [ ] **Step 6: Final commit**

```bash
git add -A
git commit -m "feat: launch modes and Google Tasks integration — end-to-end verified"
```

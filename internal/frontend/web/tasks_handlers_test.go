package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/tasks"
)

type mockTaskProvider struct {
	lists      []tasks.TaskList
	items      []tasks.Task
	addedNotes []string
}

func (m *mockTaskProvider) ListTaskLists() ([]tasks.TaskList, error) { return m.lists, nil }
func (m *mockTaskProvider) ListTasks(listID string) ([]tasks.Task, error) { return m.items, nil }
func (m *mockTaskProvider) CompleteTask(listID, taskID string) error { return nil }
func (m *mockTaskProvider) ReopenTask(listID, taskID string) error  { return nil }
func (m *mockTaskProvider) AddNote(listID, taskID, note string) error {
	m.addedNotes = append(m.addedNotes, note)
	return nil
}

func TestHandleTaskLists(t *testing.T) {
	s := NewServer(0)
	s.SetTaskProvider(&mockTaskProvider{
		lists: []tasks.TaskList{{ID: "abc", Name: "Work"}, {ID: "def", Name: "Personal"}},
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
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Lists) != 2 {
		t.Fatalf("got %d lists, want 2", len(resp.Lists))
	}
}

func TestHandleTaskListsNotConfigured(t *testing.T) {
	s := NewServer(0)
	req := httptest.NewRequest("GET", "/api/tasks/lists", nil)
	w := httptest.NewRecorder()
	s.handleTaskLists(w, req)
	if w.Code != 503 {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestHandleTasks(t *testing.T) {
	s := NewServer(0)
	s.SetTaskProvider(&mockTaskProvider{
		items: []tasks.Task{{ID: "t1", Title: "Fix bug", Status: "needsAction"}},
	})
	req := httptest.NewRequest("GET", "/api/tasks?list=abc", nil)
	w := httptest.NewRecorder()
	s.handleTasks(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestHandleTasksMissingList(t *testing.T) {
	s := NewServer(0)
	s.SetTaskProvider(&mockTaskProvider{})
	req := httptest.NewRequest("GET", "/api/tasks", nil)
	w := httptest.NewRecorder()
	s.handleTasks(w, req)
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestHandleTaskComplete(t *testing.T) {
	s := NewServer(0)
	s.SetTaskProvider(&mockTaskProvider{})
	body := strings.NewReader(`{"listId":"abc"}`)
	req := httptest.NewRequest("POST", "/api/tasks/t1/complete", body)
	req.SetPathValue("id", "t1")
	w := httptest.NewRecorder()
	s.handleTaskComplete(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestLaunchHandlerWithTaskContext(t *testing.T) {
	s := NewServer(0)

	// Set up mock task provider
	mockTasks := &mockTaskProvider{
		items: []tasks.Task{
			{
				ID:     "task-123",
				Title:  "Fix login bug",
				Notes:  "Users can't login with spaces in username",
				Status: "needsAction",
			},
		},
	}
	s.SetTaskProvider(mockTasks)

	// Set up config with task template
	cfg := config.Config{
		Tasks: config.TasksConfig{
			PromptTemplate: "Task: {title}\n\nDetails:\n{notes}\n\nUser input: {user_prompt}",
		},
	}
	s.SetConfig(cfg)

	// Capture launch arguments
	var capturedPrompt string
	s.SetLaunchFunc(func(provider, dir, model, mode, prompt string) error {
		capturedPrompt = prompt
		return nil
	})

	go func() { _ = s.Start() }()
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)

	// Launch with task context
	tmpDir := t.TempDir()
	body, _ := json.Marshal(map[string]string{
		"provider":     "claude",
		"dir":          tmpDir,
		"model":        "sonnet",
		"mode":         "plan",
		"task_id":      "task-123",
		"task_list_id": "list-1",
		"user_prompt":  "Focus on edge cases",
	})
	resp, err := http.Post(s.URL()+"/api/agents/launch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/agents/launch failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Verify the prompt was assembled correctly
	expectedPrompt := "Task: Fix login bug\n\nDetails:\nUsers can't login with spaces in username\n\nUser input: Focus on edge cases"
	if capturedPrompt != expectedPrompt {
		t.Errorf("prompt mismatch.\nGot:\n%s\n\nExpected:\n%s", capturedPrompt, expectedPrompt)
	}

	// Verify note was added to the task
	if len(mockTasks.addedNotes) != 1 {
		t.Fatalf("expected 1 note, got %d", len(mockTasks.addedNotes))
	}
	if !strings.Contains(mockTasks.addedNotes[0], "Session started by aimux") {
		t.Errorf("note should contain 'Session started by aimux', got: %s", mockTasks.addedNotes[0])
	}
}

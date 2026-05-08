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

func TestRenderPromptNoPlaceholders(t *testing.T) {
	tmpl := "Do something"
	result := RenderPrompt(tmpl, "title", "notes", "prompt")
	if result != "Do something" {
		t.Errorf("got %q, want %q", result, "Do something")
	}
}

func TestGWSParseTaskLists(t *testing.T) {
	jsonData := []byte(`{
		"items": [
			{"id": "abc123", "title": "Work"},
			{"id": "def456", "title": "Personal"}
		]
	}`)

	lists, err := parseTaskListsJSON(jsonData)
	if err != nil {
		t.Fatalf("parseTaskListsJSON failed: %v", err)
	}

	if len(lists) != 2 {
		t.Fatalf("expected 2 lists, got %d", len(lists))
	}

	if lists[0].ID != "abc123" || lists[0].Name != "Work" {
		t.Errorf("list[0] got {%q, %q}, want {%q, %q}", lists[0].ID, lists[0].Name, "abc123", "Work")
	}

	if lists[1].ID != "def456" || lists[1].Name != "Personal" {
		t.Errorf("list[1] got {%q, %q}, want {%q, %q}", lists[1].ID, lists[1].Name, "def456", "Personal")
	}
}

func TestGWSParseTasks(t *testing.T) {
	jsonData := []byte(`{
		"items": [
			{
				"id": "task1",
				"title": "Fix bug",
				"notes": "details",
				"due": "2026-05-10T00:00:00.000Z",
				"status": "needsAction",
				"updated": "2026-05-07T09:36:30.253Z"
			},
			{
				"id": "task2",
				"title": "Write tests",
				"notes": "",
				"due": "",
				"status": "completed",
				"updated": "2026-05-06T14:20:00.000Z"
			}
		]
	}`)

	tasks, err := parseTasksJSON(jsonData)
	if err != nil {
		t.Fatalf("parseTasksJSON failed: %v", err)
	}

	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}

	// Check first task (needsAction)
	task1 := tasks[0]
	if task1.ID != "task1" {
		t.Errorf("task1.ID got %q, want %q", task1.ID, "task1")
	}
	if task1.Title != "Fix bug" {
		t.Errorf("task1.Title got %q, want %q", task1.Title, "Fix bug")
	}
	if task1.Notes != "details" {
		t.Errorf("task1.Notes got %q, want %q", task1.Notes, "details")
	}
	if task1.Due != "2026-05-10T00:00:00.000Z" {
		t.Errorf("task1.Due got %q, want %q", task1.Due, "2026-05-10T00:00:00.000Z")
	}
	if task1.Status != "needsAction" {
		t.Errorf("task1.Status got %q, want %q", task1.Status, "needsAction")
	}
	if task1.Updated != "2026-05-07T09:36:30.253Z" {
		t.Errorf("task1.Updated got %q, want %q", task1.Updated, "2026-05-07T09:36:30.253Z")
	}

	// Check second task (completed)
	task2 := tasks[1]
	if task2.ID != "task2" {
		t.Errorf("task2.ID got %q, want %q", task2.ID, "task2")
	}
	if task2.Title != "Write tests" {
		t.Errorf("task2.Title got %q, want %q", task2.Title, "Write tests")
	}
	if task2.Notes != "" {
		t.Errorf("task2.Notes got %q, want empty string", task2.Notes)
	}
	if task2.Due != "" {
		t.Errorf("task2.Due got %q, want empty string", task2.Due)
	}
	if task2.Status != "completed" {
		t.Errorf("task2.Status got %q, want %q", task2.Status, "completed")
	}
	if task2.Updated != "2026-05-06T14:20:00.000Z" {
		t.Errorf("task2.Updated got %q, want %q", task2.Updated, "2026-05-06T14:20:00.000Z")
	}
}

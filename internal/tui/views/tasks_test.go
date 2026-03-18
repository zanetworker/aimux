package views

import (
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/task"
)

func TestNewTasksView_Empty(t *testing.T) {
	v := NewTasksView()
	if v == nil {
		t.Fatal("NewTasksView returned nil")
	}
	if len(v.tasks) != 0 {
		t.Errorf("expected 0 tasks, got %d", len(v.tasks))
	}
	if v.cursor != 0 {
		t.Errorf("expected cursor 0, got %d", v.cursor)
	}
	if v.Selected() != nil {
		t.Error("expected nil Selected on empty view")
	}
}

func TestSetTasks_PopulatesTaskList(t *testing.T) {
	v := NewTasksView()
	tasks := []task.Task{
		{ID: "1", Prompt: "Research LangGraph", Status: task.StatusCompleted},
		{ID: "2", Prompt: "Implement API", Status: task.StatusInProgress},
	}
	v.SetTasks(tasks)

	if len(v.tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(v.tasks))
	}
	// Selected should point to first task after SetTasks.
	if v.Selected() == nil {
		t.Fatal("expected non-nil Selected after SetTasks")
	}
}

func TestView_RendersColumns(t *testing.T) {
	v := NewTasksView()
	v.SetSize(120, 20)
	v.SetTasks([]task.Task{
		{
			ID:       "1",
			Prompt:   "Research LangGraph",
			Assignee: "researcher-1",
			Location: task.LocationK8s,
			Status:   task.StatusCompleted,
		},
	})

	output := v.View()
	// Check header columns are present.
	if !strings.Contains(output, "TASK") {
		t.Error("output missing TASK header")
	}
	if !strings.Contains(output, "AGENT") {
		t.Error("output missing AGENT header")
	}
	if !strings.Contains(output, "LOC") {
		t.Error("output missing LOC header")
	}
	if !strings.Contains(output, "STATUS") {
		t.Error("output missing STATUS header")
	}
	if !strings.Contains(output, "AGE") {
		t.Error("output missing AGE header")
	}
	// Check task data appears.
	if !strings.Contains(output, "Research LangGraph") {
		t.Error("output missing task prompt")
	}
	if !strings.Contains(output, "researcher-1") {
		t.Error("output missing assignee")
	}
	if !strings.Contains(output, "k8s") {
		t.Error("output missing location")
	}
	if !strings.Contains(output, "done") {
		t.Error("output missing status text")
	}
}

func TestView_StatusIcons(t *testing.T) {
	tests := []struct {
		status task.Status
		icon   string
	}{
		{task.StatusInProgress, "\u25cf"}, // ●
		{task.StatusClaimed, "\u25cf"},    // ●
		{task.StatusCompleted, "\u2713"},  // ✓
		{task.StatusPending, "\u25cb"},    // ○
		{task.StatusFailed, "\u2717"},     // ✗
		{task.StatusDead, "\u2717"},       // ✗
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			v := NewTasksView()
			v.SetSize(120, 20)
			v.SetTasks([]task.Task{
				{ID: "1", Prompt: "test", Status: tt.status},
			})
			output := v.View()
			if !strings.Contains(output, tt.icon) {
				t.Errorf("status %s: output missing icon %q", tt.status, tt.icon)
			}
		})
	}
}

func TestHandleKey_Navigation(t *testing.T) {
	v := NewTasksView()
	v.SetSize(120, 20)
	v.SetTasks([]task.Task{
		{ID: "1", Prompt: "task-a", Status: task.StatusPending},
		{ID: "2", Prompt: "task-b", Status: task.StatusPending},
		{ID: "3", Prompt: "task-c", Status: task.StatusPending},
	})

	// Initial cursor at 0.
	if v.cursor != 0 {
		t.Fatalf("expected cursor 0, got %d", v.cursor)
	}

	// Move down with j.
	v.HandleKey("j")
	if v.cursor != 1 {
		t.Errorf("after j: expected cursor 1, got %d", v.cursor)
	}

	// Move down again.
	v.HandleKey("j")
	if v.cursor != 2 {
		t.Errorf("after j+j: expected cursor 2, got %d", v.cursor)
	}

	// j at bottom stays.
	v.HandleKey("j")
	if v.cursor != 2 {
		t.Errorf("j at bottom: expected cursor 2, got %d", v.cursor)
	}

	// Move up with k.
	v.HandleKey("k")
	if v.cursor != 1 {
		t.Errorf("after k: expected cursor 1, got %d", v.cursor)
	}

	// k at top stays.
	v.HandleKey("k")
	v.HandleKey("k")
	if v.cursor != 0 {
		t.Errorf("k at top: expected cursor 0, got %d", v.cursor)
	}

	// G goes to end.
	v.HandleKey("G")
	if v.cursor != 2 {
		t.Errorf("after G: expected cursor 2, got %d", v.cursor)
	}

	// g goes to beginning.
	v.HandleKey("g")
	if v.cursor != 0 {
		t.Errorf("after g: expected cursor 0, got %d", v.cursor)
	}
}

func TestHandleKey_EmptyList(t *testing.T) {
	v := NewTasksView()
	handled := v.HandleKey("j")
	if handled {
		t.Error("HandleKey should return false on empty task list")
	}
}

func TestSelected_ReturnsCorrectTask(t *testing.T) {
	v := NewTasksView()
	v.SetSize(120, 20)
	// All pending so sort order preserves creation order.
	tasks := []task.Task{
		{ID: "1", Prompt: "first", Status: task.StatusPending, CreatedAt: time.Now().Add(-3 * time.Minute)},
		{ID: "2", Prompt: "second", Status: task.StatusPending, CreatedAt: time.Now().Add(-2 * time.Minute)},
		{ID: "3", Prompt: "third", Status: task.StatusPending, CreatedAt: time.Now().Add(-1 * time.Minute)},
	}
	v.SetTasks(tasks)

	sel := v.Selected()
	if sel == nil || sel.ID != "1" {
		t.Errorf("expected first task selected, got %v", sel)
	}

	v.HandleKey("j")
	sel = v.Selected()
	if sel == nil || sel.ID != "2" {
		t.Errorf("after j: expected second task, got %v", sel)
	}

	v.HandleKey("j")
	sel = v.Selected()
	if sel == nil || sel.ID != "3" {
		t.Errorf("after j+j: expected third task, got %v", sel)
	}
}

func TestSortOrder_ActiveFirst(t *testing.T) {
	v := NewTasksView()
	now := time.Now()
	tasks := []task.Task{
		{ID: "pending", Status: task.StatusPending, CreatedAt: now},
		{ID: "completed", Status: task.StatusCompleted, CreatedAt: now},
		{ID: "running", Status: task.StatusInProgress, CreatedAt: now},
		{ID: "failed", Status: task.StatusFailed, CreatedAt: now},
		{ID: "claimed", Status: task.StatusClaimed, CreatedAt: now},
	}
	v.SetTasks(tasks)

	// Expected order: in_progress, claimed, pending, completed, failed.
	expected := []string{"running", "claimed", "pending", "completed", "failed"}
	for i, exp := range expected {
		if v.tasks[i].ID != exp {
			t.Errorf("position %d: expected %s, got %s", i, exp, v.tasks[i].ID)
		}
	}
}

func TestSortOrder_WithinSamePriority_OldestFirst(t *testing.T) {
	v := NewTasksView()
	now := time.Now()
	tasks := []task.Task{
		{ID: "newer", Status: task.StatusPending, CreatedAt: now.Add(-1 * time.Minute)},
		{ID: "older", Status: task.StatusPending, CreatedAt: now.Add(-5 * time.Minute)},
	}
	v.SetTasks(tasks)

	if v.tasks[0].ID != "older" {
		t.Errorf("expected older first, got %s", v.tasks[0].ID)
	}
	if v.tasks[1].ID != "newer" {
		t.Errorf("expected newer second, got %s", v.tasks[1].ID)
	}
}

func TestFormatTaskAge_Seconds(t *testing.T) {
	created := time.Now().Add(-30 * time.Second)
	age := FormatTaskAge(created)
	if !strings.HasSuffix(age, "s") {
		t.Errorf("expected seconds suffix, got %q", age)
	}
}

func TestFormatTaskAge_Minutes(t *testing.T) {
	created := time.Now().Add(-45 * time.Minute)
	age := FormatTaskAge(created)
	if !strings.HasSuffix(age, "m") {
		t.Errorf("expected minutes suffix, got %q", age)
	}
	if !strings.Contains(age, "45") {
		t.Errorf("expected 45m, got %q", age)
	}
}

func TestFormatTaskAge_Hours(t *testing.T) {
	created := time.Now().Add(-3 * time.Hour)
	age := FormatTaskAge(created)
	if !strings.HasSuffix(age, "h") {
		t.Errorf("expected hours suffix, got %q", age)
	}
	if !strings.Contains(age, "3") {
		t.Errorf("expected 3h, got %q", age)
	}
}

func TestFormatTaskAge_Days(t *testing.T) {
	created := time.Now().Add(-48 * time.Hour)
	age := FormatTaskAge(created)
	if !strings.HasSuffix(age, "d") {
		t.Errorf("expected days suffix, got %q", age)
	}
	if !strings.Contains(age, "2") {
		t.Errorf("expected 2d, got %q", age)
	}
}

func TestFormatTaskAge_ZeroTime(t *testing.T) {
	age := FormatTaskAge(time.Time{})
	if age != "\u2014" {
		t.Errorf("expected em dash for zero time, got %q", age)
	}
}

func TestView_EmptyState(t *testing.T) {
	v := NewTasksView()
	v.SetSize(80, 20)
	v.SetTasks([]task.Task{})

	output := v.View()
	if !strings.Contains(output, "No tasks") {
		t.Error("empty state should show 'No tasks' message")
	}
}

func TestView_PromptTruncation(t *testing.T) {
	v := NewTasksView()
	v.SetSize(120, 20)
	longPrompt := strings.Repeat("A", 100)
	v.SetTasks([]task.Task{
		{ID: "1", Prompt: longPrompt, Status: task.StatusPending},
	})

	output := v.View()
	// The full 100-char prompt should not appear; it should be truncated with "...".
	if strings.Contains(output, longPrompt) {
		t.Error("long prompt should be truncated")
	}
	if !strings.Contains(output, "...") {
		t.Error("truncated prompt should end with '...'")
	}
}

func TestView_PendingAssignee(t *testing.T) {
	v := NewTasksView()
	v.SetSize(120, 20)
	v.SetTasks([]task.Task{
		{ID: "1", Prompt: "test", Status: task.StatusPending, Assignee: ""},
	})

	output := v.View()
	if !strings.Contains(output, "(pending)") {
		t.Error("empty assignee should show '(pending)'")
	}
}

func TestView_SummaryLine(t *testing.T) {
	v := NewTasksView()
	v.SetSize(120, 20)
	now := time.Now()
	v.SetTasks([]task.Task{
		{ID: "1", Status: task.StatusInProgress, CreatedAt: now},
		{ID: "2", Status: task.StatusInProgress, CreatedAt: now},
		{ID: "3", Status: task.StatusCompleted, CreatedAt: now},
		{ID: "4", Status: task.StatusPending, CreatedAt: now},
		{ID: "5", Status: task.StatusFailed, CreatedAt: now},
	})

	output := v.View()
	if !strings.Contains(output, "Tasks") {
		t.Error("summary should contain 'Tasks' label")
	}
	if !strings.Contains(output, "2 running") {
		t.Error("summary should show '2 running'")
	}
	if !strings.Contains(output, "1 done") {
		t.Error("summary should show '1 done'")
	}
	if !strings.Contains(output, "1 pending") {
		t.Error("summary should show '1 pending'")
	}
	if !strings.Contains(output, "1 failed") {
		t.Error("summary should show '1 failed'")
	}
}

func TestSetTasks_ClampsCursor(t *testing.T) {
	v := NewTasksView()
	v.SetTasks([]task.Task{
		{ID: "1", Status: task.StatusPending},
		{ID: "2", Status: task.StatusPending},
		{ID: "3", Status: task.StatusPending},
	})
	v.HandleKey("G") // cursor at 2

	// Now set fewer tasks — cursor should clamp.
	v.SetTasks([]task.Task{
		{ID: "1", Status: task.StatusPending},
	})
	if v.cursor != 0 {
		t.Errorf("expected cursor clamped to 0, got %d", v.cursor)
	}
}

func TestHandleKey_UnknownKey(t *testing.T) {
	v := NewTasksView()
	v.SetTasks([]task.Task{
		{ID: "1", Status: task.StatusPending},
	})
	handled := v.HandleKey("z")
	if handled {
		t.Error("unknown key should return false")
	}
}

func TestTaskStatusText(t *testing.T) {
	tests := []struct {
		status task.Status
		want   string
	}{
		{task.StatusInProgress, "running"},
		{task.StatusClaimed, "claimed"},
		{task.StatusCompleted, "done"},
		{task.StatusPending, "waiting"},
		{task.StatusFailed, "failed"},
		{task.StatusDead, "dead"},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			got := taskStatusText(tt.status)
			if got != tt.want {
				t.Errorf("taskStatusText(%s) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

package coordination

import (
	"testing"

	"github.com/zanetworker/aimux/pkg/rediskeys"
)

// Compile-time interface check
var _ Coordinator = (*RedisCoordinator)(nil)

func TestNewRedisCoordinatorInvalidURL(t *testing.T) {
	_, err := NewRedisCoordinator("not-a-url", "test-team")
	if err == nil {
		t.Fatal("expected error for invalid Redis URL, got nil")
	}
}

func TestNewRedisCoordinatorDefaultTeamID(t *testing.T) {
	rc := NewRedisCoordinatorFromClient(nil, "")
	if rc.teamID != "default" {
		t.Errorf("expected teamID 'default', got %q", rc.teamID)
	}
}

func TestNewRedisCoordinatorCustomTeamID(t *testing.T) {
	rc := NewRedisCoordinatorFromClient(nil, "my-team")
	if rc.teamID != "my-team" {
		t.Errorf("expected teamID 'my-team', got %q", rc.teamID)
	}
}

func TestRedisKeyPatterns(t *testing.T) {
	teamID := "test-team"

	tests := []struct {
		name     string
		got      string
		expected string
	}{
		{"task key", rediskeys.Task(teamID, "abc123"), "team:test-team:task:abc123"},
		{"tasks:pending", rediskeys.TasksPending(teamID), "team:test-team:tasks:pending"},
		{"tasks:all", rediskeys.TasksAll(teamID), "team:test-team:tasks:all"},
		{"inbox", rediskeys.Inbox(teamID, "agent-1"), "team:test-team:inbox:agent-1"},
		{"cost", rediskeys.Cost(teamID, "agent-1"), "team:test-team:cost:agent-1"},
		{"agent", rediskeys.Agent(teamID, "agent-1"), "team:test-team:agent:agent-1"},
		{"heartbeat", rediskeys.Heartbeat(teamID), "team:test-team:heartbeat"},
		{"events", rediskeys.Events(teamID), "team:test-team:events"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.expected {
				t.Errorf("got %q, want %q", tt.got, tt.expected)
			}
		})
	}
}

func TestHashToTask(t *testing.T) {
	h := map[string]string{
		"status":         "completed",
		"prompt":         "Summarize the doc",
		"required_role":  "analyzer",
		"assignee":       "agent-1",
		"depends_on":     `["task-a","task-b"]`,
		"result_summary": "Done",
		"error":          "",
		"created_at":     "1700000000",
		"completed_at":   "1700000100",
	}

	task := hashToTask("tid-1", h)

	if task.ID != "tid-1" {
		t.Errorf("expected ID 'tid-1', got %q", task.ID)
	}
	if task.Status != TaskCompleted {
		t.Errorf("expected status completed, got %q", task.Status)
	}
	if task.Prompt != "Summarize the doc" {
		t.Errorf("expected prompt 'Summarize the doc', got %q", task.Prompt)
	}
	if task.RequiredRole != "analyzer" {
		t.Errorf("expected role 'analyzer', got %q", task.RequiredRole)
	}
	if task.Assignee != "agent-1" {
		t.Errorf("expected assignee 'agent-1', got %q", task.Assignee)
	}
	if len(task.DependsOn) != 2 {
		t.Fatalf("expected 2 deps, got %d", len(task.DependsOn))
	}
	if task.DependsOn[0] != "task-a" || task.DependsOn[1] != "task-b" {
		t.Errorf("unexpected deps: %v", task.DependsOn)
	}
	if task.ResultSummary != "Done" {
		t.Errorf("expected result_summary 'Done', got %q", task.ResultSummary)
	}
	if task.CreatedAt.Unix() != 1700000000 {
		t.Errorf("expected created_at 1700000000, got %d", task.CreatedAt.Unix())
	}
	if task.CompletedAt.Unix() != 1700000100 {
		t.Errorf("expected completed_at 1700000100, got %d", task.CompletedAt.Unix())
	}
}

func TestHashToTaskEmptyDeps(t *testing.T) {
	h := map[string]string{
		"status":     "pending",
		"prompt":     "Test",
		"depends_on": "[]",
		"created_at": "1700000000",
	}

	task := hashToTask("tid-2", h)

	if task.DependsOn != nil {
		t.Errorf("expected nil deps for empty JSON array, got %v", task.DependsOn)
	}
}

func TestHashToTaskMissingTimestamps(t *testing.T) {
	h := map[string]string{
		"status":       "pending",
		"prompt":       "Test",
		"created_at":   "",
		"completed_at": "",
	}

	task := hashToTask("tid-3", h)

	if !task.CreatedAt.IsZero() {
		t.Errorf("expected zero CreatedAt for empty string, got %v", task.CreatedAt)
	}
	if !task.CompletedAt.IsZero() {
		t.Errorf("expected zero CompletedAt for empty string, got %v", task.CompletedAt)
	}
}

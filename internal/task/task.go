// Package task defines the unified Task model and loaders for both
// Redis (K8s agents) and local filesystem (Claude Code teams).
// This is a core package — it must not import bubbletea, lipgloss, or any TUI package.
package task

import "time"

// Status represents the lifecycle state of a task.
type Status string

const (
	StatusPending    Status = "pending"
	StatusClaimed    Status = "claimed"
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
	StatusDead       Status = "dead"
)

// Location indicates where a task originates from.
type Location string

const (
	LocationLocal Location = "local"
	LocationK8s   Location = "k8s"
)

// Task is the unified task model shared across local and K8s sources.
type Task struct {
	ID            string
	Status        Status
	Prompt        string
	RequiredRole  string
	Assignee      string
	DependsOn     []string
	ResultSummary string
	ResultRef     string
	SourceBranch  string
	Error         string
	RetryCount    int
	CreatedAt     time.Time
	CompletedAt   time.Time
	Location      Location
}

// IsTerminal returns true if the task is in a final state.
func (t *Task) IsTerminal() bool {
	return t.Status == StatusCompleted || t.Status == StatusFailed || t.Status == StatusDead
}

// IsActive returns true if the task is currently being worked on.
func (t *Task) IsActive() bool {
	return t.Status == StatusClaimed || t.Status == StatusInProgress
}

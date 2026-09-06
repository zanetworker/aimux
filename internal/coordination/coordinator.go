package coordination

import (
	"context"
	"time"
)

// Coordinator defines the multi-agent coordination interface.
// Implementations can be local (in-memory) or remote (Redis-backed).
type Coordinator interface {
	RegisterAgent(ctx context.Context, info AgentInfo) error
	Heartbeat(ctx context.Context, agentID string) error
	CreateTask(ctx context.Context, spec TaskSpec) (string, error)
	ListTasks(ctx context.Context) ([]Task, error)
	GetTaskResult(ctx context.Context, taskID string) (string, error)
	SendMessage(ctx context.Context, agentID, text string) error
	GetCosts(ctx context.Context) ([]AgentCost, error)
	Close() error
}

// AgentInfo holds metadata about a registered agent.
type AgentInfo struct {
	ID        string
	Provider  string
	Role      string
	Model     string
	Namespace string
}

// TaskStatus represents the lifecycle state of a task.
type TaskStatus string

const (
	TaskPending    TaskStatus = "pending"
	TaskClaimed    TaskStatus = "claimed"
	TaskInProgress TaskStatus = "in_progress"
	TaskCompleted  TaskStatus = "completed"
	TaskFailed     TaskStatus = "failed"
)

// TaskSpec defines the request to create a new task.
type TaskSpec struct {
	Prompt       string
	RequiredRole string
	DependsOn    []string
}

// Task represents a unit of work in the coordination system.
type Task struct {
	ID            string
	Status        TaskStatus
	Prompt        string
	RequiredRole  string
	Assignee      string
	DependsOn     []string
	ResultSummary string
	ResultRef     string
	Error         string
	CreatedAt     time.Time
	CompletedAt   time.Time
}

// AgentCost tracks token usage for cost accounting.
type AgentCost struct {
	AgentID    string
	TokensIn   int64
	TokensOut  int64
}

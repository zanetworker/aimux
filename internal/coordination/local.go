package coordination

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// LocalCoordinator is an in-memory, zero-config coordinator implementation.
// Data does not survive restarts. Used as the default fallback when Redis is not configured.
type LocalCoordinator struct {
	mu    sync.RWMutex
	agents map[string]AgentInfo
	tasks  map[string]*Task
	msgs   map[string][]string // agentID -> messages
	costs  map[string]*AgentCost
}

// NewLocalCoordinator creates and returns a new in-memory coordinator.
func NewLocalCoordinator() *LocalCoordinator {
	return &LocalCoordinator{
		agents: make(map[string]AgentInfo),
		tasks:  make(map[string]*Task),
		msgs:   make(map[string][]string),
		costs:  make(map[string]*AgentCost),
	}
}

// RegisterAgent stores agent metadata.
func (lc *LocalCoordinator) RegisterAgent(ctx context.Context, info AgentInfo) error {
	lc.mu.Lock()
	defer lc.mu.Unlock()

	lc.agents[info.ID] = info
	return nil
}

// Heartbeat is a no-op for the local coordinator.
// Liveness tracking is not needed in the in-memory implementation.
func (lc *LocalCoordinator) Heartbeat(ctx context.Context, agentID string) error {
	return nil
}

// CreateTask generates a new task with a unique ID and stores it.
func (lc *LocalCoordinator) CreateTask(ctx context.Context, spec TaskSpec) (string, error) {
	lc.mu.Lock()
	defer lc.mu.Unlock()

	id := uuid.New().String()
	task := &Task{
		ID:           id,
		Status:       TaskPending,
		Prompt:       spec.Prompt,
		RequiredRole: spec.RequiredRole,
		DependsOn:    spec.DependsOn,
		CreatedAt:    time.Now(),
	}
	lc.tasks[id] = task
	return id, nil
}

// ListTasks returns all tasks sorted by creation time (ascending).
func (lc *LocalCoordinator) ListTasks(ctx context.Context) ([]Task, error) {
	lc.mu.RLock()
	defer lc.mu.RUnlock()

	var tasks []Task
	for _, t := range lc.tasks {
		tasks = append(tasks, *t)
	}

	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
	})

	return tasks, nil
}

// GetTaskResult returns the ResultRef if the task is completed, otherwise an error.
func (lc *LocalCoordinator) GetTaskResult(ctx context.Context, taskID string) (string, error) {
	lc.mu.RLock()
	defer lc.mu.RUnlock()

	task, ok := lc.tasks[taskID]
	if !ok {
		return "", fmt.Errorf("task not found: %s", taskID)
	}

	if task.Status != TaskCompleted {
		return "", fmt.Errorf("task not completed: %s (status: %s)", taskID, task.Status)
	}

	return task.ResultRef, nil
}

// SendMessage appends a message to an agent's message queue.
func (lc *LocalCoordinator) SendMessage(ctx context.Context, agentID, text string) error {
	lc.mu.Lock()
	defer lc.mu.Unlock()

	lc.msgs[agentID] = append(lc.msgs[agentID], text)
	return nil
}

// GetCosts returns all tracked agent costs.
func (lc *LocalCoordinator) GetCosts(ctx context.Context) ([]AgentCost, error) {
	lc.mu.RLock()
	defer lc.mu.RUnlock()

	var costs []AgentCost
	for _, c := range lc.costs {
		costs = append(costs, *c)
	}

	return costs, nil
}

// Close is a no-op for the local coordinator.
func (lc *LocalCoordinator) Close() error {
	return nil
}

package coordination

import (
	"context"
	"testing"
	"time"
)

// Compile-time interface check
var _ Coordinator = (*LocalCoordinator)(nil)

func TestCreateTaskAndListTasks(t *testing.T) {
	lc := NewLocalCoordinator()
	ctx := context.Background()

	// Create two tasks
	spec1 := TaskSpec{
		Prompt:       "Summarize the document",
		RequiredRole: "analyzer",
	}
	id1, err := lc.CreateTask(ctx, spec1)
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	spec2 := TaskSpec{
		Prompt:       "Extract key insights",
		RequiredRole: "synthesizer",
	}
	id2, err := lc.CreateTask(ctx, spec2)
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	// List tasks
	tasks, err := lc.ListTasks(ctx)
	if err != nil {
		t.Fatalf("ListTasks failed: %v", err)
	}

	if len(tasks) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(tasks))
	}

	// Verify both task IDs are present
	foundID1, foundID2 := false, false
	for _, task := range tasks {
		if task.ID == id1 {
			foundID1 = true
		}
		if task.ID == id2 {
			foundID2 = true
		}
	}
	if !foundID1 || !foundID2 {
		t.Error("not all created task IDs found in list")
	}

	// Verify sorting: first task should be before second (by creation time)
	if len(tasks) >= 2 && tasks[0].CreatedAt.After(tasks[1].CreatedAt) {
		t.Error("tasks not sorted by creation time")
	}
}

func TestCreateTaskAndGetTaskResult(t *testing.T) {
	lc := NewLocalCoordinator()
	ctx := context.Background()

	spec := TaskSpec{Prompt: "Test task"}
	taskID, err := lc.CreateTask(ctx, spec)
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	// Task not completed yet
	result, err := lc.GetTaskResult(ctx, taskID)
	if err == nil {
		t.Error("expected error for incomplete task, got nil")
	}
	if result != "" {
		t.Errorf("expected empty result, got %q", result)
	}

	// Mark task as completed
	lc.mu.Lock()
	lc.tasks[taskID].Status = TaskCompleted
	lc.tasks[taskID].ResultRef = "result-123"
	lc.tasks[taskID].CompletedAt = time.Now()
	lc.mu.Unlock()

	// Now should return the result
	result, err = lc.GetTaskResult(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTaskResult failed: %v", err)
	}
	if result != "result-123" {
		t.Errorf("expected result-123, got %q", result)
	}
}

func TestRegisterAgent(t *testing.T) {
	lc := NewLocalCoordinator()
	ctx := context.Background()

	info := AgentInfo{
		ID:        "agent-1",
		Provider:  "claude",
		Role:      "analyzer",
		Model:     "claude-3-opus",
		Namespace: "default",
	}

	err := lc.RegisterAgent(ctx, info)
	if err != nil {
		t.Fatalf("RegisterAgent failed: %v", err)
	}

	// Verify stored
	lc.mu.RLock()
	stored, ok := lc.agents[info.ID]
	lc.mu.RUnlock()

	if !ok {
		t.Error("agent not found in storage")
	}
	if stored.Provider != "claude" {
		t.Errorf("expected provider claude, got %q", stored.Provider)
	}
}

func TestSendMessage(t *testing.T) {
	lc := NewLocalCoordinator()
	ctx := context.Background()

	agentID := "agent-1"

	// Send two messages
	err := lc.SendMessage(ctx, agentID, "First message")
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	err = lc.SendMessage(ctx, agentID, "Second message")
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	// Verify stored
	lc.mu.RLock()
	msgs := lc.msgs[agentID]
	lc.mu.RUnlock()

	if len(msgs) != 2 {
		t.Errorf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0] != "First message" {
		t.Errorf("expected 'First message', got %q", msgs[0])
	}
	if msgs[1] != "Second message" {
		t.Errorf("expected 'Second message', got %q", msgs[1])
	}
}

func TestGetCosts(t *testing.T) {
	lc := NewLocalCoordinator()
	ctx := context.Background()

	// Initially empty
	costs, err := lc.GetCosts(ctx)
	if err != nil {
		t.Fatalf("GetCosts failed: %v", err)
	}
	if len(costs) != 0 {
		t.Errorf("expected 0 costs initially, got %d", len(costs))
	}

	// Add a cost
	lc.mu.Lock()
	lc.costs["agent-1"] = &AgentCost{
		AgentID:   "agent-1",
		TokensIn:  100,
		TokensOut: 50,
	}
	lc.mu.Unlock()

	// Now should have one cost
	costs, err = lc.GetCosts(ctx)
	if err != nil {
		t.Fatalf("GetCosts failed: %v", err)
	}
	if len(costs) != 1 {
		t.Errorf("expected 1 cost, got %d", len(costs))
	}
	if costs[0].TokensIn != 100 {
		t.Errorf("expected TokensIn 100, got %d", costs[0].TokensIn)
	}
}

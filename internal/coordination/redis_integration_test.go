//go:build integration

package coordination

import (
	"context"
	"os"
	"testing"
)

func redisURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		url = "redis://localhost:6379/15"
	}
	return url
}

func newTestRedisCoordinator(t *testing.T) *RedisCoordinator {
	t.Helper()
	rc, err := NewRedisCoordinator(redisURL(t), "integration-test")
	if err != nil {
		t.Skipf("Redis not available: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		rc.rdb.FlushDB(ctx)
		rc.Close()
	})
	return rc
}

func TestRedisCreateTaskAndList(t *testing.T) {
	rc := newTestRedisCoordinator(t)
	ctx := context.Background()

	id1, err := rc.CreateTask(ctx, TaskSpec{Prompt: "Task one", RequiredRole: "coder"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	id2, err := rc.CreateTask(ctx, TaskSpec{Prompt: "Task two", DependsOn: []string{id1}})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	tasks, err := rc.ListTasks(ctx)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}

	found := map[string]bool{}
	for _, task := range tasks {
		found[task.ID] = true
		if task.Status != TaskPending {
			t.Errorf("task %s: expected pending, got %s", task.ID, task.Status)
		}
	}
	if !found[id1] || !found[id2] {
		t.Error("not all task IDs found in list")
	}
}

func TestRedisGetTaskResult(t *testing.T) {
	rc := newTestRedisCoordinator(t)
	ctx := context.Background()

	id, err := rc.CreateTask(ctx, TaskSpec{Prompt: "Result test"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Not completed yet
	_, err = rc.GetTaskResult(ctx, id)
	if err == nil {
		t.Error("expected error for incomplete task")
	}

	// Mark completed and set result
	rc.rdb.HSet(ctx, "team:integration-test:task:"+id, "status", "completed", "result_summary", "done")
	rc.rdb.Set(ctx, "team:integration-test:task:"+id+":result_full", "full output here", 0)

	result, err := rc.GetTaskResult(ctx, id)
	if err != nil {
		t.Fatalf("GetTaskResult: %v", err)
	}
	if result != "full output here" {
		t.Errorf("expected 'full output here', got %q", result)
	}
}

func TestRedisSendMessage(t *testing.T) {
	rc := newTestRedisCoordinator(t)
	ctx := context.Background()

	err := rc.SendMessage(ctx, "agent-1", "Hello agent")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	msgs, err := rc.rdb.XRange(ctx, "team:integration-test:inbox:agent-1", "-", "+").Result()
	if err != nil {
		t.Fatalf("XRange: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Values["text"] != "Hello agent" {
		t.Errorf("expected 'Hello agent', got %v", msgs[0].Values["text"])
	}
}

func TestRedisRegisterAgentAndHeartbeat(t *testing.T) {
	rc := newTestRedisCoordinator(t)
	ctx := context.Background()

	err := rc.RegisterAgent(ctx, AgentInfo{
		ID:       "agent-1",
		Provider: "claude",
		Role:     "coder",
		Model:    "opus-4",
	})
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	vals, err := rc.rdb.HGetAll(ctx, "team:integration-test:agent:agent-1").Result()
	if err != nil {
		t.Fatalf("HGetAll agent: %v", err)
	}
	if vals["provider"] != "claude" {
		t.Errorf("expected provider 'claude', got %q", vals["provider"])
	}

	err = rc.Heartbeat(ctx, "agent-1")
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	hb, err := rc.rdb.HGet(ctx, "team:integration-test:heartbeat", "agent-1").Result()
	if err != nil {
		t.Fatalf("HGet heartbeat: %v", err)
	}
	if hb == "" {
		t.Error("expected non-empty heartbeat timestamp")
	}
}

func TestRedisGetCosts(t *testing.T) {
	rc := newTestRedisCoordinator(t)
	ctx := context.Background()

	// Initially empty
	costs, err := rc.GetCosts(ctx)
	if err != nil {
		t.Fatalf("GetCosts: %v", err)
	}
	if len(costs) != 0 {
		t.Errorf("expected 0 costs, got %d", len(costs))
	}

	// Populate a cost hash
	rc.rdb.HSet(ctx, "team:integration-test:cost:agent-1", "tokens_in", "500", "tokens_out", "100")

	costs, err = rc.GetCosts(ctx)
	if err != nil {
		t.Fatalf("GetCosts: %v", err)
	}
	if len(costs) != 1 {
		t.Fatalf("expected 1 cost, got %d", len(costs))
	}
	if costs[0].AgentID != "agent-1" {
		t.Errorf("expected agent-1, got %q", costs[0].AgentID)
	}
	if costs[0].TokensIn != 500 {
		t.Errorf("expected 500 tokens_in, got %d", costs[0].TokensIn)
	}
	if costs[0].TokensOut != 100 {
		t.Errorf("expected 100 tokens_out, got %d", costs[0].TokensOut)
	}
}

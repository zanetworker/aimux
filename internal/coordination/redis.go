package coordination

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/zanetworker/aimux/pkg/rediskeys"
)

// RedisCoordinator implements Coordinator with a Redis backend.
// Extracted from internal/mcpserver/server.go Redis operations.
type RedisCoordinator struct {
	rdb    *redis.Client
	teamID string
}

// NewRedisCoordinator connects to Redis and verifies the connection.
// teamID defaults to "default" if empty.
func NewRedisCoordinator(redisURL, teamID string) (*RedisCoordinator, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("invalid Redis URL: %w", err)
	}
	rdb := redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("redis ping: %w", err)
	}

	if teamID == "" {
		teamID = "default"
	}

	return &RedisCoordinator{rdb: rdb, teamID: teamID}, nil
}

// NewRedisCoordinatorFromClient creates a RedisCoordinator from an existing client.
// Used for testing with miniredis or pre-configured clients.
func NewRedisCoordinatorFromClient(rdb *redis.Client, teamID string) *RedisCoordinator {
	if teamID == "" {
		teamID = "default"
	}
	return &RedisCoordinator{rdb: rdb, teamID: teamID}
}

func (c *RedisCoordinator) RegisterAgent(ctx context.Context, info AgentInfo) error {
	fields := map[string]any{
		"provider":      info.Provider,
		"role":          info.Role,
		"model":         info.Model,
		"namespace":     info.Namespace,
		"registered_at": fmt.Sprintf("%d", time.Now().Unix()),
	}
	return c.rdb.HSet(ctx, rediskeys.Agent(c.teamID, info.ID), fields).Err()
}

func (c *RedisCoordinator) Heartbeat(ctx context.Context, agentID string) error {
	return c.rdb.HSet(ctx, rediskeys.Heartbeat(c.teamID), agentID, fmt.Sprintf("%d", time.Now().Unix())).Err()
}

func (c *RedisCoordinator) CreateTask(ctx context.Context, spec TaskSpec) (string, error) {
	taskID := uuid.New().String()[:8]

	deps := "[]"
	if len(spec.DependsOn) > 0 {
		b, err := json.Marshal(spec.DependsOn)
		if err != nil {
			return "", fmt.Errorf("marshal depends_on: %w", err)
		}
		deps = string(b)
	}

	now := time.Now().Unix()
	taskHash := map[string]any{
		"status":         string(TaskPending),
		"prompt":         spec.Prompt,
		"required_role":  spec.RequiredRole,
		"assignee":       "",
		"depends_on":     deps,
		"result_summary": "",
		"error":          "",
		"retry_count":    "0",
		"created_at":     fmt.Sprintf("%d", now),
		"completed_at":   "",
	}

	if err := c.rdb.HSet(ctx, rediskeys.Task(c.teamID, taskID), taskHash).Err(); err != nil {
		return "", fmt.Errorf("redis HSet task: %w", err)
	}

	score := float64(now)
	if err := c.rdb.ZAdd(ctx, rediskeys.TasksPending(c.teamID), redis.Z{Score: score, Member: taskID}).Err(); err != nil {
		return "", fmt.Errorf("redis ZAdd tasks:pending: %w", err)
	}
	if err := c.rdb.ZAdd(ctx, rediskeys.TasksAll(c.teamID), redis.Z{Score: score, Member: taskID}).Err(); err != nil {
		return "", fmt.Errorf("redis ZAdd tasks:all: %w", err)
	}

	return taskID, nil
}

func (c *RedisCoordinator) ListTasks(ctx context.Context) ([]Task, error) {
	taskIDs, err := c.rdb.ZRange(ctx, rediskeys.TasksAll(c.teamID), 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("redis ZRange tasks:all: %w", err)
	}

	// Fallback: SCAN for task hashes if sorted set is empty
	if len(taskIDs) == 0 {
		var cursor uint64
		prefix := rediskeys.Task(c.teamID, "")
		for {
			batch, nextCursor, scanErr := c.rdb.Scan(ctx, cursor, prefix+"*", 100).Result()
			if scanErr != nil {
				break
			}
			for _, key := range batch {
				taskIDs = append(taskIDs, key[len(prefix):])
			}
			cursor = nextCursor
			if cursor == 0 {
				break
			}
		}
	}

	var tasks []Task
	for _, tid := range taskIDs {
		t, hErr := c.rdb.HGetAll(ctx, rediskeys.Task(c.teamID, tid)).Result()
		if hErr != nil || len(t) == 0 {
			continue
		}
		tasks = append(tasks, hashToTask(tid, t))
	}

	return tasks, nil
}

func (c *RedisCoordinator) GetTaskResult(ctx context.Context, taskID string) (string, error) {
	t, err := c.rdb.HGetAll(ctx, rediskeys.Task(c.teamID, taskID)).Result()
	if err != nil {
		return "", fmt.Errorf("redis HGetAll task: %w", err)
	}
	if len(t) == 0 {
		return "", fmt.Errorf("task not found: %s", taskID)
	}

	status := TaskStatus(t["status"])
	if status != TaskCompleted {
		return "", fmt.Errorf("task not completed: %s (status: %s)", taskID, status)
	}

	// Try the dedicated full-result key first
	fullKey := rediskeys.Task(c.teamID, taskID) + ":result_full"
	val, err := c.rdb.Get(ctx, fullKey).Result()
	if err == nil {
		return val, nil
	}

	// Fall back to result_summary from the task hash
	if summary := t["result_summary"]; summary != "" {
		return summary, nil
	}

	return "", nil
}

func (c *RedisCoordinator) SendMessage(ctx context.Context, agentID, text string) error {
	return c.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: rediskeys.Inbox(c.teamID, agentID),
		MaxLen: 1000,
		Approx: true,
		Values: map[string]any{
			"from":      "lead",
			"text":      text,
			"timestamp": fmt.Sprintf("%d", time.Now().Unix()),
		},
	}).Err()
}

func (c *RedisCoordinator) GetCosts(ctx context.Context) ([]AgentCost, error) {
	var costKeys []string
	var cursor uint64
	prefix := rediskeys.Cost(c.teamID, "")
	for {
		batch, nextCursor, err := c.rdb.Scan(ctx, cursor, prefix+"*", 100).Result()
		if err != nil {
			return nil, fmt.Errorf("redis SCAN cost keys: %w", err)
		}
		costKeys = append(costKeys, batch...)
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	var costs []AgentCost
	for _, key := range costKeys {
		vals, err := c.rdb.HGetAll(ctx, key).Result()
		if err != nil {
			continue
		}
		agentID := key[len(prefix):]
		tokensIn, _ := strconv.ParseInt(vals["tokens_in"], 10, 64)
		tokensOut, _ := strconv.ParseInt(vals["tokens_out"], 10, 64)
		costs = append(costs, AgentCost{
			AgentID:  agentID,
			TokensIn: tokensIn,
			TokensOut: tokensOut,
		})
	}

	return costs, nil
}

func (c *RedisCoordinator) Close() error {
	return c.rdb.Close()
}

// hashToTask converts a Redis hash to a Task struct.
func hashToTask(id string, h map[string]string) Task {
	t := Task{
		ID:            id,
		Status:        TaskStatus(h["status"]),
		Prompt:        h["prompt"],
		RequiredRole:  h["required_role"],
		Assignee:      h["assignee"],
		ResultSummary: h["result_summary"],
		Error:         h["error"],
	}

	if h["depends_on"] != "" && h["depends_on"] != "[]" {
		var deps []string
		if err := json.Unmarshal([]byte(h["depends_on"]), &deps); err == nil {
			t.DependsOn = deps
		}
	}

	if ts, err := strconv.ParseInt(h["created_at"], 10, 64); err == nil && ts > 0 {
		t.CreatedAt = time.Unix(ts, 0)
	}
	if ts, err := strconv.ParseInt(h["completed_at"], 10, 64); err == nil && ts > 0 {
		t.CompletedAt = time.Unix(ts, 0)
	}

	return t
}

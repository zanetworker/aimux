package task

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/zanetworker/aimux/pkg/rediskeys"
)

// LoadFromRedis loads all tasks for a team by scanning Redis for task hash keys.
// It uses SCAN (not KEYS) to avoid blocking Redis on large datasets.
func LoadFromRedis(ctx context.Context, rdb *redis.Client, teamID string) ([]Task, error) {
	pattern := rediskeys.TeamKey(teamID, "task:*")
	prefix := rediskeys.TeamKey(teamID, "task:")

	var tasks []Task
	var cursor uint64

	for {
		keys, next, err := rdb.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return nil, fmt.Errorf("scan tasks for team %q: %w", teamID, err)
		}

		for _, key := range keys {
			taskID := key[len(prefix):]
			t, err := loadOneTask(ctx, rdb, teamID, taskID)
			if err != nil {
				return nil, err
			}
			tasks = append(tasks, t)
		}

		cursor = next
		if cursor == 0 {
			break
		}
	}

	return tasks, nil
}

// GetFullResult retrieves the full result for a task. The result_ref field
// contains either a git branch reference or OTEL span ID pointing to the
// complete output, while result_summary is truncated to 500 chars.
func GetFullResult(ctx context.Context, rdb *redis.Client, teamID, taskID string) (string, error) {
	key := rediskeys.Task(teamID, taskID)
	ref, err := rdb.HGet(ctx, key, "result_ref").Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get result_ref for task %q in team %q: %w", taskID, teamID, err)
	}
	return ref, nil
}

// loadOneTask reads a single task hash from Redis and converts it to a Task struct.
func loadOneTask(ctx context.Context, rdb *redis.Client, teamID, taskID string) (Task, error) {
	key := rediskeys.Task(teamID, taskID)
	fields, err := rdb.HGetAll(ctx, key).Result()
	if err != nil {
		return Task{}, fmt.Errorf("hgetall task %q in team %q: %w", taskID, teamID, err)
	}
	if len(fields) == 0 {
		return Task{}, fmt.Errorf("task %q in team %q: not found", taskID, teamID)
	}

	return parseRedisFields(taskID, fields), nil
}

// parseRedisFields converts a Redis hash field map into a Task struct.
func parseRedisFields(taskID string, fields map[string]string) Task {
	t := Task{
		ID:            taskID,
		Status:        Status(fields["status"]),
		Prompt:        fields["prompt"],
		RequiredRole:  fields["required_role"],
		Assignee:      fields["assignee"],
		ResultSummary: fields["result_summary"],
		ResultRef:     fields["result_ref"],
		SourceBranch:  fields["source_branch"],
		Error:         fields["error"],
		Location:      LocationK8s,
	}

	if deps := fields["depends_on"]; deps != "" {
		var parsed []string
		if json.Unmarshal([]byte(deps), &parsed) == nil {
			t.DependsOn = parsed
		}
	}

	if rc := fields["retry_count"]; rc != "" {
		if n, err := strconv.Atoi(rc); err == nil {
			t.RetryCount = n
		}
	}

	if ca := fields["created_at"]; ca != "" {
		t.CreatedAt = parseUnixTimestamp(ca)
	}

	if ca := fields["completed_at"]; ca != "" {
		t.CompletedAt = parseUnixTimestamp(ca)
	}

	return t
}

// parseUnixTimestamp parses a string unix timestamp (integer or float seconds).
func parseUnixTimestamp(s string) time.Time {
	// Try float first (Python's time.time() produces floats like "1709654321.123")
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		sec := int64(f)
		nsec := int64((f - float64(sec)) * 1e9)
		return time.Unix(sec, nsec)
	}
	// Try integer
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0)
	}
	return time.Time{}
}

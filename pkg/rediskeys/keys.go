// Package rediskeys defines all Redis key patterns for the k8s-agents system.
// Imported by both the MCP server (cmd/mcp/) and aimux's K8s provider.
// Single source of truth for key naming — never duplicate these strings elsewhere.
package rediskeys

import "fmt"

// TeamKey is the base key constructor. All other functions call this.
// Format: team:{teamID}:{suffix}
func TeamKey(teamID, suffix string) string {
	return fmt.Sprintf("team:%s:%s", teamID, suffix)
}

// Inbox returns the per-agent inbox stream key.
// Type: Stream. MAXLEN ~1000.
// Fields: from, text, summary, type, timestamp.
func Inbox(teamID, agentID string) string {
	return TeamKey(teamID, "inbox:"+agentID)
}

// Events returns the broadcast events stream key.
// Type: Stream. MAXLEN ~10000.
// Fields: from, text, type, summary, timestamp.
func Events(teamID string) string {
	return TeamKey(teamID, "events")
}

// TasksPending returns the sorted set of pending task IDs.
// Type: Sorted Set. Score = creation unix timestamp. Members = task IDs.
func TasksPending(teamID string) string {
	return TeamKey(teamID, "tasks:pending")
}

// TasksAll returns the sorted set of all task IDs (pending + claimed + completed).
// Type: Sorted Set. Score = creation unix timestamp. Members = task IDs.
// Used by list_tasks for ordered listing without SCAN.
func TasksAll(teamID string) string {
	return TeamKey(teamID, "tasks:all")
}

// Task returns the hash key for a specific task.
// Type: Hash.
// Fields: status, prompt, required_role, assignee, depends_on (JSON),
//
//	result_summary, result_ref, source_branch, retry_count, error,
//	created_at, completed_at.
func Task(teamID, taskID string) string {
	return TeamKey(teamID, "task:"+taskID)
}

// Agent returns the hash key for a specific agent's metadata.
// Type: Hash.
// Fields: provider, role, model, namespace, pod_name, registered_at.
func Agent(teamID, agentID string) string {
	return TeamKey(teamID, "agent:"+agentID)
}

// Heartbeat returns the heartbeat hash for the team.
// Type: Hash. Field = agent ID, value = unix timestamp (updated every 10s).
func Heartbeat(teamID string) string {
	return TeamKey(teamID, "heartbeat")
}

// Cost returns the cost hash for a specific agent.
// Type: Hash.
// Fields: tokens_in, tokens_out, model. Incremented via HINCRBY.
func Cost(teamID, agentID string) string {
	return TeamKey(teamID, "cost:"+agentID)
}

// Config returns the team configuration hash.
// Type: Hash.
// Fields: name, description, members (JSON).
func Config(teamID string) string {
	return TeamKey(teamID, "config")
}

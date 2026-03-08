package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/redis/go-redis/v9"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	rdb       *redis.Client
	k8s       *kubernetes.Clientset
	namespace string
	teamID    string
	maxAgents int
	maxCost   float64
)

func main() {
	// Init Redis
	redisURL := envOr("REDIS_URL", "redis://localhost:6379")
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid REDIS_URL %q: %v\n", redisURL, err)
		os.Exit(1)
	}
	rdb = redis.NewClient(opt)

	// Init K8s — use KUBECONFIG env var if set, otherwise in-cluster config
	kubeconfig := os.Getenv("KUBECONFIG")
	k8sConfig, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot build K8s config (KUBECONFIG=%q): %v\n", kubeconfig, err)
		os.Exit(1)
	}
	k8s, err = kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot create K8s client: %v\n", err)
		os.Exit(1)
	}

	namespace = envOr("K8S_NAMESPACE", "agents")
	teamID = envOr("TEAM_ID", "default")
	maxAgents, _ = strconv.Atoi(envOr("MAX_AGENTS", "20"))
	maxCost, _ = strconv.ParseFloat(envOr("MAX_COST_USD", "100"), 64)

	s := server.NewMCPServer("k8s-agents", "1.0.0")
	s.AddTool(spawnAgentTool(), handleSpawnAgent)
	s.AddTool(createTaskTool(), handleCreateTask)
	s.AddTool(listTasksTool(), handleListTasks)
	s.AddTool(getTaskTool(), handleGetTask)
	s.AddTool(getTaskResultTool(), handleGetTaskResult)
	s.AddTool(listAgentsTool(), handleListAgents)
	s.AddTool(sendMessageTool(), handleSendMessage)
	s.AddTool(scaleDownTool(), handleScaleDown)
	s.AddTool(getCostsTool(), handleGetCosts)

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "MCP server error: %v\n", err)
		os.Exit(1)
	}
}

// --- Tool definitions ---

func spawnAgentTool() mcp.Tool {
	return mcp.NewTool("spawn_agent",
		mcp.WithDescription("Spawn AI agents on Kubernetes for parallel work. "+
			"Scales up a Deployment matching the provider+role. Use when you need "+
			"multiple agents working in parallel. Call once per role needed "+
			"(e.g. spawn coders then spawn reviewers separately). "+
			"Waits up to 120s for agents to register. Always call scale_down when done."),
		mcp.WithString("provider", mcp.Required(), mcp.Description("claude, codex, or gemini")),
		mcp.WithString("role", mcp.Required(), mcp.Description("coder, researcher, or reviewer")),
		mcp.WithNumber("count", mcp.Description("Number of agents to add (default 1)")),
	)
}

func handleSpawnAgent(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	provider, err := req.RequireString("provider")
	if err != nil {
		return mcp.NewToolResultText("Error: provider is required"), nil
	}
	role, err := req.RequireString("role")
	if err != nil {
		return mcp.NewToolResultText("Error: role is required"), nil
	}
	count := req.GetInt("count", 1)
	if count < 1 {
		return mcp.NewToolResultText("Error: count must be at least 1"), nil
	}

	// Enforce agent limit
	heartbeats, err := rdb.HGetAll(ctx, teamKey("heartbeat")).Result()
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Error reading heartbeat from Redis: %v", err)), nil
	}
	if len(heartbeats)+count > maxAgents {
		return mcp.NewToolResultText(fmt.Sprintf(
			"Error: %d agents running, adding %d exceeds limit of %d.",
			len(heartbeats), count, maxAgents)), nil
	}

	deployName := fmt.Sprintf("agent-%s-%s", provider, role)
	deploy, err := k8s.AppsV1().Deployments(namespace).Get(ctx, deployName, metav1.GetOptions{})
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf(
			"Error: Deployment %s not found in namespace %s: %v", deployName, namespace, err)), nil
	}

	newReplicas := int32(0)
	if deploy.Spec.Replicas != nil {
		newReplicas = *deploy.Spec.Replicas
	}
	targetReplicas := newReplicas + int32(count)
	deploy.Spec.Replicas = &targetReplicas

	_, err = k8s.AppsV1().Deployments(namespace).Update(ctx, deploy, metav1.UpdateOptions{})
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Error scaling Deployment %s: %v", deployName, err)), nil
	}

	// Wait for agents to register in Redis (pods take 30-60s to start)
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)
		current, err := rdb.HGetAll(ctx, teamKey("heartbeat")).Result()
		if err != nil {
			return mcp.NewToolResultText(fmt.Sprintf("Error polling heartbeat: %v", err)), nil
		}
		if len(current) >= int(targetReplicas) {
			return mcp.NewToolResultText(fmt.Sprintf(
				"Scaled %s to %d replicas. %d agents registered and ready.",
				deployName, targetReplicas, len(current))), nil
		}
	}
	return mcp.NewToolResultText(fmt.Sprintf(
		"Scaled %s to %d replicas. Agents still starting — check list_agents before creating tasks.",
		deployName, targetReplicas)), nil
}

func createTaskTool() mcp.Tool {
	return mcp.NewTool("create_task",
		mcp.WithDescription("Create a task for K8s agents to work on. Goes into a Redis queue. "+
			"Agents matching required_role pick it up automatically. "+
			"Use depends_on to chain tasks sequentially."),
		mcp.WithString("prompt", mcp.Required(), mcp.Description("Task instructions for the agent")),
		mcp.WithString("required_role", mcp.Description("Only agents with this role can claim it")),
		mcp.WithString("depends_on", mcp.Description("Comma-separated task IDs that must complete first")),
	)
}

func handleCreateTask(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	prompt, err := req.RequireString("prompt")
	if err != nil {
		return mcp.NewToolResultText("Error: prompt is required"), nil
	}
	role := req.GetString("required_role", "")
	depsStr := req.GetString("depends_on", "")

	taskID := uuid.New().String()[:8]
	deps := "[]"
	if depsStr != "" {
		parts := splitComma(depsStr)
		b, err := json.Marshal(parts)
		if err != nil {
			return mcp.NewToolResultText(fmt.Sprintf("Error marshaling depends_on: %v", err)), nil
		}
		deps = string(b)
	}

	now := time.Now().Unix()
	taskHash := map[string]any{
		"status":         "pending",
		"prompt":         prompt,
		"required_role":  role,
		"assignee":       "",
		"depends_on":     deps,
		"result_summary": "",
		"error":          "",
		"retry_count":    "0",
		"created_at":     fmt.Sprintf("%d", now),
		"completed_at":   "",
	}

	if err := rdb.HSet(ctx, teamKey("task:"+taskID), taskHash).Err(); err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Error writing task to Redis: %v", err)), nil
	}

	score := float64(now)
	if err := rdb.ZAdd(ctx, teamKey("tasks:pending"), redis.Z{Score: score, Member: taskID}).Err(); err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Error writing to tasks:pending: %v", err)), nil
	}
	if err := rdb.ZAdd(ctx, teamKey("tasks:all"), redis.Z{Score: score, Member: taskID}).Err(); err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Error writing to tasks:all: %v", err)), nil
	}

	label := "any"
	if role != "" {
		label = role
	}
	return mcp.NewToolResultText(fmt.Sprintf("Task %s created (role=%s)", taskID, label)), nil
}

func listTasksTool() mcp.Tool {
	return mcp.NewTool("list_tasks",
		mcp.WithDescription("Show all tasks and their status."),
	)
}

func handleListTasks(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Use tasks:all sorted set for ordered listing — avoids SCAN on task hashes
	taskIDs, err := rdb.ZRange(ctx, teamKey("tasks:all"), 0, -1).Result()
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Error reading tasks:all from Redis: %v", err)), nil
	}

	// Fall back to SCAN if tasks:all is empty (e.g. tasks created before this key existed)
	if len(taskIDs) == 0 {
		var cursor uint64
		prefix := teamKey("task:")
		for {
			batch, nextCursor, scanErr := rdb.Scan(ctx, cursor, prefix+"*", 100).Result()
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

	if len(taskIDs) == 0 {
		return mcp.NewToolResultText("No tasks"), nil
	}

	var lines []string
	for _, tid := range taskIDs {
		t, err := rdb.HGetAll(ctx, teamKey("task:"+tid)).Result()
		if err != nil || len(t) == 0 {
			continue
		}
		line := fmt.Sprintf("  %s: [%s]", tid, t["status"])
		if t["assignee"] != "" {
			line += fmt.Sprintf(" assigned=%s", t["assignee"])
		}
		prompt := t["prompt"]
		if len(prompt) > 60 {
			prompt = prompt[:60] + "..."
		}
		line += " " + prompt
		if t["status"] == "completed" && t["result_summary"] != "" {
			result := t["result_summary"]
			if len(result) > 60 {
				result = result[:60] + "..."
			}
			line += "\n         result: " + result
		}
		lines = append(lines, line)
	}

	if len(lines) == 0 {
		return mcp.NewToolResultText("No tasks"), nil
	}
	return mcp.NewToolResultText(joinLines(lines)), nil
}

func getTaskTool() mcp.Tool {
	return mcp.NewTool("get_task",
		mcp.WithDescription("Get full details of a task including result."),
		mcp.WithString("task_id", mcp.Required(), mcp.Description("Task ID to look up")),
	)
}

func handleGetTask(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	taskID, err := req.RequireString("task_id")
	if err != nil {
		return mcp.NewToolResultText("Error: task_id is required"), nil
	}
	t, err := rdb.HGetAll(ctx, teamKey("task:"+taskID)).Result()
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Error reading task from Redis: %v", err)), nil
	}
	if len(t) == 0 {
		return mcp.NewToolResultText(fmt.Sprintf("Task %s not found", taskID)), nil
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Error formatting task: %v", err)), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}

func listAgentsTool() mcp.Tool {
	return mcp.NewTool("list_agents",
		mcp.WithDescription("Show all running K8s agents with status and current work."),
	)
}

func handleListAgents(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	heartbeats, err := rdb.HGetAll(ctx, teamKey("heartbeat")).Result()
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Error reading heartbeat from Redis: %v", err)), nil
	}
	if len(heartbeats) == 0 {
		return mcp.NewToolResultText("No agents running"), nil
	}

	var lines []string
	now := float64(time.Now().Unix())
	for agentID, lastSeen := range heartbeats {
		meta, err := rdb.HGetAll(ctx, teamKey("agent:"+agentID)).Result()
		if err != nil {
			return mcp.NewToolResultText(fmt.Sprintf("Error reading agent metadata for %s: %v", agentID, err)), nil
		}
		ts, _ := strconv.ParseFloat(lastSeen, 64)
		elapsed := now - ts

		status := "active"
		if elapsed > 60 {
			status = "dead"
		} else if elapsed > 30 {
			status = "idle"
		}

		provider := meta["provider"]
		role := meta["role"]
		if provider == "" {
			provider = "?"
		}
		if role == "" {
			role = "?"
		}
		lines = append(lines, fmt.Sprintf("  %s: [%s] %s/%s", agentID, status, provider, role))
	}
	return mcp.NewToolResultText(joinLines(lines)), nil
}

func sendMessageTool() mcp.Tool {
	return mcp.NewTool("send_message",
		mcp.WithDescription("Send a message to a K8s agent. The agent reads it from "+
			"Redis on its next loop iteration (within seconds)."),
		mcp.WithString("to", mcp.Required(), mcp.Description("Agent ID to message")),
		mcp.WithString("text", mcp.Required(), mcp.Description("Message content")),
	)
}

func handleSendMessage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	to, err := req.RequireString("to")
	if err != nil {
		return mcp.NewToolResultText("Error: to is required"), nil
	}
	text, err := req.RequireString("text")
	if err != nil {
		return mcp.NewToolResultText("Error: text is required"), nil
	}

	if err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: teamKey("inbox:" + to),
		MaxLen: 1000,
		Approx: true,
		Values: map[string]any{
			"from":      "lead",
			"text":      text,
			"timestamp": fmt.Sprintf("%d", time.Now().Unix()),
		},
	}).Err(); err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Error sending message to Redis: %v", err)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Message sent to %s", to)), nil
}

func scaleDownTool() mcp.Tool {
	return mcp.NewTool("scale_down",
		mcp.WithDescription("Scale a K8s agent deployment to 0 replicas. "+
			"Call when all tasks for this agent type are complete to stop costs."),
		mcp.WithString("provider", mcp.Required(), mcp.Description("claude, codex, or gemini")),
		mcp.WithString("role", mcp.Required(), mcp.Description("coder, researcher, or reviewer")),
	)
}

func handleScaleDown(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	provider, err := req.RequireString("provider")
	if err != nil {
		return mcp.NewToolResultText("Error: provider is required"), nil
	}
	role, err := req.RequireString("role")
	if err != nil {
		return mcp.NewToolResultText("Error: role is required"), nil
	}
	deployName := fmt.Sprintf("agent-%s-%s", provider, role)

	deploy, err := k8s.AppsV1().Deployments(namespace).Get(ctx, deployName, metav1.GetOptions{})
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Error: Deployment %s not found: %v", deployName, err)), nil
	}

	zero := int32(0)
	deploy.Spec.Replicas = &zero
	if _, err := k8s.AppsV1().Deployments(namespace).Update(ctx, deploy, metav1.UpdateOptions{}); err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Error scaling down %s: %v", deployName, err)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Scaled %s to 0", deployName)), nil
}

func getCostsTool() mcp.Tool {
	return mcp.NewTool("get_costs",
		mcp.WithDescription("Show accumulated costs across all K8s agents."),
	)
}

func handleGetCosts(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Use SCAN to avoid blocking Redis with KEYS
	var costKeys []string
	var cursor uint64
	prefix := teamKey("cost:")
	for {
		batch, nextCursor, err := rdb.Scan(ctx, cursor, prefix+"*", 100).Result()
		if err != nil {
			return mcp.NewToolResultText(fmt.Sprintf("Error scanning cost keys: %v", err)), nil
		}
		costKeys = append(costKeys, batch...)
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	var total float64
	var lines []string

	for _, key := range costKeys {
		c, err := rdb.HGetAll(ctx, key).Result()
		if err != nil {
			return mcp.NewToolResultText(fmt.Sprintf("Error reading cost hash %s: %v", key, err)), nil
		}
		agentID := key[len(prefix):]
		tokensIn, _ := strconv.ParseInt(c["tokens_in"], 10, 64)
		tokensOut, _ := strconv.ParseInt(c["tokens_out"], 10, 64)
		// Claude Sonnet pricing: $0.015/1K input tokens, $0.075/1K output tokens
		cost := float64(tokensIn)*0.015/1000 + float64(tokensOut)*0.075/1000
		total += cost
		lines = append(lines, fmt.Sprintf("  %s: $%.2f (%d in, %d out)", agentID, cost, tokensIn, tokensOut))
	}

	if len(lines) == 0 {
		lines = append(lines, "  No cost data recorded yet")
	}
	lines = append(lines, fmt.Sprintf("  TOTAL: $%.2f", total))
	if total > maxCost {
		lines = append(lines, fmt.Sprintf("  WARNING: exceeds limit of $%.2f", maxCost))
	}
	return mcp.NewToolResultText(joinLines(lines)), nil
}

func getTaskResultTool() mcp.Tool {
	return mcp.NewTool("get_task_result",
		mcp.WithDescription("Get the full result text of a completed task. "+
			"get_task returns only a 500-char summary; use this to read the complete output."),
		mcp.WithString("task_id", mcp.Required(), mcp.Description("Task ID to fetch full result for")),
	)
}

func handleGetTaskResult(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	taskID, err := req.RequireString("task_id")
	if err != nil {
		return mcp.NewToolResultText("Error: task_id is required"), nil
	}
	val, err := rdb.Get(ctx, teamKey("task:"+taskID+":result_full")).Result()
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("No full result stored for task %s", taskID)), nil
	}
	return mcp.NewToolResultText(val), nil
}

// --- Helpers ---

// teamKey scopes a key to the current team using the global teamID.
// Format: team:{teamID}:{suffix}
func teamKey(suffix string) string { return fmt.Sprintf("team:%s:%s", teamID, suffix) }

// envOr returns the environment variable value or the default if not set.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// joinLines joins a slice of strings with newlines.
func joinLines(lines []string) string {
	return strings.Join(lines, "\n")
}

// splitComma splits a comma-separated string. Callers trim individual elements.
func splitComma(s string) []string {
	return strings.Split(s, ",")
}

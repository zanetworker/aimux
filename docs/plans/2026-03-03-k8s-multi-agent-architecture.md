# Kubernetes Multi-Agent Architecture with Redis Streams

**Date**: 2026-03-05
**Status**: Design (post-review)
**Author**: azaalouk + Claude
**Reviewed by**: Claude code-reviewer agent, OpenAI Codex CLI

## 1. Problem statement

aimux observes AI agents running on a single machine. Claude Code coordinates agents via JSON files on disk (`~/.claude/teams/{name}/inboxes/{agent}.json`). This doesn't scale beyond one machine.

**Goal**: Run 5-20 AI coding agents as Kubernetes pods, coordinating across nodes. Support multiple LLM providers (Claude, Codex, Gemini) in the same team. Extend aimux as the control plane for both local and remote agents.

## 2. Two modes: local and remote

The system has two independent modes that coexist. They don't interact.

```
LOCAL (unchanged, works today)                REMOTE (new, K8s)
─────────────────────────────                 ──────────────────
Claude Code CLI                               Claude Code CLI + MCP server
Built-in tools: Agent, TaskCreate,            MCP tools: spawn_agent, create_task,
  TaskList, TaskUpdate, SendMessage             list_tasks, send_message, scale_down
Spawns via: tmux / in-process                 Spawns via: K8s API (scale Deployment)
Coordinates via: filesystem                   Coordinates via: Redis
  ~/.claude/teams/  ~/.claude/tasks/            team:{id}:inbox:*  team:{id}:task:*
aimux discovers via: process scanning         aimux discovers via: Redis + K8s API
```

**The user decides which mode** by how they talk to Claude:

- "Create a team to review this PR" → Claude uses built-in Agent tool → local
- "Spawn K8s agents to refactor the API" → Claude uses MCP tools → remote

Claude doesn't need special logic to choose. If you mention K8s/cluster/remote, it reaches for the MCP tools. If you just say "team", it uses the built-ins. Both can run simultaneously. aimux shows both in the same table with a LOCATION column (`local` vs `k8s/agents`).

### 2.1 How Claude Code teams work locally (reference)

Claude Code has 6 built-in tools for teams, hardcoded in the CLI binary:

| Built-in tool | What it does | Storage |
|---------------|-------------|---------|
| `Agent` | Forks a new Claude Code process | tmux pane or in-process |
| `TaskCreate` | Creates a task file | `~/.claude/tasks/{team}/task-{id}.json` |
| `TaskList` | Lists all task files | reads `~/.claude/tasks/{team}/*.json` |
| `TaskGet` | Reads one task file | reads `~/.claude/tasks/{team}/task-{id}.json` |
| `TaskUpdate` | Modifies a task file (claim, complete) | writes `~/.claude/tasks/{team}/task-{id}.json` |
| `SendMessage` | Appends to recipient's inbox file | `~/.claude/teams/{team}/inboxes/{name}.json` |

Flow: Lead calls `Agent` → Claude Code forks process → Lead calls `TaskCreate` → Teammate calls `TaskList` → Teammate calls `TaskUpdate(owner=me)` → Teammate works → Teammate calls `TaskUpdate(status=completed)` → Teammate calls `SendMessage(to=lead)`.

These tools are **not configurable**. They always use the local filesystem. You can't redirect them to Redis.

### 2.2 How remote mode works (MCP server)

For K8s, an MCP server provides equivalent tools with different names:

| Built-in (local) | MCP replacement (remote) | Backend |
|-------------------|------------------------|---------|
| `Agent` (spawn) | `spawn_agent` | K8s API (scale Deployment) |
| `TaskCreate` | `create_task` | Redis hash + sorted set |
| `TaskList` | `list_tasks` | Redis ZRANGE |
| `TaskGet` | `get_task` | Redis HGETALL |
| `TaskUpdate` | `claim_task`, `complete_task` | Redis Lua script |
| `SendMessage` | `send_message` | Redis XADD |
| (none) | `list_agents` | Redis heartbeat hash |
| (none) | `scale_down` | K8s API (set replicas=0) |
| (none) | `get_costs` | Redis cost hashes |
| (none) | `cleanup_branches` | GitHub API (delete task branches) |

Claude discovers MCP tools automatically. Their descriptions tell Claude when to use them ("Spawn AI agents on Kubernetes for parallel work").

**Preventing accidental local spawning when K8s is intended:** A Claude Code hook blocks the built-in Agent tool when it includes a `team_name` parameter. Quick subagents (Explore, Plan) still work because they don't use `team_name`:

```json
{
  "hooks": {
    "PreToolUse": [{
      "matcher": "Agent",
      "hooks": [{
        "type": "command",
        "command": "python3 -c \"import sys,json; p=json.loads(sys.stdin.read()); sys.exit(1) if p.get('input',{}).get('team_name') else sys.exit(0)\""
      }]
    }]
  }
}
```

### 2.3 Smart lead, dumb workers

A critical design split: the lead (local Claude Code) is **LLM-driven**. The workers (K8s pods) are **code-driven**.

```
Lead (your local Claude Code)              Workers (K8s pods)
┌───────────────────────────┐              ┌──────────────────────────┐
│ Claude (LLM) decides:     │              │ Python loop (code):      │
│  - what tasks to create   │   K8s API    │  - claims next task      │
│  - how many agents to     │ ──────────►  │  - passes prompt to      │
│    spawn and what roles   │              │    Claude API (Agent SDK)│
│  - when to scale down     │   Redis      │  - Claude does code work │
│  - how to synthesize      │ ◄──────────  │  - reports result        │
│    results                │              │  - loops                 │
│                           │              │                          │
│ Uses MCP tools:           │              │ Claude inside worker     │
│  spawn_agent, create_task,│              │ ONLY has code tools:     │
│  list_tasks, send_message,│              │  Read, Edit, Write,     │
│  scale_down, get_costs    │              │  Bash, Grep, Glob       │
└───────────────────────────┘              └──────────────────────────┘
     LLM-driven coordination                 Code-driven execution
```

Workers don't call `TaskList` or `SendMessage` through the LLM. The Python main loop handles coordination directly via Redis. The worker's Claude instance only gets a prompt and code tools. This is:
- **Cheaper** - no tokens spent on coordination reasoning
- **More reliable** - the loop is deterministic, Claude can't skip tasks
- **Simpler** - worker Claude doesn't need to understand the team system

### 2.4 MCP server implementation (Go)

The MCP server is Go for three reasons:
- Shares K8s client code with aimux (`client-go` is the best K8s client in any language)
- Shares Redis key patterns with aimux's K8s provider (`go-redis/v9`)
- Single binary, no runtime dependencies (no Python/pip on developer laptops)

Uses `mcp-go` (community Go MCP SDK, `github.com/mark3labs/mcp-go`).

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	rdb         *redis.Client
	k8s         *kubernetes.Clientset
	namespace   string
	teamID      string
	maxAgents   int
	maxCost     float64
	githubToken string   // for cleanup_branches
	githubRepo  string   // "owner/repo"
)

func main() {
	// Init Redis
	opt, _ := redis.ParseURL(os.Getenv("REDIS_URL"))
	rdb = redis.NewClient(opt)

	// Init K8s
	config, _ := clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
	k8s, _ = kubernetes.NewForConfig(config)

	namespace = envOr("K8S_NAMESPACE", "agents")
	teamID = envOr("TEAM_ID", "default")
	maxAgents, _ = strconv.Atoi(envOr("MAX_AGENTS", "20"))
	maxCost, _ = strconv.ParseFloat(envOr("MAX_COST_USD", "100"), 64)
	githubToken = os.Getenv("GITHUB_TOKEN")  // personal access token or fine-grained token
	githubRepo = os.Getenv("GITHUB_REPO")    // "owner/repo"

	s := server.NewMCPServer("k8s-agents", "1.0.0")
	s.AddTool(spawnAgentTool(), handleSpawnAgent)
	s.AddTool(createTaskTool(), handleCreateTask)
	s.AddTool(listTasksTool(), handleListTasks)
	s.AddTool(getTaskTool(), handleGetTask)
	s.AddTool(listAgentsTool(), handleListAgents)
	s.AddTool(sendMessageTool(), handleSendMessage)
	s.AddTool(scaleDownTool(), handleScaleDown)
	s.AddTool(getCostsTool(), handleGetCosts)
	s.AddTool(cleanupBranchesTool(), handleCleanupBranches)

	_ = server.ServeStdio(s)
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
	provider := req.Params.Arguments["provider"].(string)
	role := req.Params.Arguments["role"].(string)
	count := 1
	if c, ok := req.Params.Arguments["count"].(float64); ok {
		count = int(c)
	}

	// Enforce agent limit
	heartbeats, _ := rdb.HGetAll(ctx, teamKey("heartbeat")).Result()
	if len(heartbeats)+count > maxAgents {
		return mcp.NewToolResultText(fmt.Sprintf(
			"Error: %d agents running, adding %d exceeds limit of %d.",
			len(heartbeats), count, maxAgents)), nil
	}

	deployName := fmt.Sprintf("agent-%s-%s", provider, role)
	deploy, err := k8s.AppsV1().Deployments(namespace).Get(ctx, deployName, metav1.GetOptions{})
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf(
			"Error: Deployment %s not found in namespace %s.", deployName, namespace)), nil
	}

	newReplicas := int32(0)
	if deploy.Spec.Replicas != nil {
		newReplicas = *deploy.Spec.Replicas
	}
	targetReplicas := newReplicas + int32(count)
	deploy.Spec.Replicas = &targetReplicas

	_, err = k8s.AppsV1().Deployments(namespace).Update(ctx, deploy, metav1.UpdateOptions{})
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Error scaling: %v", err)), nil
	}

	// Wait for agents to register in Redis (pods take 30-60s to start)
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)
		current, _ := rdb.HGetAll(ctx, teamKey("heartbeat")).Result()
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
			"Use depends_on to chain tasks. "+
			"Use source_branch when this task should start from a prior task's git output."),
		mcp.WithString("prompt", mcp.Required(), mcp.Description("Task instructions for the agent")),
		mcp.WithString("required_role", mcp.Description("Only agents with this role can claim it")),
		mcp.WithString("depends_on", mcp.Description("Comma-separated task IDs that must complete first")),
		mcp.WithString("source_branch", mcp.Description("Git branch to pull before starting (e.g. 'task-a3f2bc'). Use when this task depends on file output from a prior task.")),
	)
}

func handleCreateTask(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	prompt := req.Params.Arguments["prompt"].(string)
	role, _ := req.Params.Arguments["required_role"].(string)
	depsStr, _ := req.Params.Arguments["depends_on"].(string)
	sourceBranch, _ := req.Params.Arguments["source_branch"].(string)

	taskID := uuid.New().String()[:8]
	deps := "[]"
	if depsStr != "" {
		parts := splitComma(depsStr)
		b, _ := json.Marshal(parts)
		deps = string(b)
	}

	rdb.HSet(ctx, teamKey("task:"+taskID), map[string]any{
		"status":         "pending",
		"prompt":         prompt,
		"required_role":  role,
		"assignee":       "",
		"depends_on":     deps,
		"source_branch":  sourceBranch,
		"result_summary": "",
		"result_ref":     "",
		"error":          "",
		"retry_count":    "0",
		"created_at":     fmt.Sprintf("%d", time.Now().Unix()),
	})
	rdb.ZAdd(ctx, teamKey("tasks:pending"), redis.Z{
		Score:  float64(time.Now().Unix()),
		Member: taskID,
	})

	label := "any"
	if role != "" {
		label = role
	}
	msg := fmt.Sprintf("Task %s created (role=%s)", taskID, label)
	if sourceBranch != "" {
		msg += fmt.Sprintf(", source_branch=%s", sourceBranch)
	}
	return mcp.NewToolResultText(msg), nil
}

func listTasksTool() mcp.Tool {
	return mcp.NewTool("list_tasks",
		mcp.WithDescription("Show all tasks and their status."),
	)
}

func handleListTasks(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Use SCAN not KEYS — KEYS blocks Redis while scanning all keys
	var keys []string
	var cursor uint64
	prefix := teamKey("task:")
	for {
		batch, nextCursor, err := rdb.Scan(ctx, cursor, prefix+"*", 100).Result()
		if err != nil {
			break
		}
		keys = append(keys, batch...)
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	if len(keys) == 0 {
		return mcp.NewToolResultText("No tasks"), nil
	}

	var lines []string
	for _, key := range keys {
		t, _ := rdb.HGetAll(ctx, key).Result()
		tid := key[len(prefix):]
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
	return mcp.NewToolResultText(joinLines(lines)), nil
}

func getTaskTool() mcp.Tool {
	return mcp.NewTool("get_task",
		mcp.WithDescription("Get full details of a task including result."),
		mcp.WithString("task_id", mcp.Required(), mcp.Description("Task ID to look up")),
	)
}

func handleGetTask(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	taskID := req.Params.Arguments["task_id"].(string)
	t, _ := rdb.HGetAll(ctx, teamKey("task:"+taskID)).Result()
	if len(t) == 0 {
		return mcp.NewToolResultText(fmt.Sprintf("Task %s not found", taskID)), nil
	}
	b, _ := json.MarshalIndent(t, "", "  ")
	return mcp.NewToolResultText(string(b)), nil
}

func listAgentsTool() mcp.Tool {
	return mcp.NewTool("list_agents",
		mcp.WithDescription("Show all running K8s agents with status and current work."),
	)
}

func handleListAgents(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	heartbeats, _ := rdb.HGetAll(ctx, teamKey("heartbeat")).Result()
	if len(heartbeats) == 0 {
		return mcp.NewToolResultText("No agents running"), nil
	}

	var lines []string
	now := float64(time.Now().Unix())
	for agentID, lastSeen := range heartbeats {
		meta, _ := rdb.HGetAll(ctx, teamKey("agent:"+agentID)).Result()
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
	to := req.Params.Arguments["to"].(string)
	text := req.Params.Arguments["text"].(string)

	rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: teamKey("inbox:" + to),
		MaxLen: 1000,
		Approx: true,
		Values: map[string]any{
			"from":      "lead",
			"text":      text,
			"timestamp": fmt.Sprintf("%d", time.Now().Unix()),
		},
	})
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
	provider := req.Params.Arguments["provider"].(string)
	role := req.Params.Arguments["role"].(string)
	deployName := fmt.Sprintf("agent-%s-%s", provider, role)

	deploy, err := k8s.AppsV1().Deployments(namespace).Get(ctx, deployName, metav1.GetOptions{})
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Error: %v", err)), nil
	}

	zero := int32(0)
	deploy.Spec.Replicas = &zero
	k8s.AppsV1().Deployments(namespace).Update(ctx, deploy, metav1.UpdateOptions{})
	return mcp.NewToolResultText(fmt.Sprintf("Scaled %s to 0", deployName)), nil
}

func getCostsTool() mcp.Tool {
	return mcp.NewTool("get_costs",
		mcp.WithDescription("Show accumulated costs across all K8s agents."),
	)
}

func handleGetCosts(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	keys, _ := rdb.Keys(ctx, teamKey("cost:*")).Result()
	var total float64
	var lines []string

	for _, key := range keys {
		c, _ := rdb.HGetAll(ctx, key).Result()
		agentID := key[len(teamKey("cost:")):]
		tokensIn, _ := strconv.ParseInt(c["tokens_in"], 10, 64)
		tokensOut, _ := strconv.ParseInt(c["tokens_out"], 10, 64)
		cost := float64(tokensIn)*0.015/1000 + float64(tokensOut)*0.075/1000
		total += cost
		lines = append(lines, fmt.Sprintf("  %s: $%.2f (%d in, %d out)", agentID, cost, tokensIn, tokensOut))
	}
	lines = append(lines, fmt.Sprintf("  TOTAL: $%.2f", total))
	if total > maxCost {
		lines = append(lines, fmt.Sprintf("  WARNING: exceeds limit of $%.2f", maxCost))
	}
	return mcp.NewToolResultText(joinLines(lines)), nil
}

func cleanupBranchesTool() mcp.Tool {
	return mcp.NewTool("cleanup_branches",
		mcp.WithDescription("Delete task branches from GitHub after work is complete. "+
			"Call after scale_down when you no longer need the agents' file output. "+
			"Only deletes branches named task-{id}. Never touches main or feature branches."),
		mcp.WithString("task_ids", mcp.Required(),
			mcp.Description("Comma-separated task IDs whose branches should be deleted (e.g. 'a3f2bc,b7d1ef')")),
	)
}

func handleCleanupBranches(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if githubToken == "" || githubRepo == "" {
		return mcp.NewToolResultText("Error: GITHUB_TOKEN and GITHUB_REPO must be set for branch cleanup"), nil
	}

	ids := splitComma(req.Params.Arguments["task_ids"].(string))
	var deleted, skipped []string

	for _, id := range ids {
		id = strings.TrimSpace(id)
		branch := "task-" + id

		// Check if branch exists before trying to delete
		checkURL := fmt.Sprintf("https://api.github.com/repos/%s/git/ref/heads/%s", githubRepo, branch)
		checkReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, checkURL, nil)
		checkReq.Header.Set("Authorization", "Bearer "+githubToken)
		checkReq.Header.Set("Accept", "application/vnd.github+json")
		resp, err := http.DefaultClient.Do(checkReq)
		if err != nil || resp.StatusCode == 404 {
			skipped = append(skipped, branch+" (not found)")
			continue
		}
		resp.Body.Close()

		// Delete the branch
		delURL := fmt.Sprintf("https://api.github.com/repos/%s/git/refs/heads/%s", githubRepo, branch)
		delReq, _ := http.NewRequestWithContext(ctx, http.MethodDelete, delURL, nil)
		delReq.Header.Set("Authorization", "Bearer "+githubToken)
		delReq.Header.Set("Accept", "application/vnd.github+json")
		delResp, err := http.DefaultClient.Do(delReq)
		if err != nil || delResp.StatusCode != 204 {
			skipped = append(skipped, branch+" (delete failed)")
			continue
		}
		delResp.Body.Close()
		deleted = append(deleted, branch)
	}

	return mcp.NewToolResultText(fmt.Sprintf(
		"Deleted: %s\nSkipped: %s",
		strings.Join(deleted, ", "),
		strings.Join(skipped, ", "),
	)), nil
}

// --- Helpers ---

func teamKey(suffix string) string { return fmt.Sprintf("team:%s:%s", teamID, suffix) }
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
func joinLines(lines []string) string {
	result := ""
	for i, l := range lines {
		if i > 0 {
			result += "\n"
		}
		result += l
	}
	return result
}
func splitComma(s string) []string {
	return strings.Split(s, ",")
}
```

**Dependencies:**

```
github.com/mark3labs/mcp-go       # Go MCP SDK (stdio transport)
github.com/redis/go-redis/v9      # Redis client
k8s.io/client-go                  # K8s API client
github.com/google/uuid            # Task ID generation
```

**Why Go over Python for this server:**
- Shares `client-go` and `go-redis` with aimux's K8s provider (same packages, same patterns)
- Single binary (~15MB), no Python runtime or pip dependencies on developer laptops
- Can eventually be compiled into the aimux binary itself as an embedded MCP server
- The `mcp-go` SDK handles stdio transport (what Claude Code expects)

Register in `~/.claude/settings.json`:

```json
{
  "mcpServers": {
    "k8s-agents": {
      "command": "/usr/local/bin/k8s-agents-mcp",
      "env": {
        "REDIS_URL": "redis://:password@redis.agents.svc:6379",
        "KUBECONFIG": "/path/to/kubeconfig",
        "K8S_NAMESPACE": "agents",
        "TEAM_ID": "my-team",
        "MAX_AGENTS": "20",
        "MAX_COST_USD": "100",
        "GITHUB_TOKEN": "ghp_...",
        "GITHUB_REPO": "owner/repo"
      }
    }
  }
}
```

Note: single binary, no `args` needed. Build with `go build -o k8s-agents-mcp ./cmd/mcp/`.

## 3. Architecture overview

```
┌─ Developer Laptop ───────────────────────────────────────────────────┐
│                                                                      │
│  Claude Code CLI (the lead)                                          │
│  ├── Built-in tools: Read, Edit, Bash, Grep, Glob, ...              │
│  ├── Built-in team tools: Agent, TaskCreate, TaskList, SendMessage   │
│  │   └── LOCAL mode: tmux spawn, filesystem coordination            │
│  ├── MCP tools (k8s-agents server):                                  │
│  │   ├── spawn_agent, scale_down     → K8s API                      │
│  │   ├── create_task, list_tasks     → Redis                        │
│  │   └── send_message, get_costs     → Redis                        │
│  │   └── REMOTE mode: K8s spawn, Redis coordination                 │
│  └── Hook: blocks built-in Agent(team_name=...) when K8s intended   │
│                                                                      │
│  aimux TUI (observability + override)                                │
│  ├── claude/codex/gemini providers → local process scan              │
│  └── k8s provider                  → Redis + K8s API                 │
│                                                                      │
└──────────────────────┬───────────────────────────────────────────────┘
                       │ kubeconfig + Redis client
                       ▼
┌─ Kubernetes Cluster ─────────────────────────────────────────────────┐
│                                                                      │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐               │
│  │ Claude Coders│  │ Gemini       │  │ Codex Coders │  Deployments  │
│  │ (replicas:   │  │ Researchers  │  │ (replicas:   │  start at 0,  │
│  │  0 → N → 0) │  │ (replicas:   │  │  0 → N → 0) │  scale on     │
│  │              │  │  0 → N → 0) │  │              │  demand       │
│  │ Python loop  │  │ Python loop  │  │ Python loop  │               │
│  │ + Agent SDK  │  │ + GenAI SDK  │  │ + OpenAI SDK │               │
│  │ + Coord Lib  │  │ + Coord Lib  │  │ + Coord Lib  │               │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘               │
│         │                 │                  │                        │
│  ┌──────▼─────────────────▼──────────────────▼───────┐               │
│  │              Redis (requirepass + NetworkPolicy)   │               │
│  │  Streams: inbox:{agent}, events                   │               │
│  │  Hashes:  task:{id}, agent:{id}, heartbeat, cost  │               │
│  │  Sets:    tasks:pending                           │               │
│  └───────────────────────────────────────────────────┘               │
│                                                                      │
│  ┌───────────────────────────────────────────────────┐               │
│  │  OTEL Collector (traces from all agents)          │               │
│  └───────────────────────────────────────────────────┘               │
└──────────────────────────────────────────────────────────────────────┘
```

Key points:
- **Lead is your local Claude Code session**, not a K8s pod. It uses MCP tools to manage remote agents.
- **Deployments start at 0 replicas.** Claude scales them up when needed, back to 0 when done. No idle pods.
- **Workers are code-driven.** Python main loop claims tasks from Redis, passes prompts to the LLM. The LLM only gets code tools (Read, Edit, Bash). No coordination tools.
- **Two modes coexist.** Local teams use built-in tools + filesystem. Remote teams use MCP tools + Redis. No conflict.

## 4. Agent model

### 4.1 Two independent dimensions: provider × role

| | coder | researcher | reviewer | lead |
|--|-------|-----------|----------|------|
| **claude** | opus for architecture | haiku for cheap search | sonnet for review | sonnet for coordination |
| **codex** | o3 for code gen | o4-mini for research | o3 for review | - |
| **gemini** | gemini-pro for coding | flash for cheap tasks | gemini-pro for review | - |

Provider determines which LLM API is called. Role determines tool access and resource limits.

### 4.2 Role presets

Role is **configuration, not a separate image**. All agents of the same provider run the same container image. The `ROLE` and `ALLOWED_TOOLS` env vars configure behaviour at runtime. This is the K8s equivalent of `subagent_type` in local mode (`Explore` = read-only, `general-purpose` = full tools).

| Role | `ALLOWED_TOOLS` | Resources | Local equiv |
|------|-----------------|-----------|-------------|
| `coder` | Read, Edit, Write, Bash, Grep, Glob | 2Gi RAM, 1 CPU, 5GB disk | `general-purpose` |
| `researcher` | Read, Grep, Glob, WebSearch, WebFetch | 512Mi RAM, 500m CPU, 2GB disk | `general-purpose` |
| `reviewer` | Read, Grep, Glob | 1Gi RAM, 500m CPU, 2GB disk | `Explore` |
| `lead` | All + task management | 1Gi RAM, 500m CPU, 2GB disk | (local Claude session) |

### 4.3 Monorepo structure

Two languages: Go for the MCP server (shares code with aimux), Python for agent workers (Agent SDK forces this). The Python coordinator library is ~100 lines of Redis calls. **One image per provider** — role is env var configuration, not a separate image.

```
k8s-agents/
├── cmd/
│   └── mcp/
│       └── main.go                # MCP server entry point (Go)
├── pkg/
│   └── rediskeys/
│       └── keys.go                # Shared Redis key patterns (Go)
│       └── keys_test.go           # Used by MCP server + aimux K8s provider
├── coordinator/                   # Shared Python package (for agent workers)
│   ├── __init__.py
│   ├── coordinator.py             # AgentCoordinator class
│   ├── lua/
│   │   └── claim_task.lua
│   └── tests/
│       └── test_coordinator.py
├── agents/
│   ├── claude/
│   │   ├── Dockerfile             # Python: COPY coordinator/ + pip install claude-code-sdk
│   │   └── main.py                # Role behaviour driven by ROLE + ALLOWED_TOOLS env vars
│   ├── codex/
│   │   ├── Dockerfile             # Python: COPY coordinator/ + pip install openai-agents
│   │   └── main.py
│   └── gemini/
│       ├── Dockerfile             # Python: COPY coordinator/ + pip install google-genai
│       └── main.py
├── manifests/
│   ├── redis.yaml
│   ├── rbac.yaml                  # ServiceAccount + Role + RoleBinding for MCP server
│   ├── agent-claude-coder.yaml    # Deployment: image=agent-claude, ROLE=coder
│   ├── agent-claude-reviewer.yaml # Deployment: image=agent-claude, ROLE=reviewer
│   ├── agent-gemini-researcher.yaml
│   └── networkpolicy.yaml
├── go.mod                         # Go module (MCP server)
├── go.sum
└── Makefile                       # build-mcp, build-claude, build-codex, push-all
```

The Go `pkg/rediskeys` package defines key patterns once:
```go
package rediskeys

func TeamKey(teamID, suffix string) string {
    return fmt.Sprintf("team:%s:%s", teamID, suffix)
}
func Inbox(teamID, agentID string) string { return TeamKey(teamID, "inbox:"+agentID) }
func Heartbeat(teamID string) string      { return TeamKey(teamID, "heartbeat") }
func TasksPending(teamID string) string    { return TeamKey(teamID, "tasks:pending") }
// ...
```

This package is imported by both the MCP server (`cmd/mcp/`) and aimux's K8s provider. Single source of truth for key naming.

Agent worker Dockerfile (one per provider, role is env var):

```dockerfile
FROM python:3.12-slim
WORKDIR /app
COPY coordinator/ /app/coordinator/
COPY agents/claude/ /app/agent/
RUN pip install -e /app/coordinator/ && pip install claude-code-sdk redis
CMD ["python", "/app/agent/main.py"]
# Role behaviour controlled entirely by env vars:
#   ROLE=coder       ALLOWED_TOOLS=Read,Edit,Write,Bash,Grep,Glob
#   ROLE=reviewer    ALLOWED_TOOLS=Read,Grep,Glob
#   ROLE=researcher  ALLOWED_TOOLS=Read,Grep,Glob,WebSearch,WebFetch
```

## 5. Redis data model

| Key | Type | Fields / Purpose |
|-----|------|-----------------|
| `team:{id}:inbox:{agent}` | Stream | `from`, `text`, `summary`, `type`, `timestamp`. Per-agent inbox. MAXLEN ~1000. |
| `team:{id}:events` | Stream | `from`, `text`, `type`, `summary`, `timestamp`. Broadcasts. MAXLEN ~10000. |
| `team:{id}:tasks:pending` | Sorted Set | Score = creation time. Members = task IDs. Single ZRANGE to list work. |
| `team:{id}:task:{task-id}` | Hash | `status`, `prompt`, `required_role`, `assignee`, `depends_on` (JSON), `result_summary` (truncated 500 chars), `result_ref` (path/OTEL span ID for full output), `source_branch` (git branch to pull before starting), `retry_count`, `error`, `created_at`, `completed_at` |
| `team:{id}:agent:{agent-id}` | Hash | `provider`, `role`, `model`, `namespace`, `pod_name`, `registered_at` |
| `team:{id}:heartbeat` | Hash | Field = agent ID, value = unix timestamp. Updated every 10s. |
| `team:{id}:cost:{agent-id}` | Hash | `tokens_in`, `tokens_out`, `model`. Incremented via HINCRBY. |
| `team:{id}:config` | Hash | `name`, `description`, `members` (JSON) |

**Key design rules:**
- Redis is for coordination, not payload. Task results truncated to 500 chars in `result_summary`. Full output stored at `result_ref` (OTEL span ID or workspace path). Dependent agents read `result_ref`, not `result_summary`.
- Pending tasks in a separate sorted set. Listing available work = one ZRANGE call, not O(N) HGETs. Use SCAN not KEYS for listing task hashes.
- Events stream uses MAXLEN ~10000 (larger than inbox) because broadcasts serve all agents.
- `source_branch`: when a task depends on a prior task's file output, set this to the git branch the prior agent committed to. The worker pulls it before starting.

## 6. Communication patterns

### 6.1 Direct message (Agent A → Agent B)

```
XADD team:research:inbox:agent-b * \
    from "agent-a" text "Here are my findings..." summary "Research done" timestamp "..."

# Agent B reads (blocking, push-like). Shared consumer group is fine for inbox (one reader).
XREADGROUP GROUP agents agent-b BLOCK 5000 COUNT 1 STREAMS team:research:inbox:agent-b >
```

Ack happens **after** processing, not immediately on read. If agent crashes before ack, message redelivers on restart.

### 6.2 Broadcast (Agent A → all agents)

Redis consumer groups deliver each message to **one** consumer in the group. For true broadcast, each agent creates its own consumer group on the events stream:

```
# On startup: per-agent consumer group (not shared)
XGROUP CREATE team:research:events agent-{agent-id} $ MKSTREAM

# Each agent reads from its own group (every agent gets every event)
XREADGROUP GROUP agent-{agent-id} {agent-id} BLOCK 5000 COUNT 10 \
    STREAMS team:research:events >
```

### 6.3 Task claiming (atomic Lua script)

Checks status, role match, and dependency completion atomically:

```lua
local task_key = KEYS[1]
local team_prefix = KEYS[2]
local agent_id = ARGV[1]
local agent_role = ARGV[2]
local task_id = ARGV[3]

-- Check status
local status = redis.call('HGET', task_key, 'status')
if status ~= 'pending' then return 0 end

-- Check role (empty = any role)
local required_role = redis.call('HGET', task_key, 'required_role')
if required_role and required_role ~= '' and required_role ~= agent_role then
    return -1
end

-- Check dependencies
local deps_json = redis.call('HGET', task_key, 'depends_on')
if deps_json and deps_json ~= '[]' then
    local deps = cjson.decode(deps_json)
    for _, dep_id in ipairs(deps) do
        local dep_status = redis.call('HGET', team_prefix .. ':task:' .. dep_id, 'status')
        if dep_status ~= 'completed' then return -2 end
    end
end

-- Claim: update status + remove from pending set
redis.call('HSET', task_key, 'status', 'claimed', 'assignee', agent_id)
redis.call('ZREM', team_prefix .. ':tasks:pending', task_id)
return 1
```

Returns: `1` = claimed, `0` = already taken, `-1` = wrong role, `-2` = dependency not met.

### 6.4 Task lifecycle

```
  pending ──── Agent claims (Lua atomic) ──── claimed ──── Agent works ──── in_progress
     ▲                                                                          │
     │                                                                     ┌────▼────┐
     │  retry (count < 3)                                                  │ success? │
     │◄────────── failed ◄───────────────── no ◄───────────────────────────┤          │
     │                                                                     │          │
     │         dead (count >= 3,                                           └────┬────┘
     │          needs human)             completed ◄──────── yes ───────────────┘
     │
     └── heartbeat timeout (lead reassigns) ◄── agent crash during claimed/in_progress
```

Task hash fields for failure handling: `retry_count` (int, default 0), `error` (last error message).

### 6.5 Heartbeat

Runs in a **separate asyncio task**, not in the main loop. Long API calls (60s+) don't block it.

```python
async def heartbeat_loop(coord):
    while True:
        await coord.heartbeat()     # HSET team:{id}:heartbeat {agent} {timestamp}
        await asyncio.sleep(10)

# Started independently from main loop
asyncio.create_task(heartbeat_loop(coord))
```

Lead checks every 60s. Dead agent (no heartbeat in 60s): reassign its tasks to `pending`, increment `retry_count`.

### 6.6 Agent registration and deregistration

```python
# On startup
await coord.r.hset(f"team:{team}:agent:{agent_id}", mapping={
    "provider": "claude", "role": "coder", "model": "opus-4.6",
    "namespace": "agents", "pod_name": agent_id, "registered_at": str(time.time()),
})

# On graceful shutdown (preStop hook or SIGTERM handler)
await coord.r.hdel(f"team:{team}:heartbeat", agent_id)
await coord.r.delete(f"team:{team}:agent:{agent_id}")
await coord.r.delete(f"team:{team}:cost:{agent_id}")
```

Lead reaper runs every 5 minutes: cleans up entries for agents dead > 5 minutes that didn't deregister.

### 6.7 Shutdown protocol

```
Lead → XADD team:{id}:inbox:{agent} * type "shutdown_request" request_id "..." reason "..."
Agent → deregister() + XADD team:{id}:inbox:lead * type "shutdown_response" approve "true"
Agent → exit
```

### 6.8 Git workflow and branch handoff

This section explains the end-to-end flow for tasks that pass file output between agents (e.g. coder → reviewer).

**Prerequisites:**
- All pods have a git init container that clones the repo from GitHub on startup
- A GitHub token with write access is stored in a K8s Secret and passed to pods
- The token is also configured in the MCP server for `cleanup_branches`

**Full flow — concrete example: refactor `/users`, then review**

```
SETUP
─────
GitHub: main branch exists
K8s:    agent-claude-coder (0 replicas), agent-claude-reviewer (0 replicas)

STEP 1 — Lead spawns agents
───────────────────────────
Lead calls spawn_agent(provider=claude, role=coder, count=1)
  → K8s scales agent-claude-coder to 1 replica
  → Pod starts, init container: git clone github.com/org/repo /workspace
  → Pod registers in Redis heartbeat
  → MCP server polls, sees heartbeat → returns "1 agent ready"

Lead calls spawn_agent(provider=claude, role=reviewer, count=1)
  → Same for agent-claude-reviewer pod
  → Both pods now have a fresh clone of main in /workspace

STEP 2 — Lead creates tasks
────────────────────────────
Lead calls create_task(
  prompt="Refactor /users endpoint in routes/users.go. Run tests.",
  required_role=coder
) → task_id="a3f2bc"

Lead calls create_task(
  prompt="Review the refactored /users endpoint. Check correctness and test coverage.",
  required_role=reviewer,
  depends_on=["a3f2bc"],
  source_branch="task-a3f2bc"    ← reviewer will pull this branch before starting
) → task_id="b7d1ef"

Task b7d1ef is blocked: Lua script sees a3f2bc not yet completed.

STEP 3 — Coder works
─────────────────────
Coder pod:
  claims task a3f2bc from Redis (Lua script: status=pending → claimed)
  calls Claude API with prompt, ALLOWED_TOOLS=Read,Edit,Write,Bash,Grep,Glob
    Claude reads  routes/users.go
    Claude edits  routes/users.go
    Claude runs   go test ./...   → PASS
  commits and pushes:
    git checkout -b task-a3f2bc
    git add -A
    git commit -m "task a3f2bc: refactor /users endpoint"
    git push origin task-a3f2bc
  reports to Redis:
    result_summary = "Refactored /users, extracted userService, all 42 tests pass"
    result_ref     = "branch:task-a3f2bc"
    status         = completed

GitHub now has branch: task-a3f2bc

STEP 4 — Reviewer unblocks and works
──────────────────────────────────────
Reviewer pod (polls Redis every second):
  tries to claim b7d1ef
    → Lua script: depends_on=[a3f2bc], checks a3f2bc.status = completed ✓
    → claims b7d1ef
  sees source_branch = "task-a3f2bc"
  pulls the branch:
    git fetch origin task-a3f2bc
    git checkout task-a3f2bc
    (/workspace now has the coder's changes)
  calls Claude API with prompt, ALLOWED_TOOLS=Read,Grep,Glob  (read-only)
    Claude reads routes/users.go
    Claude reads routes/users_test.go
    Claude produces review findings
  reports to Redis:
    result_summary = "LGTM. Suggest: userService should be an interface for testability."
    status         = completed
  (reviewer does not push — read-only role)

STEP 5 — Lead synthesizes and cleans up
─────────────────────────────────────────
Lead calls list_tasks()       → both completed
Lead calls get_task("b7d1ef") → reads reviewer's full findings
Lead presents results to you

Lead calls scale_down(provider=claude, role=coder)
Lead calls scale_down(provider=claude, role=reviewer)
  → both Deployments back to 0, pods deleted

Lead calls cleanup_branches(task_ids="a3f2bc,b7d1ef")
  → MCP server: DELETE github.com/org/repo/git/refs/heads/task-a3f2bc
  → b7d1ef has no branch (reviewer was read-only), skipped

GitHub after: only main remains
```

**What gets merged to main?**

Nothing automatically. The task branch (`task-a3f2bc`) is deleted by `cleanup_branches` after the lead has read the results. If you want the changes on main, you tell the lead: "merge the coder's changes" and it can create a PR or merge via the GitHub API before cleanup.

**Constraint: non-overlapping files per task**

Two coder agents writing to the same file will produce merge conflicts when pushing. The lead must scope tasks to non-overlapping files. This is the same constraint as local worktree isolation in the dev-team skill.

## 7. Kubernetes manifests

### 7.1 Secrets

```yaml
# Redis password
apiVersion: v1
kind: Secret
metadata:
  name: redis-secret
  namespace: agents
type: Opaque
data:
  password: <base64-encoded-password>

---
# GitHub token for git clone (init container) and branch cleanup (MCP server)
# token: GitHub PAT with repo scope (or fine-grained token scoped to one repo, contents: write)
# host:  github.com/azaalouk/myrepo.git  (no https://, token is prepended at runtime)
# Create with: kubectl create secret generic repo-secret \
#   --from-literal=token=ghp_... \
#   --from-literal=host=github.com/azaalouk/myrepo.git \
#   -n agents
apiVersion: v1
kind: Secret
metadata:
  name: repo-secret
  namespace: agents
type: Opaque
data:
  token: <base64-encoded-github-token>
  host: <base64-encoded-host>   # e.g. base64("github.com/azaalouk/myrepo.git")

---
# LLM API keys
apiVersion: v1
kind: Secret
metadata:
  name: llm-keys
  namespace: agents
type: Opaque
data:
  anthropic: <base64-encoded-anthropic-api-key>
```

### 7.2 Redis (with authentication)

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: redis-data
spec:
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 10Gi

---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: redis
spec:
  replicas: 1
  selector:
    matchLabels:
      app: redis
  template:
    metadata:
      labels:
        app: redis
    spec:
      containers:
      - name: redis
        image: redis:7-alpine
        command: ["redis-server"]
        args: ["--appendonly", "yes", "--appendfsync", "everysec",
               "--requirepass", "$(REDIS_PASSWORD)"]
        env:
        - name: REDIS_PASSWORD
          valueFrom:
            secretKeyRef:
              name: redis-secret
              key: password
        ports:
        - containerPort: 6379
        resources:
          requests: { memory: "256Mi", cpu: "100m" }
          limits: { memory: "512Mi", cpu: "500m" }
        volumeMounts:
        - name: data
          mountPath: /data
      volumes:
      - name: data
        persistentVolumeClaim:
          claimName: redis-data

---
apiVersion: v1
kind: Service
metadata:
  name: redis
spec:
  selector:
    app: redis
  ports:
  - port: 6379

---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: redis-access
spec:
  podSelector:
    matchLabels:
      app: redis
  ingress:
  - from:
    - podSelector:
        matchExpressions:
        - key: team-component
          operator: In
          values: [agent, lead]
    ports:
    - port: 6379
```

### 7.2 Agent deployments (one per provider+role, same image)

Each role is a separate Deployment but uses the **same image per provider**. Role behaviour is driven entirely by env vars (`ROLE`, `ALLOWED_TOOLS`, `MODEL`). When Claude needs both coders and reviewers, it calls `spawn_agent` twice — once per role.

```yaml
# Coder Deployment
apiVersion: apps/v1
kind: Deployment
metadata:
  name: agent-claude-coder
  labels:
    team-component: agent
    provider: claude
    role: coder
spec:
  replicas: 0          # scale-to-zero: Claude scales up via MCP spawn_agent tool
  selector:
    matchLabels:
      app: agent-claude-coder
  template:
    metadata:
      labels:
        app: agent-claude-coder
        team-component: agent
        provider: claude
        role: coder
    spec:
      serviceAccountName: mcp-server   # RBAC: see §7.4
      initContainers:
      - name: clone-repo
        image: alpine/git
        # HTTPS token auth: token embedded in URL so no SSH key needed.
        # repo-secret holds two keys: token (GitHub PAT) and url (https://github.com/org/repo.git)
        command:
        - sh
        - -c
        - git clone https://$(GIT_TOKEN)@$(GIT_HOST) /workspace
        env:
        - name: GIT_TOKEN
          valueFrom:
            secretKeyRef:
              name: repo-secret
              key: token
        - name: GIT_HOST
          valueFrom:
            secretKeyRef:
              name: repo-secret
              key: host      # e.g. "github.com/azaalouk/myrepo.git"
        volumeMounts:
        - name: workspace
          mountPath: /workspace
      containers:
      - name: agent
        image: quay.io/azaalouk/agent-claude:latest   # same image for all claude roles
        env:
        - name: REDIS_URL
          value: "redis://:$(REDIS_PASSWORD)@redis:6379"
        - name: REDIS_PASSWORD
          valueFrom:
            secretKeyRef:
              name: redis-secret
              key: password
        - name: TEAM_ID
          value: "my-team"
        - name: AGENT_ID
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        - name: PROVIDER
          value: "claude"
        - name: ROLE
          value: "coder"
        - name: MODEL
          value: "claude-opus-4-6"
        - name: ALLOWED_TOOLS
          value: "Read,Edit,Write,Bash,Grep,Glob"
        - name: ANTHROPIC_API_KEY
          valueFrom:
            secretKeyRef:
              name: llm-keys
              key: anthropic
        - name: GIT_TOKEN
          valueFrom:
            secretKeyRef:
              name: repo-secret
              key: token
        - name: GIT_HOST
          valueFrom:
            secretKeyRef:
              name: repo-secret
              key: host
        resources:
          requests: { memory: "2Gi", cpu: "1000m" }
          limits: { memory: "3Gi", cpu: "2000m" }
        volumeMounts:
        - name: workspace
          mountPath: /workspace
      volumes:
      - name: workspace
        emptyDir:
          sizeLimit: 5Gi

---
# Reviewer Deployment — same image, different env vars
apiVersion: apps/v1
kind: Deployment
metadata:
  name: agent-claude-reviewer
  labels:
    team-component: agent
    provider: claude
    role: reviewer
spec:
  replicas: 0
  selector:
    matchLabels:
      app: agent-claude-reviewer
  template:
    metadata:
      labels:
        app: agent-claude-reviewer
        team-component: agent
        provider: claude
        role: reviewer
    spec:
      serviceAccountName: mcp-server
      initContainers:
      - name: clone-repo
        image: alpine/git
        command:
        - sh
        - -c
        - git clone https://$(GIT_TOKEN)@$(GIT_HOST) /workspace
        env:
        - name: GIT_TOKEN
          valueFrom:
            secretKeyRef:
              name: repo-secret
              key: token
        - name: GIT_HOST
          valueFrom:
            secretKeyRef:
              name: repo-secret
              key: host
        volumeMounts:
        - name: workspace
          mountPath: /workspace
      containers:
      - name: agent
        image: quay.io/azaalouk/agent-claude:latest   # same image as coder
        env:
        - name: REDIS_URL
          value: "redis://:$(REDIS_PASSWORD)@redis:6379"
        - name: REDIS_PASSWORD
          valueFrom:
            secretKeyRef:
              name: redis-secret
              key: password
        - name: TEAM_ID
          value: "my-team"
        - name: AGENT_ID
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        - name: PROVIDER
          value: "claude"
        - name: ROLE
          value: "reviewer"
        - name: MODEL
          value: "claude-sonnet-4-6"
        - name: ALLOWED_TOOLS
          value: "Read,Grep,Glob"     # read-only: reviewer cannot edit files
        - name: ANTHROPIC_API_KEY
          valueFrom:
            secretKeyRef:
              name: llm-keys
              key: anthropic
        - name: GIT_TOKEN
          valueFrom:
            secretKeyRef:
              name: repo-secret
              key: token
        - name: GIT_HOST
          valueFrom:
            secretKeyRef:
              name: repo-secret
              key: host
        resources:
          requests: { memory: "1Gi", cpu: "500m" }
          limits: { memory: "2Gi", cpu: "1000m" }
        volumeMounts:
        - name: workspace
          mountPath: /workspace
      volumes:
      - name: workspace
        emptyDir:
          sizeLimit: 2Gi
```

Spawning a new agent via aimux `:new` = **scale up the matching Deployment**, not create a Job:

```go
deploy, _ := k8sClient.AppsV1().Deployments(ns).Get(ctx, "agent-claude-coder", ...)
replicas := *deploy.Spec.Replicas + 1
deploy.Spec.Replicas = &replicas
k8sClient.AppsV1().Deployments(ns).Update(ctx, deploy, ...)
```

Agents are long-lived loops that keep claiming tasks. Jobs are for run-to-completion workloads. Deployments auto-restart on crash, support rolling updates, and integrate with HPA.

### Workspace and git handoff

Workers use `emptyDir` + a git init container. This means:
- Each pod starts with a clean clone of the repo
- Coder agents commit and push their changes to a task-specific branch
- When a reviewer task has `source_branch` set, the worker pulls that branch before starting
- This is how multi-step workflows pass file output between agents — not via Redis (Redis is coordination only)

Worker pseudocode for branch handoff:
```python
# Before starting work
if task.get("source_branch"):
    subprocess.run(["git", "fetch", "origin", task["source_branch"]], cwd="/workspace")
    subprocess.run(["git", "checkout", task["source_branch"]], cwd="/workspace")

# After completing work (coder only)
branch = f"task-{task_id}"
subprocess.run(["git", "checkout", "-b", branch], cwd="/workspace")
subprocess.run(["git", "add", "-A"], cwd="/workspace")
subprocess.run(["git", "commit", "-m", f"task {task_id}: {result[:80]}"], cwd="/workspace")
subprocess.run(["git", "push", "origin", branch], cwd="/workspace")
await coord.complete_task(task_id, result[:500], result_ref=f"branch:{branch}")
```

The lead sets `source_branch` when creating dependent tasks:
```
create_task(prompt="Review the API changes", required_role=reviewer, depends_on=[task1], source_branch="task-{task1_id}")
```

### 7.3 Lead (your local Claude Code session)

The lead is **not** a K8s pod. It's your local Claude Code session with the k8s-agents MCP server configured (see §2.4). This means:
- No separate lead Deployment or StatefulSet on K8s
- No lead crash recovery needed (if your session ends, restart it)
- Lead has full access to your local filesystem for synthesizing results
- MCP server handles K8s API and Redis calls on behalf of Claude

If you need an autonomous lead that runs without your laptop (e.g., CI/CD triggered), deploy the MCP server as a K8s pod and run the Agent SDK headless with the same MCP tools registered. But for interactive use, the local session is simpler.

### 7.4 RBAC

The MCP server needs permission to `get` and `update` Deployments to scale them. Without this, `spawn_agent` and `scale_down` get 403s from the K8s API.

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: mcp-server
  namespace: agents
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: deployment-scaler
  namespace: agents
rules:
- apiGroups: ["apps"]
  resources: ["deployments"]
  verbs: ["get", "update"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: mcp-server-scaler
  namespace: agents
subjects:
- kind: ServiceAccount
  name: mcp-server
  namespace: agents
roleRef:
  kind: Role
  name: deployment-scaler
  apiGroup: rbac.authorization.k8s.io
```

Register the `serviceAccountName: mcp-server` in each agent Deployment's pod spec (see §7.2). When running the MCP server locally (not in-cluster), it uses your kubeconfig credentials instead — no ServiceAccount needed.

## 8. Coordination library (Python)

```python
import redis.asyncio as redis
import json, time

class AgentCoordinator:
    def __init__(self, redis_url: str, team_id: str, agent_id: str, role: str = ""):
        self.r = redis.from_url(redis_url)
        self.team = team_id
        self.agent = agent_id
        self.role = role

    # --- Setup ---

    async def register(self, provider: str, model: str, namespace: str = ""):
        """Register this agent in Redis on startup."""
        # Per-agent consumer group for events (broadcast delivery)
        try:
            await self.r.xgroup_create(
                f"team:{self.team}:events", f"agent-{self.agent}",
                id="$", mkstream=True,  # $ = start from latest, don't replay history
            )
        except Exception:
            pass

        # Shared consumer group for inbox (only this agent reads its own inbox)
        try:
            await self.r.xgroup_create(
                f"team:{self.team}:inbox:{self.agent}", "agents",
                id="0", mkstream=True,  # 0 = catch unacked messages after restart
            )
        except Exception:
            pass

        await self.r.hset(f"team:{self.team}:agent:{self.agent}", mapping={
            "provider": provider, "role": self.role, "model": model,
            "namespace": namespace, "registered_at": str(time.time()),
        })

    async def deregister(self):
        """Clean up on graceful shutdown."""
        await self.r.hdel(f"team:{self.team}:heartbeat", self.agent)
        await self.r.delete(f"team:{self.team}:agent:{self.agent}")
        await self.r.delete(f"team:{self.team}:cost:{self.agent}")

    # --- Messaging ---

    async def send(self, to: str, text: str, summary: str = ""):
        await self.r.xadd(
            f"team:{self.team}:inbox:{to}",
            {"from": self.agent, "text": text, "summary": summary,
             "timestamp": str(time.time())},
            maxlen=1000,
        )

    async def broadcast(self, text: str, summary: str = ""):
        await self.r.xadd(
            f"team:{self.team}:events",
            {"from": self.agent, "type": "broadcast", "text": text,
             "summary": summary, "timestamp": str(time.time())},
            maxlen=10000,
        )

    async def receive(self, timeout_ms: int = 5000) -> list[tuple[str, str, dict]]:
        """Returns (stream, msg_id, data) tuples. Caller must ack after processing."""
        streams = {
            f"team:{self.team}:inbox:{self.agent}": ">",
            f"team:{self.team}:events": ">",
        }
        groups = {
            f"team:{self.team}:inbox:{self.agent}": "agents",
            f"team:{self.team}:events": f"agent-{self.agent}",
        }
        messages = []
        for stream, group in groups.items():
            try:
                results = await self.r.xreadgroup(
                    group, self.agent, {stream: ">"}, count=10, block=timeout_ms,
                )
                for stream_name, entries in results:
                    for msg_id, data in entries:
                        messages.append((stream_name.decode(), msg_id.decode(), data))
            except Exception:
                pass
        return messages

    async def ack(self, stream: str, msg_id: str):
        """Ack a message after processing. Determines group from stream name."""
        group = f"agent-{self.agent}" if ":events" in stream else "agents"
        await self.r.xack(stream, group, msg_id)

    # --- Tasks ---

    async def create_task(self, task_id: str, prompt: str,
                          required_role: str = "", depends_on: list[str] = None,
                          source_branch: str = ""):
        await self.r.hset(f"team:{self.team}:task:{task_id}", mapping={
            "status": "pending", "prompt": prompt, "required_role": required_role,
            "assignee": "", "result_summary": "", "result_ref": "",
            "source_branch": source_branch, "error": "",
            "depends_on": json.dumps(depends_on or []),
            "retry_count": "0", "created_at": str(time.time()),
        })
        await self.r.zadd(f"team:{self.team}:tasks:pending", {task_id: time.time()})

    async def claim_task(self) -> str | None:
        """Try to claim the next available task matching this agent's role.
        Returns task_id if claimed, None otherwise."""
        pending = await self.r.zrange(f"team:{self.team}:tasks:pending", 0, -1)
        for tid in pending:
            task_id = tid.decode() if isinstance(tid, bytes) else tid
            result = await self.r.eval(
                CLAIM_SCRIPT, 2,
                f"team:{self.team}:task:{task_id}", f"team:{self.team}",
                self.agent, self.role, task_id,
            )
            if result == 1:
                return task_id
        return None

    async def complete_task(self, task_id: str, result_summary: str, result_ref: str = ""):
        """Mark task complete. result_ref is the git branch or OTEL span ID with full output."""
        await self.r.hset(f"team:{self.team}:task:{task_id}", mapping={
            "status": "completed", "result_summary": result_summary[:500],
            "result_ref": result_ref, "completed_at": str(time.time()),
        })

    async def fail_task(self, task_id: str, error: str):
        retry = int(await self.r.hget(f"team:{self.team}:task:{task_id}", "retry_count") or 0)
        if retry >= 3:
            new_status = "dead"  # needs human
        else:
            new_status = "pending"
            await self.r.zadd(f"team:{self.team}:tasks:pending", {task_id: time.time()})
        await self.r.hset(f"team:{self.team}:task:{task_id}", mapping={
            "status": new_status, "error": error, "assignee": "",
            "retry_count": str(retry + 1),
        })

    # --- Heartbeat ---

    async def heartbeat(self):
        await self.r.hset(f"team:{self.team}:heartbeat", self.agent, str(time.time()))

    # --- Cost ---

    async def report_tokens(self, tokens_in: int, tokens_out: int):
        key = f"team:{self.team}:cost:{self.agent}"
        pipe = self.r.pipeline()
        pipe.hincrby(key, "tokens_in", tokens_in)
        pipe.hincrby(key, "tokens_out", tokens_out)
        await pipe.execute()
```

### Agent main loop

```python
import asyncio, os, signal, subprocess
from claude_code_sdk import query, ClaudeCodeOptions, AssistantMessage
from coordinator import AgentCoordinator

WORKSPACE = "/workspace"
ROLE = os.environ.get("ROLE", "coder")
ALLOWED_TOOLS = os.environ.get("ALLOWED_TOOLS", "Read,Grep,Glob").split(",")
GIT_TOKEN = os.environ.get("GIT_TOKEN", "")
GIT_HOST = os.environ.get("GIT_HOST", "")   # e.g. github.com/azaalouk/myrepo.git

def git(*args):
    """Run a git command in /workspace. Raises on failure."""
    subprocess.run(["git"] + list(args), cwd=WORKSPACE, check=True)

def configure_git_auth():
    """Set remote URL with token so git push works without interactive auth."""
    if GIT_TOKEN and GIT_HOST:
        git("remote", "set-url", "origin", f"https://{GIT_TOKEN}@{GIT_HOST}")

async def run_task(coord, task_id: str, task: dict) -> tuple[str, str]:
    """Execute one task. Returns (result_summary, result_ref)."""
    prompt = task.get(b"prompt", b"").decode()
    source_branch = task.get(b"source_branch", b"").decode()

    # Pull source branch if this task depends on a prior task's file output
    if source_branch:
        git("fetch", "origin", source_branch)
        git("checkout", source_branch)

    # Run the LLM agent — filter to text output only
    result_text = ""
    async for msg in query(
        prompt=prompt,
        options=ClaudeCodeOptions(allowed_tools=ALLOWED_TOOLS),
    ):
        if isinstance(msg, AssistantMessage):
            for block in msg.content:
                if hasattr(block, "text"):
                    result_text += block.text

    result_ref = ""

    # Coders commit and push their file changes to a task branch
    if ROLE == "coder":
        configure_git_auth()
        branch = f"task-{task_id}"
        git("checkout", "-b", branch)
        git("add", "-A")
        git("commit", "-m", f"task {task_id}: {result_text[:80]}")
        git("push", "origin", branch)
        result_ref = f"branch:{branch}"

    return result_text[:500], result_ref


async def main():
    coord = AgentCoordinator(
        redis_url=os.environ["REDIS_URL"],
        team_id=os.environ["TEAM_ID"],
        agent_id=os.environ["AGENT_ID"],
        role=ROLE,
    )
    await coord.register(
        provider=os.environ.get("PROVIDER", "claude"),
        model=os.environ.get("MODEL", "default"),
        namespace=os.environ.get("POD_NAMESPACE", ""),
    )

    # Heartbeat runs independently (not blocked by slow API calls)
    async def heartbeat_loop():
        while True:
            await coord.heartbeat()
            await asyncio.sleep(10)
    asyncio.create_task(heartbeat_loop())

    # Graceful shutdown
    loop = asyncio.get_event_loop()
    for sig in (signal.SIGTERM, signal.SIGINT):
        loop.add_signal_handler(sig, lambda: asyncio.create_task(shutdown(coord)))

    # Main loop
    while True:
        # 1. Check for messages
        messages = await coord.receive(timeout_ms=2000)
        for stream, msg_id, data in messages:
            if data.get(b"type") == b"shutdown_request":
                await coord.send("lead", "Shutting down", "shutdown approved")
                await coord.deregister()
                return
            await coord.ack(stream, msg_id)

        # 2. Look for work
        task_id = await coord.claim_task()
        if task_id:
            task = await coord.r.hgetall(f"team:{coord.team}:task:{task_id}")
            try:
                summary, result_ref = await run_task(coord, task_id, task)
                await coord.complete_task(task_id, summary, result_ref=result_ref)
                await coord.send("lead", f"Task {task_id} done: {summary[:200]}", f"Task {task_id} done")
            except Exception as e:
                await coord.fail_task(task_id, str(e))
                await coord.send("lead", f"Task {task_id} failed: {e}", f"Task {task_id} failed")

        await asyncio.sleep(1)

async def shutdown(coord):
    await coord.deregister()
    raise SystemExit(0)

if __name__ == "__main__":
    asyncio.run(main())
```

## 9. aimux as observability plane

With the lead being your local Claude Code session (using MCP tools to manage K8s), aimux shifts from control plane to **observability + emergency override**. It watches Claude orchestrate agents across the cluster, shows costs, and lets you intervene.

What aimux does:
- **Observe**: shows local + K8s agents in one table, traces, costs, teams
- **Override**: force-kill runaway agents (delete pod), send messages (course-correct), force-scale
- **Guard**: cost threshold alerts, dead agent detection

What aimux doesn't do (Claude does it via MCP):
- Decide how many agents to spawn
- Create tasks
- Assign work to agents

### 9.1 Provider interface extensions

The existing `Provider` interface has methods that don't fit K8s (`SpawnCommand() *exec.Cmd`, `ParseTrace(filePath string)`). Rather than break the interface, add optional interfaces following the existing `DirectRenderer` pattern:

```go
// Already exists in codebase
type DirectRenderer interface {
    RenderDirect(w, h int) string
}

// New: for providers that spawn remotely
type RemoteSpawner interface {
    SpawnRemote(ctx context.Context, opts RemoteSpawnOpts) error
}

// New: for providers that get traces from non-file sources
type RemoteTracer interface {
    ParseTraceRemote(agentID string) ([]trace.Turn, error)
}
```

TUI code: `if spawner, ok := provider.(RemoteSpawner); ok { spawner.SpawnRemote(...) } else { provider.SpawnCommand(...) }`.

### 9.2 Feature mapping (local → K8s)

| Feature | Local | K8s Provider |
|---------|-------|-------------|
| Discovery | `ps` process scan | `HGETALL team:{id}:heartbeat` + K8s pod metadata |
| Status | Process activity heuristic | Heartbeat recency + task status from Redis |
| Traces | JSONL files | OTEL spans (aimux already has OTEL receiver) |
| Sessions | PTY embed / tmux mirror | Read-only trace + message input (no remote PTY) |
| Spawn | `exec.Command("claude")` | Scale up Deployment replicas via K8s API |
| Kill | SIGTERM | Delete pod via K8s API |
| Costs | Parse session JSONL | Read `team:{id}:cost:{agent}` hash |
| Teams | `~/.claude/teams/*/config.json` | Read `team:{id}:config` hash |

### 9.3 aimux config

```yaml
providers:
  claude:
    enabled: true
  codex:
    enabled: true
  kubernetes:
    enabled: true
    redis_url: "redis://:password@redis.agents.svc:6379"
    namespace: "agents"
    team_id: "my-team"
    kubeconfig: ""        # empty = in-cluster, or path for remote
    images:
      claude: "quay.io/azaalouk/agent-claude:latest"
      codex: "quay.io/azaalouk/agent-codex:latest"
      gemini: "quay.io/azaalouk/agent-gemini:latest"
```

### 9.4 TUI mockups

**Main view (mixed local + K8s):**

```
┌─ Agents 8 ──────────────────┐  ┌─ Cost ──┐  ┌─ Providers ─────────┐
│ ●3 active ◐1 wait ○4 idle   │  │ $127.43 │  │ claude:4 codex:2    │
│                              │  │         │  │ gemini:2             │
└──────────────────────────────┘  └─────────┘  └──────────────────────┘
 Agents
 ❯ Enter:open  t:traces  c:costs  T:teams  :new:launch  x:kill  ?:help
──────────────────────────────────────────────────────────────────────────

 STATUS  PROJECT        PROVIDER  MODEL        LOCATION     COST    AGE
 ● act   blog-concept   claude    opus-4.6     local        $47.13  2h
 ● act   aimux          claude    opus-4.6     local         $3.58  25m
 ◐ wait  trustyai       claude    sonnet-4.5   local         $1.20  1h
 ● act   api-service    claude    opus-4.6     k8s/agents   $32.10  45m
 ○ idle  api-service    codex     o3           k8s/agents   $18.50  40m
 ○ idle  api-service    codex     o4-mini      k8s/agents    $4.22  40m
 ○ idle  ml-pipeline    gemini    gemini-pro   k8s/agents    $8.90  30m
 ○ idle  ml-pipeline    gemini    flash-3.1    k8s/agents   $11.80  30m
```

**Split view for K8s agent (trace + messages):**

```
┌─ TRACE (api-service / claude / k8s) ──────┬─ MESSAGES ─────────────────────┐
│                                            │                                │
│  ▸ [assistant] Reading routes.go...        │  [lead → agent-7x]            │
│  ▸ [tool] Read routes.go                   │  Focus on /users endpoint.    │
│  ▸ [assistant] Adding PUT and DELETE...    │                                │
│  ▸ [tool] Edit routes.go:45               │  [agent-7x → lead]            │
│  ▸ [tool] Bash: go test ./...             │  Done. All 42 tests pass.     │
│    ok  api-service/routes  0.8s           │                                │
│                                            │ ┌──────────────────────────┐  │
│                                            │ │ > type message here...   │  │
│                                            │ └──────────────────────────┘  │
└────────────────────────────────────────────┴────────────────────────────────┘
```

**Teams view:**

```
 ▸ api-service-team (4 members)                              k8s/agents
     agent-claude-7x    claude    opus-4.6     coder         ● active
     agent-codex-k9     codex     o3           coder         ○ idle
     agent-codex-m2     codex     o4-mini      reviewer      ○ idle
     agent-lead-0       claude    sonnet-4.5   lead          ● active

 ▸ openai-research (2 members)                               local
     team-lead          claude    opus-4.6     team-lead
     crewai-researcher  claude    opus-4.6     general-purpose
```

## 10. Scaling guide

| Agents | Architecture | What changes |
|--------|-------------|-------------|
| 5-20 | Current design | Nothing. Single Redis, Deployments, no HA. |
| 20-50 | Add monitoring | Prometheus metrics on Redis, OTEL collector, Grafana. |
| 50-100 | Add HA | Redis Sentinel (3 nodes). HPA on agent Deployments. |
| 100+ | Rearchitect | Redis Cluster or Dapr Agents. Argo Workflows for task DAGs. |

**What NOT to add yet**: Kafka, NATS, Istio, custom CRD operators.

## 11. Open questions

1. **Multi-cluster**: One K8s provider per cluster, or one provider with cluster selector?
2. **Rate limits**: Multiple agents hitting same API. Per-team rate limit tracking?
3. **Git auth in init container**: SSH key vs token. Where to store credentials securely (K8s Secret, Vault)?

## 12. Decision log

| # | Decision | Alternatives | Rationale |
|---|----------|-------------|-----------|
| 1 | Redis Streams for messaging | Postgres, NATS, gRPC, K8s-native | Simple, persistent, team knows it. Postgres = 5-10s latency. NATS = steep learning curve. gRPC = no persistence. |
| 2 | Lua scripts for task claiming | SQL locks, Redlock | Atomic in Redis. No external lock manager. |
| 3 | AOF with `everysec` fsync | RDB, AOF `always`, none | At most 1s data loss. `always` is slow. RDB loses minutes. |
| 4 | Single Redis pod (no HA) | Sentinel, Cluster | 5-20 agents don't justify HA. Recovery = seconds. |
| 5 | Anthropic Agent SDK for runtime | Raw API, LangChain, CrewAI | Same agent loop as Claude Code. Headless. No framework overhead. |
| 6 | Deployment for agent pools | Jobs, StatefulSet, DaemonSet | Long-lived, auto-restart, HPA. Jobs are one-shot. |
| 7 | `emptyDir` + git init container for workspace | Shared PVC, hostPath | Each pod gets a clean clone. Agents commit and push to task branches. Dependent tasks pull the branch. Avoids shared PVC (requires ReadWriteMany storage class). |
| 8 | Stream MAXLEN ~1000 inbox / ~10000 events | Unbounded, TTL-based | Prevents OOM. Events larger because all agents read them. |
| 9 | Heartbeat via Redis Hash | K8s liveness probes, health service | Lead reads directly. K8s probes only tell K8s. |
| 10 | Per-agent consumer group for inbox | Shared group, pub/sub | Tracks read position per agent. Restart resumes. |
| 11 | OTEL for traces, not Redis | Duplicate to Redis | aimux already has OTEL receiver. Avoids redundant storage. |
| 12 | aimux as control plane via Provider | Separate dashboard | Reuses all existing TUI code and views. |
| 13 | Read-only trace + message input for remote | Websocket PTY, kubectl exec | No server code in agent pods. Redis inbox handles it. |
| 14 | aimux runs locally, connects remotely | aimux inside cluster | Devs want local + remote in one view. |
| 15 | Multi-provider (claude, codex, gemini) | Single provider | Different LLMs for different tasks. Same Redis protocol. |
| 16 | One Deployment per provider+role | Single pool | K8s resource limits per-pod. Autoscaling per-Deployment. |
| 17 | Provider and role as independent dims | Combined enum | A Claude researcher and Gemini researcher share role config. |
| 18 | Per-agent consumer group for broadcasts | Shared group | Redis delivers to one consumer per group. Per-agent = true broadcast. |
| 19 | Ack after processing | Ack on read | Crash between ack and processing loses message. |
| 20 | `failed` + `dead` task states | Only pending/completed | Tasks fail. 3 retries then dead for human review. |
| 21 | Truncated results in Redis | Full results | Large results bloat memory. OTEL/PVC for full output. |
| 22 | Optional RemoteSpawner/RemoteTracer | Change Provider interface | Non-breaking. Follows existing DirectRenderer pattern. |
| 23 | Heartbeat in background asyncio task | In main loop | Long API calls block main loop. Separate task stays alive. |
| 24 | Redis requirepass + NetworkPolicy | Unauthenticated | Any pod can tamper without auth. |
| 25 | StatefulSet for lead | Deployment | Stable identity survives restart. Workers stay as Deployment. |
| 26 | Monorepo with shared coordinator package | Copy file, PyPI, sidecar | One source of truth, three images, no registry needed. |
| 27 | Scale Deployment replicas for spawn | Create Jobs | Agents are long-lived loops. Jobs are run-to-completion. |
| 28 | MCP server for K8s tools (not CLAUDE.md) | CLAUDE.md instructions, Agent SDK custom tools | MCP tools are discoverable. Tool descriptions guide Claude. Server-side limits enforceable. CLAUDE.md can be ignored. |
| 29 | Hook blocks Agent(team_name=...) for K8s mode | No hook (rely on Claude choosing correctly) | Prevents accidental local spawn when K8s is intended. Quick subagents (Explore, Plan) still work. |
| 30 | Smart lead (local), dumb workers (K8s) | LLM-driven workers | Workers don't need coordination intelligence. Python loop is cheaper, more reliable. Worker Claude only gets code tools. |
| 31 | Lead runs locally as Claude Code session | Lead as K8s pod | No separate lead Deployment needed. Your Claude Code session IS the lead. Uses MCP tools for K8s. |
| 32 | Deployments start at 0 replicas (scale-to-zero) | Pre-deployed pool always running | No idle pods. Claude scales up on demand, down when done. Cost = zero when not working. |
| 33 | Local and remote modes coexist independently | Replace local teams with K8s | Built-in tools unchanged. MCP tools additive. User decides mode by how they talk to Claude. |
| 34 | Go for MCP server | Python (FastMCP) | Shares client-go and go-redis with aimux. Single binary. Can embed in aimux later. No Python runtime needed on dev laptop. |
| 35 | Shared `pkg/rediskeys` Go package | Duplicate key strings | Single source of truth for Redis key naming across MCP server and aimux K8s provider. |
| 36 | One image per provider, role as env var | One image per provider+role | Role only differs by ALLOWED_TOOLS, MODEL, and resource limits. Same coordinator code. Fewer images to build and maintain. |
| 37 | RBAC ServiceAccount for MCP server | Cluster-admin, kubeconfig only | Least privilege. MCP server only needs get+update on Deployments. |
| 38 | spawn_agent polls for agent readiness | Return immediately | Pods take 30-60s to start. Without polling, Claude creates tasks before any agent is ready to claim them. |
| 39 | SCAN not KEYS for task listing | KEYS | KEYS blocks Redis while scanning. SCAN is incremental. Safe for production. |
| 40 | git branch per task for file handoff | Shared PVC, Redis payload, S3 | Git is already present (init container). Natural fit for code artifacts. Dependent tasks specify source_branch. No extra infrastructure. |
| 41 | result_ref field for full task output | Only result_summary | 500 char truncation is fine for lead status. Dependent agents need the full output — result_ref points to the branch or OTEL span with complete content. |
| 42 | Lead calls cleanup_branches after scale_down | Manual cleanup, TTL-based | Lead has full context of which task IDs were used. Cleanup is a natural final step. Keeps GitHub clean. Never touches main or non-task branches. |
| 43 | cleanup_branches via GitHub API (not git CLI) | git CLI in MCP server, separate job | MCP server already has GITHUB_TOKEN for repo access. No git binary needed in the server binary. Atomic per-branch deletes with clear error reporting. |

## 13. Background: why Redis

We evaluated 6 options for inter-agent messaging on Kubernetes. Full comparison:

| Option | Latency | Ops complexity | Crash recovery | Right for 5-20 agents? |
|--------|---------|---------------|----------------|----------------------|
| PostgreSQL polling | 5-10s | Very low (existing) | ACID | Yes, if you have Postgres |
| Redis Streams | <5ms | Low | Good (AOF) | **Yes (selected)** |
| NATS JetStream | <1ms | Medium | Excellent | Overkill |
| Valkey | <5ms | Low | Good | Too immature |
| K8s native (ConfigMaps/PVCs) | High | Medium | Poor | No (race conditions) |
| gRPC direct | <1ms | Medium | None | No (you become the broker) |

Redis won because: agents think for minutes (latency doesn't matter), most engineers know Redis (NATS requires specialized knowledge), and Redis Streams have built-in consumer groups with crash recovery. At 5-20 agents, NATS's advantages (better pub/sub, lower latency) don't justify its operational overhead.

## 14. Background: how Claude Code teams work today

Claude Code coordinates local agents via filesystem:

```
~/.claude/teams/{team}/
├── config.json              # team metadata + member list
└── inboxes/
    ├── team-lead.json       # JSON array of messages TO lead
    └── researcher.json      # JSON array of messages TO researcher
```

Each inbox is a JSON array. `SendMessage` appends to the file. The agent polls for new unread entries. This architecture replaces the filesystem with Redis Streams while keeping the same coordination semantics.

Neither Codex CLI nor Gemini CLI has an equivalent team system. Codex has experimental multi-agent (parent-child only). Gemini has subagents (tool-based delegation, no peer messaging).

## 15. Implementation order

1. **MCP server** - spawn_agent, create_task, list_tasks, send_message, scale_down, get_costs, cleanup_branches
2. **Monorepo scaffold** - coordinator package with tests, Dockerfile template (one per provider)
3. **Claude agent worker** - container image (claude-code-sdk + coordinator + main loop, role via env vars)
4. **Redis + agent manifests + RBAC** - deploy to dev cluster, replicas: 0
5. **Git init container** - add to Deployment template, test clone + push with GitHub token secret
6. **End-to-end test** - Claude Code + MCP server spawns agents, creates tasks, agents complete them, branches cleaned up
7. **Hook config** - PreToolUse hook that blocks Agent(team_name=...) for K8s mode
8. **Integration tests** - multi-agent claiming, crash recovery, broadcast delivery, branch handoff
9. **Second provider image** - Codex or Gemini worker
10. **aimux K8s provider** (Go) - implements Provider + RemoteSpawner + RemoteTracer, reads Redis + K8s API
11. **aimux message input** - split view right pane writes to Redis inbox

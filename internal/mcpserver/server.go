// Package mcpserver provides the MCP server for remote agent orchestration.
// It supports two backends: K8s+Redis (original) and OpenShell (new).
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	mcplib "github.com/mark3labs/mcp-go/server"
	"github.com/redis/go-redis/v9"
)

var validTaskID = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// Options configures the MCP server.
type Options struct {
	// Backend selection: "openshell" or "k8s" (default: "k8s")
	Backend         string
	// ExternalBackend allows the caller to inject a pre-built Backend,
	// avoiding import cycles. When set, Backend field is ignored.
	ExternalBackend Backend
	// OpenShell config (used when Backend=="openshell" and ExternalBackend is nil)
	GatewayEndpoint string
	GatewayInsecure bool
	OpenShellBinary string
	Image           string
	WarmPool        int
	// K8s config (backward compat)
	RedisURL   string
	Kubeconfig string
	Namespace  string
	TeamID     string
	// Shared
	JournalPath string
	MaxAgents   int
	MaxCost     float64
	GithubToken string
	GithubRepo  string
}

func (o Options) withDefaults() Options {
	if o.MaxAgents == 0 {
		o.MaxAgents = 20
	}
	if o.MaxCost == 0 {
		o.MaxCost = 100
	}
	// K8s-specific defaults only when using K8s backend
	if o.Backend == "" || o.Backend == "k8s" {
		if o.RedisURL == "" {
			o.RedisURL = "redis://localhost:6379"
		}
		if o.Namespace == "" {
			o.Namespace = "agents"
		}
		if o.TeamID == "" {
			o.TeamID = "default"
		}
	}
	return o
}

// Server holds all state for the MCP server.
type Server struct {
	backend     Backend
	journal     *Journal
	pool        *Pool
	// K8s-specific: kept for backward compat with Redis task handlers.
	// Nil when using OpenShell backend.
	rdb         *redis.Client
	teamID      string
	namespace   string
	maxAgents   int
	maxCost     float64
	warmPool    int
	githubToken string
	githubRepo  string
}

// NewServer creates a Server with the configured backend.
func NewServer(opts Options) (*Server, error) {
	opts = opts.withDefaults()

	s := &Server{
		maxAgents:   opts.MaxAgents,
		maxCost:     opts.MaxCost,
		warmPool:    opts.WarmPool,
		githubToken: opts.GithubToken,
		githubRepo:  opts.GithubRepo,
	}

	if opts.ExternalBackend != nil {
		s.backend = opts.ExternalBackend
	}

	switch {
	case s.backend != nil:
		// Already set via ExternalBackend
	case opts.Backend == "openshell":
		return nil, fmt.Errorf("openshell backend requires ExternalBackend (create compose.NewBackend in caller)")
	case opts.Backend == "k8s" || opts.Backend == "":
		k8sBackend, err := NewK8sBackend(K8sConfig{
			RedisURL:   opts.RedisURL,
			Kubeconfig: opts.Kubeconfig,
			Namespace:  opts.Namespace,
			TeamID:     opts.TeamID,
			MaxAgents:  opts.MaxAgents,
		})
		if err != nil {
			return nil, err
		}
		s.backend = k8sBackend
		s.rdb = k8sBackend.Redis()
		s.teamID = k8sBackend.TeamID()
		s.namespace = opts.Namespace
	default:
		return nil, fmt.Errorf("unknown backend %q (want \"openshell\" or \"k8s\")", opts.Backend)
	}

	// Journal for durable task state
	journalPath := opts.JournalPath
	if journalPath == "" {
		home, _ := os.UserHomeDir()
		if home != "" {
			journalPath = filepath.Join(home, ".aimux", "tasks.jsonl")
			_ = os.MkdirAll(filepath.Dir(journalPath), 0o750)
		}
	}
	if journalPath != "" {
		journal, err := NewJournal(journalPath)
		if err != nil {
			return nil, fmt.Errorf("open task journal %s: %w", journalPath, err)
		}
		s.journal = journal
	}

	// Warm pool
	if s.warmPool > 0 {
		s.pool = NewPool(s.backend, s.warmPool)
	}

	return s, nil
}

// Serve registers all MCP tools and starts the stdio server. Blocks until done.
func (s *Server) Serve() error {
	if s.pool != nil {
		if err := s.pool.WarmUp(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "warn: warm pool failed: %v\n", err)
		}
	}

	srv := mcplib.NewMCPServer("aimux-agents", "1.0.0")
	srv.AddTool(s.spawnAgentTool(), s.handleSpawnAgent)
	srv.AddTool(s.createTaskTool(), s.handleCreateTask)
	srv.AddTool(s.listTasksTool(), s.handleListTasks)
	srv.AddTool(s.getTaskTool(), s.handleGetTask)
	srv.AddTool(s.getTaskResultTool(), s.handleGetTaskResult)
	srv.AddTool(s.waitForTaskTool(), s.handleWaitForTask)
	srv.AddTool(s.listAgentsTool(), s.handleListAgents)
	srv.AddTool(s.sendMessageTool(), s.handleSendMessage)
	srv.AddTool(s.scaleDownTool(), s.handleScaleDown)
	srv.AddTool(s.getCostsTool(), s.handleGetCosts)
	srv.AddTool(s.cleanupBranchesTool(), s.handleCleanupBranches)
	return mcplib.ServeStdio(srv)
}

// teamKey scopes a Redis key to the current team.
// Format: team:{teamID}:{suffix}
func (s *Server) teamKey(suffix string) string {
	return fmt.Sprintf("team:%s:%s", s.teamID, suffix)
}

// --- Tool definitions ---

func (s *Server) spawnAgentTool() mcp.Tool {
	return mcp.NewTool("spawn_agent",
		mcp.WithDescription("Ensure remote agents are available for parallel work. "+
			"Pre-creates sandboxed execution environments on remote infrastructure. "+
			"Use when tasks need dedicated compute, cross-provider execution (Claude, Codex, Gemini), "+
			"or isolation from your local session. For simple parallel tasks on the same machine, "+
			"prefer Claude's built-in Agent tool instead. "+
			"Call before create_task to ensure capacity."),
		mcp.WithString("provider", mcp.Required(), mcp.Description("Agent provider: claude, codex, or gemini")),
		mcp.WithString("role", mcp.Required(), mcp.Description("Agent role: coder, researcher, or reviewer")),
		mcp.WithNumber("count", mcp.Description("Number of remote agents to ensure are available (default 1)")),
	)
}

func (s *Server) handleSpawnAgent(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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

	var spawned []string
	for i := 0; i < count; i++ {
		name, err := s.backend.CreateSandbox(ctx, SandboxOpts{
			Mode: "worker",
			Labels: map[string]string{
				"provider": provider,
				"role":     role,
			},
		})
		if err != nil {
			return mcp.NewToolResultText(fmt.Sprintf(
				"Error creating sandbox %d/%d: %v (spawned so far: %s)",
				i+1, count, err, strings.Join(spawned, ", "))), nil
		}
		spawned = append(spawned, name)
	}

	return mcp.NewToolResultText(fmt.Sprintf(
		"Spawned %d agent(s): %s", len(spawned), strings.Join(spawned, ", "))), nil
}

func (s *Server) createTaskTool() mcp.Tool {
	return mcp.NewTool("create_task",
		mcp.WithDescription("Create a task for remote agents to work on. "+
			"Dispatches work to an available sandboxed agent. "+
			"Use depends_on to chain tasks sequentially."),
		mcp.WithString("prompt", mcp.Required(), mcp.Description("Task instructions for the agent")),
		mcp.WithString("required_role", mcp.Description("Only agents with this role can claim it")),
		mcp.WithString("depends_on", mcp.Description("Comma-separated task IDs that must complete first")),
	)
}

func (s *Server) handleCreateTask(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	prompt, err := req.RequireString("prompt")
	if err != nil {
		return mcp.NewToolResultText("Error: prompt is required"), nil
	}

	taskID := uuid.New().String()[:8]

	// OpenShell path: exec directly into a sandbox
	if s.rdb == nil {
		return s.createTaskExec(ctx, taskID, prompt)
	}

	// K8s path: push to Redis queue
	return s.createTaskRedis(ctx, taskID, prompt, req)
}

// PoolBackend is an optional interface for backends that support idle pool management.
type PoolBackend interface {
	ClaimIdle() string
	Release(name string)
}

// createTaskExec dispatches a task by exec'ing into an available sandbox.
func (s *Server) createTaskExec(ctx context.Context, taskID, prompt string) (*mcp.CallToolResult, error) {
	poolBackend, ok := s.backend.(PoolBackend)
	if !ok {
		return mcp.NewToolResultText("Error: exec dispatch requires a pool-capable backend"), nil
	}

	sandbox := poolBackend.ClaimIdle()
	if sandbox == "" {
		return mcp.NewToolResultText("Error: no idle sandboxes available. Call spawn_agent first."), nil
	}

	// Record task creation
	s.recordEvent(TaskEvent{TaskID: taskID, State: "created", Prompt: prompt})
	s.recordEvent(TaskEvent{TaskID: taskID, State: "running", Sandbox: sandbox})

	start := time.Now()
	result, err := s.backend.ExecStream(ctx, sandbox, []string{"sh", "-c", prompt})
	duration := int(time.Since(start).Seconds())
	poolBackend.Release(sandbox)

	summary := truncate(result.Output, 200)

	tr := TaskResult{
		Type:     "text",
		Summary:  summary,
		FullText: result.Output,
		Duration: duration,
	}

	if err != nil {
		s.recordEvent(TaskEvent{TaskID: taskID, State: "failed", Error: fmt.Sprintf("exit %d", result.ExitCode)})
		trJSON, _ := json.Marshal(tr)
		return mcp.NewToolResultText(fmt.Sprintf(
			"Task %s failed (sandbox=%s, exit=%d):\n%s\n\nStructured result:\n%s",
			taskID, sandbox, result.ExitCode, summary, string(trJSON))), nil
	}

	s.recordEvent(TaskEvent{TaskID: taskID, State: "done", Result: result.Output})
	trJSON, _ := json.Marshal(tr)
	return mcp.NewToolResultText(fmt.Sprintf(
		"Task %s completed (sandbox=%s, exit=%d):\n%s\n\nStructured result:\n%s",
		taskID, sandbox, result.ExitCode, summary, string(trJSON))), nil
}

func (s *Server) recordEvent(ev TaskEvent) {
	if s.journal != nil {
		_ = s.journal.Record(ev)
	}
}

// createTaskRedis pushes a task to the Redis queue for K8s agents.
func (s *Server) createTaskRedis(ctx context.Context, taskID string, prompt string, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	role := req.GetString("required_role", "")
	depsStr := req.GetString("depends_on", "")

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

	if err := s.rdb.HSet(ctx, s.teamKey("task:"+taskID), taskHash).Err(); err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Error writing task to Redis: %v", err)), nil
	}

	score := float64(now)
	if err := s.rdb.ZAdd(ctx, s.teamKey("tasks:pending"), redis.Z{Score: score, Member: taskID}).Err(); err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Error writing to tasks:pending: %v", err)), nil
	}
	if err := s.rdb.ZAdd(ctx, s.teamKey("tasks:all"), redis.Z{Score: score, Member: taskID}).Err(); err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Error writing to tasks:all: %v", err)), nil
	}

	label := "any"
	if role != "" {
		label = role
	}
	return mcp.NewToolResultText(fmt.Sprintf("Task %s created (role=%s)", taskID, label)), nil
}

func (s *Server) listTasksTool() mcp.Tool {
	return mcp.NewTool("list_tasks",
		mcp.WithDescription("Show all remote tasks and their status."),
	)
}

func (s *Server) handleListTasks(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if s.rdb == nil {
		return mcp.NewToolResultText("Tasks are executed synchronously on the OpenShell backend. Use create_task to run a command and get results immediately."), nil
	}
	taskIDs, err := s.rdb.ZRange(ctx, s.teamKey("tasks:all"), 0, -1).Result()
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Error reading tasks:all from Redis: %v", err)), nil
	}

	if len(taskIDs) == 0 {
		var cursor uint64
		prefix := s.teamKey("task:")
		for {
			batch, nextCursor, scanErr := s.rdb.Scan(ctx, cursor, prefix+"*", 100).Result()
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
		t, err := s.rdb.HGetAll(ctx, s.teamKey("task:"+tid)).Result()
		if err != nil || len(t) == 0 {
			continue
		}
		line := fmt.Sprintf("  %s: [%s]", tid, t["status"])
		if t["assignee"] != "" {
			line += fmt.Sprintf(" assigned=%s", t["assignee"])
		}
		prompt := truncate(t["prompt"], 60)
		line += " " + prompt
		if t["status"] == "completed" && t["result_summary"] != "" {
			result := truncate(t["result_summary"], 60)
			line += "\n         result: " + result
		}
		lines = append(lines, line)
	}

	if len(lines) == 0 {
		return mcp.NewToolResultText("No tasks"), nil
	}
	return mcp.NewToolResultText(joinLines(lines)), nil
}

func (s *Server) getTaskTool() mcp.Tool {
	return mcp.NewTool("get_task",
		mcp.WithDescription("Get full details of a remote task including result."),
		mcp.WithString("task_id", mcp.Required(), mcp.Description("Task ID to look up")),
	)
}

func (s *Server) handleGetTask(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if s.rdb == nil {
		return mcp.NewToolResultText("Tasks are executed synchronously on the OpenShell backend. Results are returned directly from create_task."), nil
	}
	taskID, err := req.RequireString("task_id")
	if err != nil {
		return mcp.NewToolResultText("Error: task_id is required"), nil
	}
	t, err := s.rdb.HGetAll(ctx, s.teamKey("task:"+taskID)).Result()
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

func (s *Server) listAgentsTool() mcp.Tool {
	return mcp.NewTool("list_agents",
		mcp.WithDescription("Show all remote agents with status. Only shows agents running on remote infrastructure, not local agents."),
	)
}

func (s *Server) handleListAgents(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sandboxes, err := s.backend.ListSandboxes(ctx)
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Error listing agents: %v", err)), nil
	}
	if len(sandboxes) == 0 {
		return mcp.NewToolResultText("No agents running"), nil
	}

	var lines []string
	for _, sb := range sandboxes {
		idle := ""
		if sb.Idle {
			idle = " (idle)"
		}
		lines = append(lines, fmt.Sprintf("  %s: [%s]%s", sb.Name, sb.Status, idle))
	}
	return mcp.NewToolResultText(joinLines(lines)), nil
}

func (s *Server) sendMessageTool() mcp.Tool {
	return mcp.NewTool("send_message",
		mcp.WithDescription("Send a message to a remote agent."),
		mcp.WithString("to", mcp.Required(), mcp.Description("Agent ID to message")),
		mcp.WithString("text", mcp.Required(), mcp.Description("Message content")),
	)
}

func (s *Server) handleSendMessage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if s.rdb == nil {
		return mcp.NewToolResultText("send_message is not supported on the OpenShell backend. Use create_task to exec commands directly."), nil
	}
	to, err := req.RequireString("to")
	if err != nil {
		return mcp.NewToolResultText("Error: to is required"), nil
	}
	text, err := req.RequireString("text")
	if err != nil {
		return mcp.NewToolResultText("Error: text is required"), nil
	}

	if err := s.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: s.teamKey("inbox:" + to),
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

func (s *Server) scaleDownTool() mcp.Tool {
	return mcp.NewTool("scale_down",
		mcp.WithDescription("Remove all remote agent sandboxes to stop costs. "+
			"Call when all remote tasks are complete. Does not affect local agents."),
	)
}

func (s *Server) handleScaleDown(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sandboxes, err := s.backend.ListSandboxes(ctx)
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Error listing sandboxes: %v", err)), nil
	}

	if len(sandboxes) == 0 {
		return mcp.NewToolResultText("No remote sandboxes running"), nil
	}

	var deleted, failed []string
	for _, sb := range sandboxes {
		if err := s.backend.DeleteSandbox(ctx, sb.Name); err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", sb.Name, err))
		} else {
			deleted = append(deleted, sb.Name)
		}
	}

	msg := fmt.Sprintf("Scaled down %d sandbox(es): %s", len(deleted), strings.Join(deleted, ", "))
	if len(failed) > 0 {
		msg += fmt.Sprintf("\nFailed: %s", strings.Join(failed, ", "))
	}
	return mcp.NewToolResultText(msg), nil
}

func (s *Server) getCostsTool() mcp.Tool {
	return mcp.NewTool("get_costs",
		mcp.WithDescription("Show accumulated costs across all remote agents."),
	)
}

func (s *Server) handleGetCosts(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if s.rdb == nil {
		return mcp.NewToolResultText("Cost tracking is not yet available on the OpenShell backend."), nil
	}
	var costKeys []string
	var cursor uint64
	prefix := s.teamKey("cost:")
	for {
		batch, nextCursor, err := s.rdb.Scan(ctx, cursor, prefix+"*", 100).Result()
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
		c, err := s.rdb.HGetAll(ctx, key).Result()
		if err != nil {
			return mcp.NewToolResultText(fmt.Sprintf("Error reading cost hash %s: %v", key, err)), nil
		}
		agentID := key[len(prefix):]
		tokensIn, _ := strconv.ParseInt(c["tokens_in"], 10, 64)
		tokensOut, _ := strconv.ParseInt(c["tokens_out"], 10, 64)
		cost := float64(tokensIn)*0.015/1000 + float64(tokensOut)*0.075/1000
		total += cost
		lines = append(lines, fmt.Sprintf("  %s: $%.2f (%d in, %d out)", agentID, cost, tokensIn, tokensOut))
	}

	if len(lines) == 0 {
		lines = append(lines, "  No cost data recorded yet")
	}
	lines = append(lines, fmt.Sprintf("  TOTAL: $%.2f", total))
	if total > s.maxCost {
		lines = append(lines, fmt.Sprintf("  WARNING: exceeds limit of $%.2f", s.maxCost))
	}
	return mcp.NewToolResultText(joinLines(lines)), nil
}

func (s *Server) waitForTaskTool() mcp.Tool {
	return mcp.NewTool("wait_for_task",
		mcp.WithDescription("Block until a remote task reaches completed, failed, or dead status. "+
			"Use instead of polling get_task in a loop. Times out after 10 minutes."),
		mcp.WithString("task_id", mcp.Required(), mcp.Description("Task ID to wait for")),
	)
}

func (s *Server) handleWaitForTask(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if s.rdb == nil {
		return mcp.NewToolResultText("Tasks execute synchronously on the OpenShell backend. Results are returned directly from create_task."), nil
	}
	taskID, err := req.RequireString("task_id")
	if err != nil {
		return mcp.NewToolResultText("Error: task_id is required"), nil
	}

	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		t, err := s.rdb.HGetAll(ctx, s.teamKey("task:"+taskID)).Result()
		if err != nil {
			return mcp.NewToolResultText(fmt.Sprintf("Error reading task: %v", err)), nil
		}
		if len(t) == 0 {
			return mcp.NewToolResultText(fmt.Sprintf("Task %s not found", taskID)), nil
		}
		status := t["status"]
		if status == "completed" || status == "failed" || status == "dead" {
			return mcp.NewToolResultText(fmt.Sprintf("Task %s: status=%s summary=%s",
				taskID, status, t["result_summary"])), nil
		}
		time.Sleep(5 * time.Second)
	}
	return mcp.NewToolResultText(fmt.Sprintf("Task %s timed out after 10 minutes", taskID)), nil
}

func (s *Server) getTaskResultTool() mcp.Tool {
	return mcp.NewTool("get_task_result",
		mcp.WithDescription("Get the full result of a completed remote task. "+
			"get_task returns only a summary; use this to read the complete output."),
		mcp.WithString("task_id", mcp.Required(), mcp.Description("Task ID to fetch full result for")),
	)
}

func (s *Server) handleGetTaskResult(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if s.rdb == nil {
		return mcp.NewToolResultText("Tasks execute synchronously on the OpenShell backend. Results are returned directly from create_task."), nil
	}
	taskID, err := req.RequireString("task_id")
	if err != nil {
		return mcp.NewToolResultText("Error: task_id is required"), nil
	}
	val, err := s.rdb.Get(ctx, s.teamKey("task:"+taskID+":result_full")).Result()
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("No full result stored for task %s", taskID)), nil
	}
	return mcp.NewToolResultText(val), nil
}

func (s *Server) cleanupBranchesTool() mcp.Tool {
	return mcp.NewTool("cleanup_branches",
		mcp.WithDescription("Delete task branches from GitHub after work is complete. "+
			"Call after scale_down when you no longer need the agents' file output. "+
			"Only deletes branches named task-{id}. Never touches main or feature branches."),
		mcp.WithString("task_ids", mcp.Required(),
			mcp.Description("Comma-separated task IDs whose branches should be deleted (e.g. 'a3f2bc,b7d1ef')")),
	)
}

func (s *Server) handleCleanupBranches(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if s.githubToken == "" || s.githubRepo == "" {
		return mcp.NewToolResultText("Error: GITHUB_TOKEN and GITHUB_REPO must be set for branch cleanup"), nil
	}

	idsStr, err := req.RequireString("task_ids")
	if err != nil {
		return mcp.NewToolResultText("Error: task_ids is required"), nil
	}

	ids := splitComma(idsStr)
	var deleted, skipped []string

	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if !validTaskID.MatchString(id) {
			skipped = append(skipped, id+" (invalid task ID)")
			continue
		}
		branch := "task-" + id

		checkURL := fmt.Sprintf("https://api.github.com/repos/%s/git/ref/heads/%s", s.githubRepo, branch)
		checkReq, err := http.NewRequestWithContext(ctx, http.MethodGet, checkURL, nil)
		if err != nil {
			skipped = append(skipped, branch+" (request error)")
			continue
		}
		checkReq.Header.Set("Authorization", "Bearer "+s.githubToken)
		checkReq.Header.Set("Accept", "application/vnd.github+json")
		resp, err := http.DefaultClient.Do(checkReq)
		if err != nil || resp.StatusCode == 404 {
			if resp != nil {
				_ = resp.Body.Close()
			}
			skipped = append(skipped, branch+" (not found)")
			continue
		}
		_ = resp.Body.Close()

		delURL := fmt.Sprintf("https://api.github.com/repos/%s/git/refs/heads/%s", s.githubRepo, branch)
		delReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, delURL, nil)
		if err != nil {
			skipped = append(skipped, branch+" (request error)")
			continue
		}
		delReq.Header.Set("Authorization", "Bearer "+s.githubToken)
		delReq.Header.Set("Accept", "application/vnd.github+json")
		delResp, err := http.DefaultClient.Do(delReq)
		if err != nil || delResp.StatusCode != 204 {
			if delResp != nil {
				_ = delResp.Body.Close()
			}
			skipped = append(skipped, branch+" (delete failed)")
			continue
		}
		_ = delResp.Body.Close()
		deleted = append(deleted, branch)
	}

	return mcp.NewToolResultText(fmt.Sprintf(
		"Deleted: %s\nSkipped: %s",
		strings.Join(deleted, ", "),
		strings.Join(skipped, ", "),
	)), nil
}

// --- Helpers ---

func joinLines(lines []string) string {
	return strings.Join(lines, "\n")
}

func splitComma(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// truncate shortens s to at most max runes, appending "..." if truncated.
// Uses rune-safe slicing to avoid splitting multi-byte UTF-8 sequences.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}

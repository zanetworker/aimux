package provider

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/debuglog"
	"github.com/zanetworker/aimux/internal/subagent"
	"github.com/zanetworker/aimux/internal/task"
	"github.com/zanetworker/aimux/internal/trace"
	"github.com/zanetworker/aimux/pkg/rediskeys"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// K8sConfig holds connection settings for the Kubernetes provider.
// All fields may be populated from ~/.aimux/config.yaml under the
// "kubernetes" key.
type K8sConfig struct {
	RedisURL   string // e.g. "redis://:pass@localhost:6380"
	TeamID     string // e.g. "my-team"
	Namespace  string // K8s namespace, e.g. "agents"
	Kubeconfig string // path to kubeconfig; empty = in-cluster or KUBECONFIG env
}

// K8s is a Provider implementation for Kubernetes-hosted AI agents.
// Agents are discovered via Redis heartbeats rather than local process scanning.
// This provider never embeds a PTY — agents run in pods and are viewed trace-only.
type K8s struct {
	cfg K8sConfig
	mu  sync.Mutex
	rdb *redis.Client

	// Circuit breaker: skip Redis calls for a cooldown period after failure.
	// Prevents the TUI from freezing when Redis is unreachable.
	lastRedisErr  time.Time
	redisCooldown time.Duration
}

// NewK8s constructs a K8s provider with the given configuration.
func NewK8s(cfg K8sConfig) *K8s {
	return &K8s{cfg: cfg, redisCooldown: 30 * time.Second}
}

// redisClient returns the shared Redis client, creating it lazily on first use.
// Thread-safe via mutex. Returns an error if RedisURL is not configured or the
// server is unreachable on first connection.
func (k *K8s) redisClient() (*redis.Client, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	// Circuit breaker: skip Redis for cooldown period after a failure.
	if !k.lastRedisErr.IsZero() && time.Since(k.lastRedisErr) < k.redisCooldown {
		return nil, fmt.Errorf("redis in cooldown (failed %s ago)", time.Since(k.lastRedisErr).Truncate(time.Second))
	}

	if k.rdb != nil {
		return k.rdb, nil
	}
	if k.cfg.RedisURL == "" {
		debuglog.Log("k8s: redis not configured")
		return nil, fmt.Errorf("redis not configured")
	}
	rdb, err := newRedisClient(k.cfg.RedisURL)
	if err != nil {
		k.lastRedisErr = time.Now()
		debuglog.Log("k8s: redis connect failed: %v", err)
		return nil, err
	}
	k.rdb = rdb
	k.lastRedisErr = time.Time{} // reset on success
	debuglog.Log("k8s: redis connected")
	return k.rdb, nil
}

// markRedisErr records a Redis command failure and triggers the circuit breaker cooldown.
// Also closes the cached client so the next attempt creates a fresh connection.
func (k *K8s) markRedisErr() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.lastRedisErr = time.Now()
	if k.rdb != nil {
		k.rdb.Close()
		k.rdb = nil
	}
	debuglog.Log("k8s: redis error, circuit breaker active for %s", k.redisCooldown)
}

// Close shuts down the shared Redis client if one was created.
// Safe to call multiple times or when no client was initialized.
func (k *K8s) Close() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.rdb != nil {
		k.rdb.Close()
		k.rdb = nil
	}
}

// K8s maps its infrastructure to the generic HealthStatus:
//   CoordOK   = Redis connectivity
//   ComputeOK = Kubernetes API server
//   Workloads = agent Deployments in namespace

// CheckHealth probes Redis and K8s connectivity. Designed to be called once
// when the :new picker opens — not on every tick. Each check has a 1-second timeout.
func (k *K8s) CheckHealth() HealthStatus {
	status := HealthStatus{
		Configured: k.cfg.RedisURL != "",
	}
	if !status.Configured {
		status.CoordErr = "redis_url not set in ~/.aimux/config.yaml"
		status.ComputeErr = "kubernetes not configured"
		return status
	}

	// Check coordination layer (Redis)
	rdb, err := k.redisClient()
	if err != nil {
		status.CoordErr = "cannot connect — check redis_url in config"
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		if err := rdb.Ping(ctx).Err(); err != nil {
			status.CoordErr = "not responding — is Redis running?"
			k.markRedisErr()
		} else {
			status.CoordOK = true
		}
	}

	// Check compute layer (K8s API)
	client, err := k.kubeClient()
	if err != nil {
		status.ComputeErr = "cannot connect — check kubeconfig"
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		deploys, err := client.AppsV1().Deployments(k.cfg.Namespace).List(ctx, metav1.ListOptions{
			LabelSelector: "team-component in (agent,session)",
		})
		if err != nil {
			status.ComputeErr = err.Error()
		} else {
			status.ComputeOK = true
			for _, d := range deploys.Items {
				status.Workloads = append(status.Workloads, d.Name)
			}
		}
	}

	return status
}

// Status returns a human-readable connection status for display in the TUI.
func (k *K8s) Status() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.cfg.RedisURL == "" {
		return "not configured"
	}
	if !k.lastRedisErr.IsZero() && time.Since(k.lastRedisErr) < k.redisCooldown {
		return fmt.Sprintf("disconnected (retry in %s)", (k.redisCooldown - time.Since(k.lastRedisErr)).Truncate(time.Second))
	}
	if k.rdb != nil {
		return "connected"
	}
	return "connecting"
}

// Name returns the provider identifier used in config and display.
func (k *K8s) Name() string { return "k8s" }

// Discover reads Redis heartbeats and agent metadata to enumerate live and
// recently-seen K8s agents. Returns (nil, nil) when RedisURL is not configured
// so that a missing Redis setup never crashes aimux on startup.
//
// Status assignment:
//   - Active:  heartbeat < 30 s ago
//   - Idle:    heartbeat 30–60 s ago
//   - Unknown: heartbeat > 60 s ago (dead but still shown)
func (k *K8s) Discover() ([]agent.Agent, error) {
	var agents []agent.Agent

	// Phase 1: Redis heartbeats (coordinator-managed agents).
	agents = append(agents, k.discoverFromRedis()...)

	// Phase 2: K8s API session pods (sleep-infinity pods for kubectl exec).
	agents = mergeAgents(agents, k.discoverSessionPods())

	// Stable sort: active first, then idle, then unknown; alphabetical within tier.
	sort.SliceStable(agents, func(i, j int) bool {
		if agents[i].Status != agents[j].Status {
			return agents[i].Status < agents[j].Status
		}
		return agents[i].Name < agents[j].Name
	})

	return agents, nil
}

// discoverFromRedis queries Redis heartbeats for coordinator-managed agents.
// Returns nil (not error) when Redis is unavailable.
func (k *K8s) discoverFromRedis() []agent.Agent {
	rdb, err := k.redisClient()
	if err != nil {
		debuglog.Log("k8s: redis discover skipped: %v", err)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	heartbeats, err := rdb.HGetAll(ctx, rediskeys.Heartbeat(k.cfg.TeamID)).Result()
	if err != nil {
		debuglog.Log("k8s: redis heartbeat fetch failed: %v", err)
		k.markRedisErr()
		return nil
	}
	debuglog.Log("k8s: redis discover found %d heartbeats", len(heartbeats))

	now := time.Now()
	var agents []agent.Agent

	for agentID, tsStr := range heartbeats {
		ts, err := strconv.ParseInt(tsStr, 10, 64)
		if err != nil {
			continue
		}
		lastSeen := time.Unix(ts, 0)
		age := now.Sub(lastSeen)

		var status agent.Status
		switch {
		case age < 30*time.Second:
			status = agent.StatusActive
		case age < 60*time.Second:
			status = agent.StatusIdle
		default:
			status = agent.StatusUnknown
		}

		// Fetch per-agent metadata hash.
		meta, err := rdb.HGetAll(ctx, rediskeys.Agent(k.cfg.TeamID, agentID)).Result()
		if err != nil {
			meta = map[string]string{}
		}

		role := meta["role"]
		model := meta["model"]
		namespace := meta["namespace"]
		if namespace == "" {
			namespace = k.cfg.Namespace
		}

		registeredAt := time.Time{}
		if raw := meta["registered_at"]; raw != "" {
			if epoch, err := strconv.ParseInt(raw, 10, 64); err == nil {
				registeredAt = time.Unix(epoch, 0)
			}
		}

		// Fetch cost data.
		var tokensIn, tokensOut int64
		costMeta, err := rdb.HGetAll(ctx, rediskeys.Cost(k.cfg.TeamID, agentID)).Result()
		if err == nil {
			tokensIn, _ = strconv.ParseInt(costMeta["tokens_in"], 10, 64)
			tokensOut, _ = strconv.ParseInt(costMeta["tokens_out"], 10, 64)
		}

		// Find the current task assigned to this agent.
		taskSubject := k.findCurrentTask(ctx, rdb, agentID)

		displayName := role
		if displayName == "" {
			displayName = agentID
		}

		a := agent.Agent{
			PID:          0, // no local process
			SessionID:    agentID,
			Name:         displayName,
			ProviderName: "k8s",
			Model:        model,
			WorkingDir:   "k8s://" + namespace + "/" + agentID,
			Status:       status,
			TeamName:     k.cfg.TeamID,
			LastActivity: lastSeen,
			StartTime:    registeredAt,
			TaskSubject:  taskSubject,
			TokensIn:     tokensIn,
			TokensOut:    tokensOut,
			Source:       agent.SourceSDK,
		}

		agents = append(agents, a)
	}

	return agents
}

// findCurrentTask looks for the most recent claimed or in-progress task
// assigned to agentID. Returns the first 60 characters of the prompt, or "".
// Errors are swallowed — task subject is best-effort display data.
// discoverSessionPods queries the K8s API for running session pods (label
// team-component=session). All running pods are shown — idle pods represent
// available capacity for new sessions. Status is set to Idle for pods
// without an active tmux session, Active for pods with one.
func (k *K8s) discoverSessionPods() []agent.Agent {
	client, err := k.kubeClient()
	if err != nil {
		debuglog.Log("k8s: pod discover skipped (no kube client): %v", err)
		return nil
	}

	namespace := k.cfg.Namespace
	if namespace == "" {
		namespace = "agents"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "team-component=session",
		FieldSelector: "status.phase!=Succeeded,status.phase!=Failed",
	})
	if err != nil {
		debuglog.Log("k8s: pod discover failed: %v", err)
		return nil
	}

	var agents []agent.Agent
	for _, pod := range pods.Items {
		// ProviderName is the agent type (claude, gemini), not "k8s".
		// The LOC column derives "k8s" from the WorkingDir prefix.
		providerLabel := pod.Labels["provider"]
		if providerLabel == "" {
			providerLabel = "claude"
		}

		startTime := pod.CreationTimestamp.Time

		a := agent.Agent{
			PID:          0,
			SessionID:    "pod-" + pod.Name,
			Name:         pod.Name,
			ProviderName: providerLabel,
			Model:        pod.Labels["model"],
			WorkingDir:   "k8s://" + namespace + "/" + pod.Name,
			Status:       agent.StatusIdle,
			TeamName:     k.cfg.TeamID,
			StartTime:    startTime,
			LastActivity: startTime,
			Source:       agent.SourceSDK,
		}
		a.Status = podStatus(pod)
		if a.Status == agent.StatusError {
			a.LastAction = podErrorReason(pod)
		}
		agents = append(agents, a)
	}

	debuglog.Log("k8s: pod discover found %d session pods", len(agents))
	return agents
}

// podStatus derives agent.Status from pod container states.
func podStatus(pod corev1.Pod) agent.Status {
	// Check init containers first.
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Waiting != nil {
			switch cs.State.Waiting.Reason {
			case "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull":
				return agent.StatusError
			}
			return agent.StatusIdle // still initializing
		}
	}
	// Check main containers.
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil {
			switch cs.State.Waiting.Reason {
			case "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull":
				return agent.StatusError
			}
			return agent.StatusIdle
		}
		if cs.State.Running != nil && cs.Ready {
			return agent.StatusActive
		}
	}
	if pod.Status.Phase == corev1.PodPending {
		return agent.StatusIdle
	}
	return agent.StatusIdle
}

// podErrorReason returns a human-readable reason for an unhealthy pod.
func podErrorReason(pod corev1.Pod) string {
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return "init:" + cs.State.Waiting.Reason
		}
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return cs.State.Waiting.Reason
		}
	}
	return string(pod.Status.Phase)
}

// mergeAgents combines two agent lists, deduplicating by SessionID.
// If both lists contain an agent with the same SessionID, the first list wins
// (Redis heartbeat data is richer than pod metadata alone).
func mergeAgents(primary, secondary []agent.Agent) []agent.Agent {
	if len(secondary) == 0 {
		return primary
	}
	seen := make(map[string]bool, len(primary))
	for _, a := range primary {
		seen[a.SessionID] = true
	}
	for _, a := range secondary {
		if !seen[a.SessionID] {
			primary = append(primary, a)
		}
	}
	return primary
}

func (k *K8s) findCurrentTask(ctx context.Context, rdb *redis.Client, agentID string) string {
	// Use the TasksAll sorted set for ordered access (avoids SCAN).
	taskIDs, err := rdb.ZRangeArgs(ctx, redis.ZRangeArgs{
		Key:   rediskeys.TasksAll(k.cfg.TeamID),
		Start: 0,
		Stop:  99,
		Rev:   true,
	}).Result()
	if err != nil {
		return ""
	}

	for _, taskID := range taskIDs {
		fields, err := rdb.HGetAll(ctx, rediskeys.Task(k.cfg.TeamID, taskID)).Result()
		if err != nil {
			continue
		}
		if fields["assignee"] != agentID {
			continue
		}
		status := fields["status"]
		if status != "claimed" && status != "in_progress" {
			continue
		}
		prompt := fields["prompt"]
		if len(prompt) > 60 {
			prompt = prompt[:60]
		}
		return prompt
	}
	return ""
}

// CanEmbed returns false — K8s agents run in pods and cannot be embedded
// as a local PTY. The TUI shows trace-only view with a jump-out option.
func (k *K8s) CanEmbed() bool { return false }

// ResumeCommand returns nil. K8s agents cannot be resumed as local processes.
func (k *K8s) ResumeCommand(_ agent.Agent) *exec.Cmd { return nil }

// FindSessionFile returns a "k8s://<sessionID>" sentinel so the trace pane
// can pass it to ParseTrace, which uses it to query Redis task history.
func (k *K8s) FindSessionFile(a agent.Agent) string {
	if a.SessionID == "" {
		return ""
	}
	return "k8s://" + a.SessionID
}

// RecentDirs returns nil — K8s agents do not work on local directories.
func (k *K8s) RecentDirs(_ int) []RecentDir { return nil }

// SpawnCommand returns nil — K8s agents are not spawned locally.
func (k *K8s) SpawnCommand(_, _, _ string) *exec.Cmd { return nil }

// SpawnArgs describes the models and modes available when launching a K8s agent.
func (k *K8s) SpawnArgs() SpawnArgs {
	return SpawnArgs{
		Models: []string{"claude-opus-4-6", "claude-sonnet-4-6", "claude-haiku-4-5", "gemini-2.0-flash"},
		Modes:  []string{"coder", "researcher", "reviewer"},
	}
}

// ParseTrace reads completed task history from Redis for the agent identified
// by filePath. For this provider filePath is unused (FindSessionFile returns "");
// the caller passes the agent's SessionID via the agent struct. Because the
// current Provider interface passes only filePath, we return an informational
// turn explaining how to view task history.
//
// Design note: full task history requires the agent's SessionID. The interface
// contract passes filePath (empty for K8s). Returning a descriptive single Turn
// gives the user useful information without panicking or returning an error that
// would suppress the trace view entirely.
// ParseTrace reads completed task history from Redis using the agentID
// encoded in filePath as "k8s://<agentID>". Each completed task becomes
// one trace Turn. Returns an informational Turn if Redis is unconfigured
// or filePath is not a k8s:// sentinel.
func (k *K8s) ParseTrace(filePath string) ([]trace.Turn, error) {
	if k.cfg.RedisURL == "" {
		return []trace.Turn{{
			Number:      1,
			Timestamp:   time.Now(),
			OutputLines: []string{"K8s provider: Redis not configured. Set redis_url in ~/.aimux/config.yaml."},
		}}, nil
	}
	if !strings.HasPrefix(filePath, "k8s://") {
		return []trace.Turn{{
			Number:      1,
			Timestamp:   time.Now(),
			OutputLines: []string{"K8s agent: no task history available yet."},
		}}, nil
	}

	agentID := strings.TrimPrefix(filePath, "k8s://")

	rdb, err := k.redisClient()
	if err != nil {
		debuglog.Log("k8s: ParseTrace redis connect failed: %v", err)
		return []trace.Turn{{
			Number:      1,
			Timestamp:   time.Now(),
			OutputLines: []string{fmt.Sprintf("K8s: cannot connect to Redis: %v", err)},
		}}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// Read all task IDs in creation order from the tasks:all sorted set.
	taskIDs, err := rdb.ZRangeArgs(ctx, redis.ZRangeArgs{
		Key:   rediskeys.TasksAll(k.cfg.TeamID),
		Start: 0,
		Stop:  -1,
	}).Result()
	if err != nil {
		debuglog.Log("k8s: ParseTrace task scan failed: %v", err)
		k.markRedisErr()
		return []trace.Turn{{
			Number:      1,
			Timestamp:   time.Now(),
			OutputLines: []string{fmt.Sprintf("K8s: Redis query failed: %v", err)},
		}}, nil
	}

	var turns []trace.Turn
	for i, taskID := range taskIDs {
		fields, err := rdb.HGetAll(ctx, rediskeys.Task(k.cfg.TeamID, taskID)).Result()
		if err != nil || len(fields) == 0 {
			continue
		}
		// Show all tasks for this agent, or all tasks if agent filter is not applicable.
		if fields["assignee"] != agentID && fields["assignee"] != "" {
			continue
		}

		completedAt := time.Now()
		if ts, err := strconv.ParseFloat(fields["completed_at"], 64); err == nil && ts > 0 {
			completedAt = time.Unix(int64(ts), 0)
		}

		status := fields["status"]
		summary := fields["result_summary"]
		prompt := fields["prompt"]
		if len(prompt) > 80 {
			prompt = prompt[:80] + "..."
		}

		lines := []string{
			fmt.Sprintf("[%s] task %s — %s", status, taskID, prompt),
		}
		if summary != "" {
			lines = append(lines, "  "+summary)
		}
		if errMsg := fields["error"]; errMsg != "" {
			lines = append(lines, "  error: "+errMsg)
		}

		turns = append(turns, trace.Turn{
			Number:      i + 1,
			Timestamp:   completedAt,
			OutputLines: lines,
		})
	}

	if len(turns) == 0 {
		turns = []trace.Turn{{
			Number:      1,
			Timestamp:   time.Now(),
			OutputLines: []string{fmt.Sprintf("No tasks found for agent %s in team %s.", agentID, k.cfg.TeamID)},
		}}
	}
	return turns, nil
}

// SendMessage writes a message to the agent's Redis inbox stream.
// Implements the optional Messenger interface.
func (k *K8s) SendMessage(agentID, text string) error {
	rdb, err := k.redisClient()
	if err != nil {
		return fmt.Errorf("k8s SendMessage: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	return rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: rediskeys.Inbox(k.cfg.TeamID, agentID),
		MaxLen: 1000,
		Approx: true,
		Values: map[string]any{
			"from":      "lead",
			"text":      text,
			"timestamp": fmt.Sprintf("%d", time.Now().Unix()),
		},
	}).Err()
}

// ListTasks returns all tasks for the configured team by delegating to
// task.LoadFromRedis. Returns (nil, nil) when Redis is not configured.
// Implements the optional TaskLister interface.
func (k *K8s) ListTasks() ([]task.Task, error) {
	rdb, err := k.redisClient()
	if err != nil {
		// Unconfigured Redis returns empty task list, not an error.
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	tasks, err := task.LoadFromRedis(ctx, rdb, k.cfg.TeamID)
	if err != nil {
		debuglog.Log("k8s: ListTasks failed: %v", err)
		k.markRedisErr()
		return nil, nil
	}
	return tasks, nil
}

// GetTaskResult returns the full result reference for a task by delegating to
// task.GetFullResult. Returns ("", nil) when Redis is not configured.
// Implements the optional TaskLister interface.
func (k *K8s) GetTaskResult(taskID string) (string, error) {
	rdb, err := k.redisClient()
	if err != nil {
		// Unconfigured Redis returns empty result, not an error.
		return "", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	return task.GetFullResult(ctx, rdb, k.cfg.TeamID, taskID)
}

// SpawnRemote scales up the Kubernetes Deployment named "agent-{provider}-{role}"
// to the specified replica count. Returns an error if the kube client cannot be
// constructed or the deployment does not exist.
// Implements the optional Spawner interface.
func (k *K8s) SpawnRemote(provider, role string, count int) error {
	clientset, err := k.kubeClient()
	if err != nil {
		return fmt.Errorf("cannot connect to cluster — check kubeconfig")
	}

	namespace := k.cfg.Namespace
	if namespace == "" {
		namespace = "agents"
	}

	deployName := spawnDeploymentName(provider, role)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	deploy, err := clientset.AppsV1().Deployments(namespace).Get(ctx, deployName, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		// Auto-create the deployment if it doesn't exist.
		debuglog.Log("k8s: deployment %q not found, creating automatically", deployName)
		deploy, err = k.createAgentDeployment(ctx, clientset, namespace, provider, role)
		if err != nil {
			return fmt.Errorf("cannot create deployment %q: %w", deployName, err)
		}
	} else if err != nil {
		return fmt.Errorf("cannot get deployment %q: %w", deployName, err)
	}

	replicas := int32(count)
	if deploy.Spec.Replicas != nil {
		replicas = *deploy.Spec.Replicas + int32(count)
	}
	deploy.Spec.Replicas = &replicas
	_, err = clientset.AppsV1().Deployments(namespace).Update(ctx, deploy, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("cannot scale %q — check RBAC permissions", deployName)
	}
	return nil
}

// createAgentDeployment builds and creates a Deployment for the given provider
// and role. The deployment starts at 0 replicas (SpawnRemote scales it up).
//
// The deployment is minimal — no service accounts, no repo cloning, no Redis.
// Auth secrets (llm-keys, gcp-adc) are referenced as optional so the pod
// starts even if they don't exist. For Vertex AI, env vars are forwarded
// by aimux at attach time (sessions) or baked in (tasks via ensureAuthSecret).
func (k *K8s) createAgentDeployment(ctx context.Context, clientset kubernetes.Interface, namespace, provider, role string) (*appsv1.Deployment, error) {
	deployName := spawnDeploymentName(provider, role)
	image := k.imageForProvider(provider)
	replicas := int32(0)

	// Ensure the namespace exists.
	k.ensureNamespace(ctx, clientset, namespace)

	// Ensure auth secrets exist from local env vars.
	k.ensureAuthSecrets(ctx, clientset, namespace)

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deployName,
			Namespace: namespace,
			Labels: map[string]string{
				"app":                          deployName,
				"team-component":               role,
				"provider":                     provider,
				"app.kubernetes.io/part-of":    "k8s-agents",
				"app.kubernetes.io/managed-by": "aimux",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": deployName},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":                       deployName,
						"team-component":            role,
						"provider":                  provider,
						"app.kubernetes.io/part-of": "k8s-agents",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:    role,
						Image:   image,
						Command: []string{"sleep", "infinity"},
						Env: []corev1.EnvVar{
							// Vertex AI auth via mounted ADC file
							{Name: "GOOGLE_APPLICATION_CREDENTIALS", Value: "/var/secrets/gcp/adc.json"},
							// API key auth via secret
							{Name: "ANTHROPIC_API_KEY", ValueFrom: &corev1.EnvVarSource{
								SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{Name: "llm-keys"},
									Key:                  "anthropic",
									Optional:             boolPtr(true),
								},
							}},
							{Name: "TERM", Value: "xterm-256color"},
						},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourceCPU:    resource.MustParse("500m"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceMemory: resource.MustParse("2Gi"),
								corev1.ResourceCPU:    resource.MustParse("1000m"),
							},
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "gcp-adc", MountPath: "/var/secrets/gcp", ReadOnly: true},
						},
					}},
					Volumes: []corev1.Volume{
						{Name: "gcp-adc", VolumeSource: corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{
								SecretName: "gcp-adc",
								Optional:   boolPtr(true),
							},
						}},
					},
				},
			},
		},
	}

	debuglog.Log("k8s: creating deployment %s/%s (image=%s)", namespace, deployName, image)
	return clientset.AppsV1().Deployments(namespace).Create(ctx, deploy, metav1.CreateOptions{})
}

// ensureNamespace creates the namespace if it doesn't exist.
func (k *K8s) ensureNamespace(ctx context.Context, clientset kubernetes.Interface, namespace string) {
	_, err := clientset.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   namespace,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "aimux"},
			},
		}
		if _, err := clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil {
			debuglog.Log("k8s: failed to create namespace %s: %v", namespace, err)
		} else {
			debuglog.Log("k8s: created namespace %s", namespace)
		}
	}
}

// ensureAuthSecrets creates auth secrets from local environment if they don't
// already exist in the cluster. This lets users point at a bare cluster
// without manual kubectl create secret commands.
func (k *K8s) ensureAuthSecrets(ctx context.Context, clientset kubernetes.Interface, namespace string) {
	// GCP ADC: copy local credentials file into a secret
	if adcPath := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); adcPath != "" {
		_, err := clientset.CoreV1().Secrets(namespace).Get(ctx, "gcp-adc", metav1.GetOptions{})
		if errors.IsNotFound(err) {
			data, readErr := os.ReadFile(adcPath)
			if readErr == nil {
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gcp-adc",
						Namespace: namespace,
						Labels:    map[string]string{"app.kubernetes.io/managed-by": "aimux"},
					},
					Data: map[string][]byte{"adc.json": data},
				}
				if _, err := clientset.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
					debuglog.Log("k8s: failed to create gcp-adc secret: %v", err)
				} else {
					debuglog.Log("k8s: created gcp-adc secret from %s", adcPath)
				}
			}
		}
	}

	// API key: create llm-keys secret from env
	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		_, err := clientset.CoreV1().Secrets(namespace).Get(ctx, "llm-keys", metav1.GetOptions{})
		if errors.IsNotFound(err) {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "llm-keys",
					Namespace: namespace,
					Labels:    map[string]string{"app.kubernetes.io/managed-by": "aimux"},
				},
				Data: map[string][]byte{"anthropic": []byte(apiKey)},
			}
			if _, err := clientset.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
				debuglog.Log("k8s: failed to create llm-keys secret: %v", err)
			} else {
				debuglog.Log("k8s: created llm-keys secret from ANTHROPIC_API_KEY")
			}
		}
	}
}

// imageForProvider returns the container image for the given provider.
func (k *K8s) imageForProvider(provider string) string {
	switch provider {
	case "claude":
		return "quay.io/azaalouk/claude-session:latest"
	case "gemini":
		return "quay.io/azaalouk/gemini-session:latest"
	default:
		return "quay.io/azaalouk/" + provider + "-session:latest"
	}
}

func boolPtr(b bool) *bool { return &b }

// SpawnSession scales up the session deployment by one replica, waits for the
// new pod to be Running, and returns its name and namespace. The caller uses
// the pod name to attach via KubectlExecBackend.
func (k *K8s) SpawnSession(providerName string) (podName, namespace string, err error) {
	clientset, err := k.kubeClient()
	if err != nil {
		return "", "", fmt.Errorf("cannot connect to cluster: %w", err)
	}

	namespace = k.cfg.Namespace
	if namespace == "" {
		namespace = "agents"
	}

	deployName := spawnDeploymentName(providerName, "session")

	// Snapshot existing pod names before scaling up.
	existingPods := make(map[string]bool)
	podList, err := clientset.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: "app=" + deployName,
	})
	if err == nil {
		for _, p := range podList.Items {
			existingPods[p.Name] = true
		}
	}

	// Scale up by 1.
	if err := k.SpawnRemote(providerName, "session", 1); err != nil {
		return "", "", err
	}
	debuglog.Log("k8s: SpawnSession scaled up %s, waiting for new pod...", deployName)

	// Poll for the new pod (one that wasn't in the snapshot).
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", "", fmt.Errorf("timed out waiting for pod to start (60s)")
		case <-ticker.C:
			pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: "app=" + deployName,
				FieldSelector: "status.phase=Running",
			})
			if err != nil {
				continue
			}
			for _, p := range pods.Items {
				if !existingPods[p.Name] {
					debuglog.Log("k8s: SpawnSession new pod ready: %s", p.Name)
					return p.Name, namespace, nil
				}
			}
		}
	}
}

// ScaleDown scales the Kubernetes Deployment named "agent-{provider}-{role}"
// to 0 replicas. Returns an error if the kube client cannot be constructed
// or the deployment does not exist.
// Implements the optional Spawner interface.
func (k *K8s) ScaleDown(provider, role string) error {
	clientset, err := k.kubeClient()
	if err != nil {
		return fmt.Errorf("cannot connect to cluster — check kubeconfig")
	}

	namespace := k.cfg.Namespace
	if namespace == "" {
		namespace = "agents"
	}

	deployName := spawnDeploymentName(provider, role)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	deploy, err := clientset.AppsV1().Deployments(namespace).Get(ctx, deployName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("deployment %q not found — run: kubectl apply -f deploy/k8s/", deployName)
	}

	zero := int32(0)
	deploy.Spec.Replicas = &zero
	_, err = clientset.AppsV1().Deployments(namespace).Update(ctx, deploy, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("cannot scale down %q — check RBAC permissions", deployName)
	}
	return nil
}

// ScaleDownOne decrements the replica count of the deployment by 1 (min 0).
// Used when deleting a single session pod.
func (k *K8s) ScaleDownOne(providerName, role string) error {
	clientset, err := k.kubeClient()
	if err != nil {
		return fmt.Errorf("cannot connect to cluster: %w", err)
	}

	namespace := k.cfg.Namespace
	if namespace == "" {
		namespace = "agents"
	}

	deployName := spawnDeploymentName(providerName, role)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	deploy, err := clientset.AppsV1().Deployments(namespace).Get(ctx, deployName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	current := int32(1)
	if deploy.Spec.Replicas != nil {
		current = *deploy.Spec.Replicas
	}
	desired := current - 1
	if desired < 0 {
		desired = 0
	}
	deploy.Spec.Replicas = &desired
	_, err = clientset.AppsV1().Deployments(namespace).Update(ctx, deploy, metav1.UpdateOptions{})
	return err
}

// spawnDeploymentName constructs the Kubernetes deployment name for spawning.
// Convention: "agent-{provider}-{role}", e.g. "agent-claude-coder".
func spawnDeploymentName(provider, role string) string {
	return "agent-" + provider + "-" + role
}

// OTELEnv returns "" — K8s agents configure their own OTEL settings via
// pod environment variables managed outside aimux.
func (k *K8s) OTELEnv(_ string) string { return "" }

// OTELServiceName returns the service.name for K8s agents in OTEL telemetry.
func (k *K8s) OTELServiceName() string { return "k8s-agent" }

// SubagentAttrKeys returns zero value — K8s agents do not emit subagent
// identity attributes in the format aimux's OTEL receiver understands.
func (k *K8s) SubagentAttrKeys() subagent.AttrKeys { return subagent.AttrKeys{} }

// Kill scales down the Kubernetes deployment that runs the target agent.
// The deployment name is derived from "agent-" + provider + "-" + role.
// If no matching deployment is found, the error is returned to the caller.
// If the K8s client cannot be constructed (bad kubeconfig), the error is
// returned rather than panicking.
func (k *K8s) Kill(a agent.Agent) error {
	clientset, err := k.kubeClient()
	if err != nil {
		return fmt.Errorf("k8s Kill: build kube client: %w", err)
	}

	namespace := k.cfg.Namespace
	if namespace == "" {
		namespace = "agents"
	}

	// Derive deployment name from role (Name field holds the role).
	deployName := k.deploymentName(a)

	ctx, cancel := context.WithTimeout(context.Background(), 11*time.Second)
	defer cancel()

	deploy, err := clientset.AppsV1().Deployments(namespace).Get(ctx, deployName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("k8s Kill: get deployment %q in namespace %q: %w", deployName, namespace, err)
	}

	current := int32(1)
	if deploy.Spec.Replicas != nil {
		current = *deploy.Spec.Replicas
	}
	desired := current - 1
	if desired < 0 {
		desired = 0
	}

	deploy.Spec.Replicas = &desired
	_, err = clientset.AppsV1().Deployments(namespace).Update(ctx, deploy, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("k8s Kill: scale deployment %q to %d: %w", deployName, desired, err)
	}
	return nil
}

// deploymentName derives the Kubernetes deployment name from agent metadata.
// Convention: "agent-" + providerFromSessionID + "-" + role
// e.g. agent ID "claude-coder-abc123" with role "coder" -> "agent-claude-coder"
func (k *K8s) deploymentName(a agent.Agent) string {
	// SessionID may encode the provider prefix (e.g. "claude-coder-abc123").
	// Extract the first segment before the role to identify the provider prefix.
	parts := strings.SplitN(a.SessionID, "-", 3)
	if len(parts) >= 2 {
		return "agent-" + parts[0] + "-" + a.Name
	}
	return "agent-" + a.Name
}

// kubeClient builds a Kubernetes clientset from the configured kubeconfig path.
// Falls back to in-cluster config when Kubeconfig is empty and the process is
// running inside a pod.
func (k *K8s) kubeClient() (*kubernetes.Clientset, error) {
	var restCfg *rest.Config
	var err error

	if k.cfg.Kubeconfig != "" {
		restCfg, err = clientcmd.BuildConfigFromFlags("", k.cfg.Kubeconfig)
	} else {
		// Try in-cluster first, then fall back to default kubeconfig locations.
		restCfg, err = rest.InClusterConfig()
		if err != nil {
			loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
			restCfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
				loadingRules,
				&clientcmd.ConfigOverrides{},
			).ClientConfig()
		}
	}
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("create kubernetes clientset: %w", err)
	}
	return clientset, nil
}

// newRedisClient parses a Redis URL and returns a connected client.
// Returns an error when the URL is malformed or the server is unreachable.
func newRedisClient(url string) (*redis.Client, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis URL: %w", err)
	}
	// Short timeouts so commands fail fast; circuit breaker handles retries.
	opts.DialTimeout = 1 * time.Second
	opts.ReadTimeout = 1 * time.Second
	opts.WriteTimeout = 1 * time.Second

	// Prevent the connection pool from spamming stderr when Redis is down.
	// The pool's background goroutine retries dials and logs failures via
	// the global redis logger. A small pool + limited retries keeps noise
	// minimal, and the nopLogger silences the remaining pool-level output.
	opts.PoolSize = 2
	opts.MinIdleConns = 0
	opts.MaxRetries = 1

	rdb := redis.NewClient(opts)
	return rdb, nil
}

func init() {
	// Silence the go-redis internal logger. Without this, the connection
	// pool writes "failed to dial after N attempts" directly to stderr,
	// which corrupts the TUI. Errors are surfaced via the circuit breaker
	// in redisClient()/markRedisErr() instead.
	redis.SetLogger(nopRedisLogger{})
}

// nopRedisLogger implements redis/internal.Logging and discards all output.
type nopRedisLogger struct{}

func (nopRedisLogger) Printf(_ context.Context, _ string, _ ...interface{}) {}

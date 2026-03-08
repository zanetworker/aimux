package provider

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/subagent"
	"github.com/zanetworker/aimux/internal/trace"
	"github.com/zanetworker/aimux/pkg/rediskeys"
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
}

// NewK8s constructs a K8s provider with the given configuration.
func NewK8s(cfg K8sConfig) *K8s {
	return &K8s{cfg: cfg}
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
	if k.cfg.RedisURL == "" {
		return nil, nil
	}

	rdb, err := newRedisClient(k.cfg.RedisURL)
	if err != nil {
		// Unreachable Redis must not crash the TUI.
		return nil, nil
	}
	defer rdb.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Fetch all agent heartbeats: field=agentID, value=unix timestamp.
	heartbeats, err := rdb.HGetAll(ctx, rediskeys.Heartbeat(k.cfg.TeamID)).Result()
	if err != nil {
		// Redis reachable but key missing or transient error — skip silently.
		return nil, nil
	}

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

	// Stable sort: active first, then idle, then unknown; alphabetical within tier.
	sort.SliceStable(agents, func(i, j int) bool {
		if agents[i].Status != agents[j].Status {
			return agents[i].Status < agents[j].Status
		}
		return agents[i].Name < agents[j].Name
	})

	return agents, nil
}

// findCurrentTask looks for the most recent claimed or in-progress task
// assigned to agentID. Returns the first 60 characters of the prompt, or "".
// Errors are swallowed — task subject is best-effort display data.
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

	opt, err := redis.ParseURL(k.cfg.RedisURL)
	if err != nil {
		return nil, fmt.Errorf("k8s ParseTrace: parse Redis URL: %w", err)
	}
	rdb := redis.NewClient(opt)
	defer rdb.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Read all task IDs in creation order from the tasks:all sorted set.
	taskIDs, err := rdb.ZRangeArgs(ctx, redis.ZRangeArgs{
		Key:   rediskeys.TasksAll(k.cfg.TeamID),
		Start: 0,
		Stop:  -1,
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("k8s ParseTrace: read tasks:all: %w", err)
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
	if k.cfg.RedisURL == "" {
		return fmt.Errorf("k8s SendMessage: Redis not configured")
	}
	opt, err := redis.ParseURL(k.cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("k8s SendMessage: parse Redis URL: %w", err)
	}
	rdb := redis.NewClient(opt)
	defer rdb.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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

	rdb := redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		rdb.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return rdb, nil
}

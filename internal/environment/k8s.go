package environment

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/debuglog"
	aimuxrt "github.com/zanetworker/aimux/internal/runtime"
	"github.com/zanetworker/aimux/internal/task"
	"github.com/zanetworker/aimux/pkg/rediskeys"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Compile-time interface check.
var _ Environment = (*K8sEnvironment)(nil)

// K8sEnvironmentConfig holds connection settings for a Kubernetes environment.
type K8sEnvironmentConfig struct {
	RedisURL   string
	TeamID     string
	Namespace  string
	Kubeconfig string
}

// K8sEnvironment discovers and manages agents running on a Kubernetes cluster.
// Discovery uses Redis heartbeats and the K8s API. The circuit breaker prevents
// the TUI from freezing when Redis is unreachable.
type K8sEnvironment struct {
	name    string
	cfg     K8sEnvironmentConfig
	mu      sync.Mutex
	rdb     *redis.Client
	backend *aimuxrt.K8sBackend

	lastRedisErr  time.Time
	redisCooldown time.Duration
}

// NewK8sEnvironment creates a K8sEnvironment with the given configuration.
func NewK8sEnvironment(name string, cfg K8sEnvironmentConfig) *K8sEnvironment {
	if name == "" {
		name = "k8s"
	}
	return &K8sEnvironment{
		name:          name,
		cfg:           cfg,
		redisCooldown: 30 * time.Second,
		backend:       aimuxrt.NewK8sBackend(cfg.Namespace, cfg.Kubeconfig),
	}
}

func (e *K8sEnvironment) Name() string { return e.name }
func (e *K8sEnvironment) Type() string { return "k8s" }

// redisClient returns the shared Redis client, creating it lazily on first use.
func (e *K8sEnvironment) redisClient() (*redis.Client, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.lastRedisErr.IsZero() && time.Since(e.lastRedisErr) < e.redisCooldown {
		return nil, fmt.Errorf("redis in cooldown (failed %s ago)", time.Since(e.lastRedisErr).Truncate(time.Second))
	}

	if e.rdb != nil {
		return e.rdb, nil
	}
	if e.cfg.RedisURL == "" {
		debuglog.Log("k8s-env: redis not configured")
		return nil, fmt.Errorf("redis not configured")
	}
	rdb, err := newK8sRedisClient(e.cfg.RedisURL)
	if err != nil {
		e.lastRedisErr = time.Now()
		debuglog.Log("k8s-env: redis connect failed: %v", err)
		return nil, err
	}
	e.rdb = rdb
	e.lastRedisErr = time.Time{}
	debuglog.Log("k8s-env: redis connected")
	return e.rdb, nil
}

// markRedisErr records a Redis failure and triggers the circuit breaker.
func (e *K8sEnvironment) markRedisErr() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastRedisErr = time.Now()
	if e.rdb != nil {
		_ = e.rdb.Close()
		e.rdb = nil
	}
	debuglog.Log("k8s-env: redis error, circuit breaker active for %s", e.redisCooldown)
}

// Close shuts down the shared Redis client.
func (e *K8sEnvironment) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.rdb != nil {
		_ = e.rdb.Close()
		e.rdb = nil
	}
}

// Discover reads Redis heartbeats and K8s API to enumerate live agents.
func (e *K8sEnvironment) Discover() ([]agent.Agent, error) {
	var agents []agent.Agent

	agents = append(agents, e.discoverFromRedis()...)
	agents = k8sMergeAgents(agents, e.discoverSessionPods())

	sort.SliceStable(agents, func(i, j int) bool {
		if agents[i].Status != agents[j].Status {
			return agents[i].Status < agents[j].Status
		}
		return agents[i].Name < agents[j].Name
	})

	return agents, nil
}

// discoverFromRedis queries Redis heartbeats for coordinator-managed agents.
// CRITICAL FIX: reads ProviderName from Redis metadata instead of hardcoding "k8s".
func (e *K8sEnvironment) discoverFromRedis() []agent.Agent {
	rdb, err := e.redisClient()
	if err != nil {
		debuglog.Log("k8s-env: redis discover skipped: %v", err)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	heartbeats, err := rdb.HGetAll(ctx, rediskeys.Heartbeat(e.cfg.TeamID)).Result()
	if err != nil {
		debuglog.Log("k8s-env: redis heartbeat fetch failed: %v", err)
		e.markRedisErr()
		return nil
	}
	debuglog.Log("k8s-env: redis discover found %d heartbeats", len(heartbeats))

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

		meta, err := rdb.HGetAll(ctx, rediskeys.Agent(e.cfg.TeamID, agentID)).Result()
		if err != nil {
			meta = map[string]string{}
		}

		// Read the real provider from metadata instead of hardcoding "k8s".
		providerName := meta["provider"]
		if providerName == "" {
			providerName = "claude"
		}

		role := meta["role"]
		model := meta["model"]
		namespace := meta["namespace"]
		if namespace == "" {
			namespace = e.cfg.Namespace
		}

		registeredAt := time.Time{}
		if raw := meta["registered_at"]; raw != "" {
			if epoch, err := strconv.ParseInt(raw, 10, 64); err == nil {
				registeredAt = time.Unix(epoch, 0)
			}
		}

		var tokensIn, tokensOut int64
		costMeta, err := rdb.HGetAll(ctx, rediskeys.Cost(e.cfg.TeamID, agentID)).Result()
		if err == nil {
			tokensIn, _ = strconv.ParseInt(costMeta["tokens_in"], 10, 64)
			tokensOut, _ = strconv.ParseInt(costMeta["tokens_out"], 10, 64)
		}

		taskSubject := e.findCurrentTask(ctx, rdb, agentID)

		displayName := role
		if displayName == "" {
			displayName = agentID
		}

		a := agent.Agent{
			PID:          0,
			SessionID:    agentID,
			Name:         displayName,
			ProviderName: providerName,
			Model:        model,
			WorkingDir:   "k8s://" + namespace + "/" + agentID,
			Status:       status,
			TeamName:     e.cfg.TeamID,
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
func (e *K8sEnvironment) findCurrentTask(ctx context.Context, rdb *redis.Client, agentID string) string {
	taskIDs, err := rdb.ZRangeArgs(ctx, redis.ZRangeArgs{
		Key:   rediskeys.TasksAll(e.cfg.TeamID),
		Start: 0,
		Stop:  99,
		Rev:   true,
	}).Result()
	if err != nil {
		return ""
	}

	for _, taskID := range taskIDs {
		fields, err := rdb.HGetAll(ctx, rediskeys.Task(e.cfg.TeamID, taskID)).Result()
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

// discoverSessionPods queries the K8s API for running session pods.
func (e *K8sEnvironment) discoverSessionPods() []agent.Agent {
	client, err := e.backend.KubeClient()
	if err != nil {
		debuglog.Log("k8s-env: pod discover skipped (no kube client): %v", err)
		return nil
	}

	namespace := e.cfg.Namespace
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
		debuglog.Log("k8s-env: pod discover failed: %v", err)
		return nil
	}

	var agents []agent.Agent
	for _, pod := range pods.Items {
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
			TeamName:     e.cfg.TeamID,
			StartTime:    startTime,
			LastActivity: startTime,
			Source:       agent.SourceSDK,
		}
		a.Status = k8sPodStatus(pod)
		if a.Status == agent.StatusError {
			a.LastAction = k8sPodErrorReason(pod)
		}
		agents = append(agents, a)
	}

	debuglog.Log("k8s-env: pod discover found %d session pods", len(agents))
	return agents
}

// Kill deletes the agent's K8s deployment.
func (e *K8sEnvironment) Kill(a agent.Agent) error {
	return e.backend.Delete(e.deploymentName(a))
}

// CreateSandbox creates a new K8s agent deployment.
func (e *K8sEnvironment) CreateSandbox(ctx context.Context, opts SandboxOpts) (string, error) {
	provider := opts.Provider
	if provider == "" {
		provider = "claude"
	}
	role := opts.Mode
	if role == "" {
		role = "session"
	}

	deployName := k8sSpawnDeploymentName(provider, role)
	image, err := k8sImageForProvider(provider)
	if err != nil {
		return "", err
	}

	labels := map[string]string{
		"team-component": role,
		"provider":       provider,
	}
	for k, v := range opts.Labels {
		labels[k] = v
	}

	backendOpts := aimuxrt.BackendCreateOpts{
		Image:  image,
		Labels: labels,
	}
	if opts.Image != "" {
		backendOpts.Image = opts.Image
	}

	if err := e.backend.Create(deployName, backendOpts); err != nil {
		return "", fmt.Errorf("cannot create sandbox %q: %w", deployName, err)
	}
	return deployName, nil
}

// DeleteSandbox scales down the named deployment.
func (e *K8sEnvironment) DeleteSandbox(ctx context.Context, name string) error {
	return e.backend.Stop(name)
}

// ListSandboxes lists K8s agent deployments.
func (e *K8sEnvironment) ListSandboxes(ctx context.Context) ([]SandboxStatus, error) {
	client, err := e.backend.KubeClient()
	if err != nil {
		return nil, nil
	}

	namespace := e.cfg.Namespace
	if namespace == "" {
		namespace = "agents"
	}

	deploys, err := client.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "team-component in (agent,session)",
	})
	if err != nil {
		return nil, nil
	}

	var statuses []SandboxStatus
	for _, d := range deploys.Items {
		idle := d.Status.ReadyReplicas == 0
		statuses = append(statuses, SandboxStatus{
			Name:   d.Name,
			Status: fmt.Sprintf("%d/%d ready", d.Status.ReadyReplicas, *d.Spec.Replicas),
			Idle:   idle,
		})
	}
	return statuses, nil
}


// Status returns a human-readable connection status.
func (e *K8sEnvironment) Status() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cfg.RedisURL == "" {
		return "not configured"
	}
	if !e.lastRedisErr.IsZero() && time.Since(e.lastRedisErr) < e.redisCooldown {
		return fmt.Sprintf("disconnected (retry in %s)", (e.redisCooldown - time.Since(e.lastRedisErr)).Truncate(time.Second))
	}
	if e.rdb != nil {
		return "connected"
	}
	return "connecting"
}

// SpawnRemote scales up a K8s Deployment for the given provider and role.
func (e *K8sEnvironment) SpawnRemote(provider, role string, count int) error {
	deployName := k8sSpawnDeploymentName(provider, role)
	image, err := k8sImageForProvider(provider)
	if err != nil {
		return err
	}
	opts := aimuxrt.BackendCreateOpts{
		Image: image,
		Labels: map[string]string{
			"team-component": role,
			"provider":       provider,
		},
	}
	for i := 0; i < count; i++ {
		if err := e.backend.Create(deployName, opts); err != nil {
			return fmt.Errorf("cannot scale %q: %w", deployName, err)
		}
	}
	return nil
}

// SpawnSession creates a session pod and waits for it to become ready.
func (e *K8sEnvironment) SpawnSession(providerName string) (podName, namespace string, err error) {
	deployName := k8sSpawnDeploymentName(providerName, "session")
	image, err := k8sImageForProvider(providerName)
	if err != nil {
		return "", "", err
	}
	pod, createErr := e.backend.CreateAndWait(deployName, aimuxrt.BackendCreateOpts{Image: image})
	if createErr != nil {
		return "", "", createErr
	}
	ns := e.backend.Namespace()
	return pod, ns, nil
}

// ScaleDown scales the deployment to 0.
func (e *K8sEnvironment) ScaleDown(provider, role string) error {
	return e.backend.Stop(k8sSpawnDeploymentName(provider, role))
}

// ScaleDownOne decrements the deployment by 1.
func (e *K8sEnvironment) ScaleDownOne(providerName, role string) error {
	return e.backend.ScaleDownOne(k8sSpawnDeploymentName(providerName, role))
}

// SendMessage writes a message to the agent's Redis inbox stream.
func (e *K8sEnvironment) SendMessage(agentID, text string) error {
	rdb, err := e.redisClient()
	if err != nil {
		return fmt.Errorf("k8s SendMessage: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	return rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: rediskeys.Inbox(e.cfg.TeamID, agentID),
		MaxLen: 1000,
		Approx: true,
		Values: map[string]any{
			"from":      "lead",
			"text":      text,
			"timestamp": fmt.Sprintf("%d", time.Now().Unix()),
		},
	}).Err()
}

// ListTasks returns all tasks for the configured team.
func (e *K8sEnvironment) ListTasks() ([]task.Task, error) {
	rdb, err := e.redisClient()
	if err != nil {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	tasks, err := task.LoadFromRedis(ctx, rdb, e.cfg.TeamID)
	if err != nil {
		debuglog.Log("k8s-env: ListTasks failed: %v", err)
		e.markRedisErr()
		return nil, nil
	}
	return tasks, nil
}

// GetTaskResult returns the full result reference for a task.
func (e *K8sEnvironment) GetTaskResult(taskID string) (string, error) {
	rdb, err := e.redisClient()
	if err != nil {
		return "", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	return task.GetFullResult(ctx, rdb, e.cfg.TeamID, taskID)
}

// Backend returns the underlying K8sBackend for callers that need
// direct access (e.g., SetClientset in tests).
func (e *K8sEnvironment) Backend() *aimuxrt.K8sBackend {
	return e.backend
}

// deploymentName derives the Kubernetes deployment name from agent metadata.
func (e *K8sEnvironment) deploymentName(a agent.Agent) string {
	parts := strings.SplitN(a.SessionID, "-", 3)
	if len(parts) >= 2 {
		return "agent-" + parts[0] + "-" + a.Name
	}
	return "agent-" + a.Name
}

// k8sPodStatus derives agent.Status from pod container states.
func k8sPodStatus(pod corev1.Pod) agent.Status {
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Waiting != nil {
			switch cs.State.Waiting.Reason {
			case "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull":
				return agent.StatusError
			}
			return agent.StatusIdle
		}
	}
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

// k8sPodErrorReason returns a human-readable reason for an unhealthy pod.
func k8sPodErrorReason(pod corev1.Pod) string {
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

// k8sMergeAgents combines two agent lists, deduplicating by SessionID.
// If both lists contain an agent with the same SessionID, primary wins.
func k8sMergeAgents(primary, secondary []agent.Agent) []agent.Agent {
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

// k8sSpawnDeploymentName constructs the K8s deployment name.
func k8sSpawnDeploymentName(provider, role string) string {
	return "agent-" + provider + "-" + role
}

// k8sImageForProvider returns the container image for the given provider.
func k8sImageForProvider(provider string) (string, error) {
	switch provider {
	case "claude":
		return "quay.io/azaalouk/claude-session:latest", nil
	default:
		return "", fmt.Errorf("provider %q has no Kubernetes image — only claude is supported for K8s sessions", provider)
	}
}

// newK8sRedisClient parses a Redis URL and returns a connected client.
func newK8sRedisClient(url string) (*redis.Client, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis URL: %w", err)
	}
	opts.DialTimeout = 1 * time.Second
	opts.ReadTimeout = 1 * time.Second
	opts.WriteTimeout = 1 * time.Second
	opts.PoolSize = 2
	opts.MinIdleConns = 0
	opts.MaxRetries = 1

	rdb := redis.NewClient(opts)
	return rdb, nil
}

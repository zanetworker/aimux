package provider

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/debuglog"
	"github.com/zanetworker/aimux/internal/subagent"
	"github.com/zanetworker/aimux/internal/trace"
	"github.com/zanetworker/aimux/pkg/rediskeys"
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
// Infrastructure operations (Discover, Kill, Spawn, Health) have moved to
// environment.K8sEnvironment. This provider retains identity methods and
// ParseTrace (which reads Redis task history).
type K8s struct {
	cfg     K8sConfig
	mu      sync.Mutex
	rdb     *redis.Client

	// Circuit breaker: skip Redis calls for a cooldown period after failure.
	lastRedisErr  time.Time
	redisCooldown time.Duration
}

// NewK8s constructs a K8s provider with the given configuration.
func NewK8s(cfg K8sConfig) *K8s {
	return &K8s{
		cfg:           cfg,
		redisCooldown: 30 * time.Second,
	}
}

// redisClient returns the shared Redis client, creating it lazily on first use.
func (k *K8s) redisClient() (*redis.Client, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

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
	k.lastRedisErr = time.Time{}
	debuglog.Log("k8s: redis connected")
	return k.rdb, nil
}

// markRedisErr records a Redis command failure and triggers the circuit breaker cooldown.
func (k *K8s) markRedisErr() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.lastRedisErr = time.Now()
	if k.rdb != nil {
		_ = k.rdb.Close()
		k.rdb = nil
	}
	debuglog.Log("k8s: redis error, circuit breaker active for %s", k.redisCooldown)
}

// Close shuts down the shared Redis client if one was created.
func (k *K8s) Close() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.rdb != nil {
		_ = k.rdb.Close()
		k.rdb = nil
	}
}

// Name returns the provider identifier used in config and display.
func (k *K8s) Name() string { return "k8s" }

// Discover returns nil — discovery has moved to K8sEnvironment.
// Kept to satisfy the Provider interface until Task 1.6 wires
// environment-based discovery into the orchestrator.
func (k *K8s) Discover() ([]agent.Agent, error) { return nil, nil }

// CanEmbed returns false — K8s agents run in pods and cannot be embedded
// as a local PTY.
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
		Models: []string{"claude-opus-4-6", "claude-sonnet-4-6", "claude-haiku-4-5"},
		Modes:  []string{"coder", "researcher", "reviewer"},
	}
}

// ParseTrace reads completed task history from Redis using the agentID
// encoded in filePath as "k8s://<agentID>".
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

// OTELEnv returns "" — K8s agents configure their own OTEL settings via
// pod environment variables managed outside aimux.
func (k *K8s) OTELEnv(_ string) string { return "" }

// OTELServiceName returns the service.name for K8s agents in OTEL telemetry.
func (k *K8s) OTELServiceName() string { return "k8s-agent" }

// SubagentAttrKeys returns zero value — K8s agents do not emit subagent
// identity attributes in the format aimux's OTEL receiver understands.
func (k *K8s) SubagentAttrKeys() subagent.AttrKeys { return subagent.AttrKeys{} }

// newRedisClient parses a Redis URL and returns a connected client.
func newRedisClient(url string) (*redis.Client, error) {
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

func init() {
	redis.SetLogger(nopRedisLogger{})
}

// nopRedisLogger implements redis/internal.Logging and discards all output.
type nopRedisLogger struct{}

func (nopRedisLogger) Printf(_ context.Context, _ string, _ ...interface{}) {}

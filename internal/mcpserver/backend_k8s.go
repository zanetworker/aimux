package mcpserver

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// K8sBackend implements Backend using Redis + K8s Deployments.
// This is the original backend; it stays operational until the
// OpenShell backend proves the full workflow end-to-end.
type K8sBackend struct {
	rdb       *redis.Client
	k8s       *kubernetes.Clientset
	namespace string
	teamID    string
	maxAgents int
}

// K8sConfig holds connection settings for the K8s+Redis backend.
type K8sConfig struct {
	RedisURL   string
	Kubeconfig string
	Namespace  string
	TeamID     string
	MaxAgents  int
}

func (c K8sConfig) withDefaults() K8sConfig {
	if c.Namespace == "" {
		c.Namespace = "agents"
	}
	if c.TeamID == "" {
		c.TeamID = "default"
	}
	if c.MaxAgents == 0 {
		c.MaxAgents = 20
	}
	return c
}

// NewK8sBackend creates a K8s backend from config.
func NewK8sBackend(cfg K8sConfig) (*K8sBackend, error) {
	cfg = cfg.withDefaults()

	redisOpt, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return nil, fmt.Errorf("invalid Redis URL %q: %w", cfg.RedisURL, err)
	}
	rdb := redis.NewClient(redisOpt)

	k8sConfig, err := clientcmd.BuildConfigFromFlags("", cfg.Kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("cannot build K8s config: %w", err)
	}
	k8sClient, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		return nil, fmt.Errorf("cannot create K8s client: %w", err)
	}

	return &K8sBackend{
		rdb:       rdb,
		k8s:       k8sClient,
		namespace: cfg.Namespace,
		teamID:    cfg.TeamID,
		maxAgents: cfg.MaxAgents,
	}, nil
}

func (b *K8sBackend) teamKey(suffix string) string {
	return fmt.Sprintf("team:%s:%s", b.teamID, suffix)
}

// Redis returns the underlying Redis client for task-level operations
// (createTask, getTask, etc.) that haven't been migrated to the journal yet.
func (b *K8sBackend) Redis() *redis.Client {
	return b.rdb
}

// TeamID returns the team identifier for scoping Redis keys.
func (b *K8sBackend) TeamID() string {
	return b.teamID
}

func (b *K8sBackend) CreateSandbox(ctx context.Context, opts SandboxOpts) (string, error) {
	provider := opts.Labels["provider"]
	role := opts.Labels["role"]
	if provider == "" {
		provider = "claude"
	}
	if role == "" {
		role = "coder"
	}

	deployName := fmt.Sprintf("agent-%s-%s", provider, role)
	deploy, err := b.k8s.AppsV1().Deployments(b.namespace).Get(ctx, deployName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("deployment %s not found: %w", deployName, err)
	}

	current := int32(0)
	if deploy.Spec.Replicas != nil {
		current = *deploy.Spec.Replicas
	}
	target := current + 1
	deploy.Spec.Replicas = &target

	_, err = b.k8s.AppsV1().Deployments(b.namespace).Update(ctx, deploy, metav1.UpdateOptions{})
	if err != nil {
		return "", fmt.Errorf("scale %s: %w", deployName, err)
	}

	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)
		heartbeats, _ := b.rdb.HGetAll(ctx, b.teamKey("heartbeat")).Result()
		for agentID := range heartbeats {
			meta, _ := b.rdb.HGetAll(ctx, b.teamKey("agent:"+agentID)).Result()
			if meta["provider"] == provider && meta["role"] == role {
				return agentID, nil
			}
		}
	}
	return deployName, nil
}

func (b *K8sBackend) DeleteSandbox(ctx context.Context, name string) error {
	deployName := deploymentNameFromPod(name)
	deploy, err := b.k8s.AppsV1().Deployments(b.namespace).Get(ctx, deployName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	zero := int32(0)
	deploy.Spec.Replicas = &zero
	_, err = b.k8s.AppsV1().Deployments(b.namespace).Update(ctx, deploy, metav1.UpdateOptions{})
	return err
}

// deploymentNameFromPod extracts the Deployment name from a pod name.
// Pod: agent-claude-coder-78564fdf75-4rxlk -> Deployment: agent-claude-coder
// Convention: deployment-<replicaset-hash>-<pod-hash>
func deploymentNameFromPod(podName string) string {
	parts := strings.Split(podName, "-")
	// A Kubernetes pod name is <deployment>-<rs-hash>-<pod-hash>.
	// We need at least 4 segments to reliably strip the two hash suffixes;
	// a 3-segment name is ambiguous (could be a 3-word deployment name) and
	// is returned unchanged.
	if len(parts) < 4 {
		return podName
	}
	// Remove last two segments (replicaset hash + pod hash)
	return strings.Join(parts[:len(parts)-2], "-")
}

func (b *K8sBackend) ListSandboxes(ctx context.Context) ([]SandboxStatus, error) {
	heartbeats, err := b.rdb.HGetAll(ctx, b.teamKey("heartbeat")).Result()
	if err != nil {
		return nil, err
	}
	now := float64(time.Now().Unix())
	var result []SandboxStatus
	for agentID, lastSeen := range heartbeats {
		ts, _ := strconv.ParseFloat(lastSeen, 64)
		elapsed := now - ts
		status := "running"
		if elapsed > 60 {
			status = "dead"
		} else if elapsed > 30 {
			status = "idle"
		}
		result = append(result, SandboxStatus{
			Name:   agentID,
			Status: status,
			Idle:   status == "idle" || status == "running",
		})
	}
	return result, nil
}

func (b *K8sBackend) ExecStream(_ context.Context, _ string, _ []string) (ExecResult, error) {
	return ExecResult{}, fmt.Errorf("ExecStream not supported on K8s backend; tasks are pushed via Redis")
}

func (b *K8sBackend) IdleCount(ctx context.Context) (int, error) {
	sandboxes, err := b.ListSandboxes(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, s := range sandboxes {
		if s.Idle {
			count++
		}
	}
	return count, nil
}

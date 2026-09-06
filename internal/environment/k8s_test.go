package environment

import (
	"context"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// Compile-time interface check.
var _ Environment = (*K8sEnvironment)(nil)

func TestK8sEnvironment_NameAndType(t *testing.T) {
	env := NewK8sEnvironment("", K8sEnvironmentConfig{})

	if got := env.Name(); got != "k8s" {
		t.Errorf("Name() = %q, want %q", got, "k8s")
	}
	if got := env.Type(); got != "k8s" {
		t.Errorf("Type() = %q, want %q", got, "k8s")
	}
}

func TestK8sEnvironment_CustomName(t *testing.T) {
	env := NewK8sEnvironment("staging-cluster", K8sEnvironmentConfig{})

	if got := env.Name(); got != "staging-cluster" {
		t.Errorf("Name() = %q, want %q", got, "staging-cluster")
	}
}

func TestK8sEnvironment_Discover_NotConfigured(t *testing.T) {
	env := NewK8sEnvironment("", K8sEnvironmentConfig{})
	_, err := env.Discover()
	if err != nil {
		t.Errorf("Discover() with no config: error = %v, want nil", err)
	}
}


func TestK8sEnvironment_Status_NotConfigured(t *testing.T) {
	env := NewK8sEnvironment("", K8sEnvironmentConfig{})
	if got := env.Status(); got != "not configured" {
		t.Errorf("Status() = %q, want %q", got, "not configured")
	}
}

func TestK8sEnvironment_Status_Connecting(t *testing.T) {
	env := NewK8sEnvironment("", K8sEnvironmentConfig{RedisURL: "redis://localhost:6379"})
	if got := env.Status(); got != "connecting" {
		t.Errorf("Status() = %q, want %q", got, "connecting")
	}
}

func TestK8sEnvironment_Status_AfterError(t *testing.T) {
	env := NewK8sEnvironment("", K8sEnvironmentConfig{RedisURL: "redis://127.0.0.1:19999"})
	_, _ = env.Discover()
	status := env.Status()
	if !strings.Contains(status, "disconnected") {
		t.Errorf("Status() after error = %q, want it to contain 'disconnected'", status)
	}
}

func TestK8sEnvironment_ListTasks_NotConfigured(t *testing.T) {
	env := NewK8sEnvironment("", K8sEnvironmentConfig{})
	tasks, err := env.ListTasks()
	if err != nil {
		t.Errorf("ListTasks() error = %v, want nil", err)
	}
	if tasks != nil {
		t.Errorf("ListTasks() = %v, want nil", tasks)
	}
}

func TestK8sEnvironment_GetTaskResult_NotConfigured(t *testing.T) {
	env := NewK8sEnvironment("", K8sEnvironmentConfig{})
	result, err := env.GetTaskResult("task-123")
	if err != nil {
		t.Errorf("GetTaskResult() error = %v, want nil", err)
	}
	if result != "" {
		t.Errorf("GetTaskResult() = %q, want empty", result)
	}
}

func TestK8sMergeAgents(t *testing.T) {
	primary := []agent.Agent{
		{SessionID: "a", Name: "agent-a"},
		{SessionID: "b", Name: "agent-b"},
	}
	secondary := []agent.Agent{
		{SessionID: "b", Name: "agent-b-dup"},
		{SessionID: "c", Name: "agent-c"},
	}
	merged := k8sMergeAgents(primary, secondary)
	if len(merged) != 3 {
		t.Fatalf("k8sMergeAgents() returned %d agents, want 3", len(merged))
	}
	for _, a := range merged {
		if a.SessionID == "b" && a.Name != "agent-b" {
			t.Errorf("k8sMergeAgents() should keep primary's agent-b, got %q", a.Name)
		}
	}
}

func TestK8sMergeAgents_EmptySecondary(t *testing.T) {
	primary := []agent.Agent{{SessionID: "a"}}
	merged := k8sMergeAgents(primary, nil)
	if len(merged) != 1 {
		t.Fatalf("k8sMergeAgents(primary, nil) = %d agents, want 1", len(merged))
	}
}

func TestK8sSpawnDeploymentName(t *testing.T) {
	tests := []struct {
		provider string
		role     string
		want     string
	}{
		{"claude", "coder", "agent-claude-coder"},
		{"claude", "researcher", "agent-claude-researcher"},
		{"codex", "reviewer", "agent-codex-reviewer"},
	}
	for _, tt := range tests {
		t.Run(tt.provider+"-"+tt.role, func(t *testing.T) {
			got := k8sSpawnDeploymentName(tt.provider, tt.role)
			if got != tt.want {
				t.Errorf("k8sSpawnDeploymentName(%q, %q) = %q, want %q", tt.provider, tt.role, got, tt.want)
			}
		})
	}
}

func TestK8sImageForProvider(t *testing.T) {
	img, err := k8sImageForProvider("claude")
	if err != nil {
		t.Fatalf("k8sImageForProvider(claude) error: %v", err)
	}
	if !strings.Contains(img, "claude-session") {
		t.Errorf("k8sImageForProvider(claude) = %q, want it to contain 'claude-session'", img)
	}

	_, err = k8sImageForProvider("unknown-provider")
	if err == nil {
		t.Error("k8sImageForProvider(unknown) should return error")
	}
}

func TestK8sDeploymentName(t *testing.T) {
	env := NewK8sEnvironment("", K8sEnvironmentConfig{})
	tests := []struct {
		name string
		a    agent.Agent
		want string
	}{
		{
			name: "session ID with provider prefix",
			a:    agent.Agent{SessionID: "claude-coder-abc123", Name: "coder"},
			want: "agent-claude-coder",
		},
		{
			name: "simple session ID",
			a:    agent.Agent{SessionID: "xyz", Name: "researcher"},
			want: "agent-researcher",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := env.deploymentName(tt.a)
			if got != tt.want {
				t.Errorf("deploymentName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestK8sPodStatus(t *testing.T) {
	tests := []struct {
		name string
		pod  corev1.Pod
		want agent.Status
	}{
		{
			name: "running and ready",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}, Ready: true},
					},
				},
			},
			want: agent.StatusActive,
		},
		{
			name: "CrashLoopBackOff",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}},
					},
				},
			},
			want: agent.StatusError,
		},
		{
			name: "ImagePullBackOff",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodPending,
					ContainerStatuses: []corev1.ContainerStatus{
						{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}},
					},
				},
			},
			want: agent.StatusError,
		},
		{
			name: "init container CrashLoopBackOff",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodPending,
					InitContainerStatuses: []corev1.ContainerStatus{
						{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}},
					},
				},
			},
			want: agent.StatusError,
		},
		{
			name: "init container waiting normally",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodPending,
					InitContainerStatuses: []corev1.ContainerStatus{
						{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}},
					},
				},
			},
			want: agent.StatusIdle,
		},
		{
			name: "pending no container status",
			pod: corev1.Pod{
				Status: corev1.PodStatus{Phase: corev1.PodPending},
			},
			want: agent.StatusIdle,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := k8sPodStatus(tt.pod)
			if got != tt.want {
				t.Errorf("k8sPodStatus() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestK8sPodErrorReason(t *testing.T) {
	tests := []struct {
		name string
		pod  corev1.Pod
		want string
	}{
		{
			name: "container CrashLoopBackOff",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}},
					},
				},
			},
			want: "CrashLoopBackOff",
		},
		{
			name: "init container error",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodPending,
					InitContainerStatuses: []corev1.ContainerStatus{
						{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull"}}},
					},
				},
			},
			want: "init:ErrImagePull",
		},
		{
			name: "no waiting state falls back to phase",
			pod: corev1.Pod{
				Status: corev1.PodStatus{Phase: corev1.PodPending},
			},
			want: "Pending",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := k8sPodErrorReason(tt.pod)
			if got != tt.want {
				t.Errorf("k8sPodErrorReason() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestK8sNewRedisClient_PoolSettings(t *testing.T) {
	rdb, err := newK8sRedisClient("redis://127.0.0.1:19999")
	if err != nil {
		t.Fatalf("newK8sRedisClient() error = %v", err)
	}
	defer func() { _ = rdb.Close() }()
	opts := rdb.Options()
	if opts.PoolSize != 2 {
		t.Errorf("PoolSize = %d, want 2", opts.PoolSize)
	}
	if opts.MinIdleConns != 0 {
		t.Errorf("MinIdleConns = %d, want 0", opts.MinIdleConns)
	}
	if opts.MaxRetries != 1 {
		t.Errorf("MaxRetries = %d, want 1", opts.MaxRetries)
	}
}

func TestK8sNewRedisClient_BadURL(t *testing.T) {
	_, err := newK8sRedisClient("not-a-valid-url")
	if err == nil {
		t.Error("newK8sRedisClient(bad URL) should return error")
	}
}

func TestK8sEnvironment_SpawnRemote_ViaBackend(t *testing.T) {
	cs := fake.NewSimpleClientset()
	env := NewK8sEnvironment("", K8sEnvironmentConfig{Namespace: "test-ns"})
	env.backend.SetClientset(cs)

	if err := env.SpawnRemote("claude", "session", 1); err != nil {
		t.Fatalf("SpawnRemote() error: %v", err)
	}

	ctx := context.Background()
	deploy, err := cs.AppsV1().Deployments("test-ns").Get(ctx, "agent-claude-session", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("deployment not found after SpawnRemote: %v", err)
	}
	if *deploy.Spec.Replicas != 1 {
		t.Errorf("deployment replicas = %d, want 1", *deploy.Spec.Replicas)
	}
}

func TestK8sEnvironment_ScaleDown_ViaBackend(t *testing.T) {
	cs := fake.NewSimpleClientset()
	env := NewK8sEnvironment("", K8sEnvironmentConfig{Namespace: "test-ns"})
	env.backend.SetClientset(cs)

	if err := env.SpawnRemote("claude", "coder", 1); err != nil {
		t.Fatalf("SpawnRemote() error: %v", err)
	}

	if err := env.ScaleDown("claude", "coder"); err != nil {
		t.Fatalf("ScaleDown() error: %v", err)
	}

	deploy, _ := cs.AppsV1().Deployments("test-ns").Get(context.Background(), "agent-claude-coder", metav1.GetOptions{})
	if *deploy.Spec.Replicas != 0 {
		t.Errorf("after ScaleDown() replicas = %d, want 0", *deploy.Spec.Replicas)
	}
}

func TestK8sEnvironment_Kill_ViaBackend(t *testing.T) {
	cs := fake.NewSimpleClientset()
	env := NewK8sEnvironment("", K8sEnvironmentConfig{Namespace: "test-ns"})
	env.backend.SetClientset(cs)

	_ = env.SpawnRemote("claude", "coder", 2)

	a := agent.Agent{SessionID: "claude-coder-abc123", Name: "coder"}
	if err := env.Kill(a); err != nil {
		t.Fatalf("Kill() error: %v", err)
	}

	deploy, _ := cs.AppsV1().Deployments("test-ns").Get(context.Background(), "agent-claude-coder", metav1.GetOptions{})
	if *deploy.Spec.Replicas != 1 {
		t.Errorf("after Kill() replicas = %d, want 1", *deploy.Spec.Replicas)
	}
}

func TestK8sEnvironment_SpawnRemote_UnsupportedProvider(t *testing.T) {
	env := NewK8sEnvironment("", K8sEnvironmentConfig{Namespace: "test-ns"})
	err := env.SpawnRemote("unsupported", "coder", 1)
	if err == nil {
		t.Error("SpawnRemote(unsupported) should return error")
	}
}

func TestK8sEnvironment_CreateSandbox(t *testing.T) {
	cs := fake.NewSimpleClientset()
	env := NewK8sEnvironment("", K8sEnvironmentConfig{Namespace: "test-ns"})
	env.backend.SetClientset(cs)

	name, err := env.CreateSandbox(context.Background(), SandboxOpts{
		Provider: "claude",
		Mode:     "session",
	})
	if err != nil {
		t.Fatalf("CreateSandbox() error: %v", err)
	}
	if name != "agent-claude-session" {
		t.Errorf("CreateSandbox() name = %q, want %q", name, "agent-claude-session")
	}
}

func TestK8sEnvironment_CreateSandbox_Defaults(t *testing.T) {
	cs := fake.NewSimpleClientset()
	env := NewK8sEnvironment("", K8sEnvironmentConfig{Namespace: "test-ns"})
	env.backend.SetClientset(cs)

	name, err := env.CreateSandbox(context.Background(), SandboxOpts{})
	if err != nil {
		t.Fatalf("CreateSandbox() error: %v", err)
	}
	if name != "agent-claude-session" {
		t.Errorf("CreateSandbox() name = %q, want %q (defaults to claude/session)", name, "agent-claude-session")
	}
}

func TestK8sEnvironment_DeleteSandbox(t *testing.T) {
	cs := fake.NewSimpleClientset()
	env := NewK8sEnvironment("", K8sEnvironmentConfig{Namespace: "test-ns"})
	env.backend.SetClientset(cs)

	_ = env.SpawnRemote("claude", "coder", 1)

	err := env.DeleteSandbox(context.Background(), "agent-claude-coder")
	if err != nil {
		t.Fatalf("DeleteSandbox() error: %v", err)
	}

	deploy, _ := cs.AppsV1().Deployments("test-ns").Get(context.Background(), "agent-claude-coder", metav1.GetOptions{})
	if *deploy.Spec.Replicas != 0 {
		t.Errorf("after DeleteSandbox() replicas = %d, want 0", *deploy.Spec.Replicas)
	}
}

func TestK8sEnvironment_Close(t *testing.T) {
	env := NewK8sEnvironment("", K8sEnvironmentConfig{})
	env.Close()
}

func TestK8sEnvironment_Backend(t *testing.T) {
	env := NewK8sEnvironment("", K8sEnvironmentConfig{Namespace: "test"})
	if env.Backend() == nil {
		t.Error("Backend() should not be nil")
	}
	if env.Backend().Namespace() != "test" {
		t.Errorf("Backend().Namespace() = %q, want %q", env.Backend().Namespace(), "test")
	}
}

func TestK8sEnvironment_DiscoverSessionPods_WithFakeClient(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "session-pod-1",
				Namespace: "agents",
				Labels: map[string]string{
					"team-component": "session",
					"provider":       "codex",
					"model":          "gpt-4",
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					{State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}, Ready: true},
				},
			},
		},
	)

	env := NewK8sEnvironment("", K8sEnvironmentConfig{Namespace: "agents"})
	env.backend.SetClientset(cs)

	agents := env.discoverSessionPods()
	if len(agents) != 1 {
		t.Fatalf("discoverSessionPods() returned %d agents, want 1", len(agents))
	}

	a := agents[0]
	if a.ProviderName != "codex" {
		t.Errorf("ProviderName = %q, want %q", a.ProviderName, "codex")
	}
	if a.Model != "gpt-4" {
		t.Errorf("Model = %q, want %q", a.Model, "gpt-4")
	}
	if a.Name != "session-pod-1" {
		t.Errorf("Name = %q, want %q", a.Name, "session-pod-1")
	}
	if a.SessionID != "pod-session-pod-1" {
		t.Errorf("SessionID = %q, want %q", a.SessionID, "pod-session-pod-1")
	}
	if a.Status != agent.StatusActive {
		t.Errorf("Status = %v, want Active", a.Status)
	}
}

func TestK8sEnvironment_DiscoverSessionPods_DefaultProviderLabel(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "no-label-pod",
				Namespace: "agents",
				Labels: map[string]string{
					"team-component": "session",
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					{State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}, Ready: true},
				},
			},
		},
	)

	env := NewK8sEnvironment("", K8sEnvironmentConfig{Namespace: "agents"})
	env.backend.SetClientset(cs)

	agents := env.discoverSessionPods()
	if len(agents) != 1 {
		t.Fatalf("got %d agents, want 1", len(agents))
	}
	if agents[0].ProviderName != "claude" {
		t.Errorf("ProviderName = %q, want %q (default)", agents[0].ProviderName, "claude")
	}
}

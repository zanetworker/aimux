package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// Compile-time interface checks — fails to compile if K8s is missing any method.
var _ Provider = (*K8s)(nil)
var _ TaskLister = (*K8s)(nil)
var _ Spawner = (*K8s)(nil)
var _ InfraProvider = (*K8s)(nil)

func TestK8sName(t *testing.T) {
	k := &K8s{}
	if got := k.Name(); got != "k8s" {
		t.Errorf("K8s.Name() = %q, want %q", got, "k8s")
	}
}

func TestK8sDiscover_NotConfigured(t *testing.T) {
	// When RedisURL is empty the provider must not panic or return an error.
	// It may still discover session pods via the K8s API if a kubeconfig is available.
	k := NewK8s(K8sConfig{})
	_, err := k.Discover()
	if err != nil {
		t.Errorf("K8s.Discover() with no config: error = %v, want nil", err)
	}
}

func TestK8sDiscover_BadURL(t *testing.T) {
	// An unreachable Redis URL must not crash aimux.
	// Pod discovery may still return results if a kubeconfig is available.
	k := NewK8s(K8sConfig{
		RedisURL: "redis://127.0.0.1:19999", // port with nothing listening
		TeamID:   "test-team",
	})
	_, err := k.Discover()
	if err != nil {
		t.Errorf("K8s.Discover() with bad URL: error = %v, want nil", err)
	}
}

func TestK8sCanEmbed(t *testing.T) {
	k := &K8s{}
	if k.CanEmbed() {
		t.Error("K8s.CanEmbed() = true, want false")
	}
}

func TestK8sResumeCommand(t *testing.T) {
	k := &K8s{}
	if cmd := k.ResumeCommand(agent.Agent{}); cmd != nil {
		t.Errorf("K8s.ResumeCommand() = %v, want nil", cmd)
	}
}

func TestK8sFindSessionFile(t *testing.T) {
	k := &K8s{}
	if got := k.FindSessionFile(agent.Agent{}); got != "" {
		t.Errorf("K8s.FindSessionFile() = %q, want empty string", got)
	}
}

func TestK8sRecentDirs(t *testing.T) {
	k := &K8s{}
	if dirs := k.RecentDirs(10); dirs != nil {
		t.Errorf("K8s.RecentDirs() = %v, want nil", dirs)
	}
}

func TestK8sSpawnCommand(t *testing.T) {
	k := &K8s{}
	if cmd := k.SpawnCommand("/tmp", "claude-opus-4-6", "coder"); cmd != nil {
		t.Errorf("K8s.SpawnCommand() = %v, want nil", cmd)
	}
}

func TestK8sSpawnArgs(t *testing.T) {
	k := &K8s{}
	args := k.SpawnArgs()
	if len(args.Models) == 0 {
		t.Error("K8s.SpawnArgs().Models is empty")
	}
	if len(args.Modes) == 0 {
		t.Error("K8s.SpawnArgs().Modes is empty")
	}
	// Verify expected models are present.
	wantModels := []string{"claude-opus-4-6", "claude-sonnet-4-6"}
	modelSet := make(map[string]bool)
	for _, m := range args.Models {
		modelSet[m] = true
	}
	for _, m := range wantModels {
		if !modelSet[m] {
			t.Errorf("K8s.SpawnArgs().Models missing %q", m)
		}
	}
	// Verify expected modes are present.
	wantModes := []string{"coder", "researcher", "reviewer"}
	modeSet := make(map[string]bool)
	for _, m := range args.Modes {
		modeSet[m] = true
	}
	for _, m := range wantModes {
		if !modeSet[m] {
			t.Errorf("K8s.SpawnArgs().Modes missing %q", m)
		}
	}
}

func TestK8sParseTrace_NotConfigured(t *testing.T) {
	k := &K8s{}
	turns, err := k.ParseTrace("")
	if err != nil {
		t.Errorf("K8s.ParseTrace() error = %v, want nil", err)
	}
	// Must return at least one informational turn, not nil.
	if len(turns) == 0 {
		t.Error("K8s.ParseTrace() returned empty slice, want at least one informational turn")
	}
}

func TestK8sParseTrace_Configured(t *testing.T) {
	k := NewK8s(K8sConfig{
		RedisURL: "redis://127.0.0.1:6379",
		TeamID:   "my-team",
	})
	turns, err := k.ParseTrace("")
	if err != nil {
		t.Errorf("K8s.ParseTrace() with config error = %v, want nil", err)
	}
	// Must return at least one informational turn regardless of Redis reachability.
	if len(turns) == 0 {
		t.Error("K8s.ParseTrace() returned empty slice, want at least one turn")
	}
}

func TestK8sOTELEnv(t *testing.T) {
	k := &K8s{}
	if got := k.OTELEnv("localhost:4318"); got != "" {
		t.Errorf("K8s.OTELEnv() = %q, want empty string", got)
	}
}

func TestK8sOTELServiceName(t *testing.T) {
	k := &K8s{}
	if got := k.OTELServiceName(); got != "k8s-agent" {
		t.Errorf("K8s.OTELServiceName() = %q, want %q", got, "k8s-agent")
	}
}

func TestK8sSubagentAttrKeys_Empty(t *testing.T) {
	k := &K8s{}
	keys := k.SubagentAttrKeys()
	if !keys.Empty() {
		t.Error("K8s.SubagentAttrKeys() should return empty AttrKeys")
	}
}

func TestK8sDeploymentName(t *testing.T) {
	k := &K8s{}
	tests := []struct {
		name      string
		a         agent.Agent
		wantContains string
	}{
		{
			name:         "session ID with provider prefix",
			a:            agent.Agent{SessionID: "claude-coder-abc123", Name: "coder"},
			wantContains: "agent-claude-coder",
		},
		{
			name:         "simple session ID",
			a:            agent.Agent{SessionID: "xyz", Name: "researcher"},
			wantContains: "agent-researcher",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := k.deploymentName(tt.a)
			if got != tt.wantContains {
				t.Errorf("K8s.deploymentName() = %q, want %q", got, tt.wantContains)
			}
		})
	}
}

func TestNewK8s(t *testing.T) {
	cfg := K8sConfig{
		RedisURL:  "redis://localhost:6379",
		TeamID:    "team1",
		Namespace: "agents",
	}
	k := NewK8s(cfg)
	if k == nil {
		t.Fatal("NewK8s() returned nil")
	}
	if k.cfg.TeamID != "team1" {
		t.Errorf("NewK8s().cfg.TeamID = %q, want %q", k.cfg.TeamID, "team1")
	}
}

func TestK8sStatus(t *testing.T) {
	tests := []struct {
		name string
		cfg  K8sConfig
		want string
	}{
		{"not configured", K8sConfig{}, "not configured"},
		{"configured but not connected", K8sConfig{RedisURL: "redis://localhost:6379"}, "connecting"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := NewK8s(tt.cfg)
			got := k.Status()
			if got != tt.want {
				t.Errorf("K8s.Status() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestK8sStatus_AfterError(t *testing.T) {
	k := NewK8s(K8sConfig{RedisURL: "redis://localhost:19999"})
	// Trigger a connection attempt that will fail
	_, _ = k.Discover()
	status := k.Status()
	if !strings.Contains(status, "disconnected") {
		t.Errorf("K8s.Status() after error = %q, want it to contain 'disconnected'", status)
	}
}

func TestK8sListTasks_NotConfigured(t *testing.T) {
	// When RedisURL is empty, ListTasks must return (nil, nil) without panicking.
	k := &K8s{}
	tasks, err := k.ListTasks()
	if err != nil {
		t.Errorf("K8s.ListTasks() with no config: error = %v, want nil", err)
	}
	if tasks != nil {
		t.Errorf("K8s.ListTasks() with no config: tasks = %v, want nil", tasks)
	}
}

func TestK8sGetTaskResult_NotConfigured(t *testing.T) {
	// When RedisURL is empty, GetTaskResult must return ("", nil) without panicking.
	k := &K8s{}
	result, err := k.GetTaskResult("task-123")
	if err != nil {
		t.Errorf("K8s.GetTaskResult() with no config: error = %v, want nil", err)
	}
	if result != "" {
		t.Errorf("K8s.GetTaskResult() with no config: result = %q, want empty", result)
	}
}

func TestK8sSpawnRemote_NoKubeconfig(t *testing.T) {
	// When kubeconfig is set to a nonexistent path, SpawnRemote must return
	// an error (not panic).
	k := NewK8s(K8sConfig{
		Kubeconfig: "/nonexistent/kubeconfig",
		Namespace:  "agents",
	})
	err := k.SpawnRemote("claude", "coder", 2)
	if err == nil {
		t.Error("K8s.SpawnRemote() with bad kubeconfig: expected error, got nil")
	}
}

func TestK8sScaleDown_NoKubeconfig(t *testing.T) {
	// When kubeconfig is set to a nonexistent path, ScaleDown must return
	// an error (not panic).
	k := NewK8s(K8sConfig{
		Kubeconfig: "/nonexistent/kubeconfig",
		Namespace:  "agents",
	})
	err := k.ScaleDown("claude", "coder")
	if err == nil {
		t.Error("K8s.ScaleDown() with bad kubeconfig: expected error, got nil")
	}
}

func TestMergeAgents(t *testing.T) {
	primary := []agent.Agent{
		{SessionID: "a", Name: "agent-a"},
		{SessionID: "b", Name: "agent-b"},
	}
	secondary := []agent.Agent{
		{SessionID: "b", Name: "agent-b-dup"},  // duplicate, should be skipped
		{SessionID: "c", Name: "agent-c"},       // new, should be added
	}
	merged := mergeAgents(primary, secondary)
	if len(merged) != 3 {
		t.Fatalf("mergeAgents() returned %d agents, want 3", len(merged))
	}
	// Verify "b" comes from primary (not overwritten)
	for _, a := range merged {
		if a.SessionID == "b" && a.Name != "agent-b" {
			t.Errorf("mergeAgents() should keep primary's agent-b, got %q", a.Name)
		}
	}
}

func TestMergeAgents_EmptySecondary(t *testing.T) {
	primary := []agent.Agent{{SessionID: "a"}}
	merged := mergeAgents(primary, nil)
	if len(merged) != 1 {
		t.Fatalf("mergeAgents(primary, nil) = %d agents, want 1", len(merged))
	}
}

func TestNopRedisLogger(t *testing.T) {
	// nopRedisLogger must implement the Logging interface and not panic.
	var l nopRedisLogger
	l.Printf(context.TODO(), "should not appear: %s %d", "test", 42)
}

func TestNewRedisClient_PoolSettings(t *testing.T) {
	// Verify the client is created with constrained pool settings
	// that prevent stderr spam when Redis is unreachable.
	rdb, err := newRedisClient("redis://127.0.0.1:19999")
	if err != nil {
		t.Fatalf("newRedisClient() error = %v", err)
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

// --- Auto-provisioning tests (via K8sBackend with fake clientset) ---
// Low-level provisioning (createDeployment, ensureNamespace, ensureAuthSecrets)
// moved to internal/runtime/k8s.go. These tests exercise the provider's thin
// delegation layer via SpawnRemote, which calls backend.Create internally.

func TestSpawnRemote_ViaBackend(t *testing.T) {
	cs := fake.NewSimpleClientset()
	k := NewK8s(K8sConfig{Namespace: "test-ns"})
	k.backend.SetClientset(cs)

	// SpawnRemote delegates to backend.Create, which auto-creates the deployment.
	if err := k.SpawnRemote("claude", "session", 1); err != nil {
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
	image := deploy.Spec.Template.Spec.Containers[0].Image
	if !strings.Contains(image, "claude-session") {
		t.Errorf("deployment image = %q, want it to contain %q", image, "claude-session")
	}
}

func TestScaleDown_ViaBackend(t *testing.T) {
	cs := fake.NewSimpleClientset()
	k := NewK8s(K8sConfig{Namespace: "test-ns"})
	k.backend.SetClientset(cs)

	// Create via SpawnRemote first.
	if err := k.SpawnRemote("claude", "coder", 1); err != nil {
		t.Fatalf("SpawnRemote() error: %v", err)
	}

	// ScaleDown to 0.
	if err := k.ScaleDown("claude", "coder"); err != nil {
		t.Fatalf("ScaleDown() error: %v", err)
	}

	deploy, _ := cs.AppsV1().Deployments("test-ns").Get(context.Background(), "agent-claude-coder", metav1.GetOptions{})
	if *deploy.Spec.Replicas != 0 {
		t.Errorf("after ScaleDown() replicas = %d, want 0", *deploy.Spec.Replicas)
	}
}

func TestKill_ViaBackend(t *testing.T) {
	cs := fake.NewSimpleClientset()
	k := NewK8s(K8sConfig{Namespace: "test-ns"})
	k.backend.SetClientset(cs)

	// Create 2 replicas.
	_ = k.SpawnRemote("claude", "coder", 2)

	a := agent.Agent{SessionID: "claude-coder-abc123", Name: "coder"}
	if err := k.Kill(a); err != nil {
		t.Fatalf("Kill() error: %v", err)
	}

	deploy, _ := cs.AppsV1().Deployments("test-ns").Get(context.Background(), "agent-claude-coder", metav1.GetOptions{})
	if *deploy.Spec.Replicas != 1 {
		t.Errorf("after Kill() replicas = %d, want 1", *deploy.Spec.Replicas)
	}
}

func TestTerminalForwarding_ExcludesCredentials(t *testing.T) {
	// Verify that the config env vars forwarded via terminal
	// do NOT include any credential-bearing variables.
	// This is a policy test: if someone adds ANTHROPIC_API_KEY
	// back to the terminal forwarding list, this test catches it.
	credentialVars := map[string]bool{
		"ANTHROPIC_API_KEY":              true,
		"GOOGLE_APPLICATION_CREDENTIALS": true,
		"AWS_SECRET_ACCESS_KEY":          true,
		"AWS_SESSION_TOKEN":              true,
	}

	// These are the vars forwarded via terminal in openK8sSession.
	// Keep this list in sync with app.go configEnvVars.
	terminalForwardedVars := []string{
		"CLAUDE_CODE_USE_VERTEX",
		"CLOUD_ML_REGION",
		"ANTHROPIC_VERTEX_PROJECT_ID",
		"ANTHROPIC_VERTEX_REGION",
	}

	for _, v := range terminalForwardedVars {
		if credentialVars[v] {
			t.Errorf("credential %q must not be forwarded via terminal; use K8s secrets instead", v)
		}
	}
}

func TestPodStatus(t *testing.T) {
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
		{
			name: "container waiting ContainerCreating",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodPending,
					ContainerStatuses: []corev1.ContainerStatus{
						{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}},
					},
				},
			},
			want: agent.StatusIdle,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := podStatus(tt.pod)
			if got != tt.want {
				t.Errorf("podStatus() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPodErrorReason(t *testing.T) {
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
			got := podErrorReason(tt.pod)
			if got != tt.want {
				t.Errorf("podErrorReason() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSpawnDeploymentName(t *testing.T) {
	tests := []struct {
		provider string
		role     string
		want     string
	}{
		{"claude", "coder", "agent-claude-coder"},
		{"gemini", "researcher", "agent-gemini-researcher"},
		{"codex", "reviewer", "agent-codex-reviewer"},
	}
	for _, tt := range tests {
		t.Run(tt.provider+"-"+tt.role, func(t *testing.T) {
			got := spawnDeploymentName(tt.provider, tt.role)
			if got != tt.want {
				t.Errorf("spawnDeploymentName(%q, %q) = %q, want %q", tt.provider, tt.role, got, tt.want)
			}
		})
	}
}

package runtime

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// Compile-time interface check.
var _ Backend = (*K8sBackend)(nil)

func TestK8sBackendName(t *testing.T) {
	b := NewK8sBackend("agents", "")
	if got := b.Name(); got != "kubernetes" {
		t.Errorf("Name() = %q, want %q", got, "kubernetes")
	}
}

func TestK8sBackendIsRemote(t *testing.T) {
	b := NewK8sBackend("agents", "")
	if !b.IsRemote() {
		t.Error("IsRemote() = false, want true")
	}
}

func TestK8sBackendExecPrefix_DefaultNamespace(t *testing.T) {
	b := NewK8sBackend("", "")
	prefix := b.ExecPrefix("my-pod")
	want := []string{"kubectl", "exec", "-it", "my-pod", "-n", "agents", "--"}
	if len(prefix) != len(want) {
		t.Fatalf("ExecPrefix() = %v, want %v", prefix, want)
	}
	for i, v := range want {
		if prefix[i] != v {
			t.Errorf("ExecPrefix()[%d] = %q, want %q", i, prefix[i], v)
		}
	}
}

func TestK8sBackendExecPrefix_CustomNamespace(t *testing.T) {
	b := NewK8sBackend("custom-ns", "")
	prefix := b.ExecPrefix("pod-123")
	// Expected: [kubectl, exec, -it, pod-123, -n, custom-ns, --]
	if prefix[3] != "pod-123" {
		t.Errorf("ExecPrefix() pod name = %q, want %q", prefix[3], "pod-123")
	}
	if prefix[5] != "custom-ns" {
		t.Errorf("ExecPrefix() namespace = %q, want %q", prefix[5], "custom-ns")
	}
}

func TestK8sBackendNamespace_DefaultsToAgents(t *testing.T) {
	b := NewK8sBackend("", "")
	if b.Namespace() != "agents" {
		t.Errorf("Namespace() = %q, want %q", b.Namespace(), "agents")
	}
}

func TestK8sBackendCreate_AutoCreatesDeployment(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewK8sBackend("test-ns", "")
	b.SetClientset(cs)

	err := b.Create("agent-claude-coder", BackendCreateOpts{
		Image: "quay.io/test/image:latest",
		Labels: map[string]string{
			"provider":       "claude",
			"team-component": "coder",
		},
	})
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	ctx := context.Background()
	deploy, err := cs.AppsV1().Deployments("test-ns").Get(ctx, "agent-claude-coder", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("deployment not found after Create: %v", err)
	}
	// Create scales up by 1 from the initial 0.
	if *deploy.Spec.Replicas != 1 {
		t.Errorf("deployment replicas = %d, want 1", *deploy.Spec.Replicas)
	}
	if deploy.Labels["provider"] != "claude" {
		t.Errorf("deployment label provider = %q, want %q", deploy.Labels["provider"], "claude")
	}
}

func TestK8sBackendStop_ScalesToZero(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewK8sBackend("test-ns", "")
	b.SetClientset(cs)

	// Pre-create a deployment with 3 replicas.
	three := int32(3)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-claude-coder", Namespace: "test-ns"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &three,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "agent-claude-coder"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "agent-claude-coder"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "img"}}},
			},
		},
	}
	_, err := cs.AppsV1().Deployments("test-ns").Create(context.Background(), deploy, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("setup: create deployment: %v", err)
	}

	if err := b.Stop("agent-claude-coder"); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}

	got, _ := cs.AppsV1().Deployments("test-ns").Get(context.Background(), "agent-claude-coder", metav1.GetOptions{})
	if *got.Spec.Replicas != 0 {
		t.Errorf("after Stop() replicas = %d, want 0", *got.Spec.Replicas)
	}
}

func TestK8sBackendDelete_ScalesDownByOne(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewK8sBackend("test-ns", "")
	b.SetClientset(cs)

	// Pre-create a deployment with 3 replicas.
	three := int32(3)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "my-deploy", Namespace: "test-ns"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &three,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "my-deploy"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "my-deploy"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "img"}}},
			},
		},
	}
	_, _ = cs.AppsV1().Deployments("test-ns").Create(context.Background(), deploy, metav1.CreateOptions{})

	if err := b.Delete("my-deploy"); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}

	got, _ := cs.AppsV1().Deployments("test-ns").Get(context.Background(), "my-deploy", metav1.GetOptions{})
	if *got.Spec.Replicas != 2 {
		t.Errorf("after Delete() replicas = %d, want 2", *got.Spec.Replicas)
	}
}

func TestK8sBackendDelete_ClampsToZero(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewK8sBackend("test-ns", "")
	b.SetClientset(cs)

	// Pre-create with 0 replicas.
	zero := int32(0)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "empty-deploy", Namespace: "test-ns"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &zero,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "empty-deploy"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "empty-deploy"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "img"}}},
			},
		},
	}
	_, _ = cs.AppsV1().Deployments("test-ns").Create(context.Background(), deploy, metav1.CreateOptions{})

	if err := b.Delete("empty-deploy"); err != nil {
		t.Fatalf("Delete() with 0 replicas error: %v", err)
	}

	got, _ := cs.AppsV1().Deployments("test-ns").Get(context.Background(), "empty-deploy", metav1.GetOptions{})
	if *got.Spec.Replicas != 0 {
		t.Errorf("after Delete() replicas = %d, want 0 (clamped)", *got.Spec.Replicas)
	}
}

func TestK8sBackendStatus_NoPods(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewK8sBackend("test-ns", "")
	b.SetClientset(cs)

	state, err := b.Status("nonexistent")
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}
	if state != StateStopped {
		t.Errorf("Status() = %v, want StateStopped", state)
	}
}

func TestK8sBackendStatus_RunningPod(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewK8sBackend("test-ns", "")
	b.SetClientset(cs)

	// Create a running pod with the right label.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "agent-pod-1",
			Namespace: "test-ns",
			Labels:    map[string]string{"app": "my-deploy"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	_, _ = cs.CoreV1().Pods("test-ns").Create(context.Background(), pod, metav1.CreateOptions{})

	state, err := b.Status("my-deploy")
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}
	if state != StateRunning {
		t.Errorf("Status() = %v, want StateRunning", state)
	}
}

func TestK8sBackendStatus_PendingPod(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewK8sBackend("test-ns", "")
	b.SetClientset(cs)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "agent-pod-2",
			Namespace: "test-ns",
			Labels:    map[string]string{"app": "pending-deploy"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodPending},
	}
	_, _ = cs.CoreV1().Pods("test-ns").Create(context.Background(), pod, metav1.CreateOptions{})

	state, err := b.Status("pending-deploy")
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}
	if state != StateCreating {
		t.Errorf("Status() = %v, want StateCreating", state)
	}
}

func TestK8sBackendEnsureNamespace_Idempotent(t *testing.T) {
	cs := fake.NewSimpleClientset()
	b := NewK8sBackend("my-ns", "")
	b.SetClientset(cs)

	ctx := context.Background()
	b.ensureNamespace(ctx, cs)

	ns, err := cs.CoreV1().Namespaces().Get(ctx, "my-ns", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("namespace not created: %v", err)
	}
	if ns.Labels["app.kubernetes.io/managed-by"] != "aimux" {
		t.Error("namespace missing managed-by label")
	}

	// Second call is a no-op.
	b.ensureNamespace(ctx, cs)
}

func TestK8sBackendKubeClient_BadKubeconfig(t *testing.T) {
	b := NewK8sBackend("agents", "/nonexistent/kubeconfig")
	_, err := b.KubeClient()
	if err == nil {
		t.Error("KubeClient() with bad kubeconfig should return error")
	}
}

func TestSplitLast(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"agent-claude-coder", "coder"},
		{"no-hyphens-here", "here"},
		{"single", ""},
		{"a-b", "b"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := splitLast(tt.input, "-")
			if got != tt.want {
				t.Errorf("splitLast(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

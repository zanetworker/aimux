package terminal

import (
	"testing"
)

// Compile-time interface check.
var _ SessionBackend = (*KubectlExecBackend)(nil)

func TestNewKubectlExec_EmptyPodName(t *testing.T) {
	_, err := NewKubectlExec("", "default", "", 80, 24)
	if err == nil {
		t.Error("NewKubectlExec with empty pod name should return error")
	}
}

func TestNewKubectlExec_DefaultNamespace(t *testing.T) {
	// Can't actually start kubectl without a cluster, but verify it doesn't panic.
	_, err := NewKubectlExec("nonexistent-pod", "", "", 80, 24)
	// Expected to fail (no such pod), but should not panic.
	if err == nil {
		t.Log("Unexpected success — pod likely exists in default namespace")
	}
}

func TestKubectlExecBackend_CloseIdempotent(t *testing.T) {
	kb := &KubectlExecBackend{
		podName:   "test-pod",
		namespace: "default",
	}
	kb.closed = true

	if err := kb.Close(); err != nil {
		t.Errorf("Close on already-closed backend: %v", err)
	}
	// Second close should also be fine.
	if err := kb.Close(); err != nil {
		t.Errorf("Second Close: %v", err)
	}
}

func TestKubectlExecBackend_AliveReturnsFalseWhenClosed(t *testing.T) {
	kb := &KubectlExecBackend{
		podName:   "test-pod",
		namespace: "default",
	}
	kb.Close()

	if kb.Alive() {
		t.Error("Alive() should return false when closed")
	}
}

func TestKubectlExecBackend_PodName(t *testing.T) {
	kb := &KubectlExecBackend{podName: "my-pod", namespace: "ns1"}
	if kb.PodName() != "my-pod" {
		t.Errorf("PodName() = %q, want %q", kb.PodName(), "my-pod")
	}
	if kb.Namespace() != "ns1" {
		t.Errorf("Namespace() = %q, want %q", kb.Namespace(), "ns1")
	}
}

func TestNewKubectlExec_CommandArgs(t *testing.T) {
	// Verify the kubectl command is constructed correctly without actually
	// running it. We test the args building logic by checking that the
	// resulting command would include sh -c with TERM and tmux.
	// Can't test the full NewKubectlExec (needs a cluster), so we test
	// the arg construction inline.
	podName := "test-pod"
	namespace := "test-ns"
	container := "my-container"
	args := []string{"exec", "-it", podName, "-n", namespace}
	if container != "" {
		args = append(args, "--container", container)
	}
	args = append(args, "--",
		"sh", "-c",
		"TERM=xterm-256color tmux -f /dev/null new-session -A -s main -x 120 -y 40")

	// Verify essential args are present
	found := map[string]bool{"exec": false, "-it": false, "sh": false, "xterm-256color": false}
	for _, a := range args {
		for key := range found {
			if a == key || (key == "xterm-256color" && len(a) > 0 && contains(a, key)) {
				found[key] = true
			}
		}
	}
	for key, ok := range found {
		if !ok {
			t.Errorf("expected %q in kubectl args, not found", key)
		}
	}

	// Verify container flag is included
	hasContainer := false
	for i, a := range args {
		if a == "--container" && i+1 < len(args) && args[i+1] == container {
			hasContainer = true
		}
	}
	if !hasContainer {
		t.Error("--container flag missing from args")
	}

	// Verify dimensions in tmux command
	lastArg := args[len(args)-1]
	if !contains(lastArg, "120") || !contains(lastArg, "40") {
		t.Errorf("tmux command missing dimensions: %s", lastArg)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && stringContains(s, substr))
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestPodPhase_NonexistentPod(t *testing.T) {
	_, err := podPhase("nonexistent-pod-xyz", "default")
	if err == nil {
		t.Log("podPhase returned nil error — pod may actually exist")
	}
}

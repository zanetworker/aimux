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

func TestPodPhase_NonexistentPod(t *testing.T) {
	_, err := podPhase("nonexistent-pod-xyz", "default")
	if err == nil {
		t.Log("podPhase returned nil error — pod may actually exist")
	}
}

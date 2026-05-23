package controller

import (
	"strings"

	"github.com/zanetworker/aimux/internal/agent"
)

// KillType describes how an agent should be terminated.
type KillType int

const (
	KillProcess    KillType = iota // local process: SIGTERM via provider
	KillPod                       // K8s pod: kubectl delete + scale down
	KillRemoveOnly                // session-only entry: hide from view
)

// KillAction holds the determined kill strategy and any provider-specific
// parameters (e.g., pod name and namespace for K8s agents).
type KillAction struct {
	Type      KillType
	PodName   string
	Namespace string
}

// DetermineKillAction inspects an agent and returns the appropriate kill
// strategy. The logic is:
//   - SessionID starting with "pod-" indicates a Kubernetes pod.
//   - PID == 0 with no pod prefix is a session-only entry (no live process).
//   - Otherwise it is a local process to be terminated via SIGTERM.
func DetermineKillAction(ag agent.Agent) KillAction {
	if strings.HasPrefix(ag.SessionID, "pod-") {
		podName := strings.TrimPrefix(ag.SessionID, "pod-")
		namespace := "agents"
		if strings.HasPrefix(ag.WorkingDir, "k8s://") {
			if parts := strings.SplitN(strings.TrimPrefix(ag.WorkingDir, "k8s://"), "/", 2); len(parts) == 2 && parts[0] != "" {
				namespace = parts[0]
			}
		}
		return KillAction{Type: KillPod, PodName: podName, Namespace: namespace}
	}
	if ag.PID == 0 {
		return KillAction{Type: KillRemoveOnly}
	}
	return KillAction{Type: KillProcess}
}

package controller

import (
	"context"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/openshell"
	"github.com/zanetworker/aimux/internal/spawn"
)

// KillType describes how an agent should be terminated.
type KillType int

const (
	KillProcess    KillType = iota // local process: SIGTERM via provider
	KillPod                       // K8s pod: kubectl delete + scale down
	KillRemoveOnly                // session-only entry: hide from view
	KillSandbox                   // OpenShell sandbox: delete + kill tmux
)

// KillAction holds the determined kill strategy and any provider-specific
// parameters (e.g., pod name and namespace for K8s agents).
type KillAction struct {
	Type        KillType
	PodName     string
	Namespace   string
	SandboxName string // OpenShell sandbox name
	TmuxSession string // tmux session to kill
}

// DetermineKillAction inspects an agent and returns the appropriate kill
// strategy.
func DetermineKillAction(ag agent.Agent) KillAction {
	if ag.Location == "remote" && ag.SandboxName != "" {
		return KillAction{
			Type:        KillSandbox,
			SandboxName: ag.SandboxName,
			TmuxSession: ag.TMuxSession,
		}
	}
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

// ExecuteKillSandbox kills the tmux session (closing the SSH connection),
// waits for the connection to close, then deletes the OpenShell sandbox.
func ExecuteKillSandbox(action KillAction) error {
	if action.TmuxSession != "" {
		spawn.KillTmuxSession(action.TmuxSession)
		time.Sleep(2 * time.Second)
	}
	if action.SandboxName != "" {
		client := openshell.NewClient(openshell.Config{})
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return client.DeleteSandbox(ctx, action.SandboxName)
	}
	return nil
}

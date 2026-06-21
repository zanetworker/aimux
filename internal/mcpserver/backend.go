package mcpserver

import "context"

// Backend abstracts the compute layer for sandbox lifecycle and task execution.
// Implementations: K8sBackend (Redis + K8s Deployments), OpenShellBackend (openshell CLI).
type Backend interface {
	CreateSandbox(ctx context.Context, opts SandboxOpts) (name string, err error)
	DeleteSandbox(ctx context.Context, name string) error
	ListSandboxes(ctx context.Context) ([]SandboxStatus, error)
	ExecStream(ctx context.Context, name string, command []string) (ExecResult, error)
	IdleCount(ctx context.Context) (int, error)
}

// SandboxOpts configures a new sandbox.
type SandboxOpts struct {
	Image  string
	Mode   string // "worker" or "session"
	Env    map[string]string
	Labels map[string]string
	NoKeep bool
}

// SandboxStatus describes a sandbox returned by ListSandboxes.
type SandboxStatus struct {
	Name   string
	Status string // "running", "ready", "stopped", "dead"
	Idle   bool
}

// ExecResult holds the output of a completed exec.
type ExecResult struct {
	ExitCode int
	Output   string
}

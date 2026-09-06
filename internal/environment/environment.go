package environment

import (
	"context"

	"github.com/zanetworker/aimux/internal/agent"
)

// Environment represents an execution environment where agents can run.
// This can be a local machine, an OpenShell sandbox, a Kubernetes cluster, or another backend.
type Environment interface {
	// Name returns the friendly name of this environment.
	Name() string

	// Type returns the type of environment ("local", "openshell", "k8s", etc).
	Type() string

	// Discover discovers running agents in this environment.
	Discover() ([]agent.Agent, error)

	// CreateSandbox creates a new sandbox for agent execution with the given options.
	// Returns the sandbox name or an error.
	CreateSandbox(ctx context.Context, opts SandboxOpts) (string, error)

	// DeleteSandbox deletes a sandbox by name.
	DeleteSandbox(ctx context.Context, name string) error

	// ListSandboxes lists all sandboxes in this environment.
	ListSandboxes(ctx context.Context) ([]SandboxStatus, error)

	// Kill terminates an agent process.
	Kill(a agent.Agent) error
}

// SandboxOpts contains options for creating a sandbox.
type SandboxOpts struct {
	// Image is the container image or environment template to use.
	Image string

	// Provider is the AI provider for the sandbox ("claude", "codex", "gemini", etc).
	Provider string

	// Mode is the sandbox execution mode ("worker" for background jobs, "session" for interactive).
	Mode string

	// Env is a map of environment variables to set in the sandbox.
	Env map[string]string

	// Labels are key-value pairs for sandbox metadata and management.
	Labels map[string]string
}

// SandboxStatus represents the status of a sandbox.
type SandboxStatus struct {
	// Name is the unique identifier of the sandbox.
	Name string

	// Status is the current state of the sandbox ("running", "ready", "stopped", "dead", etc).
	Status string

	// Idle indicates whether the sandbox is currently idle (not running workloads).
	Idle bool
}

package controller

import (
	"context"

	"github.com/zanetworker/aimux/internal/mcpserver"
	"github.com/zanetworker/aimux/internal/spawn"
)

// RemoteStatus holds the result of a remote backend health check.
type RemoteStatus struct {
	Backend    string
	Sandboxes  []mcpserver.SandboxStatus
	IdleCount  int
	Error      string
}

// RemoteBackendStatus queries the remote backend for its current state.
// Returns sandbox list and idle count. Usable from TUI, Web, and CLI.
func RemoteBackendStatus(ctx context.Context, backend mcpserver.Backend) RemoteStatus {
	if backend == nil {
		return RemoteStatus{Error: "no remote backend configured"}
	}

	sandboxes, err := backend.ListSandboxes(ctx)
	if err != nil {
		return RemoteStatus{Error: err.Error()}
	}

	idle, _ := backend.IdleCount(ctx)

	return RemoteStatus{
		Sandboxes: sandboxes,
		IdleCount: idle,
	}
}

// RemoteSpawn creates count sandboxes via the backend and returns their names.
// This is the controller function that TUI, Web, and CLI call.
func RemoteSpawn(ctx context.Context, backend mcpserver.Backend, provider string, count int) ([]string, error) {
	if backend == nil {
		return nil, nil
	}

	var names []string
	for i := 0; i < count; i++ {
		name, err := backend.CreateSandbox(ctx, mcpserver.SandboxOpts{
			Mode: "worker",
			Labels: map[string]string{
				"provider": provider,
			},
		})
		if err != nil {
			return names, err
		}
		names = append(names, name)
	}
	return names, nil
}

// RemoteSession holds the result of launching an interactive remote session.
type RemoteSession struct {
	SandboxName string
	TmuxSession string
}

// RemoteLaunchSession creates a sandbox and opens an interactive terminal
// session via tmux. This is the controller function for interactive remote
// agents. Returns the sandbox and tmux session names.
func RemoteLaunchSession(provider, dir string, opts RemoteSessionOpts) (*RemoteSession, error) {
	result, err := spawn.LaunchInSandbox(provider, dir, spawn.SandboxOpts{
		Name:   opts.Name,
		Image:  opts.Image,
		Binary: opts.Binary,
	})
	if err != nil {
		return nil, err
	}
	return &RemoteSession{
		SandboxName: result.SandboxName,
		TmuxSession: result.TmuxSession,
	}, nil
}

// RemoteSessionOpts configures an interactive remote session.
type RemoteSessionOpts struct {
	Name    string
	Image   string
	Binary  string
}

// RemoteScaleDown deletes all sandboxes via the backend. Returns names deleted.
func RemoteScaleDown(ctx context.Context, backend mcpserver.Backend) ([]string, error) {
	if backend == nil {
		return nil, nil
	}

	sandboxes, err := backend.ListSandboxes(ctx)
	if err != nil {
		return nil, err
	}

	var deleted []string
	for _, sb := range sandboxes {
		if err := backend.DeleteSandbox(ctx, sb.Name); err != nil {
			return deleted, err
		}
		deleted = append(deleted, sb.Name)
	}
	return deleted, nil
}

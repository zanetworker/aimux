package controller

import (
	"context"

	aimuxcompose "github.com/zanetworker/aimux/internal/compose"
	"github.com/zanetworker/aimux/internal/mcpserver"
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
func RemoteBackendStatus(ctx context.Context, b mcpserver.Backend) RemoteStatus {
	if b == nil {
		return RemoteStatus{Error: "no remote backend configured"}
	}

	sandboxes, err := b.ListSandboxes(ctx)
	if err != nil {
		return RemoteStatus{Error: err.Error()}
	}

	idle, _ := b.IdleCount(ctx)

	return RemoteStatus{
		Sandboxes: sandboxes,
		IdleCount: idle,
	}
}

// RemoteSpawn creates count sandboxes via the backend and returns their names.
// This is the controller function that TUI, Web, and CLI call.
func RemoteSpawn(ctx context.Context, b mcpserver.Backend, provider string, count int) ([]string, error) {
	if b == nil {
		return nil, nil
	}

	var names []string
	for i := 0; i < count; i++ {
		name, err := b.CreateSandbox(ctx, mcpserver.SandboxOpts{
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

// RemoteSession holds the result of provisioning an interactive remote session.
type RemoteSession struct {
	SandboxName   string
	OTELSessionID string
}

// RemoteLaunchSession provisions a sandbox for an interactive remote session.
// The interactive terminal itself is established by the caller via
// terminal.NewOpenShellExec(SandboxName, ...).
func RemoteLaunchSession(engine *aimuxcompose.Engine, provider, dir string, opts RemoteSessionOpts) (*RemoteSession, error) {
	result, err := engine.LaunchInSandbox(provider, dir, aimuxcompose.LaunchOpts{
		Name:  opts.Name,
		Image: opts.Image,
	})
	if err != nil {
		return nil, err
	}
	return &RemoteSession{
		SandboxName:   result.SandboxName,
		OTELSessionID: result.OTELSessionID,
	}, nil
}

// RemoteSessionOpts configures an interactive remote session.
type RemoteSessionOpts struct {
	Name    string
	Image   string
	Binary  string
}

// RemoteScaleDown deletes all sandboxes via the backend. Returns names deleted.
func RemoteScaleDown(ctx context.Context, b mcpserver.Backend) ([]string, error) {
	if b == nil {
		return nil, nil
	}

	sandboxes, err := b.ListSandboxes(ctx)
	if err != nil {
		return nil, err
	}

	var deleted []string
	for _, sb := range sandboxes {
		if err := b.DeleteSandbox(ctx, sb.Name); err != nil {
			return deleted, err
		}
		deleted = append(deleted, sb.Name)
	}
	return deleted, nil
}

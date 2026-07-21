package compose

import (
	"context"
	"fmt"
	"sync"

	"github.com/zanetworker/aimux/internal/mcpserver"
	pkgcompose "github.com/zanetworker/agent-compose/pkg/compose"
)

type poolEntry struct {
	name string
	idle bool
}

// Backend implements backend.Backend using agent-compose.
type Backend struct {
	engine *Engine
	image  string
	mu     sync.Mutex
	pool   map[string]*poolEntry
}

// NewBackend creates a Backend wrapping a compose Engine.
func NewBackend(engine *Engine) *Backend {
	return &Backend{
		engine: engine,
		image:  engine.image,
		pool:   make(map[string]*poolEntry),
	}
}

func (b *Backend) CreateSandbox(ctx context.Context, opts mcpserver.SandboxOpts) (string, error) {
	image := opts.Image
	if image == "" {
		image = b.image
	}

	agent := &pkgcompose.Agent{
		Runtime: "claude-code",
		Image:   image,
		Env:     opts.Env,
		Sandbox: pkgcompose.SandboxOpts{
			Scope: "agent",
			Mode:  "all",
		},
	}

	run, err := b.engine.inner.Start(ctx, "", pkgcompose.RunOpts{
		Agent: agent,
	})
	if err != nil {
		return "", fmt.Errorf("compose backend: create sandbox: %w", err)
	}

	name := run.Sandbox
	if name == "" {
		name = run.Agent
	}

	b.mu.Lock()
	b.pool[name] = &poolEntry{name: name, idle: true}
	b.mu.Unlock()

	return name, nil
}

func (b *Backend) DeleteSandbox(ctx context.Context, name string) error {
	err := b.engine.KillSandbox(ctx, name)
	b.mu.Lock()
	delete(b.pool, name)
	b.mu.Unlock()
	return err
}

func (b *Backend) ListSandboxes(ctx context.Context) ([]mcpserver.SandboxStatus, error) {
	agents, err := b.engine.inner.List(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]mcpserver.SandboxStatus, len(agents))
	for i, a := range agents {
		b.mu.Lock()
		entry, inPool := b.pool[a.Name]
		idle := inPool && entry.idle
		b.mu.Unlock()

		result[i] = mcpserver.SandboxStatus{
			Name:   a.Name,
			Status: string(a.Status),
			Idle:   idle,
		}
	}
	return result, nil
}

func (b *Backend) ExecStream(ctx context.Context, name string, command []string) (mcpserver.ExecResult, error) {
	// Mark as busy
	b.mu.Lock()
	if entry, ok := b.pool[name]; ok {
		entry.idle = false
	}
	b.mu.Unlock()

	// Mark as idle when done
	defer func() {
		b.mu.Lock()
		if entry, ok := b.pool[name]; ok {
			entry.idle = true
		}
		b.mu.Unlock()
	}()

	// Execute command in sandbox using the stored executor
	err := b.engine.executor.ExecInSandbox(ctx, name, command)
	if err != nil {
		return mcpserver.ExecResult{ExitCode: 1, Output: err.Error()}, err
	}

	// Get output from agent logs
	output, err := b.engine.inner.AgentOutput(ctx, name)
	if err != nil {
		return mcpserver.ExecResult{ExitCode: 1, Output: err.Error()}, err
	}

	return mcpserver.ExecResult{ExitCode: 0, Output: output}, nil
}

func (b *Backend) IdleCount(_ context.Context) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	count := 0
	for _, entry := range b.pool {
		if entry.idle {
			count++
		}
	}
	return count, nil
}

// ClaimIdle returns the name of an idle sandbox and marks it busy.
func (b *Backend) ClaimIdle() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, entry := range b.pool {
		if entry.idle {
			entry.idle = false
			return entry.name
		}
	}
	return ""
}

// Release marks a sandbox as idle again.
func (b *Backend) Release(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if entry, ok := b.pool[name]; ok {
		entry.idle = true
	}
}

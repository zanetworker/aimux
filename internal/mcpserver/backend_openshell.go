package mcpserver

import (
	"context"
	"sync"

	"github.com/zanetworker/aimux/internal/openshell"
)

type poolEntry struct {
	name string
	idle bool
}

// OpenShellBackend implements Backend using the shared openshell client.
type OpenShellBackend struct {
	client *openshell.Client
	image  string
	mu     sync.Mutex
	pool   map[string]*poolEntry
}

// OpenShellBackendConfig configures the OpenShell backend.
type OpenShellBackendConfig struct {
	Binary   string
	Gateway  string
	Insecure bool
	Image    string
}

// NewOpenShellBackend creates an OpenShell backend.
func NewOpenShellBackend(cfg OpenShellBackendConfig) *OpenShellBackend {
	client := openshell.NewClient(openshell.Config{
		Binary:   cfg.Binary,
		Gateway:  cfg.Gateway,
		Insecure: cfg.Insecure,
	})
	return &OpenShellBackend{
		client: client,
		image:  cfg.Image,
		pool:   make(map[string]*poolEntry),
	}
}

func (b *OpenShellBackend) CreateSandbox(ctx context.Context, opts SandboxOpts) (string, error) {
	image := opts.Image
	if image == "" {
		image = b.image
	}
	name, err := b.client.CreateSandbox(ctx, openshell.CreateOpts{
		Image:  image,
		Labels: opts.Labels,
		NoKeep: opts.NoKeep,
	})
	if err != nil {
		return "", err
	}

	b.mu.Lock()
	b.pool[name] = &poolEntry{name: name, idle: true}
	b.mu.Unlock()
	return name, nil
}

func (b *OpenShellBackend) DeleteSandbox(ctx context.Context, name string) error {
	err := b.client.DeleteSandbox(ctx, name)
	b.mu.Lock()
	delete(b.pool, name)
	b.mu.Unlock()
	return err
}

func (b *OpenShellBackend) ListSandboxes(ctx context.Context) ([]SandboxStatus, error) {
	infos, err := b.client.ListSandboxes(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]SandboxStatus, len(infos))
	for i, info := range infos {
		result[i] = SandboxStatus{
			Name:   info.Name,
			Status: info.Status,
		}
	}
	return result, nil
}

func (b *OpenShellBackend) ExecStream(ctx context.Context, name string, command []string) (ExecResult, error) {
	b.mu.Lock()
	if entry, ok := b.pool[name]; ok {
		entry.idle = false
	}
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		if entry, ok := b.pool[name]; ok {
			entry.idle = true
		}
		b.mu.Unlock()
	}()

	r, err := b.client.Exec(ctx, name, command)
	return ExecResult{ExitCode: r.ExitCode, Output: r.Output}, err
}

func (b *OpenShellBackend) IdleCount(_ context.Context) (int, error) {
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

// claimIdle returns the name of an idle sandbox and marks it busy, or "" if none available.
func (b *OpenShellBackend) claimIdle() string {
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

// release marks a sandbox as idle again.
func (b *OpenShellBackend) release(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if entry, ok := b.pool[name]; ok {
		entry.idle = true
	}
}

package mcpserver

import (
	"context"
	"fmt"
)

// Pool manages a warm pool of pre-created sandboxes.
type Pool struct {
	backend  Backend
	warmSize int
}

// NewPool creates a pool manager.
func NewPool(backend Backend, warmSize int) *Pool {
	return &Pool{backend: backend, warmSize: warmSize}
}

// WarmUp pre-creates sandboxes to reach the warm pool size.
func (p *Pool) WarmUp(ctx context.Context) error {
	idle, err := p.backend.IdleCount(ctx)
	if err != nil {
		return fmt.Errorf("check idle count: %w", err)
	}
	deficit := p.warmSize - idle
	for i := 0; i < deficit; i++ {
		_, err := p.backend.CreateSandbox(ctx, SandboxOpts{
			Mode:   "worker",
			Labels: map[string]string{"aimux-pool": "warm"},
		})
		if err != nil {
			return fmt.Errorf("create warm sandbox %d/%d: %w", i+1, deficit, err)
		}
	}
	return nil
}

// EnsureCapacity verifies at least count idle sandboxes exist.
func (p *Pool) EnsureCapacity(ctx context.Context, count int) error {
	idle, err := p.backend.IdleCount(ctx)
	if err != nil {
		return err
	}
	if idle >= count {
		return nil
	}
	deficit := count - idle
	for i := 0; i < deficit; i++ {
		_, err := p.backend.CreateSandbox(ctx, SandboxOpts{
			Mode:   "worker",
			Labels: map[string]string{"aimux-pool": "warm"},
		})
		if err != nil {
			return fmt.Errorf("scale up %d/%d: %w", i+1, deficit, err)
		}
	}
	return nil
}

package mcpserver

import (
	"context"
	"fmt"
	"testing"
)

func TestPool_WarmUp(t *testing.T) {
	created := 0
	b := &warmPoolBackend{
		createFn: func(ctx context.Context, opts SandboxOpts) (string, error) {
			created++
			return fmt.Sprintf("warm-%d", created), nil
		},
		idleCount: &created,
	}
	p := NewPool(b, 3)
	if err := p.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}
	if created != 3 {
		t.Errorf("expected 3 created, got %d", created)
	}
}

func TestPool_WarmUp_AlreadyWarm(t *testing.T) {
	created := 0
	existing := 3
	b := &warmPoolBackend{
		createFn: func(ctx context.Context, opts SandboxOpts) (string, error) {
			created++
			return fmt.Sprintf("warm-%d", created), nil
		},
		idleCount: &existing,
	}
	p := NewPool(b, 3)
	if err := p.WarmUp(context.Background()); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}
	if created != 0 {
		t.Errorf("expected 0 created (already warm), got %d", created)
	}
}

func TestPool_EnsureCapacity(t *testing.T) {
	created := 0
	idle := 1
	b := &warmPoolBackend{
		createFn: func(ctx context.Context, opts SandboxOpts) (string, error) {
			created++
			idle++
			return fmt.Sprintf("extra-%d", created), nil
		},
		idleCount: &idle,
	}
	p := NewPool(b, 3)
	if err := p.EnsureCapacity(context.Background(), 3); err != nil {
		t.Fatalf("EnsureCapacity: %v", err)
	}
	if created != 2 {
		t.Errorf("expected 2 created (need 3, had 1), got %d", created)
	}
}

func TestPool_EnsureCapacity_AlreadySufficient(t *testing.T) {
	created := 0
	idle := 5
	b := &warmPoolBackend{
		createFn: func(ctx context.Context, opts SandboxOpts) (string, error) {
			created++
			return "x", nil
		},
		idleCount: &idle,
	}
	p := NewPool(b, 3)
	if err := p.EnsureCapacity(context.Background(), 2); err != nil {
		t.Fatalf("EnsureCapacity: %v", err)
	}
	if created != 0 {
		t.Errorf("expected 0 created (5 >= 2), got %d", created)
	}
}

func TestPool_WarmUp_CreateError(t *testing.T) {
	b := &warmPoolBackend{
		createFn: func(ctx context.Context, opts SandboxOpts) (string, error) {
			return "", fmt.Errorf("out of resources")
		},
		idleCount: new(int),
	}
	p := NewPool(b, 2)
	err := p.WarmUp(context.Background())
	if err == nil {
		t.Fatal("expected error from WarmUp")
	}
}

func TestPool_Labels(t *testing.T) {
	var gotLabels map[string]string
	b := &warmPoolBackend{
		createFn: func(ctx context.Context, opts SandboxOpts) (string, error) {
			gotLabels = opts.Labels
			return "warm-1", nil
		},
		idleCount: new(int),
	}
	p := NewPool(b, 1)
	_ = p.WarmUp(context.Background())

	if gotLabels == nil || gotLabels["aimux-pool"] != "warm" {
		t.Errorf("expected label aimux-pool=warm, got %v", gotLabels)
	}
}

// warmPoolBackend is a test double for pool tests.
type warmPoolBackend struct {
	createFn  func(ctx context.Context, opts SandboxOpts) (string, error)
	idleCount *int
}

func (b *warmPoolBackend) CreateSandbox(ctx context.Context, opts SandboxOpts) (string, error) {
	return b.createFn(ctx, opts)
}
func (b *warmPoolBackend) DeleteSandbox(_ context.Context, _ string) error    { return nil }
func (b *warmPoolBackend) ListSandboxes(_ context.Context) ([]SandboxStatus, error) { return nil, nil }
func (b *warmPoolBackend) ExecStream(_ context.Context, _ string, _ []string) (ExecResult, error) {
	return ExecResult{}, nil
}
func (b *warmPoolBackend) IdleCount(_ context.Context) (int, error) { return *b.idleCount, nil }

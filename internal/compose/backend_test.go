package compose

import (
	"bytes"
	"context"
	"testing"

	"github.com/zanetworker/aimux/internal/mcpserver"
	pkgcompose "github.com/zanetworker/agent-compose/pkg/compose"
)

// Compile-time check that Backend implements mcpserver.Backend.
var _ mcpserver.Backend = (*Backend)(nil)

func testEngine(t *testing.T) *Engine {
	t.Helper()
	var buf bytes.Buffer
	e, err := New(Options{
		Executor: pkgcompose.NewDryRunExecutor(&buf),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func TestBackend_IdleCount_Empty(t *testing.T) {
	b := NewBackend(testEngine(t))
	ctx := context.Background()

	count, err := b.IdleCount(ctx)
	if err != nil {
		t.Fatalf("IdleCount: %v", err)
	}
	if count != 0 {
		t.Errorf("IdleCount = %d, want 0", count)
	}
}

func TestBackend_ClaimRelease(t *testing.T) {
	b := NewBackend(testEngine(t))

	// Manually seed the pool (simulating a created sandbox)
	b.mu.Lock()
	b.pool["sb-1"] = &poolEntry{name: "sb-1", idle: true}
	b.mu.Unlock()

	name := b.ClaimIdle()
	if name != "sb-1" {
		t.Errorf("ClaimIdle = %q, want sb-1", name)
	}

	// After claim, idle count should be 0
	count, _ := b.IdleCount(context.Background())
	if count != 0 {
		t.Errorf("IdleCount after claim = %d, want 0", count)
	}

	// Release it
	b.Release("sb-1")
	count, _ = b.IdleCount(context.Background())
	if count != 1 {
		t.Errorf("IdleCount after release = %d, want 1", count)
	}
}

func TestBackend_ClaimIdle_NoneAvailable(t *testing.T) {
	b := NewBackend(testEngine(t))
	name := b.ClaimIdle()
	if name != "" {
		t.Errorf("ClaimIdle on empty pool = %q, want empty", name)
	}
}

func TestBackend_DeleteRemovesFromPool(t *testing.T) {
	b := NewBackend(testEngine(t))

	b.mu.Lock()
	b.pool["sb-1"] = &poolEntry{name: "sb-1", idle: true}
	b.mu.Unlock()

	// DeleteSandbox may fail (dry-run executor), but pool should still be cleaned
	_ = b.DeleteSandbox(context.Background(), "sb-1")

	b.mu.Lock()
	_, exists := b.pool["sb-1"]
	b.mu.Unlock()
	if exists {
		t.Error("sandbox sb-1 still in pool after delete")
	}
}

func TestBackend_CreateSandbox(t *testing.T) {
	b := NewBackend(testEngine(t))
	ctx := context.Background()

	// DryRunExecutor will succeed but won't actually create anything
	name, err := b.CreateSandbox(ctx, mcpserver.SandboxOpts{
		Image: "test-image",
		Mode:  "worker",
		Env:   map[string]string{"TEST": "value"},
	})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	if name == "" {
		t.Error("CreateSandbox returned empty name")
	}

	// Should be in pool and idle
	b.mu.Lock()
	entry, exists := b.pool[name]
	b.mu.Unlock()
	if !exists {
		t.Error("sandbox not in pool after create")
	}
	if !entry.idle {
		t.Error("sandbox not idle after create")
	}
}

func TestBackend_ListSandboxes_Empty(t *testing.T) {
	b := NewBackend(testEngine(t))
	ctx := context.Background()

	sandboxes, err := b.ListSandboxes(ctx)
	if err != nil {
		t.Fatalf("ListSandboxes: %v", err)
	}
	if len(sandboxes) != 0 {
		t.Errorf("ListSandboxes = %d sandboxes, want 0", len(sandboxes))
	}
}

func TestBackend_ExecStream_MarksNotIdle(t *testing.T) {
	b := NewBackend(testEngine(t))
	ctx := context.Background()

	// Seed pool
	b.mu.Lock()
	b.pool["sb-1"] = &poolEntry{name: "sb-1", idle: true}
	b.mu.Unlock()

	// ExecStream will fail with dry-run executor, but we're testing the idle flag behavior
	_, _ = b.ExecStream(ctx, "sb-1", []string{"echo", "test"})

	// After ExecStream completes (even with error), should be idle again due to defer
	b.mu.Lock()
	entry := b.pool["sb-1"]
	b.mu.Unlock()
	if !entry.idle {
		t.Error("sandbox not idle after ExecStream completes")
	}
}

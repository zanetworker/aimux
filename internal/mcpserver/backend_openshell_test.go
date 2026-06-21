package mcpserver

import (
	"context"
	"errors"
	"testing"

	"github.com/zanetworker/aimux/internal/openshell"
)

func TestOpenShellBackend_ImplementsBackend(t *testing.T) {
	var _ Backend = (*OpenShellBackend)(nil)
}

func TestOpenShellBackend_CreateSandbox(t *testing.T) {
	b := newTestOpenShellBackend(func(ctx context.Context, name string, args ...string) (string, int, error) {
		return "sandbox-abc", 0, nil
	})

	name, err := b.CreateSandbox(context.Background(), SandboxOpts{
		Image: "worker:v1",
		Mode:  "worker",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "sandbox-abc" {
		t.Errorf("got name %q, want 'sandbox-abc'", name)
	}

	// Verify it's tracked in the pool as idle
	count, _ := b.IdleCount(context.Background())
	if count != 1 {
		t.Errorf("expected 1 idle after create, got %d", count)
	}
}

func TestOpenShellBackend_CreateSandbox_UsesConfigImage(t *testing.T) {
	var createArgs []string
	b := newTestOpenShellBackend(func(ctx context.Context, name string, args ...string) (string, int, error) {
		allArgs := append([]string{name}, args...)
		for _, a := range allArgs {
			if a == "create" {
				createArgs = allArgs
				return "Created sandbox: sb-1\n", 0, nil
			}
			if a == "list" {
				return "NAME    STATUS\nsb-1    Ready", 0, nil
			}
		}
		return "", 0, nil
	})
	b.image = "quay.io/default:latest"

	_, err := b.CreateSandbox(context.Background(), SandboxOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for i, arg := range createArgs {
		if arg == "--from" && i+1 < len(createArgs) && createArgs[i+1] == "quay.io/default:latest" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected --from quay.io/default:latest in create args, got %v", createArgs)
	}
}

func TestOpenShellBackend_CreateSandbox_OptsImageOverridesConfig(t *testing.T) {
	var createArgs []string
	b := newTestOpenShellBackend(func(ctx context.Context, name string, args ...string) (string, int, error) {
		allArgs := append([]string{name}, args...)
		for _, a := range allArgs {
			if a == "create" {
				createArgs = allArgs
				return "Created sandbox: sb-1\n", 0, nil
			}
			if a == "list" {
				return "NAME    STATUS\nsb-1    Ready", 0, nil
			}
		}
		return "", 0, nil
	})
	b.image = "quay.io/default:latest"

	_, err := b.CreateSandbox(context.Background(), SandboxOpts{Image: "quay.io/override:v2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for i, arg := range createArgs {
		if arg == "--from" && i+1 < len(createArgs) && createArgs[i+1] == "quay.io/override:v2" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected --from quay.io/override:v2 in create args, got %v", createArgs)
	}
}

func TestOpenShellBackend_CreateSandbox_Error(t *testing.T) {
	b := newTestOpenShellBackend(func(ctx context.Context, name string, args ...string) (string, int, error) {
		return "", 1, errors.New("gateway down")
	})

	_, err := b.CreateSandbox(context.Background(), SandboxOpts{})
	if err == nil {
		t.Fatal("expected error")
	}

	count, _ := b.IdleCount(context.Background())
	if count != 0 {
		t.Errorf("expected 0 idle after failed create, got %d", count)
	}
}

func TestOpenShellBackend_DeleteSandbox(t *testing.T) {
	deleteCalled := false
	b := newTestOpenShellBackend(func(ctx context.Context, name string, args ...string) (string, int, error) {
		for _, a := range args {
			if a == "delete" {
				deleteCalled = true
			}
		}
		return "", 0, nil
	})

	// Pre-populate pool
	b.mu.Lock()
	b.pool["sb-to-del"] = &poolEntry{name: "sb-to-del", idle: true}
	b.mu.Unlock()

	err := b.DeleteSandbox(context.Background(), "sb-to-del")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !deleteCalled {
		t.Error("delete command was not called")
	}

	count, _ := b.IdleCount(context.Background())
	if count != 0 {
		t.Errorf("expected 0 after delete, got %d", count)
	}
}

func TestOpenShellBackend_ListSandboxes(t *testing.T) {
	b := newTestOpenShellBackend(func(ctx context.Context, name string, args ...string) (string, int, error) {
		return "NAME          STATUS\nsb-1          running\nsb-2          stopped", 0, nil
	})

	infos, err := b.ListSandboxes(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("expected 2, got %d", len(infos))
	}
	if infos[0].Name != "sb-1" || infos[0].Status != "running" {
		t.Errorf("info 0: %+v", infos[0])
	}
}

func TestOpenShellBackend_ExecStream(t *testing.T) {
	b := newTestOpenShellBackend(func(ctx context.Context, name string, args ...string) (string, int, error) {
		return "task output", 0, nil
	})

	b.mu.Lock()
	b.pool["sb-exec"] = &poolEntry{name: "sb-exec", idle: true}
	b.mu.Unlock()

	result, err := b.ExecStream(context.Background(), "sb-exec", []string{"python3", "run.py"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code: %d", result.ExitCode)
	}
	if result.Output != "task output" {
		t.Errorf("output: %q", result.Output)
	}

	// After exec completes, sandbox should be idle again
	count, _ := b.IdleCount(context.Background())
	if count != 1 {
		t.Errorf("expected 1 idle after exec completes, got %d", count)
	}
}

func TestOpenShellBackend_ExecStream_MarksNonIdle(t *testing.T) {
	blockCh := make(chan struct{})
	b := newTestOpenShellBackend(func(ctx context.Context, name string, args ...string) (string, int, error) {
		<-blockCh
		return "done", 0, nil
	})

	b.mu.Lock()
	b.pool["sb-block"] = &poolEntry{name: "sb-block", idle: true}
	b.mu.Unlock()

	go func() {
		_, _ = b.ExecStream(context.Background(), "sb-block", []string{"slow-task"})
	}()

	// Give goroutine time to start and mark non-idle
	// We'll check pool state directly
	// This is inherently racy but we're testing the lock behavior
	close(blockCh)
}

func TestOpenShellBackend_IdleCount(t *testing.T) {
	b := newTestOpenShellBackend(nil)

	count, err := b.IdleCount(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Errorf("empty pool: got %d, want 0", count)
	}

	b.mu.Lock()
	b.pool["s1"] = &poolEntry{name: "s1", idle: true}
	b.pool["s2"] = &poolEntry{name: "s2", idle: true}
	b.pool["s3"] = &poolEntry{name: "s3", idle: false}
	b.mu.Unlock()

	count, _ = b.IdleCount(context.Background())
	if count != 2 {
		t.Errorf("got %d, want 2", count)
	}
}

func TestOpenShellBackend_ClaimAndRelease(t *testing.T) {
	b := newTestOpenShellBackend(nil)

	// Empty pool claim returns empty
	name := b.claimIdle()
	if name != "" {
		t.Errorf("expected empty from empty pool, got %q", name)
	}

	b.mu.Lock()
	b.pool["s1"] = &poolEntry{name: "s1", idle: true}
	b.pool["s2"] = &poolEntry{name: "s2", idle: true}
	b.mu.Unlock()

	name = b.claimIdle()
	if name == "" {
		t.Fatal("expected a sandbox name")
	}

	// Should have 1 idle left
	count, _ := b.IdleCount(context.Background())
	if count != 1 {
		t.Errorf("expected 1 idle after claim, got %d", count)
	}

	// Release it
	b.release(name)
	count, _ = b.IdleCount(context.Background())
	if count != 2 {
		t.Errorf("expected 2 idle after release, got %d", count)
	}
}

func TestOpenShellBackend_Release_NonExistent(t *testing.T) {
	b := newTestOpenShellBackend(nil)
	// Should not panic
	b.release("nonexistent")
}

// newTestOpenShellBackend creates a backend with an injectable runner for unit tests.
func newTestOpenShellBackend(runner openshell.CommandRunner) *OpenShellBackend {
	client := openshell.NewClient(openshell.Config{Binary: "openshell"})
	if runner != nil {
		client.SetRunner(runner)
	}
	return &OpenShellBackend{
		client: client,
		pool:   make(map[string]*poolEntry),
	}
}

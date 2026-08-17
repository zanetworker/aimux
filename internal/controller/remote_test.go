package controller

import (
	"context"
	"fmt"
	"testing"

	"github.com/zanetworker/aimux/internal/mcpserver"
)

func TestRemoteBackendStatus_NilBackend(t *testing.T) {
	status := RemoteBackendStatus(context.Background(), nil)
	if status.Error == "" {
		t.Error("expected error for nil backend")
	}
}

func TestRemoteBackendStatus_ListError(t *testing.T) {
	b := &fakeRemoteBackend{
		listErr: fmt.Errorf("gateway unreachable"),
	}
	status := RemoteBackendStatus(context.Background(), b)
	if status.Error == "" {
		t.Error("expected error")
	}
}

func TestRemoteBackendStatus_Success(t *testing.T) {
	b := &fakeRemoteBackend{
		sandboxes: []mcpserver.SandboxStatus{
			{Name: "sb-1", Status: "running", Idle: true},
			{Name: "sb-2", Status: "running", Idle: false},
		},
		idle: 1,
	}
	status := RemoteBackendStatus(context.Background(), b)
	if status.Error != "" {
		t.Fatalf("unexpected error: %s", status.Error)
	}
	if len(status.Sandboxes) != 2 {
		t.Errorf("expected 2 sandboxes, got %d", len(status.Sandboxes))
	}
	if status.IdleCount != 1 {
		t.Errorf("expected 1 idle, got %d", status.IdleCount)
	}
}

func TestRemoteSpawn_NilBackend(t *testing.T) {
	names, err := RemoteSpawn(context.Background(), nil, "claude", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if names != nil {
		t.Error("expected nil for nil backend")
	}
}

func TestRemoteSpawn_CreatesMultiple(t *testing.T) {
	created := 0
	b := &fakeRemoteBackend{
		createFn: func(ctx context.Context, opts mcpserver.SandboxOpts) (string, error) {
			created++
			if opts.Labels["provider"] != "claude" {
				t.Errorf("expected provider=claude, got %q", opts.Labels["provider"])
			}
			return fmt.Sprintf("sb-%d", created), nil
		},
	}
	names, err := RemoteSpawn(context.Background(), b, "claude", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 3 {
		t.Errorf("expected 3 names, got %d", len(names))
	}
	if created != 3 {
		t.Errorf("expected 3 created, got %d", created)
	}
}

func TestRemoteScaleDown_NilBackend(t *testing.T) {
	deleted, err := RemoteScaleDown(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleted != nil {
		t.Error("expected nil for nil backend")
	}
}

func TestRemoteScaleDown_DeletesAll(t *testing.T) {
	var deletedNames []string
	b := &fakeRemoteBackend{
		sandboxes: []mcpserver.SandboxStatus{
			{Name: "sb-1"},
			{Name: "sb-2"},
		},
		deleteFn: func(ctx context.Context, name string) error {
			deletedNames = append(deletedNames, name)
			return nil
		},
	}
	deleted, err := RemoteScaleDown(context.Background(), b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deleted) != 2 {
		t.Errorf("expected 2 deleted, got %d", len(deleted))
	}
}

// fakeRemoteBackend is a test double for controller remote tests.
type fakeRemoteBackend struct {
	sandboxes []mcpserver.SandboxStatus
	idle      int
	listErr   error
	createFn  func(ctx context.Context, opts mcpserver.SandboxOpts) (string, error)
	deleteFn  func(ctx context.Context, name string) error
}

func (f *fakeRemoteBackend) CreateSandbox(ctx context.Context, opts mcpserver.SandboxOpts) (string, error) {
	if f.createFn != nil {
		return f.createFn(ctx, opts)
	}
	return "fake", nil
}

func (f *fakeRemoteBackend) DeleteSandbox(ctx context.Context, name string) error {
	if f.deleteFn != nil {
		return f.deleteFn(ctx, name)
	}
	return nil
}

func (f *fakeRemoteBackend) ListSandboxes(_ context.Context) ([]mcpserver.SandboxStatus, error) {
	return f.sandboxes, f.listErr
}

func (f *fakeRemoteBackend) ExecStream(_ context.Context, _ string, _ []string) (mcpserver.ExecResult, error) {
	return mcpserver.ExecResult{}, nil
}

func (f *fakeRemoteBackend) IdleCount(_ context.Context) (int, error) {
	return f.idle, nil
}

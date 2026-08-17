package mcpserver

import (
	"context"
	"errors"
	"testing"
)

func TestBackendInterface_MockSatisfies(t *testing.T) {
	var _ Backend = (*fakeBackend)(nil)
}

func TestBackendInterface_AllMethods(t *testing.T) {
	b := &fakeBackend{
		createFn: func(ctx context.Context, opts SandboxOpts) (string, error) {
			if opts.Mode != "worker" {
				t.Errorf("mode: got %q, want 'worker'", opts.Mode)
			}
			return "sb-1", nil
		},
	}

	name, err := b.CreateSandbox(context.Background(), SandboxOpts{Mode: "worker"})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	if name != "sb-1" {
		t.Errorf("name: got %q, want 'sb-1'", name)
	}

	if err := b.DeleteSandbox(context.Background(), "sb-1"); err != nil {
		t.Fatalf("DeleteSandbox: %v", err)
	}

	infos, err := b.ListSandboxes(context.Background())
	if err != nil {
		t.Fatalf("ListSandboxes: %v", err)
	}
	if infos != nil {
		t.Errorf("expected nil, got %v", infos)
	}

	result, err := b.ExecStream(context.Background(), "sb-1", []string{"echo", "hi"})
	if err != nil {
		t.Fatalf("ExecStream: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode: got %d", result.ExitCode)
	}

	count, err := b.IdleCount(context.Background())
	if err != nil {
		t.Fatalf("IdleCount: %v", err)
	}
	if count != 0 {
		t.Errorf("IdleCount: got %d", count)
	}
}

func TestBackendInterface_ErrorPropagation(t *testing.T) {
	sentinel := errors.New("backend unavailable")
	b := &fakeBackend{
		createFn: func(ctx context.Context, opts SandboxOpts) (string, error) {
			return "", sentinel
		},
	}

	_, err := b.CreateSandbox(context.Background(), SandboxOpts{})
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
}

func TestSandboxOpts_Fields(t *testing.T) {
	opts := SandboxOpts{
		Image:  "quay.io/agent:latest",
		Mode:   "session",
		Env:    map[string]string{"KEY": "val"},
		Labels: map[string]string{"team": "alpha"},
		NoKeep: true,
	}
	if opts.Image != "quay.io/agent:latest" {
		t.Errorf("Image: %q", opts.Image)
	}
	if opts.Mode != "session" {
		t.Errorf("Mode: %q", opts.Mode)
	}
	if opts.Env["KEY"] != "val" {
		t.Error("Env not set")
	}
	if !opts.NoKeep {
		t.Error("NoKeep should be true")
	}
}

// fakeBackend is a test double implementing Backend with overridable functions.
type fakeBackend struct {
	createFn func(ctx context.Context, opts SandboxOpts) (string, error)
}

func (f *fakeBackend) CreateSandbox(ctx context.Context, opts SandboxOpts) (string, error) {
	if f.createFn != nil {
		return f.createFn(ctx, opts)
	}
	return "fake-sb", nil
}

func (f *fakeBackend) DeleteSandbox(_ context.Context, _ string) error { return nil }

func (f *fakeBackend) ListSandboxes(_ context.Context) ([]SandboxStatus, error) { return nil, nil }

func (f *fakeBackend) ExecStream(_ context.Context, _ string, _ []string) (ExecResult, error) {
	return ExecResult{ExitCode: 0, Output: "ok"}, nil
}

func (f *fakeBackend) IdleCount(_ context.Context) (int, error) { return 0, nil }

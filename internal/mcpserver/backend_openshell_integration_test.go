//go:build integration

package mcpserver

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// These tests require:
//   - openshell CLI installed and in PATH
//   - OpenShell gateway running (openshell gateway start)
//
// Run with: go test ./internal/mcpserver/ -tags integration -timeout 120s -v

func skipIfNoGateway(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("openshell"); err != nil {
		t.Skip("openshell CLI not found in PATH")
	}
}

func integrationBackend() *OpenShellBackend {
	cfg := OpenShellBackendConfig{
		Gateway: os.Getenv("OPENSHELL_GATEWAY"),
		Image:   os.Getenv("OPENSHELL_IMAGE"),
	}
	return NewOpenShellBackend(cfg)
}

func TestIntegration_OpenShellBackend_CreateAndDelete(t *testing.T) {
	skipIfNoGateway(t)
	b := integrationBackend()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	name, err := b.CreateSandbox(ctx, SandboxOpts{Mode: "worker"})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	t.Logf("created: %s", name)

	// Should be tracked in pool
	count, _ := b.IdleCount(ctx)
	if count < 1 {
		t.Errorf("expected at least 1 idle, got %d", count)
	}

	// Clean up
	if err := b.DeleteSandbox(ctx, name); err != nil {
		t.Errorf("DeleteSandbox: %v", err)
	}

	count, _ = b.IdleCount(ctx)
	if count != 0 {
		t.Errorf("expected 0 idle after delete, got %d", count)
	}
}

func TestIntegration_OpenShellBackend_ExecStream(t *testing.T) {
	skipIfNoGateway(t)
	b := integrationBackend()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	name, err := b.CreateSandbox(ctx, SandboxOpts{Mode: "worker"})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	defer b.DeleteSandbox(ctx, name)

	result, err := b.ExecStream(ctx, name, []string{"echo", "hello from backend"})
	if err != nil {
		t.Fatalf("ExecStream: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code: %d", result.ExitCode)
	}
	if result.Output != "hello from backend" {
		t.Errorf("output: %q", result.Output)
	}

	// After exec, should be idle again
	count, _ := b.IdleCount(ctx)
	if count != 1 {
		t.Errorf("expected 1 idle after exec, got %d", count)
	}
}

func TestIntegration_OpenShellBackend_ListSandboxes(t *testing.T) {
	skipIfNoGateway(t)
	b := integrationBackend()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	name, err := b.CreateSandbox(ctx, SandboxOpts{Mode: "worker"})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	defer b.DeleteSandbox(ctx, name)

	sandboxes, err := b.ListSandboxes(ctx)
	if err != nil {
		t.Fatalf("ListSandboxes: %v", err)
	}

	found := false
	for _, sb := range sandboxes {
		if sb.Name == name {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("sandbox %s not found in list (%d total)", name, len(sandboxes))
	}
}

func TestIntegration_Pool_WarmUp(t *testing.T) {
	skipIfNoGateway(t)
	b := integrationBackend()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	p := NewPool(b, 2)
	if err := p.WarmUp(ctx); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}

	count, _ := b.IdleCount(ctx)
	if count < 2 {
		t.Errorf("expected at least 2 idle after warm-up, got %d", count)
	}

	// Claim one
	name := b.claimIdle()
	if name == "" {
		t.Fatal("expected to claim an idle sandbox")
	}

	count, _ = b.IdleCount(ctx)
	if count < 1 {
		t.Errorf("expected at least 1 idle after claim, got %d", count)
	}

	// Clean up all
	sandboxes, _ := b.ListSandboxes(ctx)
	for _, sb := range sandboxes {
		b.DeleteSandbox(ctx, sb.Name)
	}
}

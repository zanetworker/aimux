//go:build integration

package openshell

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
// Run with: go test ./internal/openshell/ -tags integration -timeout 120s -v
//
// Set OPENSHELL_GATEWAY to override the gateway endpoint.
// Set OPENSHELL_IMAGE to override the sandbox image.

func skipIfNoOpenShell(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("openshell"); err != nil {
		t.Skip("openshell CLI not found in PATH")
	}
}

func integrationConfig() Config {
	cfg := Config{Binary: "openshell"}
	if gw := os.Getenv("OPENSHELL_GATEWAY"); gw != "" {
		cfg.Gateway = gw
	}
	return cfg
}

func TestIntegration_Status(t *testing.T) {
	skipIfNoOpenShell(t)
	c := NewClient(integrationConfig())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := c.Status(ctx); err != nil {
		t.Fatalf("gateway not reachable: %v\nIs the gateway running? Try: openshell gateway start", err)
	}
}

func TestIntegration_CreateAndDeleteSandbox(t *testing.T) {
	skipIfNoOpenShell(t)
	c := NewClient(integrationConfig())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Verify gateway is up before creating
	if err := c.Status(ctx); err != nil {
		t.Skipf("gateway not reachable, skipping: %v", err)
	}

	image := os.Getenv("OPENSHELL_IMAGE")

	name, err := c.CreateSandbox(ctx, CreateOpts{
		Image: image,
		Labels: map[string]string{
			"aimux-test": "integration",
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	if name == "" {
		t.Fatal("CreateSandbox returned empty name")
	}
	t.Logf("created sandbox: %s", name)

	// Clean up
	defer func() {
		delCtx, delCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer delCancel()
		if err := c.DeleteSandbox(delCtx, name); err != nil {
			t.Errorf("DeleteSandbox(%s) failed: %v", name, err)
		}
	}()

	// List should include our sandbox
	infos, err := c.ListSandboxes(ctx)
	if err != nil {
		t.Fatalf("ListSandboxes failed: %v", err)
	}
	found := false
	for _, info := range infos {
		if info.Name == name {
			found = true
			t.Logf("found sandbox %s with status %s", info.Name, info.Status)
			break
		}
	}
	if !found {
		t.Errorf("sandbox %s not found in list (got %d sandboxes)", name, len(infos))
	}
}

func TestIntegration_ExecInSandbox(t *testing.T) {
	skipIfNoOpenShell(t)
	c := NewClient(integrationConfig())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := c.Status(ctx); err != nil {
		t.Skipf("gateway not reachable, skipping: %v", err)
	}

	image := os.Getenv("OPENSHELL_IMAGE")

	name, err := c.CreateSandbox(ctx, CreateOpts{Image: image})
	if err != nil {
		t.Fatalf("CreateSandbox failed: %v", err)
	}
	t.Logf("created sandbox: %s", name)

	defer func() {
		delCtx, delCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer delCancel()
		c.DeleteSandbox(delCtx, name)
	}()

	// Run a simple command
	result, err := c.Exec(ctx, name, []string{"echo", "hello from sandbox"})
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code: got %d, want 0", result.ExitCode)
	}
	if result.Output != "hello from sandbox" {
		t.Errorf("output: got %q, want 'hello from sandbox'", result.Output)
	}

	// Run a command that exits non-zero
	result, err = c.Exec(ctx, name, []string{"sh", "-c", "exit 42"})
	if err == nil {
		t.Error("expected error for exit 42")
	}
	if result.ExitCode != 42 {
		t.Errorf("exit code: got %d, want 42", result.ExitCode)
	}

	// Run a command that produces multi-line output
	result, err = c.Exec(ctx, name, []string{"sh", "-c", "echo line1 && echo line2 && echo line3"})
	if err != nil {
		t.Fatalf("multi-line exec failed: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code: got %d, want 0", result.ExitCode)
	}
	lines := len(splitNonEmpty(result.Output))
	if lines != 3 {
		t.Errorf("expected 3 lines, got %d: %q", lines, result.Output)
	}
}

func TestIntegration_SandboxIsolation(t *testing.T) {
	skipIfNoOpenShell(t)
	c := NewClient(integrationConfig())

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := c.Status(ctx); err != nil {
		t.Skipf("gateway not reachable, skipping: %v", err)
	}

	image := os.Getenv("OPENSHELL_IMAGE")

	// Create two sandboxes
	sb1, err := c.CreateSandbox(ctx, CreateOpts{Image: image})
	if err != nil {
		t.Fatalf("CreateSandbox 1 failed: %v", err)
	}
	defer func() {
		dCtx, dCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer dCancel()
		c.DeleteSandbox(dCtx, sb1)
	}()

	sb2, err := c.CreateSandbox(ctx, CreateOpts{Image: image})
	if err != nil {
		t.Fatalf("CreateSandbox 2 failed: %v", err)
	}
	defer func() {
		dCtx, dCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer dCancel()
		c.DeleteSandbox(dCtx, sb2)
	}()

	// Write a file in sandbox 1
	_, err = c.Exec(ctx, sb1, []string{"sh", "-c", "echo secret > /tmp/testfile"})
	if err != nil {
		t.Fatalf("write in sb1 failed: %v", err)
	}

	// Verify sandbox 2 cannot see it
	result, err := c.Exec(ctx, sb2, []string{"cat", "/tmp/testfile"})
	if err == nil && result.ExitCode == 0 {
		t.Error("sandbox 2 should NOT be able to read sandbox 1's /tmp/testfile (isolation broken)")
	}
}

func splitNonEmpty(s string) []string {
	var result []string
	for _, line := range splitLines(s) {
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

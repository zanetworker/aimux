package openshell

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

func TestNewClient_DefaultBinary(t *testing.T) {
	c := NewClient(Config{})
	if c.cfg.Binary != "openshell" {
		t.Errorf("expected default binary 'openshell', got %q", c.cfg.Binary)
	}
}

func TestNewClient_CustomBinary(t *testing.T) {
	c := NewClient(Config{Binary: "/usr/local/bin/openshell"})
	if c.cfg.Binary != "/usr/local/bin/openshell" {
		t.Errorf("got %q", c.cfg.Binary)
	}
}

func TestClient_CreateSandbox_MissingBinary(t *testing.T) {
	c := NewClient(Config{Binary: "/nonexistent/openshell"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.CreateSandbox(ctx, CreateOpts{Name: "test"})
	if err == nil {
		t.Fatal("expected error when binary not found")
	}
}

func TestClient_CreateSandbox_InjectableRunner(t *testing.T) {
	callCount := 0
	c := NewClient(Config{Binary: "openshell"})
	c.SetRunner(func(ctx context.Context, name string, args ...string) (string, int, error) {
		callCount++
		allArgs := append([]string{name}, args...)
		for _, a := range allArgs {
			if a == "create" {
				return "Created sandbox: my-sandbox\n", 0, nil
			}
			if a == "list" {
				return "NAME          STATUS\nmy-sandbox    Ready", 0, nil
			}
		}
		return "", 0, nil
	})

	name, err := c.CreateSandbox(context.Background(), CreateOpts{Image: "worker:latest"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "my-sandbox" {
		t.Errorf("got name %q, want 'my-sandbox'", name)
	}
	if callCount < 2 {
		t.Errorf("expected at least 2 calls (create + list), got %d", callCount)
	}
}

func TestClient_CreateSandbox_GatewayArgs(t *testing.T) {
	var createArgs []string
	c := NewClient(Config{
		Binary:   "openshell",
		Gateway:  "http://gw:8090",
		Insecure: true,
	})
	c.SetRunner(func(ctx context.Context, name string, args ...string) (string, int, error) {
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

	_, err := c.CreateSandbox(context.Background(), CreateOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContains(t, createArgs, "--gateway-endpoint")
	assertContains(t, createArgs, "http://gw:8090")
	assertContains(t, createArgs, "--gateway-insecure")
}

func TestClient_Exec_CorrectSyntax(t *testing.T) {
	var gotArgs []string
	c := NewClient(Config{Binary: "openshell"})
	c.SetRunner(func(ctx context.Context, name string, args ...string) (string, int, error) {
		gotArgs = append([]string{name}, args...)
		return "output here", 0, nil
	})

	result, err := c.Exec(context.Background(), "sandbox-abc", []string{"python3", "run_task.py"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code: got %d, want 0", result.ExitCode)
	}
	if result.Output != "output here" {
		t.Errorf("output: got %q", result.Output)
	}
	// Verify positional syntax: openshell sandbox exec <NAME> --no-tty -- python3 run_task.py
	assertContains(t, gotArgs, "sandbox")
	assertContains(t, gotArgs, "exec")
	assertContains(t, gotArgs, "sandbox-abc")
	assertContains(t, gotArgs, "--")
	assertContains(t, gotArgs, "python3")
}

func TestClient_Exec_NonZeroExit(t *testing.T) {
	c := NewClient(Config{Binary: "openshell"})
	c.SetRunner(func(ctx context.Context, name string, args ...string) (string, int, error) {
		return "error output", 1, errors.New("command failed")
	})

	result, err := c.Exec(context.Background(), "sb-1", []string{"false"})
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
	if result.ExitCode != 1 {
		t.Errorf("exit code: got %d, want 1", result.ExitCode)
	}
}

func TestClient_DefaultRunner_PreservesExitError(t *testing.T) {
	c := NewClient(Config{Binary: "false"}) // "false" always exits 1
	_, _, err := c.run(context.Background(), /* no extra args */)
	if err == nil {
		t.Fatal("expected error from 'false' command")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Errorf("error should wrap exec.ExitError, got %T: %v", err, err)
	}
}

func TestClient_DeleteSandbox(t *testing.T) {
	var gotArgs []string
	c := NewClient(Config{Binary: "openshell"})
	c.SetRunner(func(ctx context.Context, name string, args ...string) (string, int, error) {
		gotArgs = append([]string{name}, args...)
		return "", 0, nil
	})

	err := c.DeleteSandbox(context.Background(), "sb-to-delete")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertContains(t, gotArgs, "sandbox")
	assertContains(t, gotArgs, "delete")
	assertContains(t, gotArgs, "sb-to-delete")
}

func TestClient_ListSandboxes(t *testing.T) {
	c := NewClient(Config{Binary: "openshell"})
	c.SetRunner(func(ctx context.Context, name string, args ...string) (string, int, error) {
		return "NAME          STATUS\nsb-1          running\nsb-2          stopped", 0, nil
	})

	infos, err := c.ListSandboxes(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("expected 2 sandboxes, got %d", len(infos))
	}
	if infos[0].Name != "sb-1" || infos[0].Status != "running" {
		t.Errorf("sandbox 0: got %+v", infos[0])
	}
	if infos[1].Name != "sb-2" || infos[1].Status != "stopped" {
		t.Errorf("sandbox 1: got %+v", infos[1])
	}
}

func TestClient_Status(t *testing.T) {
	c := NewClient(Config{Binary: "openshell"})
	c.SetRunner(func(ctx context.Context, name string, args ...string) (string, int, error) {
		assertContains(t, append([]string{name}, args...), "status")
		return "gateway ok", 0, nil
	})

	if err := c.Status(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseSandboxName_CreatedPrefix(t *testing.T) {
	got := parseSandboxName("Created sandbox: solid-takin\n\n  [0.0s] Requesting compute...")
	if got != "solid-takin" {
		t.Errorf("got %q, want 'solid-takin'", got)
	}
}

func TestParseSandboxName_WithAnsi(t *testing.T) {
	got := parseSandboxName("\x1b[1m\x1b[36mCreated sandbox:\x1b[39m\x1b[0m \x1b[1mtest-box\x1b[0m\n")
	if got != "test-box" {
		t.Errorf("got %q, want 'test-box'", got)
	}
}

func TestParseSandboxName_Fallback(t *testing.T) {
	got := parseSandboxName("some output\nsandbox-xyz")
	if got != "sandbox-xyz" {
		t.Errorf("got %q, want 'sandbox-xyz'", got)
	}
}

func TestParseSandboxName_Empty(t *testing.T) {
	got := parseSandboxName("")
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestParseSandboxList_SkipsHeader(t *testing.T) {
	input := "NAME          STATUS\nsb-1          running"
	infos := parseSandboxList(input)
	if len(infos) != 1 {
		t.Fatalf("expected 1, got %d", len(infos))
	}
	if infos[0].Name != "sb-1" {
		t.Errorf("got %q", infos[0].Name)
	}
}

func TestParseSandboxList_NoSandboxes(t *testing.T) {
	infos := parseSandboxList("No sandboxes found")
	if len(infos) != 0 {
		t.Errorf("expected 0, got %d", len(infos))
	}
}

func assertContains(t *testing.T, slice []string, want string) {
	t.Helper()
	for _, s := range slice {
		if s == want {
			return
		}
	}
	t.Errorf("slice %v does not contain %q", slice, want)
}

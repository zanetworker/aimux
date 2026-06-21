package runtime

import (
	"context"
	"fmt"
	"testing"
)

// Compile-time interface checks: OpenShellRuntime must satisfy both
// Runtime and PolicyEnforcer.
var _ Runtime = (*OpenShellRuntime)(nil)
var _ PolicyEnforcer = (*OpenShellRuntime)(nil)

func TestOpenShell_Type(t *testing.T) {
	o := NewOpenShellRuntime("sandbox-1", "")
	if got := o.Type(); got != "openshell" {
		t.Errorf("Type() = %q, want %q", got, "openshell")
	}
}

func TestOpenShell_Name(t *testing.T) {
	o := NewOpenShellRuntime("sandbox-1", "")
	if got := o.Name(); got != "sandbox-1" {
		t.Errorf("Name() = %q, want %q", got, "sandbox-1")
	}
}

func TestOpenShell_DefaultBinary(t *testing.T) {
	o := NewOpenShellRuntime("test", "")
	prefix := o.ExecPrefix()
	if prefix[0] != "openshell" {
		t.Errorf("default binary = %q, want %q", prefix[0], "openshell")
	}
}

func TestOpenShell_CustomBinary(t *testing.T) {
	o := NewOpenShellRuntime("test", "/usr/local/bin/openshell")
	prefix := o.ExecPrefix()
	if prefix[0] != "/usr/local/bin/openshell" {
		t.Errorf("binary = %q, want %q", prefix[0], "/usr/local/bin/openshell")
	}
}

func TestOpenShell_ExecPrefix_ContainsOpenshell(t *testing.T) {
	o := NewOpenShellRuntime("agent-sandbox", "openshell")
	prefix := o.ExecPrefix()

	// Should be: openshell sandbox exec -n <name> --tty --
	want := []string{"openshell", "sandbox", "exec", "-n", "agent-sandbox", "--tty", "--"}
	if len(prefix) != len(want) {
		t.Fatalf("ExecPrefix() len = %d, want %d", len(prefix), len(want))
	}
	for i, v := range want {
		if prefix[i] != v {
			t.Errorf("ExecPrefix()[%d] = %q, want %q", i, prefix[i], v)
		}
	}
}

func TestOpenShell_Create_SetsNameFromClient(t *testing.T) {
	o := NewOpenShellRuntime("", "openshell")
	o.Client().SetRunner(func(ctx context.Context, name string, args ...string) (string, int, error) {
		for _, a := range args {
			if a == "create" {
				return "Created sandbox: test-sandbox-1\n", 0, nil
			}
			if a == "list" {
				return "NAME              STATUS\ntest-sandbox-1    Ready", 0, nil
			}
		}
		return "", 0, nil
	})

	err := o.Create(CreateOpts{Image: "worker:latest"})
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if o.Name() != "test-sandbox-1" {
		t.Errorf("Name() = %q, want 'test-sandbox-1'", o.Name())
	}
}

func TestOpenShell_Create_WithExplicitName(t *testing.T) {
	o := NewOpenShellRuntime("my-sandbox", "openshell")
	o.Client().SetRunner(func(ctx context.Context, name string, args ...string) (string, int, error) {
		for _, a := range args {
			if a == "create" {
				return "Created sandbox: my-sandbox\n", 0, nil
			}
			if a == "list" {
				return "NAME          STATUS\nmy-sandbox    Ready", 0, nil
			}
		}
		return "", 0, nil
	})

	err := o.Create(CreateOpts{})
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if o.Name() != "my-sandbox" {
		t.Errorf("Name() = %q", o.Name())
	}
}

func TestOpenShell_Create_Error(t *testing.T) {
	o := NewOpenShellRuntime("fail-sandbox", "openshell")
	o.Client().SetRunner(func(ctx context.Context, name string, args ...string) (string, int, error) {
		return "", 1, fmt.Errorf("gateway down")
	})

	err := o.Create(CreateOpts{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestOpenShell_Delete(t *testing.T) {
	deleteCalled := false
	o := NewOpenShellRuntime("del-sandbox", "openshell")
	o.Client().SetRunner(func(ctx context.Context, name string, args ...string) (string, int, error) {
		for _, a := range args {
			if a == "delete" {
				deleteCalled = true
			}
		}
		return "", 0, nil
	})

	err := o.Delete()
	if err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
	if !deleteCalled {
		t.Error("expected delete CLI call")
	}
}

func TestOpenShell_Status_AfterCreate(t *testing.T) {
	o := NewOpenShellRuntime("", "openshell")
	o.Client().SetRunner(func(ctx context.Context, name string, args ...string) (string, int, error) {
		for _, a := range args {
			if a == "create" {
				return "Created sandbox: status-test\n", 0, nil
			}
			if a == "list" {
				return "NAME          STATUS\nstatus-test   Ready", 0, nil
			}
		}
		return "", 0, nil
	})

	status := o.Status()
	if status.State != StateStopped {
		t.Errorf("before Create: state = %v, want StateStopped", status.State)
	}

	_ = o.Create(CreateOpts{})

	status = o.Status()
	if status.State != StateRunning {
		t.Errorf("after Create: state = %v, want StateRunning", status.State)
	}
}

func TestOpenShell_ConnectCommand(t *testing.T) {
	o := NewOpenShellRuntime("my-box", "openshell")
	cmd := o.ConnectCommand()
	if len(cmd) < 3 {
		t.Fatalf("ConnectCommand() too short: %v", cmd)
	}
	if cmd[0] != "openshell" {
		t.Errorf("cmd[0] = %q, want 'openshell'", cmd[0])
	}
	found := false
	for _, a := range cmd {
		if a == "my-box" {
			found = true
		}
	}
	if !found {
		t.Errorf("sandbox name 'my-box' not found in %v", cmd)
	}
}

func TestOpenShell_PolicyEnforcer(t *testing.T) {
	o := NewOpenShellRuntime("test", "")

	cfg := SandboxConfig{
		Type:    "openshell",
		Network: NetworkPolicy{DenyAll: true},
	}

	if err := o.ApplyPolicy(cfg); err != nil {
		t.Errorf("ApplyPolicy() error: %v", err)
	}
	if got := o.CurrentPolicy(); got.Type != "openshell" {
		t.Errorf("CurrentPolicy().Type = %q, want %q", got.Type, "openshell")
	}

	cfg2 := SandboxConfig{Type: "updated"}
	if err := o.UpdatePolicy(cfg2); err != nil {
		t.Errorf("UpdatePolicy() error: %v", err)
	}
	if got := o.CurrentPolicy(); got.Type != "updated" {
		t.Errorf("CurrentPolicy().Type after update = %q, want %q", got.Type, "updated")
	}
}

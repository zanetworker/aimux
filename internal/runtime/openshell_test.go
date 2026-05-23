package runtime

import "testing"

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

func TestOpenShell_LifecycleMethodsReturnError(t *testing.T) {
	o := NewOpenShellRuntime("test", "")

	methods := []struct {
		name string
		fn   func() error
	}{
		{"Create", func() error { return o.Create(CreateOpts{}) }},
		{"Start", o.Start},
		{"Stop", o.Stop},
		{"Delete", o.Delete},
		{"Attach", o.Attach},
	}

	for _, m := range methods {
		err := m.fn()
		if err == nil {
			t.Errorf("%s() = nil, want error", m.name)
		}
		if err.Error() != "openshell runtime not yet implemented" {
			t.Errorf("%s() error = %q, want %q", m.name, err.Error(), "openshell runtime not yet implemented")
		}
	}
}

func TestOpenShell_Status_Stopped(t *testing.T) {
	o := NewOpenShellRuntime("test", "")
	status := o.Status()
	if status.State != StateStopped {
		t.Errorf("Status().State = %v, want StateStopped", status.State)
	}
}

func TestOpenShell_PolicyEnforcer_Stub(t *testing.T) {
	o := NewOpenShellRuntime("test", "")

	cfg := SandboxConfig{
		Type: "openshell",
		Network: NetworkPolicy{DenyAll: true},
	}

	// ApplyPolicy stores the config but returns an error
	if err := o.ApplyPolicy(cfg); err == nil {
		t.Error("ApplyPolicy() = nil, want error")
	}
	if got := o.CurrentPolicy(); got.Type != "openshell" {
		t.Errorf("CurrentPolicy().Type = %q, want %q", got.Type, "openshell")
	}

	// UpdatePolicy likewise stores and errors
	cfg2 := SandboxConfig{Type: "updated"}
	if err := o.UpdatePolicy(cfg2); err == nil {
		t.Error("UpdatePolicy() = nil, want error")
	}
	if got := o.CurrentPolicy(); got.Type != "updated" {
		t.Errorf("CurrentPolicy().Type after update = %q, want %q", got.Type, "updated")
	}
}

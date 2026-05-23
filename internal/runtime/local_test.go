package runtime

import "testing"

// Compile-time interface check.
var _ Runtime = (*Local)(nil)

func TestLocal_Type(t *testing.T) {
	l := NewLocal("agent-1")
	if got := l.Type(); got != "local" {
		t.Errorf("Type() = %q, want %q", got, "local")
	}
}

func TestLocal_Name(t *testing.T) {
	l := NewLocal("my-agent")
	if got := l.Name(); got != "my-agent" {
		t.Errorf("Name() = %q, want %q", got, "my-agent")
	}
}

func TestLocal_ExecPrefix_ReturnsNil(t *testing.T) {
	l := NewLocal("test")
	if prefix := l.ExecPrefix(); prefix != nil {
		t.Errorf("ExecPrefix() = %v, want nil", prefix)
	}
}

func TestLocal_Status_AlwaysRunning(t *testing.T) {
	l := NewLocal("test")
	status := l.Status()
	if status.State != StateRunning {
		t.Errorf("Status().State = %v, want StateRunning", status.State)
	}
}

func TestLocal_LifecycleNoOps(t *testing.T) {
	l := NewLocal("test")
	if err := l.Create(CreateOpts{}); err != nil {
		t.Errorf("Create() = %v, want nil", err)
	}
	if err := l.Start(); err != nil {
		t.Errorf("Start() = %v, want nil", err)
	}
	if err := l.Stop(); err != nil {
		t.Errorf("Stop() = %v, want nil", err)
	}
	if err := l.Delete(); err != nil {
		t.Errorf("Delete() = %v, want nil", err)
	}
	if err := l.Attach(); err != nil {
		t.Errorf("Attach() = %v, want nil", err)
	}
}

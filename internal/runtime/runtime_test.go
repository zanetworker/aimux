package runtime

import "testing"

// mockRuntime verifies that the Runtime interface can be satisfied by a
// concrete type. It is intentionally minimal.
type mockRuntime struct{ name string }

// Compile-time interface check.
var _ Runtime = (*mockRuntime)(nil)

func (m *mockRuntime) Type() string            { return "mock" }
func (m *mockRuntime) Name() string            { return m.name }
func (m *mockRuntime) Create(_ CreateOpts) error { return nil }
func (m *mockRuntime) Start() error            { return nil }
func (m *mockRuntime) Stop() error             { return nil }
func (m *mockRuntime) Delete() error           { return nil }
func (m *mockRuntime) Status() RuntimeStatus   { return RuntimeStatus{State: StateRunning} }
func (m *mockRuntime) ExecPrefix() []string    { return nil }
func (m *mockRuntime) Attach() error           { return nil }

func TestStateString(t *testing.T) {
	tests := []struct {
		state State
		want  string
	}{
		{StateStopped, "stopped"},
		{StateCreating, "creating"},
		{StateRunning, "running"},
		{StateError, "error"},
		{State(99), "unknown(99)"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("State(%d).String() = %q, want %q", int(tt.state), got, tt.want)
		}
	}
}

func TestMockRuntime_SatisfiesInterface(t *testing.T) {
	m := &mockRuntime{name: "test"}
	if m.Type() != "mock" {
		t.Errorf("Type() = %q, want %q", m.Type(), "mock")
	}
	if m.Name() != "test" {
		t.Errorf("Name() = %q, want %q", m.Name(), "test")
	}
}

package runtime

import "testing"

var _ Backend = (*mockBackend)(nil)

type mockBackend struct {
	name     string
	remote   bool
	created  map[string]BackendCreateOpts
}

func newMockBackend(name string, remote bool) *mockBackend {
	return &mockBackend{name: name, remote: remote, created: make(map[string]BackendCreateOpts)}
}

func (m *mockBackend) Name() string    { return m.name }
func (m *mockBackend) IsRemote() bool  { return m.remote }
func (m *mockBackend) Create(name string, opts BackendCreateOpts) error {
	m.created[name] = opts
	return nil
}
func (m *mockBackend) Start(_ string) error              { return nil }
func (m *mockBackend) Stop(_ string) error               { return nil }
func (m *mockBackend) Delete(_ string) error             { return nil }
func (m *mockBackend) Status(_ string) (State, error)    { return StateRunning, nil }
func (m *mockBackend) ExecPrefix(name string) []string   { return []string{"mock", "exec", name} }

func TestMockBackend_Name(t *testing.T) {
	b := newMockBackend("test-engine", false)
	if b.Name() != "test-engine" {
		t.Errorf("expected 'test-engine', got %q", b.Name())
	}
}

func TestMockBackend_IsRemote(t *testing.T) {
	local := newMockBackend("local", false)
	remote := newMockBackend("remote", true)
	if local.IsRemote() {
		t.Error("local backend should not be remote")
	}
	if !remote.IsRemote() {
		t.Error("remote backend should be remote")
	}
}

func TestMockBackend_Create(t *testing.T) {
	b := newMockBackend("mock", false)
	err := b.Create("test-container", BackendCreateOpts{Image: "fedora:41", WorkDir: "/workspace"})
	if err != nil {
		t.Fatal(err)
	}
	opts, ok := b.created["test-container"]
	if !ok {
		t.Fatal("container not created")
	}
	if opts.Image != "fedora:41" {
		t.Errorf("expected image 'fedora:41', got %q", opts.Image)
	}
}

func TestMockBackend_ExecPrefix(t *testing.T) {
	b := newMockBackend("mock", false)
	prefix := b.ExecPrefix("my-ctr")
	if len(prefix) != 3 || prefix[2] != "my-ctr" {
		t.Errorf("unexpected prefix: %v", prefix)
	}
}

func TestMockBackend_Status(t *testing.T) {
	b := newMockBackend("mock", false)
	state, err := b.Status("any")
	if err != nil {
		t.Fatal(err)
	}
	if state != StateRunning {
		t.Errorf("expected StateRunning, got %v", state)
	}
}

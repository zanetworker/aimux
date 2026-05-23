package runtime

import "testing"

var _ Runtime = (*Container)(nil)

func TestContainer_Type(t *testing.T) {
	c := NewContainer("web", NewPodmanBackend("docker"))
	if got := c.Type(); got != "container" {
		t.Errorf("Type() = %q, want %q", got, "container")
	}
}

func TestContainer_Name(t *testing.T) {
	c := NewContainer("web", NewPodmanBackend("docker"))
	if got := c.Name(); got != "web" {
		t.Errorf("Name() = %q, want %q", got, "web")
	}
}

func TestContainer_ExecPrefix(t *testing.T) {
	c := NewContainer("agent-1", NewPodmanBackend("podman"))
	prefix := c.ExecPrefix()
	if len(prefix) != 4 {
		t.Fatalf("ExecPrefix() len = %d, want 4", len(prefix))
	}
	want := []string{"podman", "exec", "-it", "agent-1"}
	for i, v := range want {
		if prefix[i] != v {
			t.Errorf("ExecPrefix()[%d] = %q, want %q", i, prefix[i], v)
		}
	}
}

func TestContainer_DefaultEngine(t *testing.T) {
	c := NewContainer("test", NewPodmanBackend(""))
	if c.Backend().Name() != "podman" {
		t.Errorf("default backend = %q, want %q", c.Backend().Name(), "podman")
	}
}

func TestContainer_DockerEngine(t *testing.T) {
	c := NewContainer("test", NewPodmanBackend("docker"))
	prefix := c.ExecPrefix()
	if prefix[0] != "docker" {
		t.Errorf("ExecPrefix()[0] = %q, want %q", prefix[0], "docker")
	}
}

func TestContainer_Create_RequiresImage(t *testing.T) {
	c := NewContainer("test", NewPodmanBackend("podman"))
	err := c.Create(CreateOpts{})
	if err == nil {
		t.Error("Create() with empty image should return error")
	}
}

func TestContainer_Backend(t *testing.T) {
	b := NewPodmanBackend("podman")
	c := NewContainer("test", b)
	if c.Backend() != b {
		t.Error("Backend() should return the backend passed to constructor")
	}
}

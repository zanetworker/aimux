package runtime

import "testing"

// Compile-time interface check.
var _ Runtime = (*Container)(nil)

func TestContainer_Type(t *testing.T) {
	c := NewContainer("web", "docker")
	if got := c.Type(); got != "container" {
		t.Errorf("Type() = %q, want %q", got, "container")
	}
}

func TestContainer_Name(t *testing.T) {
	c := NewContainer("web", "docker")
	if got := c.Name(); got != "web" {
		t.Errorf("Name() = %q, want %q", got, "web")
	}
}

func TestContainer_ExecPrefix(t *testing.T) {
	c := NewContainer("agent-1", "podman")
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
	c := NewContainer("test", "")
	if c.Engine() != "podman" {
		t.Errorf("default engine = %q, want %q", c.Engine(), "podman")
	}
}

func TestContainer_DockerEngine(t *testing.T) {
	c := NewContainer("test", "docker")
	prefix := c.ExecPrefix()
	if prefix[0] != "docker" {
		t.Errorf("ExecPrefix()[0] = %q, want %q", prefix[0], "docker")
	}
}

func TestContainer_Create_RequiresImage(t *testing.T) {
	c := NewContainer("test", "podman")
	err := c.Create(CreateOpts{})
	if err == nil {
		t.Error("Create() with empty image should return error")
	}
}

func TestContainer_Create_WithImage(t *testing.T) {
	c := NewContainer("test", "podman")
	err := c.Create(CreateOpts{Image: "ubuntu:22.04"})
	if err != nil {
		t.Errorf("Create() with image = %v, want nil", err)
	}
}

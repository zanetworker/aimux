package runtime

import "testing"

var _ Backend = (*PodmanBackend)(nil)

func TestPodmanBackend_Name(t *testing.T) {
	p := NewPodmanBackend("podman")
	if p.Name() != "podman" {
		t.Errorf("expected 'podman', got %q", p.Name())
	}
}

func TestPodmanBackend_Docker(t *testing.T) {
	p := NewPodmanBackend("docker")
	if p.Name() != "docker" {
		t.Errorf("expected 'docker', got %q", p.Name())
	}
}

func TestPodmanBackend_DefaultEngine(t *testing.T) {
	p := NewPodmanBackend("")
	if p.Name() != "podman" {
		t.Errorf("expected default 'podman', got %q", p.Name())
	}
}

func TestPodmanBackend_IsRemote(t *testing.T) {
	p := NewPodmanBackend("podman")
	if p.IsRemote() {
		t.Error("podman should not be remote")
	}
}

func TestPodmanBackend_ExecPrefix(t *testing.T) {
	p := NewPodmanBackend("podman")
	prefix := p.ExecPrefix("my-ctr")
	if len(prefix) != 4 {
		t.Fatalf("expected 4 elements, got %d: %v", len(prefix), prefix)
	}
	if prefix[0] != "podman" || prefix[1] != "exec" || prefix[2] != "-it" || prefix[3] != "my-ctr" {
		t.Errorf("unexpected prefix: %v", prefix)
	}
}

func TestPodmanBackend_CreateRequiresImage(t *testing.T) {
	p := NewPodmanBackend("podman")
	err := p.Create("test", BackendCreateOpts{})
	if err == nil {
		t.Error("Create without image should fail")
	}
}

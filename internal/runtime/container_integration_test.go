//go:build integration

package runtime

import (
	"fmt"
	"math/rand"
	"os/exec"
	"testing"
)

func TestIntegration_PodmanRoundtrip(t *testing.T) {
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman not found, skipping container integration test")
	}

	backend := NewPodmanBackend("podman")
	name := fmt.Sprintf("aimux-test-%d", rand.Intn(100000))

	// Create container (runs detached with "sleep infinity")
	err := backend.Create(name, BackendCreateOpts{Image: "alpine:3.19"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = backend.Delete(name) }()

	// Verify running after create
	state, err := backend.Status(name)
	if err != nil {
		t.Fatalf("Status after create: %v", err)
	}
	if state != StateRunning {
		t.Errorf("expected StateRunning after create, got %v", state)
	}

	// Stop the container
	if err := backend.Stop(name); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Verify stopped
	state, err = backend.Status(name)
	if err != nil {
		t.Fatalf("Status after stop: %v", err)
	}
	if state != StateStopped {
		t.Errorf("expected StateStopped after stop, got %v", state)
	}

	// Start it again
	if err := backend.Start(name); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Verify running again
	state, err = backend.Status(name)
	if err != nil {
		t.Fatalf("Status after restart: %v", err)
	}
	if state != StateRunning {
		t.Errorf("expected StateRunning after restart, got %v", state)
	}

	// Delete happens via defer
}

func TestIntegration_PodmanStatusUnknownContainer(t *testing.T) {
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman not found, skipping container integration test")
	}

	backend := NewPodmanBackend("podman")

	// Querying a container that doesn't exist should return StateStopped, nil
	state, err := backend.Status("aimux-nonexistent-container-xyz")
	if err != nil {
		t.Fatalf("Status of nonexistent container returned error: %v", err)
	}
	if state != StateStopped {
		t.Errorf("expected StateStopped for nonexistent container, got %v", state)
	}
}

func TestIntegration_ContainerRuntimeRoundtrip(t *testing.T) {
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman not found, skipping container integration test")
	}

	backend := NewPodmanBackend("podman")
	name := fmt.Sprintf("aimux-ctr-test-%d", rand.Intn(100000))
	ctr := NewContainer(name, backend)

	// Verify type and name
	if ctr.Type() != "container" {
		t.Errorf("expected type 'container', got %q", ctr.Type())
	}
	if ctr.Name() != name {
		t.Errorf("expected name %q, got %q", name, ctr.Name())
	}

	// Create via the Container runtime wrapper
	err := ctr.Create(CreateOpts{Image: "alpine:3.19"})
	if err != nil {
		t.Fatalf("Container.Create: %v", err)
	}
	defer func() { _ = ctr.Delete() }()

	// Verify running via RuntimeStatus
	status := ctr.Status()
	if status.State != StateRunning {
		t.Errorf("expected StateRunning, got %v", status.State)
	}

	// ExecPrefix should include the container name
	prefix := ctr.ExecPrefix()
	if len(prefix) < 3 {
		t.Fatalf("expected ExecPrefix with at least 3 elements, got %v", prefix)
	}

	// Stop via runtime wrapper
	if err := ctr.Stop(); err != nil {
		t.Fatalf("Container.Stop: %v", err)
	}

	status = ctr.Status()
	if status.State != StateStopped {
		t.Errorf("expected StateStopped after stop, got %v", status.State)
	}

	// Delete happens via defer
}

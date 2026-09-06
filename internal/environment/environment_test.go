package environment_test

import (
	"testing"

	"github.com/zanetworker/aimux/internal/environment"
	"github.com/zanetworker/aimux/internal/provider"
)

// TestEnvironmentInterfaceExists verifies that the Environment interface
// and supporting types are properly defined.
func TestEnvironmentInterfaceExists(t *testing.T) {
	// This test verifies that the package compiles and the types exist.
	// Implementations will be tested separately in their own packages.

	// Verify SandboxOpts can be instantiated
	opts := environment.SandboxOpts{
		Image:    "test-image",
		Provider: "claude",
		Mode:     "session",
		Env:      map[string]string{"KEY": "value"},
		Labels:   map[string]string{"app": "test"},
	}

	if opts.Image != "test-image" {
		t.Error("SandboxOpts.Image not set correctly")
	}
	if opts.Provider != "claude" {
		t.Error("SandboxOpts.Provider not set correctly")
	}
	if opts.Mode != "session" {
		t.Error("SandboxOpts.Mode not set correctly")
	}
	if len(opts.Env) != 1 || opts.Env["KEY"] != "value" {
		t.Error("SandboxOpts.Env not set correctly")
	}
	if len(opts.Labels) != 1 || opts.Labels["app"] != "test" {
		t.Error("SandboxOpts.Labels not set correctly")
	}

	// Verify SandboxStatus can be instantiated
	status := environment.SandboxStatus{
		Name:   "test-sandbox",
		Status: "running",
		Idle:   false,
	}

	if status.Name != "test-sandbox" {
		t.Error("SandboxStatus.Name not set correctly")
	}
	if status.Status != "running" {
		t.Error("SandboxStatus.Status not set correctly")
	}
	if status.Idle {
		t.Error("SandboxStatus.Idle should be false")
	}

	// Verify HealthStatus can be instantiated (now in provider package)
	health := provider.HealthStatus{
		Configured: true,
		CoordOK:    true,
		CoordErr:   "",
		ComputeOK:  true,
		ComputeErr: "",
		Workloads:  []string{"workload1", "workload2"},
	}

	if !health.Configured {
		t.Error("HealthStatus.Configured not set correctly")
	}
	if !health.CoordOK {
		t.Error("HealthStatus.CoordOK not set correctly")
	}
	if health.CoordErr != "" {
		t.Error("HealthStatus.CoordErr should be empty")
	}
	if !health.ComputeOK {
		t.Error("HealthStatus.ComputeOK not set correctly")
	}
	if health.ComputeErr != "" {
		t.Error("HealthStatus.ComputeErr should be empty")
	}
	if len(health.Workloads) != 2 {
		t.Error("HealthStatus.Workloads not set correctly")
	}
}

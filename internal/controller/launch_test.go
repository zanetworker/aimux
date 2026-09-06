package controller_test

import (
	"os/exec"
	"testing"

	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/controller"
)

func TestResolveLaunchRuntime_Local(t *testing.T) {
	env := config.EnvironmentConfig{Type: "local"}
	got := controller.ResolveLaunchRuntime(env)
	if got != "local" {
		t.Errorf("expected 'local', got %q", got)
	}
}

func TestResolveLaunchRuntime_OpenShell(t *testing.T) {
	env := config.EnvironmentConfig{Type: "openshell", Gateway: "https://gw.example.com"}
	got := controller.ResolveLaunchRuntime(env)
	if got != "remote" {
		t.Errorf("expected 'remote', got %q", got)
	}
}

func TestResolveLaunchRuntime_K8s(t *testing.T) {
	env := config.EnvironmentConfig{Type: "k8s", RedisURL: "redis://localhost:6379"}
	got := controller.ResolveLaunchRuntime(env)
	if got != "remote" {
		t.Errorf("expected 'remote', got %q", got)
	}
}

func TestResolveLaunchRuntime_EmptyType(t *testing.T) {
	env := config.EnvironmentConfig{}
	got := controller.ResolveLaunchRuntime(env)
	if got != "local" {
		t.Errorf("expected 'local' for empty type, got %q", got)
	}
}

func TestEnvironmentNames(t *testing.T) {
	envs := map[string]config.EnvironmentConfig{
		"local":    {Type: "local"},
		"sandbox":  {Type: "openshell", Gateway: "https://gw.example.com"},
		"cluster":  {Type: "k8s", RedisURL: "redis://localhost"},
	}
	names := controller.EnvironmentNames(envs)

	if len(names) != 3 {
		t.Fatalf("expected 3 names, got %d: %v", len(names), names)
	}
	if names[0] != "local" {
		t.Errorf("expected 'local' first, got %q", names[0])
	}
}

func TestEnvironmentNames_OnlyLocal(t *testing.T) {
	envs := map[string]config.EnvironmentConfig{
		"local": {Type: "local"},
	}
	names := controller.EnvironmentNames(envs)
	if len(names) != 1 || names[0] != "local" {
		t.Errorf("expected ['local'], got %v", names)
	}
}

func TestEnvironmentNames_Empty(t *testing.T) {
	names := controller.EnvironmentNames(nil)
	if len(names) != 0 {
		t.Errorf("expected empty, got %v", names)
	}
}

// --- Launch spec tests ---

type fakeHarness struct {
	name string
	cmd  *exec.Cmd
	otel string
}

func (h *fakeHarness) Name() string                              { return h.name }
func (h *fakeHarness) SpawnCommand(dir, model, mode string) *exec.Cmd { return h.cmd }
func (h *fakeHarness) OTELEnv(endpoint string) string           { return h.otel }

func TestBuildLaunchSpec_Basic(t *testing.T) {
	harness := &fakeHarness{
		name: "claude",
		cmd:  exec.Command("claude", "--model", "opus"),
		otel: "OTEL_ENABLED=1",
	}

	spec := controller.BuildLaunchSpec(harness, controller.LaunchRequest{
		Dir:   "/tmp/project",
		Model: "opus",
		Mode:  "default",
		Shell: "/bin/zsh",
	})

	if spec.Provider != "claude" {
		t.Errorf("Provider = %q, want claude", spec.Provider)
	}
	if spec.Dir != "/tmp/project" {
		t.Errorf("Dir = %q, want /tmp/project", spec.Dir)
	}
	if spec.Cmd == nil {
		t.Fatal("Cmd should not be nil")
	}
	if spec.Shell != "/bin/zsh" {
		t.Errorf("Shell = %q, want /bin/zsh", spec.Shell)
	}
}

func TestBuildLaunchSpec_WithOTEL(t *testing.T) {
	harness := &fakeHarness{
		name: "claude",
		cmd:  exec.Command("claude"),
		otel: "CLAUDE_OTEL=1",
	}

	spec := controller.BuildLaunchSpec(harness, controller.LaunchRequest{
		Dir:          "/tmp",
		OTELEnabled:  true,
		OTELEndpoint: "http://localhost:4318",
	})

	if spec.EnvPrefix != "CLAUDE_OTEL=1" {
		t.Errorf("EnvPrefix = %q, want CLAUDE_OTEL=1", spec.EnvPrefix)
	}
}

func TestBuildLaunchSpec_NilHarness(t *testing.T) {
	spec := controller.BuildLaunchSpec(nil, controller.LaunchRequest{Dir: "/tmp"})
	if spec.Cmd != nil {
		t.Error("expected nil Cmd for nil harness")
	}
}

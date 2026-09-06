package internal_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/coordination"
	"github.com/zanetworker/aimux/internal/environment"
	"github.com/zanetworker/aimux/internal/session"
)

func TestIntegration_EnvironmentDiscovery(t *testing.T) {
	env := environment.NewLocalEnvironment()

	if env.Name() != "local" {
		t.Errorf("Name() = %q, want local", env.Name())
	}
	if env.Type() != "local" {
		t.Errorf("Type() = %q, want local", env.Type())
	}

	agents, err := env.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	// Should find at least the test process's parent (go test → shell)
	// or zero if no Claude/Codex processes are running. Either is valid.
	t.Logf("LocalEnvironment.Discover() found %d agents", len(agents))
}

func TestIntegration_CoordinatorLocalLifecycle(t *testing.T) {
	coord := coordination.NewLocalCoordinator()
	ctx := context.Background()

	// Register → Create task → List → Get result
	err := coord.RegisterAgent(ctx, coordination.AgentInfo{
		ID:       "test-agent-1",
		Provider: "claude",
		Role:     "worker",
	})
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	taskID, err := coord.CreateTask(ctx, coordination.TaskSpec{
		Prompt: "test prompt",
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if taskID == "" {
		t.Fatal("taskID should not be empty")
	}

	tasks, err := coord.ListTasks(ctx)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("expected 1 task, got %d", len(tasks))
	}

	if err := coord.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestIntegration_SessionManagerPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")

	// Create and persist
	mgr1 := session.NewManager(session.NewFileStore(path))
	s, err := mgr1.CreateSession("claude", "local", "/tmp/test", "opus", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if s.ID == "" {
		t.Fatal("session ID should not be empty")
	}

	// Reload from disk
	mgr2 := session.NewManager(session.NewFileStore(path))
	got, err := mgr2.Store().Get(s.ID)
	if err != nil {
		t.Fatalf("Get after reload: %v", err)
	}
	if got.Provider != "claude" {
		t.Errorf("Provider = %q, want claude", got.Provider)
	}
	if got.Environment != "local" {
		t.Errorf("Environment = %q, want local", got.Environment)
	}
}

func TestIntegration_EnvironmentBasedLaunchRouting(t *testing.T) {
	tests := []struct {
		name    string
		envCfg  config.EnvironmentConfig
		want    string
	}{
		{"local env", config.EnvironmentConfig{Type: "local"}, "local"},
		{"openshell env", config.EnvironmentConfig{Type: "openshell"}, "remote"},
		{"k8s env", config.EnvironmentConfig{Type: "k8s"}, "remote"},
		{"empty type defaults local", config.EnvironmentConfig{}, "local"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := controller.ResolveLaunchRuntime(tt.envCfg)
			if got != tt.want {
				t.Errorf("ResolveLaunchRuntime(%+v) = %q, want %q", tt.envCfg, got, tt.want)
			}
		})
	}
}

func TestIntegration_AgentsYAMLLoading(t *testing.T) {
	dir := t.TempDir()
	content := `
- name: my-reviewer
  runtime: claude
  model: opus
  prompt: "Review this code"
`
	agentsPath := filepath.Join(dir, "agents.yaml")
	if err := writeTestFile(t, agentsPath, content); err != nil {
		t.Fatal(err)
	}

	agents, err := config.LoadAgents(agentsPath)
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].Name != "my-reviewer" {
		t.Errorf("Name = %q, want my-reviewer", agents[0].Name)
	}
	if agents[0].Runtime != "claude" {
		t.Errorf("Runtime = %q, want claude", agents[0].Runtime)
	}

	// Verify config.Load picks up agents.yaml from same directory
	configPath := filepath.Join(dir, "config.yaml")
	if err := writeTestFile(t, configPath, "providers:\n  claude:\n    enabled: true\n"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.AgentConfigs) != 1 {
		t.Errorf("AgentConfigs length = %d, want 1", len(cfg.AgentConfigs))
	}
}

func TestIntegration_NamedEnvironmentsConfig(t *testing.T) {
	dir := t.TempDir()
	content := `
environments:
  local:
    type: local
  sandbox:
    type: openshell
    gateway: "https://gw.example.com"
  cluster:
    type: k8s
    redis_url: "redis://localhost:6379"
`
	configPath := filepath.Join(dir, "config.yaml")
	if err := writeTestFile(t, configPath, content); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(cfg.Environments) != 3 {
		t.Fatalf("expected 3 environments, got %d", len(cfg.Environments))
	}

	names := controller.EnvironmentNames(cfg.Environments)
	if names[0] != "local" {
		t.Errorf("first environment should be 'local', got %q", names[0])
	}

	// Verify routing
	for _, name := range names {
		env := cfg.Environments[name]
		runtime := controller.ResolveLaunchRuntime(env)
		switch name {
		case "local":
			if runtime != "local" {
				t.Errorf("local env should route to 'local', got %q", runtime)
			}
		case "sandbox":
			if runtime != "remote" {
				t.Errorf("sandbox env should route to 'remote', got %q", runtime)
			}
		case "cluster":
			if runtime != "remote" {
				t.Errorf("cluster env should route to 'remote', got %q", runtime)
			}
		}
	}
}

func writeTestFile(t *testing.T, path, content string) error {
	t.Helper()
	return os.WriteFile(path, []byte(content), 0o600)
}

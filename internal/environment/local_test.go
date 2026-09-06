package environment_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/environment"
)

// Compile-time interface check.
var _ environment.Environment = (*environment.LocalEnvironment)(nil)

// Compile-time check that mockSource satisfies DiscoverySource.
var _ environment.DiscoverySource = (*mockSource)(nil)

type mockSource struct {
	name   string
	agents []agent.Agent
	err    error
}

func (m *mockSource) Name() string                      { return m.name }
func (m *mockSource) Discover() ([]agent.Agent, error)  { return m.agents, m.err }

func TestLocalEnvironment_NameAndType(t *testing.T) {
	env := environment.NewLocalEnvironment()

	if got := env.Name(); got != "local" {
		t.Errorf("Name() = %q, want %q", got, "local")
	}
	if got := env.Type(); got != "local" {
		t.Errorf("Type() = %q, want %q", got, "local")
	}
}

func TestLocalEnvironment_Discover_AggregatesSources(t *testing.T) {
	src1 := &mockSource{
		name: "claude",
		agents: []agent.Agent{
			{PID: 100, Name: "project-a", ProviderName: "claude"},
		},
	}
	src2 := &mockSource{
		name: "codex",
		agents: []agent.Agent{
			{PID: 200, Name: "project-b", ProviderName: "codex"},
			{PID: 201, Name: "project-c", ProviderName: "codex"},
		},
	}

	env := environment.NewLocalEnvironment(src1, src2)
	agents, err := env.Discover()

	if err != nil {
		t.Fatalf("Discover() error = %v, want nil", err)
	}
	if len(agents) != 3 {
		t.Fatalf("Discover() returned %d agents, want 3", len(agents))
	}
	if agents[0].PID != 100 {
		t.Errorf("agents[0].PID = %d, want 100", agents[0].PID)
	}
	if agents[1].PID != 200 {
		t.Errorf("agents[1].PID = %d, want 200", agents[1].PID)
	}
	if agents[2].PID != 201 {
		t.Errorf("agents[2].PID = %d, want 201", agents[2].PID)
	}
}

func TestLocalEnvironment_Discover_FailingSourceSkipped(t *testing.T) {
	failing := &mockSource{
		name: "broken",
		err:  fmt.Errorf("connection refused"),
	}
	working := &mockSource{
		name: "claude",
		agents: []agent.Agent{
			{PID: 300, Name: "survivor", ProviderName: "claude"},
		},
	}

	env := environment.NewLocalEnvironment(failing, working)
	agents, err := env.Discover()

	if err != nil {
		t.Fatalf("Discover() error = %v, want nil (failing source should be skipped)", err)
	}
	if len(agents) != 1 {
		t.Fatalf("Discover() returned %d agents, want 1", len(agents))
	}
	if agents[0].Name != "survivor" {
		t.Errorf("agents[0].Name = %q, want %q", agents[0].Name, "survivor")
	}
}

func TestLocalEnvironment_Discover_NoSources(t *testing.T) {
	env := environment.NewLocalEnvironment()
	agents, err := env.Discover()

	if err != nil {
		t.Fatalf("Discover() error = %v, want nil", err)
	}
	if len(agents) != 0 {
		t.Errorf("Discover() returned %d agents, want 0", len(agents))
	}
}

func TestLocalEnvironment_Kill_NoPID(t *testing.T) {
	env := environment.NewLocalEnvironment()
	a := agent.Agent{PID: 0, Name: "no-pid-agent"}

	err := env.Kill(a)
	if err == nil {
		t.Fatal("Kill() with PID=0 should return error")
	}
}

func TestLocalEnvironment_Kill_NegativePID(t *testing.T) {
	env := environment.NewLocalEnvironment()
	a := agent.Agent{PID: -1, Name: "negative-pid"}

	err := env.Kill(a)
	if err == nil {
		t.Fatal("Kill() with PID=-1 should return error")
	}
}


func TestLocalEnvironment_CreateSandbox_NotImplemented(t *testing.T) {
	env := environment.NewLocalEnvironment()
	_, err := env.CreateSandbox(context.Background(), environment.SandboxOpts{})
	if err == nil {
		t.Fatal("CreateSandbox() should return error (not implemented)")
	}
}

func TestLocalEnvironment_DeleteSandbox_NotImplemented(t *testing.T) {
	env := environment.NewLocalEnvironment()
	err := env.DeleteSandbox(context.Background(), "test")
	if err == nil {
		t.Fatal("DeleteSandbox() should return error (not implemented)")
	}
}

func TestLocalEnvironment_ListSandboxes_Empty(t *testing.T) {
	env := environment.NewLocalEnvironment()
	sandboxes, err := env.ListSandboxes(context.Background())
	if err != nil {
		t.Fatalf("ListSandboxes() error = %v, want nil", err)
	}
	if sandboxes != nil {
		t.Errorf("ListSandboxes() = %v, want nil", sandboxes)
	}
}

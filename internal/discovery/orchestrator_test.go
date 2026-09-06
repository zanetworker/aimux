package discovery

import (
	"context"
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/environment"
)

func TestAssignUniqueSuffixes_NoDuplicates(t *testing.T) {
	agents := []agent.Agent{
		{Name: "project-a", WorkingDir: "/src/project-a"},
		{Name: "project-b", WorkingDir: "/src/project-b"},
	}
	assignUniqueSuffixes(agents)

	// No suffixes needed — names are already unique.
	if agents[0].Name != "project-a" {
		t.Errorf("agents[0].Name = %q, want %q", agents[0].Name, "project-a")
	}
	if agents[1].Name != "project-b" {
		t.Errorf("agents[1].Name = %q, want %q", agents[1].Name, "project-b")
	}
}

func TestAssignUniqueSuffixes_Duplicates(t *testing.T) {
	agents := []agent.Agent{
		{Name: "myapp", WorkingDir: "/src/myapp", ProviderName: "claude"},
		{Name: "myapp", WorkingDir: "/src/myapp", ProviderName: "claude"},
		{Name: "myapp", WorkingDir: "/src/myapp", ProviderName: "codex"},
	}
	assignUniqueSuffixes(agents)

	if agents[0].Name != "myapp #1" {
		t.Errorf("agents[0].Name = %q, want %q", agents[0].Name, "myapp #1")
	}
	if agents[1].Name != "myapp #2" {
		t.Errorf("agents[1].Name = %q, want %q", agents[1].Name, "myapp #2")
	}
	if agents[2].Name != "myapp #3" {
		t.Errorf("agents[2].Name = %q, want %q", agents[2].Name, "myapp #3")
	}
}

func TestAssignUniqueSuffixes_MixedDuplicates(t *testing.T) {
	agents := []agent.Agent{
		{Name: "alpha", WorkingDir: "/src/alpha"},
		{Name: "beta", WorkingDir: "/src/beta"},
		{Name: "alpha", WorkingDir: "/src/alpha"},
	}
	assignUniqueSuffixes(agents)

	if agents[0].Name != "alpha #1" {
		t.Errorf("agents[0].Name = %q, want %q", agents[0].Name, "alpha #1")
	}
	if agents[1].Name != "beta" {
		t.Errorf("agents[1].Name = %q, want %q (no suffix needed)", agents[1].Name, "beta")
	}
	if agents[2].Name != "alpha #2" {
		t.Errorf("agents[2].Name = %q, want %q", agents[2].Name, "alpha #2")
	}
}

func TestAssignUniqueSuffixes_Empty(t *testing.T) {
	assignUniqueSuffixes(nil) // should not panic
}

type fakeProvider struct {
	name   string
	agents []agent.Agent
}

func (f *fakeProvider) Name() string                    { return f.name }
func (f *fakeProvider) Discover() ([]agent.Agent, error) { return f.agents, nil }

type fakeEnv struct {
	agents []agent.Agent
	err    error
}

func (e *fakeEnv) Name() string { return "fake" }
func (e *fakeEnv) Type() string { return "fake" }
func (e *fakeEnv) Discover() ([]agent.Agent, error) {
	return e.agents, e.err
}
func (e *fakeEnv) CreateSandbox(_ context.Context, _ environment.SandboxOpts) (string, error) {
	return "", nil
}
func (e *fakeEnv) DeleteSandbox(_ context.Context, _ string) error { return nil }
func (e *fakeEnv) ListSandboxes(_ context.Context) ([]environment.SandboxStatus, error) {
	return nil, nil
}
func (e *fakeEnv) Kill(_ agent.Agent) error { return nil }

func TestOrchestrator_AddEnvironment(t *testing.T) {
	o := NewOrchestrator()
	env := &fakeEnv{
		agents: []agent.Agent{
			{Name: "remote-1", SandboxName: "sb-1", Location: "remote"},
		},
	}
	o.AddEnvironment(env)

	agents, err := o.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].Name != "remote-1" {
		t.Errorf("agent name = %q, want %q", agents[0].Name, "remote-1")
	}
}

func TestOrchestrator_EnvironmentDedup(t *testing.T) {
	prov := &fakeProvider{
		name: "claude",
		agents: []agent.Agent{
			{Name: "local-1", SandboxName: "sb-1", WorkingDir: "/src/local"},
		},
	}
	env := &fakeEnv{
		agents: []agent.Agent{
			{Name: "remote-1", SandboxName: "sb-1", Location: "remote"},
		},
	}

	o := NewOrchestrator(prov)
	o.AddEnvironment(env)

	agents, err := o.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent (dedup by SandboxName), got %d", len(agents))
	}
	if agents[0].Name != "local-1" {
		t.Errorf("expected provider agent to win, got %q", agents[0].Name)
	}
}

func TestOrchestrator_EnvironmentError(t *testing.T) {
	env := &fakeEnv{
		err: context.DeadlineExceeded,
	}

	o := NewOrchestrator()
	o.AddEnvironment(env)

	agents, err := o.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected 0 agents on env error, got %d", len(agents))
	}
}

func TestOrchestrator_MixedProviderAndEnvironment(t *testing.T) {
	prov := &fakeProvider{
		name: "claude",
		agents: []agent.Agent{
			{Name: "local-agent", WorkingDir: "/src/local"},
		},
	}
	env := &fakeEnv{
		agents: []agent.Agent{
			{Name: "remote-agent", SandboxName: "sb-2", Location: "remote"},
		},
	}

	o := NewOrchestrator(prov)
	o.AddEnvironment(env)

	agents, err := o.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(agents))
	}
}

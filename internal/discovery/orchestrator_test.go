package discovery

import (
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
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
		{Name: "myapp", WorkingDir: "/src/myapp", ProviderName: "gemini"},
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

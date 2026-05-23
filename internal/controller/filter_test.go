package controller

import (
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
)

func TestFilterAgents_EmptyQuery(t *testing.T) {
	agents := []agent.Agent{
		{WorkingDir: "/src/alpha"},
		{WorkingDir: "/src/beta"},
	}
	got := FilterAgents(agents, "")
	if len(got) != 2 {
		t.Errorf("len = %d, want 2 (empty query returns all)", len(got))
	}
}

func TestFilterAgents_ByName(t *testing.T) {
	agents := []agent.Agent{
		{WorkingDir: "/src/MyProject"},
		{WorkingDir: "/src/other"},
	}
	got := FilterAgents(agents, "myproject")
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].ShortProject() != "MyProject" {
		t.Errorf("got %q, want MyProject", got[0].ShortProject())
	}
}

func TestFilterAgents_CaseInsensitive(t *testing.T) {
	agents := []agent.Agent{
		{WorkingDir: "/src/AlPhA"},
	}
	got := FilterAgents(agents, "ALPHA")
	if len(got) != 1 {
		t.Errorf("len = %d, want 1 (case-insensitive match)", len(got))
	}
}

func TestFilterAgents_ByModel(t *testing.T) {
	agents := []agent.Agent{
		{WorkingDir: "/src/a", Model: "claude-opus-4-6[1m]"},
		{WorkingDir: "/src/b", Model: "claude-haiku-3-5"},
	}
	got := FilterAgents(agents, "opus")
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].ShortProject() != "a" {
		t.Errorf("got %q, want a", got[0].ShortProject())
	}
}

func TestFilterAgents_ByStatus(t *testing.T) {
	agents := []agent.Agent{
		{WorkingDir: "/src/a", Status: agent.StatusActive},
		{WorkingDir: "/src/b", Status: agent.StatusIdle},
	}
	got := FilterAgents(agents, "active")
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].ShortProject() != "a" {
		t.Errorf("got %q, want a", got[0].ShortProject())
	}
}

func TestFilterAgents_ByProviderName(t *testing.T) {
	agents := []agent.Agent{
		{WorkingDir: "/src/a", ProviderName: "claude"},
		{WorkingDir: "/src/b", ProviderName: "gemini"},
	}
	got := FilterAgents(agents, "gemini")
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].ShortProject() != "b" {
		t.Errorf("got %q, want b", got[0].ShortProject())
	}
}

func TestFilterAgents_ByLastAction(t *testing.T) {
	agents := []agent.Agent{
		{WorkingDir: "/src/a", LastAction: "Ed main.go"},
		{WorkingDir: "/src/b", LastAction: "Sh go test"},
	}
	got := FilterAgents(agents, "go test")
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].ShortProject() != "b" {
		t.Errorf("got %q, want b", got[0].ShortProject())
	}
}

func TestFilterAgents_NoMatches(t *testing.T) {
	agents := []agent.Agent{
		{WorkingDir: "/src/alpha"},
		{WorkingDir: "/src/beta"},
	}
	got := FilterAgents(agents, "nonexistent")
	if len(got) != 0 {
		t.Errorf("len = %d, want 0 (no matches)", len(got))
	}
}

func TestFilterAgents_EmptySlice(t *testing.T) {
	got := FilterAgents(nil, "anything")
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestFilterAgents_BySource(t *testing.T) {
	agents := []agent.Agent{
		{WorkingDir: "/src/a", Source: agent.SourceCLI},
		{WorkingDir: "/src/b", Source: agent.SourceVSCode},
	}
	got := FilterAgents(agents, "vscode")
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].ShortProject() != "b" {
		t.Errorf("got %q, want b", got[0].ShortProject())
	}
}

func TestFilterAgents_ByShortDir(t *testing.T) {
	agents := []agent.Agent{
		{WorkingDir: "/home/user/projects/alpha"},
		{WorkingDir: "/home/user/work/beta"},
	}
	got := FilterAgents(agents, "projects")
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].ShortProject() != "alpha" {
		t.Errorf("got %q, want alpha", got[0].ShortProject())
	}
}

package environment

import (
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
)

// Compile-time interface check.
var _ Environment = (*OpenShellEnvironment)(nil)

func TestOpenShellEnvironment_NameAndType(t *testing.T) {
	env := NewOpenShellEnvironment("", OpenShellConfig{})

	if got := env.Name(); got != "openshell" {
		t.Errorf("Name() = %q, want %q", got, "openshell")
	}
	if got := env.Type(); got != "openshell" {
		t.Errorf("Type() = %q, want %q", got, "openshell")
	}
}

func TestOpenShellEnvironment_CustomName(t *testing.T) {
	env := NewOpenShellEnvironment("staging", OpenShellConfig{})

	if got := env.Name(); got != "staging" {
		t.Errorf("Name() = %q, want %q", got, "staging")
	}
}

func TestParseSandboxAgents_Ready(t *testing.T) {
	output := "NAME              CREATED             PHASE\nhappy-fox         2026-06-21 10:00:00  Ready\nsad-cat           2026-06-21 10:01:00  Error\n"
	agents := parseSandboxAgents(output)

	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(agents))
	}
	if agents[0].Name != "happy-fox" {
		t.Errorf("name: got %q", agents[0].Name)
	}
	if agents[0].ProviderName != "claude" {
		t.Errorf("provider: got %q, want %q", agents[0].ProviderName, "claude")
	}
	if agents[0].Location != "remote" {
		t.Errorf("location: got %q", agents[0].Location)
	}
	if agents[0].SandboxName != "happy-fox" {
		t.Errorf("sandbox name: got %q", agents[0].SandboxName)
	}
	if agents[0].Status != agent.StatusActive {
		t.Errorf("status: got %v, want Active", agents[0].Status)
	}
	if agents[1].Status != agent.StatusError {
		t.Errorf("status: got %v, want Error", agents[1].Status)
	}
}

func TestParseSandboxAgents_Empty(t *testing.T) {
	agents := parseSandboxAgents("No sandboxes found.\n")
	if len(agents) != 0 {
		t.Errorf("expected 0, got %d", len(agents))
	}
}

func TestParseSandboxAgents_WithAnsi(t *testing.T) {
	output := "\x1b[1mNAME\x1b[0m  \x1b[1mPHASE\x1b[0m\ntest-box  \x1b[32mReady\x1b[39m\n"
	agents := parseSandboxAgents(output)

	if len(agents) != 1 {
		t.Fatalf("expected 1, got %d", len(agents))
	}
	if agents[0].Name != "test-box" {
		t.Errorf("name: got %q", agents[0].Name)
	}
	if agents[0].Status != agent.StatusActive {
		t.Errorf("status: got %v, want Active", agents[0].Status)
	}
}

func TestParseSandboxAgents_DeletingPhase(t *testing.T) {
	output := "NAME   CREATED             PHASE\nold-box 2026-06-21 10:00:00  Deleting\n"
	agents := parseSandboxAgents(output)

	if len(agents) != 1 {
		t.Fatalf("expected 1, got %d", len(agents))
	}
	if agents[0].Status != agent.StatusError {
		t.Errorf("status: got %v, want Error", agents[0].Status)
	}
	if agents[0].LastAction != "Deleting" {
		t.Errorf("lastAction: got %q, want %q", agents[0].LastAction, "Deleting")
	}
}

func TestParseSandboxAgents_UnknownPhase(t *testing.T) {
	output := "NAME   CREATED             PHASE\nmy-box 2026-06-21 10:00:00  Pending\n"
	agents := parseSandboxAgents(output)

	if len(agents) != 1 {
		t.Fatalf("expected 1, got %d", len(agents))
	}
	if agents[0].Status != agent.StatusIdle {
		t.Errorf("status: got %v, want Idle", agents[0].Status)
	}
}

func TestParseSandboxAgents_BlankInput(t *testing.T) {
	agents := parseSandboxAgents("")
	if len(agents) != 0 {
		t.Errorf("expected 0, got %d", len(agents))
	}
}

func TestParseSandboxAgents_HeaderOnly(t *testing.T) {
	agents := parseSandboxAgents("NAME   CREATED   PHASE\n")
	if len(agents) != 0 {
		t.Errorf("expected 0, got %d", len(agents))
	}
}

func TestParseSandboxAgents_WorkingDir(t *testing.T) {
	output := "NAME   PHASE\nfoo    Ready\n"
	agents := parseSandboxAgents(output)

	if len(agents) != 1 {
		t.Fatalf("expected 1, got %d", len(agents))
	}
	if agents[0].WorkingDir != "/sandbox" {
		t.Errorf("workingDir: got %q, want %q", agents[0].WorkingDir, "/sandbox")
	}
}


func TestFetchSessionReplies_EmptyInputs(t *testing.T) {
	env := NewOpenShellEnvironment("", OpenShellConfig{})

	if got := env.FetchSessionReplies("", ""); got != nil {
		t.Errorf("FetchSessionReplies(\"\", \"\") = %v, want nil", got)
	}
	if got := env.FetchSessionReplies("sandbox", ""); got != nil {
		t.Errorf("FetchSessionReplies(\"sandbox\", \"\") = %v, want nil", got)
	}
	if got := env.FetchSessionReplies("", "session"); got != nil {
		t.Errorf("FetchSessionReplies(\"\", \"session\") = %v, want nil", got)
	}
}

func TestFetchSessionTurns_EmptyInputs(t *testing.T) {
	env := NewOpenShellEnvironment("", OpenShellConfig{})

	if got := env.FetchSessionTurns("", ""); got != nil {
		t.Errorf("FetchSessionTurns(\"\", \"\") = %v, want nil", got)
	}
	if got := env.FetchSessionTurns("sandbox", ""); got != nil {
		t.Errorf("FetchSessionTurns(\"sandbox\", \"\") = %v, want nil", got)
	}
	if got := env.FetchSessionTurns("", "session"); got != nil {
		t.Errorf("FetchSessionTurns(\"\", \"session\") = %v, want nil", got)
	}
}

func TestStripAnsi(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"plain text", "plain text"},
		{"\x1b[1mbold\x1b[0m", "bold"},
		{"\x1b[32mgreen\x1b[39m", "green"},
		{"no escape", "no escape"},
		{"", ""},
	}

	for _, tt := range tests {
		got := stripAnsi(tt.input)
		if got != tt.want {
			t.Errorf("stripAnsi(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestKill_NoSandboxName(t *testing.T) {
	env := NewOpenShellEnvironment("", OpenShellConfig{})
	a := agent.Agent{Name: "test-agent"}

	err := env.Kill(a)
	if err == nil {
		t.Fatal("Kill() with empty SandboxName should return error")
	}
}

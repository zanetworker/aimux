package provider

import (
	"testing"

	"github.com/zanetworker/agentmux/internal/agent"
)

func TestClaudeName(t *testing.T) {
	c := &Claude{}
	if got := c.Name(); got != "claude" {
		t.Errorf("Claude.Name() = %q, want %q", got, "claude")
	}
}

func TestClaudeResumeCommandWithSessionID(t *testing.T) {
	c := &Claude{}
	a := agent.Agent{
		SessionID:  "abc-123",
		WorkingDir: "/tmp/project",
	}
	cmd := c.ResumeCommand(a)
	if cmd == nil {
		t.Fatal("ResumeCommand returned nil, want non-nil")
	}

	args := cmd.Args
	// args[0] is the binary path, args[1:] are the flags
	if len(args) < 3 {
		t.Fatalf("expected at least 3 args, got %d: %v", len(args), args)
	}
	if args[1] != "--resume" {
		t.Errorf("args[1] = %q, want %q", args[1], "--resume")
	}
	if args[2] != "abc-123" {
		t.Errorf("args[2] = %q, want %q", args[2], "abc-123")
	}
	if cmd.Dir != "/tmp/project" {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, "/tmp/project")
	}
}

func TestClaudeResumeCommandWithWorkingDirOnly(t *testing.T) {
	c := &Claude{}
	a := agent.Agent{
		WorkingDir: "/tmp/project",
	}
	cmd := c.ResumeCommand(a)
	if cmd == nil {
		t.Fatal("ResumeCommand returned nil, want non-nil")
	}

	args := cmd.Args
	if len(args) < 2 {
		t.Fatalf("expected at least 2 args, got %d: %v", len(args), args)
	}
	if args[1] != "--continue" {
		t.Errorf("args[1] = %q, want %q", args[1], "--continue")
	}
	if cmd.Dir != "/tmp/project" {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, "/tmp/project")
	}
}

func TestClaudeResumeCommandWithNothing(t *testing.T) {
	c := &Claude{}
	a := agent.Agent{}
	cmd := c.ResumeCommand(a)
	if cmd != nil {
		t.Errorf("ResumeCommand returned %v, want nil", cmd)
	}
}

func TestClaudeParseConversation(t *testing.T) {
	c := &Claude{}
	segments, err := c.ParseConversation("/some/path")
	if err != nil {
		t.Errorf("ParseConversation returned error: %v", err)
	}
	if segments != nil {
		t.Errorf("ParseConversation returned %v, want nil", segments)
	}
}

// Verify Claude implements the Provider interface at compile time.
var _ Provider = (*Claude)(nil)

package provider

import (
	"testing"

	"github.com/zanetworker/agentmux/internal/agent"
)

func TestCodexName(t *testing.T) {
	c := &Codex{}
	if got := c.Name(); got != "codex" {
		t.Errorf("Codex.Name() = %q, want %q", got, "codex")
	}
}

func TestCodexDiscover(t *testing.T) {
	c := &Codex{}
	agents, err := c.Discover()
	if err != nil {
		t.Errorf("Codex.Discover() error = %v, want nil", err)
	}
	if agents != nil {
		t.Errorf("Codex.Discover() = %v, want nil", agents)
	}
}

func TestCodexResumeCommand(t *testing.T) {
	c := &Codex{}
	cmd := c.ResumeCommand(agent.Agent{SessionID: "test", WorkingDir: "/tmp"})
	if cmd != nil {
		t.Errorf("Codex.ResumeCommand() = %v, want nil", cmd)
	}
}

func TestCodexParseConversation(t *testing.T) {
	c := &Codex{}
	segments, err := c.ParseConversation("/some/path")
	if err != nil {
		t.Errorf("Codex.ParseConversation() error = %v, want nil", err)
	}
	if segments != nil {
		t.Errorf("Codex.ParseConversation() = %v, want nil", segments)
	}
}

func TestGeminiName(t *testing.T) {
	g := &Gemini{}
	if got := g.Name(); got != "gemini" {
		t.Errorf("Gemini.Name() = %q, want %q", got, "gemini")
	}
}

func TestGeminiDiscover(t *testing.T) {
	g := &Gemini{}
	agents, err := g.Discover()
	if err != nil {
		t.Errorf("Gemini.Discover() error = %v, want nil", err)
	}
	if agents != nil {
		t.Errorf("Gemini.Discover() = %v, want nil", agents)
	}
}

func TestGeminiResumeCommand(t *testing.T) {
	g := &Gemini{}
	cmd := g.ResumeCommand(agent.Agent{SessionID: "test", WorkingDir: "/tmp"})
	if cmd != nil {
		t.Errorf("Gemini.ResumeCommand() = %v, want nil", cmd)
	}
}

func TestGeminiParseConversation(t *testing.T) {
	g := &Gemini{}
	segments, err := g.ParseConversation("/some/path")
	if err != nil {
		t.Errorf("Gemini.ParseConversation() error = %v, want nil", err)
	}
	if segments != nil {
		t.Errorf("Gemini.ParseConversation() = %v, want nil", segments)
	}
}

// Verify stubs implement the Provider interface at compile time.
var _ Provider = (*Codex)(nil)
var _ Provider = (*Gemini)(nil)

package provider

import (
	"os/exec"

	"github.com/zanetworker/agentmux/internal/agent"
)

// Codex is a stub Provider for the OpenAI Codex CLI.
type Codex struct{}

func (c *Codex) Name() string                                            { return "codex" }
func (c *Codex) Discover() ([]agent.Agent, error)                        { return nil, nil }
func (c *Codex) ResumeCommand(a agent.Agent) *exec.Cmd                   { return nil }
func (c *Codex) ParseConversation(sessionPath string) ([]Segment, error) { return nil, nil }

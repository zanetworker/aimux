package provider

import (
	"os/exec"

	"github.com/zanetworker/agentmux/internal/agent"
)

// Gemini is a stub Provider for the Google Gemini CLI.
type Gemini struct{}

func (g *Gemini) Name() string                                            { return "gemini" }
func (g *Gemini) Discover() ([]agent.Agent, error)                        { return nil, nil }
func (g *Gemini) ResumeCommand(a agent.Agent) *exec.Cmd                   { return nil }
func (g *Gemini) ParseConversation(sessionPath string) ([]Segment, error) { return nil, nil }

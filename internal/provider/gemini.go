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

// CanEmbed returns false because Gemini's TUI cannot run inside an embedded PTY.
func (g *Gemini) CanEmbed() bool { return false }

// FindSessionFile is not implemented for Gemini yet.
func (g *Gemini) FindSessionFile(a agent.Agent) string { return "" }

// RecentDirs is not implemented for Gemini yet.
func (g *Gemini) RecentDirs(max int) []RecentDir { return nil }

// SpawnCommand builds the exec.Cmd to launch a new Gemini session.
// No flags are supported yet.
func (g *Gemini) SpawnCommand(dir, model, mode string) *exec.Cmd {
	bin := findBinary("gemini")
	cmd := exec.Command(bin)
	cmd.Dir = dir
	return cmd
}

// SpawnArgs returns the available models and modes for launching Gemini.
func (g *Gemini) SpawnArgs() SpawnArgs {
	return SpawnArgs{
		Models: []string{"default"},
		Modes:  []string{"default"},
	}
}

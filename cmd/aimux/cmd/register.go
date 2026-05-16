package cmd

import (
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/profile"
)

// Deps holds all injectable dependencies for cobra subcommands.
type Deps struct {
	Discover         func() ([]agent.Agent, error)
	DiscoverSessions func(opts history.DiscoverOpts, dir string) ([]history.Session, error)
	SearchContent    func(query, dir string) ([]history.ContentMatch, error)
	PickSession      func(sessions []history.Session) (history.Session, error)
	ResumeBuilder    func(sessionID string, danger bool) (command, workDir string, err error)
	ResumeExec       func(sessionID string, danger bool)
	SpawnAgent       func(provider, dir, model, mode, prompt string) (pid int, tmuxSession string, err error)
	WebServer        func(port int) error
	Providers        []string
	ProfileStore     *profile.Store
	FeedbackPath     string
}

// RegisterAll wires all subcommands to rootCmd using the provided dependencies.
func RegisterAll(d Deps) {
	rootCmd.AddCommand(newVersionCmd())
	rootCmd.AddCommand(newAgentsCmd(d.Discover))
	rootCmd.AddCommand(newSessionsCmd(d.DiscoverSessions, d.SearchContent, d.PickSession, d.ResumeExec))
	rootCmd.AddCommand(newResumeCmd(d.ResumeBuilder, d.ResumeExec))
	rootCmd.AddCommand(newSpawnCmd(d.Providers, d.SpawnAgent))
	rootCmd.AddCommand(newWebCmd(d.WebServer))
	rootCmd.AddCommand(newAgentContextCmd(d.Providers))
	if d.ProfileStore != nil {
		rootCmd.AddCommand(newProfileCmd(d.ProfileStore))
	}
	rootCmd.AddCommand(newFeedbackCmd(d.FeedbackPath))
}

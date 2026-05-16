package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/history"
)

var bannedVerbs = map[string]string{
	"info":     "use 'get'",
	"show":     "use 'get'",
	"describe": "use 'get'",
	"fetch":    "use 'get'",
	"ls":       "use 'list'",
	"find":     "use 'list' or 'search'",
	"new":      "use 'create'",
	"add":      "use 'create'",
	"make":     "use 'create'",
	"set":      "use 'update'",
	"modify":   "use 'update'",
	"change":   "use 'update'",
	"edit":     "use 'update'",
	"rm":       "use 'delete'",
	"remove":   "use 'delete'",
	"destroy":  "use 'delete'",
}

var bannedFlags = map[string]string{
	"format":             "use '--json'",
	"output":             "use '--json'",
	"max":                "use '--limit'",
	"count":              "use '--limit'",
	"num":                "use '--limit'",
	"sync":               "use '--wait'",
	"block":              "use '--wait'",
	"skip-confirmations": "use '--force'",
	"no-confirm":         "use '--force'",
}

// registerTestCommands adds all subcommands to rootCmd using stub deps
// so the vocabulary test can inspect the full command tree.
func registerTestCommands(t *testing.T) func() {
	t.Helper()
	deps := Deps{
		Discover:         func() ([]agent.Agent, error) { return nil, nil },
		DiscoverSessions: func(_ history.DiscoverOpts, _ string) ([]history.Session, error) { return nil, nil },
		SearchContent:    func(_, _ string) ([]history.ContentMatch, error) { return nil, nil },
		PickSession:      func(_ []history.Session) (history.Session, error) { return history.Session{}, nil },
		ResumeBuilder:    func(_ string, _ bool) (string, string, error) { return "", "", nil },
		ResumeExec:       func(_ string, _ bool) {},
		SpawnAgent:       func(_, _, _, _, _ string) (int, string, error) { return 0, "", nil },
		WebServer:        func(_ int) error { return nil },
		Providers:        []string{"claude", "codex", "gemini"},
	}
	RegisterAll(deps)
	return func() {
		// Remove all commands we added so other tests aren't affected.
		for _, sub := range rootCmd.Commands() {
			rootCmd.RemoveCommand(sub)
		}
	}
}

func TestVocabularyCompliance_Commands(t *testing.T) {
	cleanup := registerTestCommands(t)
	defer cleanup()

	for _, sub := range rootCmd.Commands() {
		name := sub.Name()
		verb := name
		if idx := strings.IndexByte(name, '-'); idx != -1 {
			verb = name[:idx]
		}
		if suggestion, banned := bannedVerbs[verb]; banned {
			t.Errorf("command %q uses banned verb %q (%s)", name, verb, suggestion)
		}
	}
}

func TestVocabularyCompliance_Flags(t *testing.T) {
	cleanup := registerTestCommands(t)
	defer cleanup()

	checked := make(map[string]bool)

	// Check root command flags too.
	rootCmd.Flags().VisitAll(func(f *pflag.Flag) {
		key := "root:" + f.Name
		if checked[key] {
			return
		}
		checked[key] = true
		if suggestion, banned := bannedFlags[f.Name]; banned {
			t.Errorf("flag --%s on root command is banned (%s)", f.Name, suggestion)
		}
	})

	for _, sub := range rootCmd.Commands() {
		sub.Flags().VisitAll(func(f *pflag.Flag) {
			key := sub.Name() + ":" + f.Name
			if checked[key] {
				return
			}
			checked[key] = true
			if suggestion, banned := bannedFlags[f.Name]; banned {
				t.Errorf("flag --%s on command %q is banned (%s)", f.Name, sub.Name(), suggestion)
			}
		})
	}
}

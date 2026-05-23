package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/cmd/aimux/cmd"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/debuglog"
	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/frontend/tui"
	"github.com/zanetworker/aimux/internal/frontend/web"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/plugin"
	"github.com/zanetworker/aimux/internal/provider"
	"github.com/zanetworker/aimux/internal/sessions"
	"github.com/zanetworker/aimux/internal/spawn"
	"github.com/zanetworker/aimux/internal/tasks"
	"github.com/zanetworker/aimux/internal/trace"
)

// version is set via ldflags at build time: -X main.version=v0.3.0
var version = "dev"

func main() {
	disco := discovery.NewOrchestrator(
		&provider.Claude{},
		&provider.Codex{},
		&provider.Gemini{},
	)

	cfg, _ := config.Load(config.DefaultPath())
	// Merge project-local config if running from a project directory
	if cwd, err := os.Getwd(); err == nil {
		cfg, _ = config.LoadProject(cwd, cfg)
	}

	// Wire TUI launcher
	cmd.SetRunTUI(func(_ *cobra.Command, _ []string) error {
		debuglog.Init()
		defer debuglog.Close()
		debuglog.Log("aimux starting (version %s)", version)

		app := tui.NewApp()
		p := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion())
		if _, err := p.Run(); err != nil {
			return err
		}
		return nil
	})

	// Wire TUI + web launcher
	cmd.SetRunBoth(func(c *cobra.Command, _ []string) error {
		port := 3000
		if p, err := c.Flags().GetInt("port"); err == nil && p > 0 {
			port = p
		}
		s := createWebServer(port)
		go func() {
			fmt.Printf("aimux web dashboard: http://127.0.0.1:%d\n", port)
			if err := s.Start(); err != nil {
				debuglog.Log("web server error: %v", err)
			}
		}()

		debuglog.Init()
		defer debuglog.Close()
		debuglog.Log("aimux starting (version %s)", version)

		app := tui.NewApp()
		p := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion())
		if _, err := p.Run(); err != nil {
			return err
		}
		return nil
	})

	profileStore := profile.NewStore(profile.DefaultPath())
	_ = profileStore.Load()

	deps := cmd.Deps{
		Discover:         disco.Discover,
		DiscoverSessions: history.Discover,
		SearchContent:    history.SearchContent,
		PickSession:      sessions.PickSession,
		ResumeBuilder:    buildResumeBuilder(cfg),
		ResumeExec:       resumeSession,
		SpawnAgent:       buildSpawnFn(disco, cfg),
		WebServer: func(port int) error {
			return createWebServer(port).Start()
		},
		SkipPermissions: cfg.Resume.SkipPermissions,
		Providers:       []string{"claude", "codex", "gemini"},
		ProfileStore:    profileStore,
		TraceParsers: map[string]func(filePath string) ([]trace.Turn, error){
			"claude": (&provider.Claude{}).ParseTrace,
			"codex":  (&provider.Codex{}).ParseTrace,
			"gemini": (&provider.Gemini{}).ParseTrace,
		},
	}

	cmd.RegisterAll(deps)
	cmd.Execute(version)
}

// buildResumeBuilder returns a function that looks up a session's work dir
// and constructs the claude resume command string.
func buildResumeBuilder(cfg config.Config) func(sessionID string, danger bool) (string, string, error) {
	return func(sessionID string, danger bool) (string, string, error) {
		allSessions, _ := history.Discover(history.DiscoverOpts{}, "")
		var workDir string
		for _, s := range allSessions {
			if s.ID == sessionID {
				workDir = s.Project
				break
			}
		}

		claudeBin := "claude"
		if path, err := exec.LookPath("claude"); err == nil {
			claudeBin = path
		}

		command := claudeBin + " --resume " + sessionID
		if danger {
			command += " --dangerously-skip-permissions"
		}
		return command, workDir, nil
	}
}

// resumeSession executes the claude resume command directly.
func resumeSession(sessionID string, danger bool) {
	allSessions, _ := history.Discover(history.DiscoverOpts{}, "")
	var workDir string
	for _, s := range allSessions {
		if s.ID == sessionID {
			workDir = s.Project
			break
		}
	}

	claudeBin := "claude"
	if path, err := exec.LookPath("claude"); err == nil {
		claudeBin = path
	}

	cmdArgs := []string{"--resume", sessionID}
	if danger {
		cmdArgs = append(cmdArgs, "--dangerously-skip-permissions")
	}

	c := exec.Command(claudeBin, cmdArgs...) // #nosec G204
	if workDir != "" {
		if info, err := os.Stat(workDir); err == nil && info.IsDir() {
			c.Dir = workDir
		}
	}

	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	if err := c.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Resume failed: %v\n", err)
		os.Exit(1)
	}
}

// buildSpawnFn returns a function that spawns an agent via the provider's
// SpawnCommand and the spawn.Launch helper.
func buildSpawnFn(disco *discovery.Orchestrator, cfg config.Config) func(providerName, dir, model, mode, prompt string) (int, string, error) {
	return func(providerName, dir, model, mode, prompt string) (int, string, error) {
		p := disco.ProviderFor(providerName)
		if p == nil {
			return 0, "", fmt.Errorf("unknown provider: %s", providerName)
		}

		type spawner interface {
			SpawnCommand(dir, model, mode string) *exec.Cmd
		}
		sp, ok := p.(spawner)
		if !ok {
			return 0, "", fmt.Errorf("provider %s does not support spawning", providerName)
		}

		c := sp.SpawnCommand(dir, model, mode)
		if c == nil {
			return 0, "", fmt.Errorf("failed to build spawn command for %s", providerName)
		}

		if prompt != "" {
			switch providerName {
			case "claude", "gemini":
				c.Args = append(c.Args, prompt)
			case "codex":
				c.Args = append(c.Args, "--prompt", prompt)
			default:
				c.Args = append(c.Args, prompt)
			}
		}

		shell := cfg.ResolveShell()
		if err := spawn.Launch(c, providerName, dir, "tmux", shell, ""); err != nil {
			return 0, "", err
		}

		tmuxSession := spawn.TmuxSessionName(providerName, dir)
		return 0, tmuxSession, nil
	}
}

// createWebServer builds and wires a web.Server with all dependencies.
func createWebServer(port int) *web.Server {
	cfg, _ := config.Load(config.DefaultPath())
	// Merge project-local config if running from a project directory
	if cwd, err := os.Getwd(); err == nil {
		cfg, _ = config.LoadProject(cwd, cfg)
	}
	disco := discovery.NewOrchestrator(
		&provider.Claude{},
		&provider.Codex{},
		&provider.Gemini{},
	)

	s := web.NewServer(port)
	s.SetDiscoverFunc(disco.Discover)
	s.SetLaunchFunc(func(providerName, dir, model, mode, prompt string) error {
		// Find the provider to build the spawn command
		p := disco.ProviderFor(providerName)
		if p == nil {
			return fmt.Errorf("unknown provider: %s", providerName)
		}

		// Get the provider's spawn args interface to build the command
		type spawner interface {
			SpawnCommand(dir, model, mode string) *exec.Cmd
		}
		sp, ok := p.(spawner)
		if !ok {
			return fmt.Errorf("provider %s does not support spawning", providerName)
		}

		cmd := sp.SpawnCommand(dir, model, mode)
		if cmd == nil {
			return fmt.Errorf("failed to build spawn command for %s", providerName)
		}

		if prompt != "" {
			// Claude takes prompt as positional arg, Codex uses --prompt, Gemini uses positional
			switch providerName {
			case "claude", "gemini":
				cmd.Args = append(cmd.Args, prompt)
			case "codex":
				cmd.Args = append(cmd.Args, "--prompt", prompt)
			default:
				cmd.Args = append(cmd.Args, prompt)
			}
		}

		shell := cfg.ResolveShell()
		return spawn.Launch(cmd, providerName, dir, "tmux", shell, "")
	})
	s.SetKillFunc(func(pid int, tmuxSession string) error {
		// Kill the tmux session if it exists
		if tmuxSession != "" {
			_ = exec.Command("tmux", "kill-session", "-t", tmuxSession).Run() // #nosec G204
		}
		// Also kill the process
		if pid > 0 {
			proc, err := os.FindProcess(pid)
			if err == nil {
				_ = proc.Signal(syscall.SIGTERM)
			}
		}
		return nil
	})

	// Wire provider lookup for trace parsing
	s.SetProviderLookup(func(name string) interface{ ParseTrace(string) ([]trace.Turn, error) } {
		p := disco.ProviderFor(name)
		if p == nil {
			return &provider.Claude{}
		}
		type tracer interface {
			ParseTrace(string) ([]trace.Turn, error)
		}
		if t, ok := p.(tracer); ok {
			return t
		}
		return &provider.Claude{}
	})

	s.SetController(controller.New(cfg))
	s.SetConfig(cfg)

	allPlugins := plugin.Builtins()
	if custom, err := plugin.ScanPlugins(plugin.DefaultPluginsDir()); err == nil {
		allPlugins = append(allPlugins, custom...)
	}
	if len(allPlugins) > 0 {
		s.SetPluginExecutor(plugin.NewExecutor(allPlugins))
	}

	// Wire recent directories from all providers
	s.SetRecentDirsFunc(func(max int) []web.RecentDirInfo {
		type dirEntry struct {
			path     string
			lastUsed time.Time
		}
		byPath := make(map[string]*dirEntry)
		providers := []provider.Provider{&provider.Claude{}, &provider.Codex{}, &provider.Gemini{}}
		for _, p := range providers {
			for _, rd := range p.RecentDirs(max) {
				if existing, ok := byPath[rd.Path]; ok {
					if rd.LastUsed.After(existing.lastUsed) {
						existing.lastUsed = rd.LastUsed
					}
				} else {
					byPath[rd.Path] = &dirEntry{path: rd.Path, lastUsed: rd.LastUsed}
				}
			}
		}
		sorted := make([]*dirEntry, 0, len(byPath))
		for _, de := range byPath {
			sorted = append(sorted, de)
		}
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].lastUsed.After(sorted[j].lastUsed) })
		if len(sorted) > max {
			sorted = sorted[:max]
		}
		var result []web.RecentDirInfo
		for _, de := range sorted {
			display := filepath.Base(de.path)
			if display == "" || display == "." {
				display = de.path
			}
			age := ""
			if !de.lastUsed.IsZero() {
				d := time.Since(de.lastUsed)
				switch {
				case d < time.Minute:
					age = fmt.Sprintf("%ds ago", int(d.Seconds()))
				case d < time.Hour:
					age = fmt.Sprintf("%dm ago", int(d.Minutes()))
				case d < 24*time.Hour:
					age = fmt.Sprintf("%dh ago", int(d.Hours()))
				default:
					age = fmt.Sprintf("%dd ago", int(d.Hours()/24))
				}
			}
			result = append(result, web.RecentDirInfo{Path: de.path, Display: display, Age: age})
		}
		return result
	})

	// Best-effort tasks provider initialization
	taskProvider, taskErr := tasks.NewProvider(cfg.Tasks.Backend, cfg.Tasks.MCPEndpoint)
	if taskErr != nil {
		// Log but don't fail — tasks panel will just be unavailable
		debuglog.Log("tasks: %v (tasks panel will be unavailable)", taskErr)
	}
	if taskProvider != nil {
		s.SetTaskProvider(taskProvider)
	}

	return s
}

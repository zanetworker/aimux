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
	aimuxcompose "github.com/zanetworker/aimux/internal/compose"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/controller"
	aimuxotel "github.com/zanetworker/aimux/internal/otel"
	"github.com/zanetworker/aimux/internal/debuglog"
	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/environment"
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
	)

	cfg, _ := config.Load(config.DefaultPath())
	// Merge project-local config if running from a project directory
	if cwd, err := os.Getwd(); err == nil {
		cfg, _ = config.LoadProject(cwd, cfg)
	}

	cmd.AutoRegisterMCP(cfg)

	// Wire TUI launcher
	cmd.SetRunTUI(func(_ *cobra.Command, _ []string) error {
		debuglog.Init()
		defer debuglog.Close()
		debuglog.Log("aimux starting (version %s)", version)

		app := tui.NewApp()
		if exec := createPluginExecutor(); exec != nil {
			app.SetPluginExecutor(exec)
		}
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
		if exec := createPluginExecutor(); exec != nil {
			app.SetPluginExecutor(exec)
		}
		p := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion())
		if _, err := p.Run(); err != nil {
			return err
		}
		return nil
	})

	profileStore := profile.NewStore(profile.DefaultPath())
	_ = profileStore.Load()

	var cliComposeEngine *aimuxcompose.Engine
	if cfg.Remote.Backend == "openshell" {
		cliComposeEngine, _ = aimuxcompose.New(aimuxcompose.Options{
			Gateway:  cfg.Remote.Gateway,
			Insecure: true,
			Image:    cfg.Remote.Image,
		})
	}

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
		DefaultMode:     cfg.DefaultMode,
		Providers:       []string{"claude", "codex"},
		ProfileStore:    profileStore,
		TraceParsers: map[string]func(filePath string) ([]trace.Turn, error){
			"claude": (&provider.Claude{}).ParseTrace,
			"codex":  (&provider.Codex{}).ParseTrace,
		},
		ComposeEngine: cliComposeEngine,
		Environments:  cfg.Environments,
		AgentConfigs:  cfg.AgentConfigs,
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
func buildSpawnFn(disco *discovery.Orchestrator, cfg config.Config) func(opts spawn.LaunchOpts) (int, string, error) {
	return func(opts spawn.LaunchOpts) (int, string, error) {
		p := disco.ProviderFor(opts.Provider)
		if p == nil {
			return 0, "", fmt.Errorf("unknown provider: %s", opts.Provider)
		}
		h, ok := p.(controller.Harness)
		if !ok {
			return 0, "", fmt.Errorf("provider %s does not support spawning", opts.Provider)
		}

		spec := controller.BuildLaunchSpec(h, controller.LaunchRequest{
			Dir: opts.Dir, Model: opts.Model, Mode: opts.Mode, Prompt: opts.Prompt,
			Shell: opts.Shell, SessionManager: opts.SessionManager,
			OTELEnabled: opts.OTELEnabled, OTELEndpoint: opts.OTELEndpoint,
			Runtime: opts.Runtime, ContainerOpts: opts.ContainerOpts,
		})
		if spec.Shell == "/bin/sh" {
			spec.Shell = cfg.ResolveShell()
		}
		result, err := controller.ExecuteLocalLaunch(spec)
		if err != nil {
			return 0, "", err
		}
		return 0, result.TmuxSession, nil
	}
}

// createWebServer builds and wires a web.Server with all dependencies.
func createPluginExecutor() *plugin.Executor {
	allPlugins := plugin.Builtins()
	if custom, err := plugin.ScanPlugins(plugin.DefaultPluginsDir()); err == nil {
		allPlugins = append(allPlugins, custom...)
	}
	if len(allPlugins) == 0 {
		return nil
	}
	return plugin.NewExecutor(allPlugins)
}

func createWebServer(port int) *web.Server {
	cfg, _ := config.Load(config.DefaultPath())
	// Merge project-local config if running from a project directory
	if cwd, err := os.Getwd(); err == nil {
		cfg, _ = config.LoadProject(cwd, cfg)
	}
	disco := discovery.NewOrchestrator(
		&provider.Claude{},
		&provider.Codex{},
	)

	// Register environments for remote agent discovery
	if cfg.Remote.Backend == "openshell" {
		disco.AddEnvironment(environment.NewOpenShellEnvironment("openshell", environment.OpenShellConfig{
			Gateway:  cfg.Remote.Gateway,
			Insecure: true,
			Image:    cfg.Remote.Image,
		}))
	}

	// Initialize compose engine for OpenShell sandbox operations
	var composeEngine *aimuxcompose.Engine
	if cfg.Remote.Backend == "openshell" {
		composeEngine, _ = aimuxcompose.New(aimuxcompose.Options{
			Gateway:  cfg.Remote.Gateway,
			Insecure: true,
			Image:    cfg.Remote.Image,
		})
	}

	s := web.NewServer(port)
	if composeEngine != nil {
		s.SetComposeEngine(composeEngine)
	}
	s.SetDiscoverFunc(disco.Discover)
	s.SetLaunchFunc(func(opts spawn.LaunchOpts) (spawn.LaunchResult, error) {
		p := disco.ProviderFor(opts.Provider)
		if p == nil {
			return spawn.LaunchResult{}, fmt.Errorf("unknown provider: %s", opts.Provider)
		}

		if opts.Runtime == "remote" {
			if composeEngine == nil {
				return spawn.LaunchResult{}, fmt.Errorf("remote runtime requires openshell backend configured")
			}
			result, err := composeEngine.LaunchInSandbox(opts.Provider, opts.Dir, aimuxcompose.LaunchOpts{Image: cfg.Remote.Image})
			if err != nil {
				return spawn.LaunchResult{}, err
			}
			return spawn.LaunchResult{SandboxName: result.SandboxName, OTELSessionID: result.OTELSessionID}, nil
		}

		h, ok := p.(controller.Harness)
		if !ok {
			return spawn.LaunchResult{}, fmt.Errorf("provider %s does not support spawning", opts.Provider)
		}
		spec := controller.BuildLaunchSpec(h, controller.LaunchRequest{
			Dir: opts.Dir, Model: opts.Model, Mode: opts.Mode, Prompt: opts.Prompt,
			Shell: opts.Shell, SessionManager: opts.SessionManager,
			OTELEnabled: opts.OTELEnabled, OTELEndpoint: opts.OTELEndpoint,
			Runtime: opts.Runtime, ContainerOpts: opts.ContainerOpts,
		})
		if spec.Shell == "/bin/sh" {
			spec.Shell = cfg.ResolveShell()
		}
		result, err := controller.ExecuteLocalLaunch(spec)
		if err != nil {
			return spawn.LaunchResult{}, err
		}
		return spawn.LaunchResult{TmuxSession: result.TmuxSession}, nil
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

	configDir := filepath.Join(os.Getenv("HOME"), ".aimux")
	s.SetSessionStore(controller.NewSessionStore(configDir))
	s.SetOTELStore(aimuxotel.NewSpanStore())

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
		providers := []provider.Provider{&provider.Claude{}, &provider.Codex{}}
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

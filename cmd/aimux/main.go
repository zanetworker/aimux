package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/debuglog"
	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/frontend/tui"
	"github.com/zanetworker/aimux/internal/frontend/web"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/plugin"
	"github.com/zanetworker/aimux/internal/provider"
	"github.com/zanetworker/aimux/internal/tasks"
	"github.com/zanetworker/aimux/internal/trace"
	"github.com/zanetworker/aimux/internal/spawn"
)

// version is set via ldflags at build time: -X main.version=v0.3.0
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		runTUI()
		return
	}

	switch os.Args[1] {
	case "--version", "-v":
		fmt.Printf("aimux %s\n", version)
	case "--web":
		runBoth()
	case "web":
		runWeb()
	case "sessions":
		runSessions(os.Args[2:])
	case "resume":
		runResume(os.Args[2:])
	case "--help", "-h", "help":
		printHelp()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printHelp()
		os.Exit(1)
	}
}

func runTUI() {
	debuglog.Init()
	defer debuglog.Close()
	debuglog.Log("aimux starting (version %s)", version)

	app := tui.NewApp()
	p := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func createWebServer(port int) *web.Server {
	cfg, _ := config.Load(config.DefaultPath())
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
			exec.Command("tmux", "kill-session", "-t", tmuxSession).Run()
		}
		// Also kill the process
		if pid > 0 {
			proc, err := os.FindProcess(pid)
			if err == nil {
				proc.Signal(syscall.SIGTERM)
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

func runWeb() {
	port := 3000
	for i, arg := range os.Args {
		if arg == "--port" && i+1 < len(os.Args) {
			fmt.Sscanf(os.Args[i+1], "%d", &port)
		}
	}
	s := createWebServer(port)
	fmt.Printf("aimux web dashboard: http://127.0.0.1:%d\n", port)
	if err := s.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Web server error: %v\n", err)
		os.Exit(1)
	}
}

func runBoth() {
	port := 3000
	for i, arg := range os.Args {
		if arg == "--port" && i+1 < len(os.Args) {
			fmt.Sscanf(os.Args[i+1], "%d", &port)
		}
	}
	s := createWebServer(port)
	go func() {
		fmt.Printf("aimux web dashboard: http://127.0.0.1:%d\n", port)
		if err := s.Start(); err != nil {
			debuglog.Log("web server error: %v", err)
		}
	}()
	runTUI()
}

func printHelp() {
	fmt.Println(`aimux — AI agent multiplexer

Usage:
  aimux                    Launch the TUI dashboard
  aimux --web              Launch TUI + web dashboard
  aimux web                Launch web dashboard only (headless)
  aimux web --port 8080    Custom port (default: 3000)
  aimux sessions           Browse past sessions (interactive)
  aimux sessions --list    List sessions as a table
  aimux sessions --export  Export sessions as JSONL
  aimux resume <id>        Resume a session by ID
  aimux --version          Show version

Sessions flags:
  --dir <path>            Scope to a specific directory
  --list                  Plain table output (scriptable)
  --export                JSONL output for eval pipelines
  --json                  JSON output (with --list)
  --limit <n>             Max sessions to show (default: all)
  --generate-titles       Generate LLM titles for sessions without one
  --title-model <model>   Model for titles: haiku (default), sonnet, opus`)
}

// runSessions handles the "aimux sessions" subcommand.
func runSessions(args []string) {
	// Load config for session defaults
	appCfg, _ := config.Load(config.DefaultPath())

	var dir string
	var listMode, exportMode, jsonMode, generateTitles, regenerateTitles bool
	var limit int
	titleModel := appCfg.Sessions.TitleModel
	if titleModel == "" {
		titleModel = "flash"
	}

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dir":
			if i+1 < len(args) {
				dir = args[i+1]
				i++
			}
		case "--list", "-l":
			listMode = true
		case "--export":
			exportMode = true
		case "--json":
			jsonMode = true
		case "--limit":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &limit)
				i++
			}
		case "--generate-titles":
			generateTitles = true
		case "--regenerate-titles":
			generateTitles = true
			regenerateTitles = true
		case "--title-model":
			if i+1 < len(args) {
				titleModel = args[i+1]
				i++
			}
		}
	}

	opts := history.DiscoverOpts{Dir: dir, Limit: limit}
	sessions, err := history.Discover(opts, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error discovering sessions: %v\n", err)
		os.Exit(1)
	}

	// Filter out near-empty sessions
	var filtered []history.Session
	for _, s := range sessions {
		if s.TurnCount <= 5 && s.CostUSD == 0 {
			continue
		}
		if s.LastActive.IsZero() {
			continue
		}
		filtered = append(filtered, s)
	}

	if generateTitles {
		cfg := history.TitleConfig{
			Enabled:    true,
			Model:      titleModel,
			APIKey:     appCfg.Sessions.APIKey,
			Regenerate: regenerateTitles,
		}
		fmt.Printf("Generating titles using %s...\n", titleModel)
		count, err := history.GenerateTitles(filtered, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Stopped after %d titles: %v\n", count, err)
		} else {
			fmt.Printf("Generated %d titles.\n", count)
		}
		// Reload sessions to show new titles
		sessions, _ = history.Discover(opts, "")
		filtered = nil
		for _, s := range sessions {
			if s.TurnCount <= 5 && s.CostUSD == 0 {
				continue
			}
			if s.LastActive.IsZero() {
				continue
			}
			filtered = append(filtered, s)
		}
	}

	if exportMode {
		printSessionsJSONL(filtered)
		return
	}

	if listMode {
		if jsonMode {
			printSessionsJSON(filtered)
		} else {
			printSessionsTable(filtered)
		}
		return
	}

	// Interactive mode — launch a mini TUI (for now, print table)
	// TODO: Replace with interactive bubbletea browser
	printSessionsTable(filtered)
}

func printSessionsTable(sessions []history.Session) {
	if len(sessions) == 0 {
		fmt.Println("No sessions found.")
		return
	}

	// Header
	fmt.Printf("%-38s  %-14s  %-7s  %5s  %7s  %-10s  %s\n",
		"ID", "PROJECT", "AGE", "TURNS", "COST", "ANNOTATION", "PROMPT")
	fmt.Println(strings.Repeat("─", 120))

	for _, s := range sessions {
		proj := shortProjectName(s.Project)
		age := shortAge(s.LastActive)
		prompt := s.Title
		if prompt == "" {
			prompt = s.FirstPrompt
		}
		if len(prompt) > 40 {
			prompt = prompt[:37] + "..."
		}
		if prompt == "" {
			prompt = "-"
		}
		annot := s.Annotation
		if annot == "" {
			annot = "-"
		}
		tags := ""
		if len(s.Tags) > 0 {
			tags = " [" + strings.Join(s.Tags, ",") + "]"
		}

		fmt.Printf("%-38s  %-14s  %-7s  %5d  $%6.2f  %-10s  %s%s\n",
			s.ID, truncStr(proj, 14), age, s.TurnCount, s.CostUSD, annot, prompt, tags)
	}
}

func printSessionsJSON(sessions []history.Session) {
	data, _ := json.MarshalIndent(sessions, "", "  ")
	fmt.Println(string(data))
}

func printSessionsJSONL(sessions []history.Session) {
	for _, s := range sessions {
		data, _ := json.Marshal(s)
		fmt.Println(string(data))
	}
}

// runResume handles the "aimux resume <session-id>" subcommand.
func runResume(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: aimux resume <session-id>")
		os.Exit(1)
	}
	sessionID := args[0]

	// Find the session to get its project directory
	sessions, _ := history.Discover(history.DiscoverOpts{}, "")
	var workDir string
	for _, s := range sessions {
		if s.ID == sessionID {
			workDir = s.Project
			break
		}
	}

	claudeBin := "claude"
	if path, err := exec.LookPath("claude"); err == nil {
		claudeBin = path
	}

	cmd := exec.Command(claudeBin, "--resume", sessionID)
	if workDir != "" {
		if info, err := os.Stat(workDir); err == nil && info.IsDir() {
			cmd.Dir = workDir
		}
	}

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Resume failed: %v\n", err)
		os.Exit(1)
	}
}

func shortProjectName(path string) string {
	if path == "" {
		return "(unknown)"
	}
	path = strings.TrimPrefix(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) > 0 {
		last := parts[len(parts)-1]
		if last != "" {
			return last
		}
	}
	parts = strings.Split(path, "-")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return path
}

func shortAge(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dmo", int(d.Hours()/24/30))
	}
}

func truncStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

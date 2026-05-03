package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/debuglog"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/frontend/tui"
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

func printHelp() {
	fmt.Println(`aimux — AI agent multiplexer

Usage:
  aimux                    Launch the TUI dashboard
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

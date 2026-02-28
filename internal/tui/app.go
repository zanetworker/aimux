package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/agentmux/internal/agent"
	"github.com/zanetworker/agentmux/internal/config"
	"github.com/zanetworker/agentmux/internal/discovery"
	"github.com/zanetworker/agentmux/internal/evaluation"
	"github.com/zanetworker/agentmux/internal/jump"
	"github.com/zanetworker/agentmux/internal/provider"
	agentmuxotel "github.com/zanetworker/agentmux/internal/otel"
	"github.com/zanetworker/agentmux/internal/spawn"
	"github.com/zanetworker/agentmux/internal/team"
	"github.com/zanetworker/agentmux/internal/terminal"
	"github.com/zanetworker/agentmux/internal/trace"
	"github.com/zanetworker/agentmux/internal/tui/views"
)

type viewType int

const (
	viewAgents viewType = iota
	viewLogs
	viewCosts
	viewTeams
	viewHelp
)

// tickMsg triggers periodic refresh.
type tickMsg time.Time

// instancesMsg carries discovered instances.
type instancesMsg []agent.Agent

// teamsMsg carries team configs.
type teamsMsg []team.TeamConfig

// App is the root Bubble Tea model that wires all views together.
// It implements a three-state layout machine:
//   - Split view (default): agents table on left (~35%) + preview pane on right (~65%)
//   - Zoomed session: full-screen interactive PTY
//   - Sub-views: costs, teams, help (full-screen, non-interactive)
type App struct {
	// State
	currentView viewType
	instances   []agent.Agent
	teams       []team.TeamConfig
	width       int
	height      int

	// Sub-views
	headerView  *views.HeaderView
	agentsView  *views.AgentsView
	previewPane *views.PreviewPane
	sessionView *views.SessionView
	logsView    *views.LogsView
	costsView   *views.CostsView
	teamsView   *views.TeamsView
	helpView    *views.HelpView

	// Layout
	layout *Layout
	zoomed bool

	// Split mode: trace (left) + interactive session (right)
	splitMode  bool
	splitFocus string          // "trace" or "session"
	splitTrace *views.LogsView // live trace pane in split mode

	// Command palette
	commandMode  bool
	commandInput string

	// Filter mode
	filterMode  bool
	filterInput string

	// Discovery
	orchestrator *discovery.Orchestrator

	// Provider access for ResumeCommand — the orchestrator's AgentProvider
	// interface only exposes Name() and Discover(), so we keep the full
	// provider.Provider slice for ResumeCommand lookups.
	providers []provider.Provider

	// Breadcrumb trail
	breadcrumbs []string

	// Temporary status hint (shown once then cleared)
	statusHint string

	// Launcher overlay
	launcherActive bool
	launcherView   *views.LauncherView

	// Kill confirmation
	killConfirm  bool            // true when waiting for y/n confirmation
	killTarget   *agent.Agent    // agent to kill
	hiddenAgents map[string]bool // session IDs hidden from view (session-only entries removed by user)

	// Evaluation: annotation persistence
	evalStore      *evaluation.Store
	evalSessionID  string

	// Config
	cfg config.Config
}

// NewApp creates a new root TUI application.
func NewApp() App {
	cfg, _ := config.Load(config.DefaultPath())

	allProviders := []provider.Provider{
		&provider.Claude{},
		&provider.Codex{},
		&provider.Gemini{},
	}

	// Filter to enabled providers only.
	var providers []provider.Provider
	for _, p := range allProviders {
		if cfg.IsProviderEnabled(p.Name()) {
			providers = append(providers, p)
		}
	}

	// Build AgentProvider slice for the orchestrator from the same providers.
	agentProviders := make([]discovery.AgentProvider, len(providers))
	for i, p := range providers {
		agentProviders[i] = p
	}

	return App{
		currentView:  viewAgents,
		headerView:   views.NewHeaderView(),
		agentsView:   views.NewAgentsView(),
		previewPane:  views.NewPreviewPane(),
		sessionView:  views.NewSessionView(),
		costsView:    views.NewCostsView(),
		teamsView:    views.NewTeamsView(),
		helpView:     views.NewHelpView(),
		layout:       NewLayout(0, 0),
		orchestrator: discovery.NewOrchestrator(agentProviders...),
		providers:    providers,
		breadcrumbs:  []string{"Agents"},
		hiddenAgents: make(map[string]bool),
		cfg:          cfg,
	}
}

func (a App) Init() tea.Cmd {
	return tea.Batch(
		a.discoverInstances,
		a.discoverTeams,
		a.tick(),
	)
}

func (a App) tick() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (a App) discoverInstances() tea.Msg {
	instances, _ := a.orchestrator.Discover()
	return instancesMsg(instances)
}

func (a App) discoverTeams() tea.Msg {
	teams, _ := team.ListTeamsDefault()
	return teamsMsg(teams)
}

func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		a.resizeViews()
		// Resize split panes if active
		if a.splitMode {
			leftW := a.width * 40 / 100
			rightW := a.width - leftW - 1
			if a.splitTrace != nil {
				a.splitTrace.SetSize(leftW, a.height-3)
			}
			a.sessionView.SetSize(rightW, a.height)
		}
		return a, nil

	case tickMsg:
		return a, tea.Batch(a.discoverInstances, a.tick())

	case instancesMsg:
		a.instances = a.filterHidden([]agent.Agent(msg))
		a.agentsView.SetAgents(a.instances)
		a.headerView.SetAgents(a.instances)
		a.costsView.SetAgents(a.instances)
		if a.currentView == viewLogs && a.logsView != nil {
			a.logsView.Reload()
		}
		// Refresh preview pane conversation data on tick
		if a.currentView == viewAgents {
			a.previewPane.Reload()
		}
		// Refresh live trace in split mode
		if a.splitMode && a.splitTrace != nil {
			a.splitTrace.Reload()
		}
		return a, nil

	case teamsMsg:
		a.teams = []team.TeamConfig(msg)
		a.teamsView.SetTeams(a.teams)
		return a, nil

	case views.LaunchMsg:
		a.launcherActive = false
		a.launcherView = nil
		p := a.providerFor(msg.Provider)
		if p == nil {
			a.statusHint = fmt.Sprintf("Launch failed: unknown provider %q", msg.Provider)
			return a, nil
		}
		cmd := p.SpawnCommand(msg.Dir, msg.Model, msg.Mode)
		if err := spawn.Launch(cmd, msg.Provider, msg.Dir, msg.Runtime, a.cfg.ResolveShell()); err != nil {
			a.statusHint = fmt.Sprintf("Launch failed: %v", err)
		} else {
			name := filepath.Base(msg.Dir)
			a.statusHint = fmt.Sprintf("Launched %s in %s (%s)", msg.Provider, name, msg.Runtime)
		}
		return a, nil

	case views.LaunchCancelMsg:
		a.launcherActive = false
		a.launcherView = nil
		a.statusHint = "Launch cancelled"
		return a, nil

	case views.PTYOutputMsg:
		if a.sessionView != nil {
			cmd := a.sessionView.HandleOutput(msg.Data)
			return a, cmd
		}
		return a, nil

	case views.PTYExitMsg:
		a.zoomed = false
		a.splitMode = false
		a.splitTrace = nil
		a.layout.SetZoomed(false)
		if a.sessionView != nil {
			a.sessionView.Close()
		}
		return a, nil

	case views.AnnotationMsg:
		// Persist annotation to disk and update views
		if a.evalStore != nil {
			if msg.Label == "" {
				_ = a.evalStore.Remove(msg.Turn)
				a.statusHint = fmt.Sprintf("Turn %d: annotation removed", msg.Turn)
			} else {
				_ = a.evalStore.Save(evaluation.Annotation{
					Turn:      msg.Turn,
					Label:     msg.Label,
					Note:      msg.Note,
					Timestamp: time.Now(),
				})
				hint := fmt.Sprintf("Turn %d: [%s]", msg.Turn, strings.ToUpper(msg.Label))
				if msg.Note != "" {
					hint += fmt.Sprintf(" \"%s\"", msg.Note)
				}
				hint += "  a:cycle  N:note  :export"
				a.statusHint = hint
			}
		}
		// Sync annotation state to whichever trace view is active
		if a.splitTrace != nil {
			annots := a.splitTrace.Annotations()
			notes := a.splitTrace.Notes()
			if msg.Label == "" {
				delete(annots, msg.Turn)
				delete(notes, msg.Turn)
			} else {
				annots[msg.Turn] = msg.Label
				if msg.Note != "" {
					notes[msg.Turn] = msg.Note
				}
			}
		}
		return a, nil

	case tea.KeyMsg:
		// Launcher overlay active — route all keys to it
		if a.launcherActive && a.launcherView != nil {
			cmd := a.launcherView.Update(msg)
			return a, cmd
		}
		// Kill confirmation prompt
		if a.killConfirm {
			return a.handleKillConfirm(msg)
		}
		// When zoomed into a session, intercept only Ctrl+] to zoom out.
		// All other keys are forwarded to the PTY subprocess.
		if a.zoomed && a.sessionView != nil && a.sessionView.Active() {
			return a.handleZoomedKey(msg)
		}
		if a.commandMode {
			return a.handleCommandInput(msg)
		}
		if a.filterMode {
			return a.handleFilterInput(msg)
		}
		return a.handleKey(msg)
	}
	return a, nil
}

// handleZoomedKey processes keys while the session view is zoomed in.
// In split mode: Tab switches focus, Ctrl+g exits, keys go to focused pane.
// In full-screen mode: Ctrl+g/]/\ exits, all other keys go to PTY.
func (a App) handleZoomedKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Exit keys — always work regardless of mode/focus
	switch key {
	case "ctrl+]", "ctrl+\\", "ctrl+g":
		return a.exitZoom()
	}
	if len(key) == 1 && key[0] == 0x1d {
		return a.exitZoom()
	}

	// Esc exits zoomed/split view (but clears trace filter first if active)
	if key == "esc" {
		if a.splitMode && a.splitFocus == "trace" && a.splitTrace != nil && a.splitTrace.HasActiveFilter() {
			a.splitTrace.Update(msg) // let trace handle Esc to clear filter
			return a, nil
		}
		return a.exitZoom()
	}

	// Ctrl+f toggles split/fullscreen — zooms whichever pane is focused
	if key == "ctrl+f" && a.splitTrace != nil {
		a.splitMode = !a.splitMode
		if !a.splitMode {
			// Full-screen the focused pane
			if a.splitFocus == "trace" {
				a.splitTrace.SetSize(a.width, a.height-1)
			} else {
				a.sessionView.SetSize(a.width, a.height)
			}
		} else {
			// Return to split
			leftW := a.width * 40 / 100
			rightW := a.width - leftW - 1
			a.sessionView.SetSize(rightW, a.height)
			a.splitTrace.SetSize(leftW, a.height-3)
		}
		return a, nil
	}

	// Tab switches focus — only in split mode
	if key == "tab" && a.splitMode {
		if a.splitFocus == "trace" {
			a.splitFocus = "session"
		} else {
			a.splitFocus = "trace"
		}
		return a, nil
	}

	// Split mode key routing
	// In split mode with trace focused, route keys to trace pane
	if a.splitMode && a.splitFocus == "trace" && a.splitTrace != nil {
		cmd := a.splitTrace.Update(msg)
		return a, cmd
	}

	// Send to PTY session
	a.sessionView.SendKey(key)
	return a, nil
}

func (a App) exitZoom() (tea.Model, tea.Cmd) {
	a.zoomed = false
	a.splitMode = false
	a.splitTrace = nil
	a.layout.SetZoomed(false)
	a.sessionView.Close()
	return a, nil
}

func (a App) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Clear any status hint on keypress
	a.statusHint = ""

	switch msg.String() {
	case "q":
		if a.currentView == viewAgents {
			return a, tea.Quit
		}
		return a.navigateBack()
	case ":":
		a.commandMode = true
		a.commandInput = ""
		return a, nil
	case "/":
		if a.currentView == viewAgents {
			a.filterMode = true
			a.filterInput = ""
			return a, nil
		}
		if a.currentView == viewLogs && a.logsView != nil {
			cmd := a.logsView.Update(msg)
			return a, cmd
		}
	case "?":
		return a.navigateTo(viewHelp, "Help")
	case "x":
		if a.currentView == viewAgents {
			return a.promptKill()
		}
	case "l":
		if a.currentView == viewAgents {
			return a.openLogsForSelected()
		}
	case "esc":
		if a.filterInput != "" {
			a.filterInput = ""
			a.agentsView.SetFilter("")
			return a, nil
		}
		// Let logs view handle esc for its own filter/search mode first
		if a.currentView == viewLogs && a.logsView != nil && a.logsView.HasActiveFilter() {
			cmd := a.logsView.Update(msg)
			return a, cmd
		}
		return a.navigateBack()
	case "enter", " ":
		// Enter/Space in logs view -> expand/collapse turns
		if a.currentView == viewLogs && a.logsView != nil {
			cmd := a.logsView.Update(msg)
			return a, cmd
		}
		return a.handleEnter()
	case "J":
		if a.currentView == viewLogs {
			return a.jumpToSession()
		}
		return a.handleJump()
	}

	// Delegate navigation keys to the current view
	switch a.currentView {
	case viewAgents:
		a.agentsView.Update(msg)
		// Update preview pane when cursor moves
		a.syncPreview()
	case viewLogs:
		if a.logsView != nil {
			cmd := a.logsView.Update(msg)
			return a, cmd
		}
	}
	return a, nil
}

// syncPreview updates the preview pane with the currently selected agent.
func (a *App) syncPreview() {
	selected := a.agentsView.Selected()
	if selected != nil {
		if p := a.providerFor(selected.ProviderName); p != nil {
			a.previewPane.SetParser(a.parserForProvider(p))
		}
	}
	a.previewPane.SetAgent(selected)
}

// parserForProvider returns a TraceParser function wrapping the provider's ParseTrace.
func (a App) parserForProvider(p provider.Provider) views.TraceParser {
	return func(filePath string) ([]trace.Turn, error) {
		return p.ParseTrace(filePath)
	}
}

func (a App) handleCommandInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		cmd := resolveCommand(a.commandInput)
		a.commandMode = false
		a.commandInput = ""
		return a.executeCommand(cmd)
	case "esc":
		a.commandMode = false
		a.commandInput = ""
		return a, nil
	case "backspace":
		if len(a.commandInput) > 0 {
			a.commandInput = a.commandInput[:len(a.commandInput)-1]
		}
		return a, nil
	case "tab":
		completions := commandCompletions(a.commandInput)
		if len(completions) == 1 {
			a.commandInput = completions[0]
		}
		return a, nil
	default:
		if len(msg.String()) == 1 {
			a.commandInput += msg.String()
		}
		return a, nil
	}
}

func (a App) handleFilterInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		a.filterMode = false
		a.agentsView.SetFilter(a.filterInput)
		return a, nil
	case "esc":
		a.filterMode = false
		a.filterInput = ""
		a.agentsView.SetFilter("")
		return a, nil
	case "backspace":
		if len(a.filterInput) > 0 {
			a.filterInput = a.filterInput[:len(a.filterInput)-1]
		}
		return a, nil
	default:
		if len(msg.String()) == 1 {
			a.filterInput += msg.String()
		}
		return a, nil
	}
}

func (a App) executeCommand(cmd string) (tea.Model, tea.Cmd) {
	switch cmd {
	case "instances":
		return a.navigateTo(viewAgents, "Agents")
	case "logs":
		return a.openLogsForSelected()
	case "teams":
		a2, _ := a.navigateTo(viewTeams, "Teams")
		return a2, a.discoverTeams
	case "costs":
		return a.navigateTo(viewCosts, "Costs")
	case "help":
		return a.navigateTo(viewHelp, "Help")
	case "export":
		return a.exportTrace()
	case "kill":
		return a.promptKill()
	case "new":
		return a.openLauncher()
	case "export-otel":
		return a.exportOTEL()
	case "quit":
		return a, tea.Quit
	}
	return a, nil
}

func (a App) openLauncher() (tea.Model, tea.Cmd) {
	// Build recent dirs list from all enabled providers.
	type dirEntry struct {
		path     string
		lastUsed time.Time
		provider string
	}
	byPath := make(map[string]*dirEntry)

	for _, p := range a.providers {
		for _, rd := range p.RecentDirs(20) {
			if existing, ok := byPath[rd.Path]; ok {
				existing.provider = "both"
				if rd.LastUsed.After(existing.lastUsed) {
					existing.lastUsed = rd.LastUsed
				}
			} else {
				byPath[rd.Path] = &dirEntry{
					path:     rd.Path,
					lastUsed: rd.LastUsed,
					provider: p.Name(),
				}
			}
		}
	}

	// Sort by most recent first
	sorted := make([]*dirEntry, 0, len(byPath))
	for _, de := range byPath {
		sorted = append(sorted, de)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].lastUsed.After(sorted[j].lastUsed)
	})
	if len(sorted) > 20 {
		sorted = sorted[:20]
	}

	var entries []views.RecentDirEntry
	for _, de := range sorted {
		display := filepath.Base(de.path)
		if display == "" || display == "." {
			display = de.path
		}
		age := ""
		if !de.lastUsed.IsZero() {
			age = formatDurationShort(time.Since(de.lastUsed))
		}
		entries = append(entries, views.RecentDirEntry{
			Path:     de.path,
			Display:  display,
			Provider: de.provider,
			Age:      age,
		})
	}

	a.launcherView = views.NewLauncherView(entries)
	a.launcherView.SetSize(a.width, a.height)
	a.launcherActive = true
	return a, nil
}

func formatDurationShort(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func (a App) handleEnter() (tea.Model, tea.Cmd) {
	if a.currentView != viewAgents {
		return a, nil
	}
	selected := a.agentsView.Selected()
	if selected == nil {
		return a, nil
	}

	p := a.providerFor(selected.ProviderName)
	if p == nil {
		a.statusHint = "No provider for " + selected.ProviderName
		return a, nil
	}

	// Resolve session file for the trace pane via the provider.
	sessionFile := selected.SessionFile
	if sessionFile == "" {
		sessionFile = p.FindSessionFile(*selected)
	}

	cmd := p.ResumeCommand(*selected)
	if cmd == nil {
		// No resume possible — fall back to trace-only view
		if sessionFile == "" {
			a.statusHint = "No trace data yet — agent may still be starting"
			return a, nil
		}
		return a.openLogsForAgent(selected, sessionFile)
	}

	// Size the session view for the right half
	rightW := a.width * 60 / 100
	a.sessionView.SetSize(rightW, a.height)

	contentH := a.height - 2
	if contentH < 1 {
		contentH = 24
	}
	contentW := rightW
	if contentW < 1 {
		contentW = 80
	}

	// Pick backend: direct PTY for embeddable providers, tmux mirror for others
	var backend terminal.SessionBackend
	if p.CanEmbed() {
		sess, err := terminal.Start(cmd)
		if err != nil {
			a.statusHint = fmt.Sprintf("Error: %v", err)
			return a, nil
		}
		backend = sess
	} else {
		// Use tmux mirror — attach to existing session if available, else create
		var err error
		if selected.TMuxSession != "" {
			backend, err = terminal.AttachTmux(selected.TMuxSession, contentW, contentH)
		} else {
			backend, err = terminal.StartTmux(cmd, contentW, contentH, a.cfg.ResolveShell())
		}
		if err != nil {
			a.statusHint = fmt.Sprintf("Tmux mirror failed: %v", err)
			return a, nil
		}
	}

	teaCmd, err := a.sessionView.Open(selected, backend)
	if err != nil {
		a.statusHint = fmt.Sprintf("Error: %v", err)
		return a, nil
	}

	// Create live trace pane with annotations loaded
	if sessionFile != "" {
		leftW := a.width - rightW
		a.splitTrace = views.NewLogsView(selected.PID, sessionFile, a.parserForProvider(p))
		a.splitTrace.SetSize(leftW, a.height-1)

		// Set up evaluation store and load annotations into split trace
		sessionID := selected.SessionID
		if sessionID == "" {
			sessionID = fmt.Sprintf("pid-%d", selected.PID)
		}
		a.evalSessionID = sessionID
		a.evalStore = evaluation.NewStore(sessionID)
		annotations, _ := a.evalStore.Load()
		annotMap := make(map[int]string)
		noteMap := make(map[int]string)
		for _, ann := range annotations {
			annotMap[ann.Turn] = ann.Label
			if ann.Note != "" {
				noteMap[ann.Turn] = ann.Note
			}
		}
		a.splitTrace.SetAnnotations(annotMap)
		a.splitTrace.SetNotes(noteMap)
	}

	a.zoomed = true
	a.splitMode = true
	a.splitFocus = "trace" // start with focus on the trace pane (left)
	a.layout.SetZoomed(true)
	return a, teaCmd
}

// providerFor returns the full provider.Provider whose Name() matches, or nil.
func (a App) providerFor(name string) provider.Provider {
	for _, p := range a.providers {
		if p.Name() == name {
			return p
		}
	}
	return nil
}

// statusMsg is shown briefly in the status bar.
type statusMsg struct {
	text string
}

func (a App) handleJump() (tea.Model, tea.Cmd) {
	selected := a.agentsView.Selected()
	if selected == nil {
		return a, nil
	}
	// J always opens a zoomed session (same as Enter)
	return a.handleEnter()
}

func (a App) exportTrace() (tea.Model, tea.Cmd) {
	if a.logsView == nil || a.evalSessionID == "" {
		a.statusHint = "Open a trace first (l on an agent), then :export"
		return a, nil
	}

	// Build export turns from the current trace data
	turns := a.logsView.Turns()
	var exportTurns []evaluation.ExportTurn
	for _, t := range turns {
		et := evaluation.ExportTurn{
			Turn:      t.Number,
			Timestamp: t.Timestamp.Format(time.RFC3339),
			Input:     strings.Join(t.UserLines, "\n"),
			Output:    strings.Join(t.OutputLines, "\n"),
			TokensIn:  t.TokensIn,
			TokensOut: t.TokensOut,
			CostUSD:   t.CostUSD,
		}
		if dur := t.Duration(); dur > 0 {
			et.DurationMs = dur.Milliseconds()
		}
		for _, action := range t.Actions {
			et.Actions = append(et.Actions, evaluation.ExportAction{
				Tool:    action.Name,
				Input:   action.Snippet,
				Success: action.Success,
				Error:   action.ErrorMsg,
			})
		}
		// Include annotations
		if a.evalStore != nil {
			if ann := a.evalStore.GetForTurn(t.Number); ann != nil {
				et.Label = ann.Label
				et.Note = ann.Note
			}
		}
		exportTurns = append(exportTurns, et)
	}

	path := evaluation.ExportPath(a.evalSessionID)
	if err := evaluation.WriteExport(path, exportTurns); err != nil {
		a.statusHint = fmt.Sprintf("Export failed: %v", err)
		return a, nil
	}

	a.statusHint = fmt.Sprintf("Exported %d turns to %s", len(exportTurns), path)
	return a, nil
}

// exportOTEL sends the current trace + annotations as OTLP/HTTP spans to
// the configured export endpoint (e.g., MLflow, Jaeger).
func (a App) exportOTEL() (tea.Model, tea.Cmd) {
	if a.logsView == nil || a.evalSessionID == "" {
		a.statusHint = "Open a trace first (l on an agent), then :export-otel"
		return a, nil
	}

	endpoint := a.cfg.Export.Endpoint
	if endpoint == "" {
		a.statusHint = "Set export.endpoint in ~/.agentmux/config.yaml first"
		return a, nil
	}

	turns := a.logsView.Turns()
	if len(turns) == 0 {
		a.statusHint = "No trace data to export"
		return a, nil
	}

	// Determine provider name from the current agent context
	providerName := ""
	selected := a.agentsView.Selected()
	if selected != nil {
		providerName = selected.ProviderName
	}

	cfg := agentmuxotel.ExportConfig{
		Endpoint:  endpoint,
		Insecure:  a.cfg.Export.Insecure,
		SessionID: a.evalSessionID,
		Provider:  providerName,
	}

	if err := agentmuxotel.ExportTrace(cfg, turns, a.evalStore); err != nil {
		a.statusHint = fmt.Sprintf("OTEL export failed: %v", err)
		return a, nil
	}

	a.statusHint = fmt.Sprintf("Exported %d turns via OTLP to %s", len(turns), endpoint)
	return a, nil
}

// jumpToSession opens the selected agent's session in a separate terminal pane
// (iTerm split or tmux pane). Used for providers like Codex whose TUI can't embed.
func (a App) jumpToSession() (tea.Model, tea.Cmd) {
	selected := a.agentsView.Selected()
	if selected == nil {
		a.statusHint = "No agent selected"
		return a, nil
	}

	p := a.providerFor(selected.ProviderName)
	if p == nil {
		a.statusHint = "No provider for " + selected.ProviderName
		return a, nil
	}

	cmd := p.ResumeCommand(*selected)
	if cmd == nil {
		a.statusHint = "Cannot resume this session"
		return a, nil
	}

	// Build the command string for the external terminal
	cmdStr := strings.Join(cmd.Args, " ")
	if cmd.Dir != "" {
		cmdStr = fmt.Sprintf("cd %q && %s", cmd.Dir, cmdStr)
	}

	if jump.IsITerm2() {
		if err := jump.ITerm2SplitPane(cmdStr); err != nil {
			a.statusHint = fmt.Sprintf("iTerm split failed: %v", err)
		} else {
			a.statusHint = "Opened in iTerm split pane"
		}
	} else if jump.IsInsideTmux() {
		// Create a tmux split pane
		tmuxCmd := exec.Command("tmux", "split-window", "-h", cmdStr)
		if err := tmuxCmd.Run(); err != nil {
			a.statusHint = fmt.Sprintf("tmux split failed: %v", err)
		} else {
			a.statusHint = "Opened in tmux split pane"
		}
	} else {
		a.statusHint = fmt.Sprintf("Run manually: %s", cmdStr)
	}

	return a, nil
}

// promptKill shows a confirmation prompt before killing the selected agent.
// For session-only entries (PID=0), offers to remove and delete trace files.
func (a App) promptKill() (tea.Model, tea.Cmd) {
	selected := a.agentsView.Selected()
	if selected == nil {
		a.statusHint = "No agent selected"
		return a, nil
	}
	a.killConfirm = true
	a.killTarget = selected
	if selected.PID == 0 {
		a.statusHint = fmt.Sprintf("Remove %s? y:remove  d:remove+delete trace  n:cancel", selected.ShortProject())
	} else {
		a.statusHint = fmt.Sprintf("Kill %s (PID %d)? y:confirm  n:cancel", selected.ShortProject(), selected.PID)
	}
	return a, nil
}

// handleKillConfirm processes the y/n/d response to the kill confirmation.
func (a App) handleKillConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	target := a.killTarget
	a.killConfirm = false
	a.killTarget = nil

	if target == nil {
		return a, nil
	}

	switch msg.String() {
	case "y", "Y":
		if target.PID == 0 {
			// Session-only: hide from view by adding to hidden set
			a.hideAgent(target)
			a.statusHint = fmt.Sprintf("Removed %s from view", target.ShortProject())
		} else {
			err := killAgent(target)
			if err != nil {
				a.statusHint = fmt.Sprintf("Kill failed: %v", err)
			} else {
				a.statusHint = fmt.Sprintf("Killed %s (PID %d)", target.ShortProject(), target.PID)
			}
		}
		return a, nil
	case "d", "D":
		// Remove + delete trace file
		a.hideAgent(target)
		if target.SessionFile != "" {
			if err := os.Remove(target.SessionFile); err != nil {
				a.statusHint = fmt.Sprintf("Removed from view, but failed to delete trace: %v", err)
			} else {
				a.statusHint = fmt.Sprintf("Removed %s and deleted trace file", target.ShortProject())
			}
		} else {
			a.statusHint = fmt.Sprintf("Removed %s (no trace file to delete)", target.ShortProject())
		}
		return a, nil
	default:
		a.statusHint = "Cancelled"
		return a, nil
	}
}

// hideAgent adds an agent to the hidden set so it doesn't appear in the list.
func (a *App) hideAgent(ag *agent.Agent) {
	key := ag.SessionID
	if key == "" && ag.SessionFile != "" {
		key = ag.SessionFile
	}
	if key == "" {
		key = fmt.Sprintf("pid-%d", ag.PID)
	}
	a.hiddenAgents[key] = true
}

// filterHidden removes hidden agents from the list.
func (a *App) filterHidden(agents []agent.Agent) []agent.Agent {
	if len(a.hiddenAgents) == 0 {
		return agents
	}
	var result []agent.Agent
	for _, ag := range agents {
		key := ag.SessionID
		if key == "" && ag.SessionFile != "" {
			key = ag.SessionFile
		}
		if key == "" {
			key = fmt.Sprintf("pid-%d", ag.PID)
		}
		if !a.hiddenAgents[key] {
			result = append(result, ag)
		}
	}
	return result
}

// killAgent sends SIGTERM to the agent process, waits briefly, then SIGKILL
// if still alive. Also kills grouped sub-processes.
func killAgent(ag *agent.Agent) error {
	pids := []int{ag.PID}
	if len(ag.GroupPIDs) > 0 {
		pids = ag.GroupPIDs
	}

	var firstErr error
	for _, pid := range pids {
		proc, err := os.FindProcess(pid)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("find process %d: %w", pid, err)
			}
			continue
		}

		// Send SIGTERM for graceful shutdown
		if err := proc.Signal(syscall.SIGTERM); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("SIGTERM %d: %w", pid, err)
			}
			continue
		}

		// Wait briefly then force kill if still alive
		go func(p *os.Process, id int) {
			time.Sleep(3 * time.Second)
			// Check if still alive by sending signal 0
			if err := p.Signal(syscall.Signal(0)); err == nil {
				_ = p.Signal(syscall.SIGKILL)
			}
		}(proc, pid)
	}

	return firstErr
}

// openLogsForAgent opens the trace viewer for a specific agent and session file.
// Used for non-Claude providers where embedding a PTY isn't possible.
func (a App) openLogsForAgent(ag *agent.Agent, sessionFile string) (tea.Model, tea.Cmd) {
	p := a.providerFor(ag.ProviderName)
	var parser views.TraceParser
	if p != nil {
		parser = a.parserForProvider(p)
	}
	a.logsView = views.NewLogsView(ag.PID, sessionFile, parser)
	contentHeight := a.height - a.headerView.Height()
	if contentHeight < 1 {
		contentHeight = 10
	}
	a.logsView.SetSize(a.width, contentHeight)

	// Set up evaluation store
	sessionID := ag.SessionID
	if sessionID == "" {
		sessionID = fmt.Sprintf("pid-%d", ag.PID)
	}
	a.evalSessionID = sessionID
	a.evalStore = evaluation.NewStore(sessionID)
	annotations, _ := a.evalStore.Load()
	annotMap := make(map[int]string)
	noteMap := make(map[int]string)
	for _, ann := range annotations {
		annotMap[ann.Turn] = ann.Label
		if ann.Note != "" {
			noteMap[ann.Turn] = ann.Note
		}
	}
	a.logsView.SetAnnotations(annotMap)
	a.logsView.SetNotes(noteMap)

	label := fmt.Sprintf("Trace [%s: %s]", ag.ProviderName, ag.ShortProject())
	a.statusHint = "J:jump to session  :export  a:annotate  N:note"
	return a.navigateTo(viewLogs, label)
}

func (a App) openLogsForSelected() (tea.Model, tea.Cmd) {
	selected := a.agentsView.Selected()
	if selected == nil {
		return a, nil
	}
	p := a.providerFor(selected.ProviderName)
	sessionFile := selected.SessionFile
	if sessionFile == "" {
		if p != nil {
			sessionFile = p.FindSessionFile(*selected)
		}
	}
	var parser views.TraceParser
	if p != nil {
		parser = a.parserForProvider(p)
	}
	a.logsView = views.NewLogsView(selected.PID, sessionFile, parser)
	contentHeight := a.height - a.headerView.Height()
	if contentHeight < 1 {
		contentHeight = 10
	}
	a.logsView.SetSize(a.width, contentHeight)

	// Set up evaluation store and load existing annotations
	sessionID := selected.SessionID
	if sessionID == "" {
		sessionID = fmt.Sprintf("pid-%d", selected.PID)
	}
	a.evalSessionID = sessionID
	a.evalStore = evaluation.NewStore(sessionID)
	annotations, _ := a.evalStore.Load()
	annotMap := make(map[int]string)
	noteMap := make(map[int]string)
	for _, ann := range annotations {
		annotMap[ann.Turn] = ann.Label
		if ann.Note != "" {
			noteMap[ann.Turn] = ann.Note
		}
	}
	a.logsView.SetAnnotations(annotMap)
	a.logsView.SetNotes(noteMap)

	return a.navigateTo(viewLogs, fmt.Sprintf("Logs [PID %d]", selected.PID))
}

func (a App) navigateTo(v viewType, label string) (tea.Model, tea.Cmd) {
	a.currentView = v
	if v == viewAgents {
		a.breadcrumbs = []string{"Agents"}
	} else {
		a.breadcrumbs = []string{"Agents", label}
	}
	a.headerView.SetCrumbs(a.breadcrumbs)
	return a, nil
}

func (a App) navigateBack() (tea.Model, tea.Cmd) {
	if a.currentView != viewAgents {
		a.currentView = viewAgents
		a.breadcrumbs = []string{"Agents"}
		a.headerView.SetCrumbs(a.breadcrumbs)
	}
	return a, nil
}

func (a *App) resizeViews() {
	a.layout.SetSize(a.width, a.height)
	a.headerView.SetWidth(a.width)

	headerHeight := a.headerView.Height()
	contentHeight := a.layout.ContentHeight(headerHeight)

	leftW, rightW := a.layout.SplitVertical(35)

	a.agentsView.SetSize(leftW, contentHeight)
	a.previewPane.SetSize(rightW, contentHeight)
	a.costsView.SetSize(a.width, contentHeight)
	a.teamsView.SetSize(a.width, contentHeight)
	a.helpView.SetSize(a.width, contentHeight)
	if a.logsView != nil {
		a.logsView.SetSize(a.width, contentHeight)
	}
	if a.sessionView != nil {
		a.sessionView.SetSize(a.width, a.height)
	}
}

// --- View rendering ---

func (a App) View() string {
	if a.width == 0 {
		return "Loading..."
	}

	// Zoomed modes — no header, no outer status bar.
	if a.zoomed && a.sessionView != nil && a.sessionView.Active() {
		if a.splitMode {
			return a.renderSplitView()
		}
		// Full-screen whichever pane was focused
		if a.splitFocus == "trace" && a.splitTrace != nil {
			return a.splitTrace.View()
		}
		return a.sessionView.View()
	}

	// Set contextual hints based on current view
	switch a.currentView {
	case viewAgents:
		a.headerView.SetHint("Enter:open  l:logs  :new:launch  x:kill  s:sort  /:filter  ?:help  q:quit")
	case viewLogs:
		a.headerView.SetHint("j/k:scroll  Space:next  Enter:expand  a:annotate  N:note  :export  Esc:back  ?:more")
	case viewCosts:
		a.headerView.SetHint("Esc:back  ?:help")
	case viewTeams:
		a.headerView.SetHint("Esc:back  ?:help")
	case viewHelp:
		a.headerView.SetHint("Esc:back  q:quit")
	}

	header := a.headerView.View()
	headerHeight := a.headerView.Height()
	contentHeight := a.layout.ContentHeight(headerHeight)

	var content string
	switch a.currentView {
	case viewAgents:
		leftW, rightW := a.layout.SplitVertical(35)
		a.agentsView.SetSize(leftW, contentHeight)
		a.previewPane.SetSize(rightW, contentHeight)

		// Update preview with currently selected agent
		selected := a.agentsView.Selected()
		a.previewPane.SetAgent(selected)

		content = lipgloss.JoinHorizontal(lipgloss.Top,
			a.agentsView.View(),
			a.previewPane.View(),
		)
	case viewLogs:
		if a.logsView != nil {
			content = a.logsView.View()
		} else {
			content = "  No logs available"
		}
	case viewCosts:
		content = a.costsView.View()
	case viewTeams:
		content = a.teamsView.View()
	case viewHelp:
		content = a.helpView.View()
	}

	statusBar := a.renderStatusBar()

	// Fit content to exact available height: pad if short, truncate if long
	availableHeight := a.height - headerHeight - 1
	if availableHeight < 1 {
		availableHeight = 1
	}
	lines := strings.Split(content, "\n")
	if len(lines) > availableHeight {
		lines = lines[:availableHeight]
	}
	for len(lines) < availableHeight {
		lines = append(lines, "")
	}
	content = strings.Join(lines, "\n")

	result := header + "\n" + content + "\n" + statusBar

	// Overlay the launcher if active
	if a.launcherActive && a.launcherView != nil {
		a.launcherView.SetSize(a.width, a.height)
		return a.launcherView.View()
	}

	return result
}

// renderSplitView renders the split layout: live trace (left) + session (right).
func (a App) renderSplitView() string {
	leftW := a.width * 40 / 100
	rightW := a.width - leftW - 1 // -1 for divider

	contentH := a.height - 1 // reserve 1 for status bar

	// Resize panes
	if a.splitTrace != nil {
		a.splitTrace.SetSize(leftW, contentH-2) // -2 for trace header + status
	}
	a.sessionView.SetSize(rightW, contentH)

	// Styles for pane headers
	focusedHeaderStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#111827")).
		Background(lipgloss.Color("#5F87FF"))
	unfocusedHeaderStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#9CA3AF")).
		Background(lipgloss.Color("#1E293B"))
	dividerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#374151"))

	// Left pane: trace
	var leftLines []string
	traceHeaderStyle := unfocusedHeaderStyle
	if a.splitFocus == "trace" {
		traceHeaderStyle = focusedHeaderStyle
	}
	traceHeader := traceHeaderStyle.Render(padRight(" TRACE ", leftW))
	leftLines = append(leftLines, traceHeader)

	if a.splitTrace != nil {
		traceContent := a.splitTrace.View()
		for _, line := range strings.Split(traceContent, "\n") {
			leftLines = append(leftLines, line)
		}
	} else {
		leftLines = append(leftLines, lipgloss.NewStyle().Foreground(colorMuted).Render("  No trace data"))
	}

	// Pad left pane to fill height
	for len(leftLines) < contentH {
		leftLines = append(leftLines, "")
	}
	if len(leftLines) > contentH {
		leftLines = leftLines[:contentH]
	}

	// Right pane: session (rendered by SessionView with its own header/status)
	sessionContent := a.sessionView.View()
	rightLines := strings.Split(sessionContent, "\n")
	// Replace session header with our focused/unfocused version
	sessionHeaderStyle := unfocusedHeaderStyle
	if a.splitFocus == "session" {
		sessionHeaderStyle = focusedHeaderStyle
	}
	agentName := "(session)"
	if a.sessionView.Agent() != nil {
		agentName = a.sessionView.Agent().ShortProject()
	}
	rightLines[0] = sessionHeaderStyle.Render(padRight(" SESSION: "+agentName+" ", rightW))

	// Pad right pane
	for len(rightLines) < contentH {
		rightLines = append(rightLines, "")
	}
	if len(rightLines) > contentH {
		rightLines = rightLines[:contentH]
	}

	// Join left and right with divider
	divider := dividerStyle.Render("│")
	var b strings.Builder
	for i := 0; i < contentH; i++ {
		left := leftLines[i]
		right := ""
		if i < len(rightLines) {
			right = rightLines[i]
		}
		// Pad left to exact width
		leftPad := leftW - lipgloss.Width(left)
		if leftPad > 0 {
			left += strings.Repeat(" ", leftPad)
		}
		b.WriteString(left)
		b.WriteString(divider)
		b.WriteString(right)
		b.WriteString("\n")
	}

	// Status bar
	badge := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#111827")).
		Background(lipgloss.Color("#5F87FF")).
		Render(" agentmux ")
	focus := a.splitFocus
	hintStyle := lipgloss.NewStyle().Foreground(colorMuted)
	var focusHint string
	if focus == "trace" {
		focusHint = " [TRACE] j/k:turns  Enter:expand  a:annotate  N:note  /:filter"
	} else {
		focusHint = " [SESSION] typing goes to agent"
	}
	hints := hintStyle.Render(focusHint + "  Tab:switch  Ctrl+f:fullscreen  Esc:exit")
	statusGap := a.width - lipgloss.Width(badge) - lipgloss.Width(hints)
	if statusGap < 0 {
		statusGap = 0
	}
	b.WriteString(badge + hints + strings.Repeat(" ", statusGap))

	return b.String()
}

func padRight(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

func (a App) renderStatusBar() string {
	if a.commandMode {
		return lipgloss.NewStyle().
			Background(lipgloss.Color("#111827")).
			Width(a.width).
			Render(lipgloss.NewStyle().
				Foreground(colorLogo).
				Bold(true).
				Render(" :") + a.commandInput + lipgloss.NewStyle().
				Foreground(colorLogo).Render("|"))
	}
	if a.filterMode {
		return lipgloss.NewStyle().
			Background(lipgloss.Color("#111827")).
			Width(a.width).
			Render(lipgloss.NewStyle().
				Foreground(colorWaiting).
				Bold(true).
				Render(" /") + a.filterInput + lipgloss.NewStyle().
				Foreground(colorWaiting).Render("|"))
	}
	if a.currentView == viewLogs && a.logsView != nil && a.logsView.NoteMode() {
		noteText, noteTurn := a.logsView.NoteInput()
		return lipgloss.NewStyle().
			Background(lipgloss.Color("#111827")).
			Width(a.width).
			Render(lipgloss.NewStyle().
				Foreground(lipgloss.Color("#F59E0B")).
				Bold(true).
				Render(fmt.Sprintf(" Note [Turn %d]: ", noteTurn)) + noteText + lipgloss.NewStyle().
				Foreground(lipgloss.Color("#F59E0B")).Render("|"))
	}

	var hints string
	if a.statusHint != "" {
		hints = " " + lipgloss.NewStyle().Foreground(colorWaiting).Render(a.statusHint)
	} else if a.currentView == viewLogs {
		hints = " j/k:turns  Enter:expand  a:annotate  N:note  /:filter  c:collapse  :export  Esc:back"
	} else {
		// Show group hint if selected agent is grouped
		selected := a.agentsView.Selected()
		if selected != nil && selected.GroupCount > 1 {
			hints = fmt.Sprintf(" x%d = %d grouped  Enter:open  :new:launch  x:kill  l:logs  ?:help",
				selected.GroupCount, selected.GroupCount)
		} else {
			hints = " j/k:nav  Enter:open  :new:launch  x:kill  l:logs  s:sort  ?:help  q:quit"
		}
		if a.filterInput != "" {
			hints += fmt.Sprintf("  [filter: %s]", a.filterInput)
		}
	}
	return lipgloss.NewStyle().
		Foreground(colorIdle).
		Background(lipgloss.Color("#111827")).
		Width(a.width).
		Render(hints)
}

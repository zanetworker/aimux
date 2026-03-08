package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"os"
	"os/exec"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/correlator"
	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/evaluation"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/jump"
	"github.com/zanetworker/aimux/internal/provider"
	aimuxotel "github.com/zanetworker/aimux/internal/otel"
	"github.com/zanetworker/aimux/internal/spawn"
	"github.com/zanetworker/aimux/internal/subagent"
	"github.com/zanetworker/aimux/internal/team"
	"github.com/zanetworker/aimux/internal/terminal"
	"github.com/zanetworker/aimux/internal/trace"
	"github.com/zanetworker/aimux/internal/tui/views"
)

type viewType int

const (
	viewAgents viewType = iota
	viewLogs
	viewCosts
	viewTeams
	viewSessions
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
	costsView    *views.CostsView
	teamsView    *views.TeamsView
	sessionsView *views.SessionsView
	helpView     *views.HelpView

	// Layout
	layout *Layout
	zoomed bool

	// Split mode: trace (left) + interactive session (right)
	splitMode      bool
	splitFocus     string          // "trace" or "session"
	splitTrace     *views.LogsView // live trace pane in split mode
	splitLaunchTime time.Time      // when :new session was launched (filters old files)

	// Command palette
	commandMode   bool
	commandInput  string
	exportConfirm bool // showing export menu
	stickyHint    bool // true = statusHint persists until keypress (not cleared by tick)

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
	cfg  config.Config
	ctrl *controller.Controller

	// OTEL receiver (optional)
	otelReceiver    *aimuxotel.Receiver
	otelStore       *aimuxotel.SpanStore
	lastEnrichTime  time.Time
}

// NewApp creates a new root TUI application.
func NewApp() App {
	cfg, _ := config.Load(config.DefaultPath())
	ctrl := controller.New(cfg)

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

	// Build subagent attr key mapping for OTEL receiver.
	keysByService := make(map[string]subagent.AttrKeys)
	for _, p := range providers {
		keys := p.SubagentAttrKeys()
		if sn := p.OTELServiceName(); sn != "" && !keys.Empty() {
			keysByService[sn] = keys
		}
	}

	app := App{
		currentView:  viewAgents,
		headerView:   views.NewHeaderView(),
		agentsView:   views.NewAgentsView(),
		previewPane:  views.NewPreviewPane(),
		sessionView:  views.NewSessionView(),
		costsView:     views.NewCostsView(),
		sessionsView:  views.NewSessionsView(),
		teamsView:    views.NewTeamsView(),
		helpView:     views.NewHelpView(),
		layout:       NewLayout(0, 0),
		orchestrator: discovery.NewOrchestrator(agentProviders...),
		providers:    providers,
		breadcrumbs:  []string{"Agents"},
		hiddenAgents: make(map[string]bool),
		cfg:          cfg,
		ctrl:         ctrl,
		otelStore:    aimuxotel.NewSpanStore(),
	}

	// Start OTEL receiver if enabled
	if cfg.OTELReceiver.Enabled {
		app.otelReceiver = aimuxotel.NewReceiverWithKeys(app.otelStore, cfg.OTELReceiverPort(), keysByService)
		_ = app.otelReceiver.Start()
	}

	return app
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
		a.instances = controller.FilterHidden([]agent.Agent(msg), a.hiddenAgents)
		if a.otelStore.LastUpdate().After(a.lastEnrichTime) {
			a.instances = correlator.EnrichFromOTEL(a.instances, a.otelStore)
			a.lastEnrichTime = a.otelStore.LastUpdate()
		}
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
		// Clear stale status hints on tick (unless sticky)
		if !a.stickyHint {
			a.statusHint = ""
		}

		// Refresh live trace in split/zoomed mode
		if a.zoomed && a.splitTrace != nil {
			// Keep trying to discover the session file until found.
			// New sessions start with empty filePath (OTEL fills the gap),
			// then switch to file parsing once the session file is created.
			if a.splitTrace.FilePath() == "" && a.sessionView != nil && a.sessionView.Agent() != nil {
				ag := a.sessionView.Agent()
				if p := a.providerFor(ag.ProviderName); p != nil {
					if sf := p.FindSessionFile(*ag); sf != "" {
						// For :new launches, only accept files created after launch
						// to avoid showing traces from previous sessions in the same dir.
						if !a.splitLaunchTime.IsZero() {
							if info, err := os.Stat(sf); err == nil && info.ModTime().Before(a.splitLaunchTime) {
								sf = "" // stale file, skip
							}
						}
						if sf != "" {
							a.splitTrace.SetFilePath(sf)
						}
					}
				}
			}
			a.splitTrace.Reload()
		}
		return a, nil

	case teamsMsg:
		a.teams = []team.TeamConfig(msg)
		a.teamsView.SetTeams(a.teams)
		return a, nil

	case views.LaunchResumeMsg:
		a.launcherActive = false
		a.launcherView = nil
		return a.resumeSession(msg.SessionID, msg.Dir, msg.FilePath)
	case views.LaunchMsg:
		a.launcherActive = false
		a.launcherView = nil
		p := a.providerFor(msg.Provider)
		if p == nil {
			a.statusHint = fmt.Sprintf("Launch failed: unknown provider %q", msg.Provider)
			return a, nil
		}
		cmd := p.SpawnCommand(msg.Dir, msg.Model, msg.Mode)
		envPrefix := ""
		if msg.OTELEnabled && a.cfg.OTELReceiver.Enabled {
			endpoint := fmt.Sprintf("http://localhost:%d", a.cfg.OTELReceiverPort())
			envPrefix = p.OTELEnv(endpoint)
		}
		if err := spawn.Launch(cmd, msg.Provider, msg.Dir, msg.Runtime, a.cfg.ResolveShell(), envPrefix); err != nil {
			a.statusHint = fmt.Sprintf("Launch failed: %v", err)
			return a, nil
		}

		name := filepath.Base(msg.Dir)

		// Immediately open split view for the new tmux session
		if msg.Runtime == "tmux" {
			tmuxName := spawn.TmuxSessionName(msg.Provider, msg.Dir)

			// Size the session view
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

			backend, err := terminal.AttachTmux(tmuxName, contentW, contentH)
			if err != nil {
				a.statusHint = fmt.Sprintf("Launched %s in %s but mirror failed: %v", msg.Provider, name, err)
				return a, nil
			}

			// Create a temporary agent for the session view
			newAgent := &agent.Agent{
				Name:         name,
				ProviderName: msg.Provider,
				WorkingDir:   msg.Dir,
				TMuxSession:  tmuxName,
				Status:       agent.StatusActive,
				Model:        msg.Model,
				GroupCount:    1,
				GroupPIDs:     []int{},
			}

			teaCmd, err := a.sessionView.Open(newAgent, backend)
			if err != nil {
				a.statusHint = fmt.Sprintf("Launched %s in %s (%s)", msg.Provider, name, msg.Runtime)
				return a, nil
			}

			// Create trace pane -- always start empty for new sessions.
			// The tick handler will discover the session file once the agent
			// creates one. Don't use FindSessionFile here because it would
			// pick up an old session file from a previous run in the same dir.
			leftW := a.width - rightW - 1
			sessionFile := ""

			// Set launch time and eval context BEFORE creating parser,
			// since the parser closure captures a copy of App
			a.splitLaunchTime = time.Now()
			a.evalSessionID = newAgent.SessionID
			if a.evalSessionID == "" {
				a.evalSessionID = tmuxName
			}

			a.splitTrace = views.NewLogsView(0, sessionFile, a.parserForProvider(p))
			a.splitTrace.SetSize(leftW, a.height-1)
			if msg.Provider == "gemini" {
				a.splitTrace.SetWarning("Gemini traces only include user prompts (no assistant responses or tool calls)")
			}

			a.zoomed = true
			a.splitMode = true       // always split -- trace fills in as data arrives
			a.splitFocus = "session" // focus on session so user can type
			a.layout.SetZoomed(true)
			a.statusHint = fmt.Sprintf("Launched %s in %s", msg.Provider, name)
			return a, teaCmd
		}

		a.statusHint = fmt.Sprintf("Launched %s in %s (%s)", msg.Provider, name, msg.Runtime)
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

	case views.SessionToggleScopeMsg:
		dir := ""
		if !msg.ShowAll {
			dir = a.sessionsView.CurrentDir()
		}
		sessions, _ := history.Discover(history.DiscoverOpts{Dir: dir}, "")
		a.sessionsView.SetSessions(sessions)
		a.sessionsView.SetTagVocab(history.CollectTags(""))
	case views.SessionAnnotateMsg:
		// Persist session-level annotation
		meta := history.LoadMeta(msg.Session.FilePath)
		meta.Annotation = msg.Annotation
		_ = history.SaveMeta(msg.Session.FilePath, meta)
		a.statusHint = fmt.Sprintf("Session: [%s]", strings.ToUpper(msg.Annotation))
		if msg.Annotation == "" {
			a.statusHint = "Session: annotation removed"
		}
	case views.SessionTagMsg:
		meta := history.LoadMeta(msg.Session.FilePath)
		meta.Tags = msg.Tags
		_ = history.SaveMeta(msg.Session.FilePath, meta)
		a.statusHint = fmt.Sprintf("Session: tags updated (%d)", len(msg.Tags))
	case views.SessionDeleteMsg:
		if err := controller.DeleteSession(msg.Session); err != nil {
			a.statusHint = fmt.Sprintf("Delete failed: %v", err)
		} else {
			a.statusHint = "Session deleted"
		}
	case views.SessionBulkDeleteMsg:
		deleted, _ := controller.BulkDeleteSessions(msg.Sessions)
		a.statusHint = fmt.Sprintf("Deleted %d sessions", deleted)
	case views.SessionNoteMsg:
		meta := history.LoadMeta(msg.Session.FilePath)
		meta.Note = msg.Note
		_ = history.SaveMeta(msg.Session.FilePath, meta)
		a.statusHint = "Session: note saved"
	case views.SessionResumeMsg:
		if msg.SessionID == "" {
			a.statusHint = "No session ID to resume"
			return a, nil
		}
		return a.resumeSession(msg.SessionID, msg.WorkingDir, msg.FilePath)
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
				hint += "  a:cycle  N:note  :export  :export-otel"
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

	case tea.MouseMsg:
		// Intercept mouse wheel for scrolling in zoomed session view
		if a.zoomed && a.sessionView != nil && a.sessionView.Active() {
			if tv := a.sessionView.TermView(); tv != nil {
				switch msg.Button {
				case tea.MouseButtonWheelUp:
					tv.ScrollUp(3)
					return a, nil
				case tea.MouseButtonWheelDown:
					tv.ScrollDown(3)
					return a, nil
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
		// Export menu (e key in split view)
		if a.exportConfirm {
			a.exportConfirm = false
			a.stickyHint = false
			switch msg.String() {
			case "j", "J":
				return a.exportTrace()
			case "o", "O":
				return a.exportOTEL()
			default:
				a.statusHint = ""
				return a, nil
			}
		}
		// Command mode takes priority over zoomed key handling
		// so typing :export works from split view
		if a.commandMode {
			return a.handleCommandInput(msg)
		}
		// When zoomed into a session, intercept only Ctrl+] to zoom out.
		// All other keys are forwarded to the PTY subprocess.
		if a.zoomed && a.sessionView != nil && a.sessionView.Active() {
			return a.handleZoomedKey(msg)
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

	// Clear status hints on any keypress (e.g., "Launched..." or export result)
	a.statusHint = ""
	a.stickyHint = false

	// Exit keys — always work regardless of mode/focus
	switch key {
	case "ctrl+]", "ctrl+\\", "ctrl+g":
		return a.exitZoom()
	}
	if len(key) == 1 && key[0] == 0x1d {
		return a.exitZoom()
	}

	// Esc in split mode: clear trace filter if active, otherwise forward to PTY.
	// Esc is NOT used to exit zoom — use Ctrl+]/g/\ instead.
	// This allows shell features like Ctrl+R (reverse search) to work normally.
	if key == "esc" {
		if a.splitMode && a.splitFocus == "trace" && a.splitTrace != nil && a.splitTrace.HasActiveFilter() {
			a.splitTrace.Update(msg)
			return a, nil
		}
		// Forward Esc to PTY (needed for Ctrl+R cancel, vim escape, etc.)
		a.sessionView.SendKey(key)
		return a, nil
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

	// Command palette -- intercept ":" before routing to trace or PTY
	if key == ":" {
		a.commandMode = true
		a.commandInput = ""
		return a, nil
	}

	// Route keys to trace pane when focused (both split and fullscreen trace)
	if a.splitFocus == "trace" && a.splitTrace != nil {
		// Intercept "e" for export only when NOT in note/filter input mode
		if key == "e" && !a.splitTrace.HasActiveFilter() && !a.splitTrace.NoteMode() {
			a.exportConfirm = true
			a.statusHint = "Export: j:JSONL  o:OTEL  Esc:cancel"
			a.stickyHint = true
			return a, nil
		}
		cmd := a.splitTrace.Update(msg)
		return a, cmd
	}

	// Intercept scroll keys in session view
	if tv := a.sessionView.TermView(); tv != nil {
		switch key {
		case "pgup":
			tv.ScrollUp(10)
			return a, nil
		case "pgdown":
			tv.ScrollDown(10)
			return a, nil
		}
	}

	// Send to PTY session
	a.sessionView.SendKey(key)
	return a, nil
}

func (a App) exitZoom() (tea.Model, tea.Cmd) {
	// Use splitTrace nil check for TUI-specific full-screen detection:
	// the Navigator only tracks state booleans, not TUI objects.
	canReturnToSplit := !a.splitMode && a.splitTrace != nil
	if canReturnToSplit {
		a.ctrl.Nav.SplitMode = false // ensure Navigator matches before ExitZoom
		a.ctrl.Nav.Zoomed = true
	}

	exitedFully := a.ctrl.Nav.ExitZoom()

	if !exitedFully {
		// Returned to split view
		a.splitMode = true
		a.splitFocus = a.ctrl.Nav.SplitFocus
		// Resize back to split layout
		leftW := a.width * 40 / 100
		rightW := a.width - leftW - 1
		a.sessionView.SetSize(rightW, a.height)
		a.splitTrace.SetSize(leftW, a.height-3)
		return a, nil
	}

	// Fully exited
	a.zoomed = false
	a.splitMode = false
	a.splitTrace = nil
	a.splitLaunchTime = time.Time{}
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
		if a.currentView == viewSessions {
			cmd := a.sessionsView.Update(msg)
			return a, cmd
		}
	case "?":
		return a.navigateTo(viewHelp, "Help")
	case "x":
		if a.currentView == viewAgents {
			return a.promptKill()
		}
	case "t":
		if a.currentView == viewAgents {
			return a.openLogsForSelected()
		}
	case "c":
		if a.currentView == viewAgents {
			return a.navigateTo(viewCosts, "Costs")
		}
	case "T":
		if a.currentView == viewAgents {
			a2, _ := a.navigateTo(viewTeams, "Teams")
			return a2, a.discoverTeams
		}
	case "S":
		if a.currentView == viewAgents {
			return a.openSessions()
		}
	case "esc":
		if a.filterInput != "" {
			a.filterInput = ""
			a.agentsView.SetFilter("")
			return a, nil
		}
		// Let sessions view handle esc for its own input/filter modes
		if a.currentView == viewSessions {
			if a.sessionsView.HasActiveInput() || a.sessionsView.HasActiveFilter() {
				cmd := a.sessionsView.Update(msg)
				return a, cmd
			}
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
		// Enter in sessions view -> resume
		if a.currentView == viewSessions {
			cmd := a.sessionsView.Update(msg)
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
	case viewSessions:
		cmd := a.sessionsView.Update(msg)
		return a, cmd
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

// parserForProvider returns a TraceParser function that checks the OTEL store
// first (if receiver is enabled and has data), then falls back to the provider's
// file-based ParseTrace.
func (a App) parserForProvider(p provider.Provider) views.TraceParser {
	return func(filePath string) ([]trace.Turn, error) {
		// File-based parsing for display (has full response text).
		// OTEL receiver still collects data for :export-otel to MLflow/Jaeger.
		if filePath != "" {
			turns, err := p.ParseTrace(filePath)
			if err == nil && len(turns) > 0 {
				// For :new launches, filter out turns from before the launch
				// (Gemini's logs.json accumulates across sessions in same dir)
				if !a.splitLaunchTime.IsZero() {
					var filtered []trace.Turn
					for _, t := range turns {
						// Skip turns from before launch: either explicitly old
						// or missing timestamp (unparsed entries from old sessions)
						if t.Timestamp.IsZero() || t.Timestamp.Before(a.splitLaunchTime) {
							continue
						}
						filtered = append(filtered, t)
					}
					if len(filtered) > 0 {
						// Re-number turns
						for i := range filtered {
							filtered[i].Number = i + 1
						}
						return filtered, nil
					}
					// All turns are old, fall through to OTEL
				} else {
					return turns, nil
				}
			}
		}

		// Fall back to OTEL when file isn't available yet
		// (newly launched sessions before session file is created)
		if a.otelStore != nil && a.otelStore.HasData() {
			var sessionIDs []string

			if selected := a.agentsView.Selected(); selected != nil && selected.SessionID != "" {
				sessionIDs = append(sessionIDs, selected.SessionID)
			}
			if a.sessionView != nil && a.sessionView.Agent() != nil {
				ag := a.sessionView.Agent()
				if ag.SessionID != "" {
					sessionIDs = append(sessionIDs, ag.SessionID)
				}
				if ag.TMuxSession != "" {
					sessionIDs = append(sessionIDs, ag.TMuxSession)
				}
			}

			for _, id := range sessionIDs {
				if root := a.otelStore.GetByConversation(id); root != nil {
					turns := aimuxotel.SpansToTurns(root)
					if len(turns) > 0 {
						return turns, nil
					}
				}
			}
		}
		return nil, nil
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
	case "logs", "traces":
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

	// Build provider options from registered providers
	providerOpts := make(map[string]views.ProviderOptions)
	for _, p := range a.providers {
		sa := p.SpawnArgs()
		providerOpts[p.Name()] = views.ProviderOptions{
			Models: sa.Models,
			Modes:  sa.Modes,
		}
	}

	a.launcherView = views.NewLauncherView(entries, providerOpts, a.cfg.OTELReceiver.Enabled)
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

	// Build OTEL env prefix for the provider (used by both PTY and tmux paths)
	otelEnvPrefix := ""
	if endpoint := a.cfg.OTELEndpoint(); endpoint != "" {
		otelEnvPrefix = p.OTELEnv(endpoint)
	}

	// Pick backend: direct PTY for embeddable providers, tmux mirror for others
	var backend terminal.SessionBackend
	if p.CanEmbed() {
		// Inject OTEL env vars into the command's environment
		if otelEnvPrefix != "" {
			cmd.Env = otelEnvForCmd(cmd, otelEnvPrefix)
		}
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
			backend, err = terminal.StartTmux(cmd, contentW, contentH, a.cfg.ResolveShell(), otelEnvPrefix)
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
		a.splitTrace.SetSessionCost(selected.EstCostUSD)
		a.splitTrace.SetSize(leftW, a.height-1)
		if selected.ProviderName == "gemini" {
			a.splitTrace.SetWarning("Gemini traces only include user prompts (no assistant responses or tool calls)")
		}

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
	ctx := a.buildExportContext()
	if ctx.SessionID == "" || len(ctx.Turns) == 0 {
		a.statusHint = "Open a trace first (l on an agent or Enter for split view), then :export"
		return a, nil
	}

	result, err := a.ctrl.ExportJSONL(ctx)
	if err != nil {
		a.statusHint = fmt.Sprintf("Export failed: %v", err)
		a.stickyHint = true
		return a, nil
	}

	a.statusHint = fmt.Sprintf("Exported %d turns to %s (press any key to dismiss)", result.Count, result.Path)
	a.stickyHint = true
	return a, nil
}

// exportOTEL sends the current trace + annotations as OTLP/HTTP spans to
// the configured export endpoint (e.g., MLflow, Jaeger).
func (a App) exportOTEL() (tea.Model, tea.Cmd) {
	ctx := a.buildExportContext()
	if ctx.SessionID == "" || len(ctx.Turns) == 0 {
		a.statusHint = "Open a trace first (l on an agent or Enter for split view), then :export-otel"
		return a, nil
	}

	result, err := a.ctrl.ExportOTEL(ctx)
	if err != nil {
		a.statusHint = fmt.Sprintf("OTEL export failed: %v", err)
		a.stickyHint = true
		return a, nil
	}

	a.statusHint = fmt.Sprintf("Exported %d turns to %s (press any key to dismiss)", result.Count, result.Path)
	a.stickyHint = true
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

// resumeSession opens a past session in split view: trace on left, live Claude on right.
// Mirrors handleEnter() but builds the command from session history instead of a running agent.
func (a App) resumeSession(sessionID, workingDir, sessionFilePath string) (tea.Model, tea.Cmd) {
	claudeBin := "claude"
	if path, err := exec.LookPath("claude"); err == nil {
		claudeBin = path
	}

	cmd := exec.Command(claudeBin, "--resume", sessionID)
	if workingDir != "" {
		if info, err := os.Stat(workingDir); err == nil && info.IsDir() {
			cmd.Dir = workingDir
		} else {
			a.statusHint = "Cannot resolve project directory for resume"
			return a, nil
		}
	}

	// Size the session view for the right half
	rightW := a.width * 60 / 100
	a.sessionView.SetSize(rightW, a.height)

	// Start embedded PTY (Claude supports embedding)
	sess, err := terminal.Start(cmd)
	if err != nil {
		a.statusHint = fmt.Sprintf("Resume failed: %v", err)
		return a, nil
	}

	// Build a minimal agent for the session view
	resumeAgent := &agent.Agent{
		ProviderName: "claude",
		SessionID:    sessionID,
		WorkingDir:   workingDir,
	}

	teaCmd, err := a.sessionView.Open(resumeAgent, sess)
	if err != nil {
		a.statusHint = fmt.Sprintf("Error opening session: %v", err)
		return a, nil
	}

	// Create trace pane on the left from the session file
	if sessionFilePath != "" {
		leftW := a.width - rightW
		claudeProvider := a.providerFor("claude")
		var parser func(string) ([]trace.Turn, error)
		if claudeProvider != nil {
			parser = claudeProvider.ParseTrace
		}
		a.splitTrace = views.NewLogsView(0, sessionFilePath, parser)
		a.splitTrace.SetSize(leftW, a.height-1)

		// Load existing annotations
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
	a.splitFocus = "session" // start with focus on the live session (right)
	a.layout.SetZoomed(true)
	return a, teaCmd
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
			err := a.ctrl.KillAgent(target)
			if err != nil {
				a.statusHint = fmt.Sprintf("Kill failed: %v", err)
			} else {
				// Also hide so the idle session file doesn't reappear
				a.hideAgent(target)
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


// openLogsForAgent opens the trace viewer for a specific agent and session file.
// Used for non-Claude providers where embedding a PTY isn't possible.
func (a App) openLogsForAgent(ag *agent.Agent, sessionFile string) (tea.Model, tea.Cmd) {
	p := a.providerFor(ag.ProviderName)
	var parser views.TraceParser
	if p != nil {
		parser = a.parserForProvider(p)
	}
	a.logsView = views.NewLogsView(ag.PID, sessionFile, parser)
	a.logsView.SetSessionCost(ag.EstCostUSD)
	if ag.ProviderName == "gemini" {
		a.logsView.SetWarning("Gemini traces only include user prompts (no assistant responses or tool calls)")
	}
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
	a.statusHint = "J:jump  a:annotate  N:note  :export  :export-otel"
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
	a.logsView.SetSessionCost(selected.EstCostUSD)
	if selected.ProviderName == "gemini" {
		a.logsView.SetWarning("Gemini traces only include user prompts (no assistant responses or tool calls)")
	}
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
	a.ctrl.Nav.NavigateTo(controller.ViewType(v), label)
	a.currentView = v
	a.breadcrumbs = a.ctrl.Nav.Breadcrumbs
	a.headerView.SetCrumbs(a.breadcrumbs)
	return a, nil
}

// openSessions discovers past sessions and navigates to the sessions browser.
func (a App) openSessions() (tea.Model, tea.Cmd) {
	// Determine scope directory from selected agent (if any)
	dir := ""
	if sel := a.agentsView.Selected(); sel != nil {
		dir = sel.WorkingDir
	}
	a.sessionsView.SetCurrentDir(dir)

	// Set up trace parser (use Claude's parser as default)
	for _, p := range a.providers {
		if p.Name() == "claude" {
			a.sessionsView.SetTraceParser(p.ParseTrace)
			break
		}
	}

	// Discover sessions in background
	opts := history.DiscoverOpts{Dir: dir}
	sessions, _ := history.Discover(opts, "")
	a.sessionsView.SetSessions(sessions)
	a.sessionsView.SetTagVocab(history.CollectTags(""))

	return a.navigateTo(viewSessions, "Sessions")
}

func (a App) navigateBack() (tea.Model, tea.Cmd) {
	a.ctrl.Nav.NavigateBack()
	a.currentView = viewType(a.ctrl.Nav.CurrentView)
	a.breadcrumbs = a.ctrl.Nav.Breadcrumbs
	a.headerView.SetCrumbs(a.breadcrumbs)
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
		a.headerView.SetHint("Enter:open  t:traces  c:costs  T:teams  S:sessions  :new:launch  x:kill  s:sort  /:filter  ?:help")
	case viewLogs:
		a.headerView.SetHint("j/k:scroll  Enter:expand  a:annotate  N:note  :export  :export-otel  Esc:back")
	case viewCosts:
		a.headerView.SetHint("Esc:back  ?:help")
	case viewTeams:
		a.headerView.SetHint("Esc:back  ?:help")
	case viewSessions:
		a.headerView.SetHint("j/k:nav  Enter:resume  s:sort  /:filter  A:all  a:annotate  f:failure-mode  N:note  d:delete  D:cleanup  p:preview  Esc:back")
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
	case viewSessions:
		a.sessionsView.SetSize(a.width, contentHeight)
		content = a.sessionsView.View()
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
	// Show data source indicator in trace header
	traceLabel := " TRACE [FILE] "
	if a.otelReceiver != nil {
		_, logs, _ := a.otelReceiver.Stats()
		if logs > 0 {
			traceLabel = fmt.Sprintf(" TRACE [FILE] (otel:%d) ", logs)
		}
	}
	traceHeader := traceHeaderStyle.Render(padRight(traceLabel, leftW))
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
		Render(" aimux ")
	focus := a.splitFocus
	hintStyle := lipgloss.NewStyle().Foreground(colorMuted)
	var focusHint string
	if a.statusHint != "" {
		// Show export menu or other status messages
		focusHint = " " + a.statusHint
	} else if a.commandMode {
		focusHint = " :" + a.commandInput + "█"
	} else if focus == "trace" && a.splitTrace != nil && a.splitTrace.NoteMode() {
		noteText, noteTurn := a.splitTrace.NoteInput()
		noteStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true)
		focusHint = noteStyle.Render(fmt.Sprintf(" Note [Turn %d]: ", noteTurn)) + noteText + noteStyle.Render("|")
	} else if focus == "trace" {
		focusHint = " [TRACE] j/k:turns  a:annotate  N:note  e:export"
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
		hintColor := colorWaiting
		if strings.Contains(a.statusHint, "failed") || strings.Contains(a.statusHint, "Error") {
			hintColor = lipgloss.Color("#EF4444") // red for errors
		}
		hints = " " + lipgloss.NewStyle().Foreground(hintColor).Bold(true).Render(a.statusHint)
	} else if a.currentView == viewLogs {
		hints = " j/k:turns  Enter:expand  a:annotate  N:note  /:filter  :export  :export-otel  Esc:back"
	} else if a.currentView == viewSessions {
		hints = " j/k:nav  Enter:resume  s:sort  /:filter  A:all  a:annotate  f:failure-mode  N:note  d:delete  D:cleanup  p:preview  Esc:back"
		if a.sessionsView.HasActiveFilter() {
			hints += "  [Esc clears filter]"
		}
	} else {
		// Show group hint if selected agent is grouped
		selected := a.agentsView.Selected()
		if selected != nil && selected.GroupCount > 1 {
			hints = fmt.Sprintf(" x%d = %d grouped  Enter:open  t:traces  c:costs  T:teams  S:sessions  x:kill  ?:help",
				selected.GroupCount, selected.GroupCount)
		} else {
			hints = " j/k:nav  Enter:open  t:traces  c:costs  T:teams  S:sessions  s:sort  ?:help  q:quit"
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

// activeTraceTurns returns turns from whichever trace view is active:
// standalone logs view (via `l`) or split trace pane (via Enter).
func (a App) activeTraceTurns() []trace.Turn {
	if a.logsView != nil {
		return a.logsView.Turns()
	}
	if a.splitTrace != nil {
		return a.splitTrace.Turns()
	}
	return nil
}

// buildExportContext assembles an ExportContext from the current TUI state.
// This is the bridge between TUI-specific state and UI-agnostic controller logic.
func (a App) buildExportContext() controller.ExportContext {
	turns := a.activeTraceTurns()
	providerName := ""
	if selected := a.agentsView.Selected(); selected != nil {
		providerName = selected.ProviderName
	}
	if providerName == "" && a.sessionView != nil && a.sessionView.Agent() != nil {
		providerName = a.sessionView.Agent().ProviderName
	}

	return controller.ExportContext{
		SessionID:    a.activeTraceSessionID(),
		SessionFile:  a.activeTraceFilePath(),
		ProviderName: providerName,
		Turns:        controller.TurnsToInputs(turns),
		EvalStore:    a.evalStore,
	}
}

// activeTraceFilePath returns the session file path for the active trace context.
func (a App) activeTraceFilePath() string {
	if a.logsView != nil {
		return a.logsView.FilePath()
	}
	if a.splitTrace != nil {
		return a.splitTrace.FilePath()
	}
	if a.sessionView != nil && a.sessionView.Agent() != nil {
		return a.sessionView.Agent().SessionFile
	}
	return ""
}

// activeTraceSessionID returns the session ID for the active trace context.
func (a App) activeTraceSessionID() string {
	if a.evalSessionID != "" {
		return a.evalSessionID
	}
	// Derive from session view agent in split mode
	if a.sessionView != nil && a.sessionView.Agent() != nil {
		ag := a.sessionView.Agent()
		if ag.SessionID != "" {
			return ag.SessionID
		}
		return fmt.Sprintf("pid-%d", ag.PID)
	}
	return ""
}

// otelEnvForCmd merges OTEL env vars (from the provider's OTELEnv shell prefix)
// into a cmd.Env slice suitable for exec.Cmd. Starts from the current process
// environment so the child inherits everything else.
func otelEnvForCmd(cmd *exec.Cmd, shellPrefix string) []string {
	env := os.Environ()
	if cmd.Env != nil {
		env = cmd.Env
	}
	// Parse "KEY=value KEY2=value2 " shell-style prefix into individual vars
	for _, part := range strings.Fields(shellPrefix) {
		if strings.Contains(part, "=") {
			env = append(env, part)
		}
	}
	return env
}

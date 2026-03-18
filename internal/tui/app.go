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
	"github.com/zanetworker/aimux/internal/debuglog"
	"github.com/zanetworker/aimux/internal/correlator"
	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/evaluation"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/jump"
	"github.com/zanetworker/aimux/internal/provider"
	aimuxotel "github.com/zanetworker/aimux/internal/otel"
	"github.com/zanetworker/aimux/internal/spawn"
	"github.com/zanetworker/aimux/internal/subagent"
	"github.com/zanetworker/aimux/internal/task"
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
	viewTasks
)

// tickMsg triggers periodic refresh.
type tickMsg time.Time

// instancesMsg carries discovered instances.
type instancesMsg []agent.Agent

// teamsMsg carries team configs.
type teamsMsg []team.TeamConfig

// k8sSessionReadyMsg is sent when a remote session pod is ready for attachment.
type k8sSessionReadyMsg struct {
	podName   string
	namespace string
	provider  string
	err       error
}

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

	// New picker overlay (:new command)
	newPickerActive bool
	newPicker       *views.NewPickerView

	// Tasks view
	tasksView *views.TasksView

	// K8s provider: stored separately from polling providers.
	// Only queried on-demand (tasks view, :new spawn) — never on every tick.
	k8sProvider *provider.K8s

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

	// K8s provider participates in discovery (agents table) but is also
	// stored separately for on-demand operations (spawn, tasks, health check).
	// Performance is safe: circuit breaker (30s cooldown), connection pool,
	// and 1s timeouts prevent Redis from blocking the UI.
	var k8sProv *provider.K8s
	if cfg.Kubernetes.IsActive() {
		k8sProv = provider.NewK8s(provider.K8sConfig{
			RedisURL:   cfg.Kubernetes.RedisURL,
			TeamID:     cfg.Kubernetes.TeamID,
			Namespace:  cfg.Kubernetes.Namespace,
			Kubeconfig: cfg.Kubernetes.Kubeconfig,
		})
		allProviders = append(allProviders, k8sProv)
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
		tasksView:    views.NewTasksView(),
		layout:       NewLayout(0, 0),
		orchestrator: discovery.NewOrchestrator(agentProviders...),
		providers:    providers,
		breadcrumbs:  []string{"Agents"},
		hiddenAgents: make(map[string]bool),
		cfg:          cfg,
		ctrl:         ctrl,
		otelStore:    aimuxotel.NewSpanStore(),
		k8sProvider:  k8sProv,
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

		// Update K8s status in header
		if a.k8sProvider != nil {
			a.headerView.SetK8sStatus(a.k8sProvider.Status())
		}

		// Refresh tasks only when viewing the tasks tab
		if a.currentView == viewTasks {
			a.refreshTasks()
		}
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
		debuglog.Log("tui: PTYExitMsg received — exiting zoom")
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
		debuglog.Log("tui: SessionResumeMsg received: id=%q dir=%q file=%q", msg.SessionID, msg.WorkingDir, msg.FilePath)
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

	case views.NewSessionMsg:
		return a.handleNewSession(msg)
	case views.NewTaskMsg:
		return a.handleNewTask(msg)
	case views.NewPickerCancelMsg:
		a.newPickerActive = false
		a.newPicker = nil
		a.statusHint = "Cancelled"
		return a, nil

	case k8sSessionReadyMsg:
		a.stickyHint = false
		if msg.err != nil {
			a.statusHint = fmt.Sprintf("Remote session failed: %v", msg.err)
			return a, nil
		}
		// Build a synthetic agent for the new pod and attach.
		podAgent := &agent.Agent{
			SessionID:    "pod-" + msg.podName,
			Name:         msg.podName,
			ProviderName: msg.provider,
			WorkingDir:   "k8s://" + msg.namespace + "/" + msg.podName,
			Status:       agent.StatusActive,
			Source:       agent.SourceSDK,
		}
		return a.openK8sSession(podAgent)

	case tea.KeyMsg:
		// New picker overlay active — route all keys to it
		if a.newPickerActive && a.newPicker != nil {
			_, cmd := a.newPicker.Update(msg)
			return a, cmd
		}
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
		debuglog.Log("tui: key %q NOT routed to zoomed handler: zoomed=%v sessionView=%v active=%v",
			msg.String(), a.zoomed, a.sessionView != nil, a.sessionView != nil && a.sessionView.Active())
		// Fallback: if zoomed was set to false but session view is still showing,
		// handle Ctrl+] to force exit.
		if msg.String() == "ctrl+]" || msg.String() == "ctrl+g" {
			debuglog.Log("tui: fallback exit key %q (zoomed=%v)", msg.String(), a.zoomed)
			if a.sessionView != nil && a.sessionView.Active() {
				return a.exitZoom()
			}
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

	debuglog.Log("tui: zoomed key received: %q (bytes: %x)", key, []byte(key))

	// Clear status hints on any keypress (e.g., "Launched..." or export result)
	a.statusHint = ""
	a.stickyHint = false

	// Exit keys — always work regardless of mode/focus
	switch key {
	case "ctrl+]", "ctrl+\\", "ctrl+g", "ctrl+q":
		debuglog.Log("tui: exit zoom triggered by key %q", key)
		return a.exitZoom()
	}
	if len(key) == 1 && key[0] == 0x1d {
		debuglog.Log("tui: exit zoom triggered by raw 0x1d")
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
			return a.navigateTo(viewTasks, "Tasks")
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
	case viewTasks:
		if a.tasksView != nil {
			a.tasksView.HandleKey(msg.String())
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

// refreshTasks queries all providers that implement TaskLister and updates
// the tasks view and header summary with the aggregated results.
func (a *App) refreshTasks() {
	var allTasks []task.Task

	// Only query K8s when user is actively viewing tasks — not on every tick.
	if a.k8sProvider != nil && a.currentView == viewTasks {
		tasks, _ := a.k8sProvider.ListTasks()
		allTasks = append(allTasks, tasks...)
	}

	a.tasksView.SetTasks(allTasks)

	// Compute summary counts for the header
	pending, active, completed, failed := 0, 0, 0, 0
	for _, t := range allTasks {
		switch t.Status {
		case task.StatusPending:
			pending++
		case task.StatusInProgress, task.StatusClaimed:
			active++
		case task.StatusCompleted:
			completed++
		case task.StatusFailed, task.StatusDead:
			failed++
		}
	}
	a.headerView.SetTaskSummary(pending, active, completed, failed)
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
		raw := strings.TrimSpace(a.commandInput)
		a.commandMode = false
		a.commandInput = ""
		// Handle commands that take arguments (e.g. "send hello world").
		if strings.HasPrefix(raw, "send ") {
			return a.sendMessageToSelected(strings.TrimPrefix(raw, "send "))
		}
		cmd := resolveCommand(raw)
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
	case "tasks":
		return a.navigateTo(viewTasks, "Tasks")
	case "costs":
		return a.navigateTo(viewCosts, "Costs")
	case "help":
		return a.navigateTo(viewHelp, "Help")
	case "export":
		return a.exportTrace()
	case "kill":
		return a.promptKill()
	case "new":
		return a.openNewPicker()
	case "export-otel":
		return a.exportOTEL()
	case "quit":
		return a, tea.Quit
	}
	return a, nil
}

// sendMessageToSelected sends a message to the currently selected K8s agent
// via its Redis inbox. Only works for providers implementing Messenger.
// Usage: :send <text>
func (a App) sendMessageToSelected(text string) (tea.Model, tea.Cmd) {
	if text == "" {
		a.statusHint = "Usage: :send <message text>"
		return a, nil
	}
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
	m, ok := p.(provider.Messenger)
	if !ok {
		a.statusHint = selected.ProviderName + " does not support messaging"
		return a, nil
	}
	if err := m.SendMessage(selected.SessionID, text); err != nil {
		a.statusHint = "Send failed: " + err.Error()
		return a, nil
	}
	a.statusHint = fmt.Sprintf("Sent to %s: %s", selected.SessionID, text)
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

// openNewPicker shows the :new picker overlay for creating sessions or tasks.
func (a App) openNewPicker() (tea.Model, tea.Cmd) {
	var ps []views.ProviderSupport
	for _, p := range a.providers {
		name := p.Name()
		ps = append(ps, views.ProviderSupport{
			Name:          name,
			LocalSession:  true,
			LocalK8s:      name == "claude",
			RemoteSession: name == "claude",
			RemoteTask:    name == "claude" || name == "gemini",
		})
	}
	// Health check is lazy — runs when user first selects a K8s option,
	// not here. This keeps :new instant.

	a.newPicker = views.NewNewPickerView(views.NewPickerConfig{
		K8sEnabled: a.cfg.Kubernetes.IsActive(),
		Providers:  ps,
	})
	a.newPicker.SetSize(a.width, a.height)
	a.newPickerActive = true
	return a, nil
}

// handleNewSession processes a NewSessionMsg from the :new picker.
// For "local" sessions, it delegates to the existing launcher flow.
// For "remote" sessions, it calls SpawnRemote on the K8s provider.
func (a App) buildRecentDirs() []views.RecentDirEntry {
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
				byPath[rd.Path] = &dirEntry{path: rd.Path, lastUsed: rd.LastUsed, provider: p.Name()}
			}
		}
	}
	sorted := make([]*dirEntry, 0, len(byPath))
	for _, de := range byPath {
		sorted = append(sorted, de)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].lastUsed.After(sorted[j].lastUsed) })
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
		entries = append(entries, views.RecentDirEntry{Path: de.path, Display: display, Provider: de.provider, Age: age})
	}
	return entries
}

func (a *App) dismissPicker() {
	a.newPickerActive = false
	a.newPicker = nil
}

func (a *App) pickerError(msg string) {
	if a.newPicker != nil {
		a.newPicker.SetStatus(msg)
	}
}

func (a App) handleNewSession(msg views.NewSessionMsg) (tea.Model, tea.Cmd) {
	switch msg.Where {
	case "local", "local-k8s":
		// Dismiss picker, then open the full launcher at directory step.
		a.newPickerActive = false
		a.newPicker = nil
		// Build the launcher directly (avoid chained value-receiver copies)
		recentDirs := a.buildRecentDirs()
		providerOpts := make(map[string]views.ProviderOptions)
		for _, p := range a.providers {
			sa := p.SpawnArgs()
			providerOpts[p.Name()] = views.ProviderOptions{
				Models: sa.Models,
				Modes:  sa.Modes,
			}
		}
		a.launcherView = views.NewLauncherView(recentDirs, providerOpts, a.cfg.OTELReceiver.Enabled)
		a.launcherView.SetSize(a.width, a.height)
		a.launcherView.SkipToDirectory(msg.Provider)
		a.launcherActive = true
		return a, nil

	case "remote":
		if a.k8sProvider == nil {
			a.pickerError("K8s not configured — set redis_url in ~/.aimux/config.yaml")
			return a, nil
		}
		a.dismissPicker()
		a.statusHint = fmt.Sprintf("Spawning remote %s session — pod starting...", msg.Provider)
		a.stickyHint = true
		// Run spawn + wait async so the TUI stays responsive.
		k8s := a.k8sProvider
		provName := msg.Provider
		return a, func() tea.Msg {
			podName, namespace, err := k8s.SpawnSession(provName)
			if err != nil {
				return k8sSessionReadyMsg{err: err}
			}
			return k8sSessionReadyMsg{podName: podName, namespace: namespace, provider: provName}
		}

	default:
		a.newPickerActive = false
		a.newPicker = nil
		return a.openLauncher()
	}
}

// handleNewTask processes a NewTaskMsg from the :new picker.
// For "local" tasks, it runs the prompt as a local command.
// For "remote" tasks, it creates a task via TaskLister/Spawner.
func (a App) handleNewTask(msg views.NewTaskMsg) (tea.Model, tea.Cmd) {
	if msg.Prompt == "" {
		a.pickerError("Task prompt cannot be empty")
		return a, nil
	}

	switch msg.Where {
	case "remote":
		if a.k8sProvider == nil {
			a.pickerError("K8s not configured — set redis_url in ~/.aimux/config.yaml")
			return a, nil
		}
		// Lazy health check
		h := a.k8sProvider.CheckHealth()
		if !h.RedisOK {
			a.pickerError("Redis unreachable: " + h.RedisErr)
			return a, nil
		}
		if err := a.k8sProvider.SpawnRemote(msg.Provider, "task", 1); err != nil {
			a.pickerError(fmt.Sprintf("Remote task failed: %v", err))
			return a, nil
		}
		a.dismissPicker()
		a.statusHint = fmt.Sprintf("Created remote task for %s", msg.Provider)
		return a, nil
	default:
		p := a.providerFor(msg.Provider)
		if p == nil {
			a.pickerError(fmt.Sprintf("Unknown provider: %s", msg.Provider))
			return a, nil
		}
		cmd := p.SpawnCommand(".", "", "")
		if cmd == nil {
			a.pickerError(fmt.Sprintf("Provider %s cannot spawn locally", msg.Provider))
			return a, nil
		}
		cmd.Args = append(cmd.Args, "-p", msg.Prompt)
		envPrefix := ""
		if a.cfg.OTELReceiver.Enabled {
			endpoint := fmt.Sprintf("http://localhost:%d", a.cfg.OTELReceiverPort())
			envPrefix = p.OTELEnv(endpoint)
		}
		if err := spawn.Launch(cmd, msg.Provider, ".", "tmux", a.cfg.ResolveShell(), envPrefix); err != nil {
			a.pickerError(fmt.Sprintf("Task launch failed: %v", err))
			return a, nil
		}
		a.dismissPicker()
		a.statusHint = fmt.Sprintf("Launched %s task locally", msg.Provider)
		return a, nil
	}
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

	// K8s session pods: attach via kubectl exec + tmux.
	if strings.HasPrefix(selected.SessionID, "pod-") {
		return a.openK8sSession(selected)
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

// openK8sSession attaches to a K8s session pod via kubectl exec + tmux.
// The pod runs `sleep infinity` with a tmux session named "main" inside.
func (a App) openK8sSession(selected *agent.Agent) (tea.Model, tea.Cmd) {
	// Extract pod name and namespace from SessionID and WorkingDir.
	podName := strings.TrimPrefix(selected.SessionID, "pod-")
	namespace := "agents"
	if parts := strings.SplitN(strings.TrimPrefix(selected.WorkingDir, "k8s://"), "/", 2); len(parts) == 2 {
		namespace = parts[0]
	}

	// K8s sessions are zoomed full-screen (not split), so use full width.
	contentW := a.width
	contentH := a.height - 2
	if contentW < 1 {
		contentW = 80
	}
	if contentH < 1 {
		contentH = 24
	}

	backend, err := terminal.NewKubectlExec(podName, namespace, "", contentW, contentH)
	if err != nil {
		a.statusHint = fmt.Sprintf("kubectl exec failed: %v", err)
		return a, nil
	}

	a.sessionView.SetSize(a.width, a.height)
	teaCmd, err := a.sessionView.Open(selected, backend)
	if err != nil {
		a.statusHint = fmt.Sprintf("Error: %v", err)
		return a, nil
	}

	// Set up the remote session environment and start claude.
	// Forward local Claude auth env vars (Vertex AI, API key, or both)
	// so the pod inherits credentials without prompting for login.
	go func() {
		time.Sleep(1 * time.Second)
		backend.Write([]byte("export TERM=xterm-256color\n"))
		time.Sleep(100 * time.Millisecond)

		// Forward all Claude/Vertex auth-related env vars if set locally.
		// GOOGLE_APPLICATION_CREDENTIALS is skipped — it's a local file
		// path. Mount the credentials as a K8s secret instead and set
		// the env var in the deployment YAML.
		authEnvVars := []string{
			"ANTHROPIC_API_KEY",
			"CLAUDE_CODE_USE_VERTEX",
			"CLOUD_ML_REGION",
			"ANTHROPIC_VERTEX_PROJECT_ID",
			"ANTHROPIC_VERTEX_REGION",
		}
		for _, key := range authEnvVars {
			if val := os.Getenv(key); val != "" {
				backend.Write([]byte(fmt.Sprintf("export %s=%q\n", key, val)))
				time.Sleep(50 * time.Millisecond)
			}
		}
		backend.Write([]byte("cd /workspace && claude\n"))
	}()

	a.zoomed = true
	a.splitMode = false
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
	debuglog.Log("tui: resumeSession start: id=%q dir=%q file=%q", sessionID, workingDir, sessionFilePath)

	claudeBin := "claude"
	if path, err := exec.LookPath("claude"); err == nil {
		claudeBin = path
	}

	cmd := exec.Command(claudeBin, "--resume", sessionID)
	if workingDir != "" {
		if info, err := os.Stat(workingDir); err == nil && info.IsDir() {
			cmd.Dir = workingDir
		} else {
			debuglog.Log("tui: resumeSession: workingDir %q not found", workingDir)
			a.statusHint = "Cannot resolve project directory for resume"
			return a, nil
		}
	}

	// Size the session view for the right half
	rightW := a.width * 60 / 100
	a.sessionView.SetSize(rightW, a.height)

	// Start embedded PTY (Claude supports embedding)
	debuglog.Log("tui: resumeSession: starting PTY for %q", claudeBin)
	sess, err := terminal.Start(cmd)
	if err != nil {
		debuglog.Log("tui: resumeSession: PTY start failed: %v", err)
		a.statusHint = fmt.Sprintf("Resume failed: %v", err)
		return a, nil
	}
	debuglog.Log("tui: resumeSession: PTY started, opening session view")

	// Build a minimal agent for the session view
	resumeAgent := &agent.Agent{
		ProviderName: "claude",
		SessionID:    sessionID,
		WorkingDir:   workingDir,
	}

	teaCmd, err := a.sessionView.Open(resumeAgent, sess)
	if err != nil {
		debuglog.Log("tui: resumeSession: session view open failed: %v", err)
		a.statusHint = fmt.Sprintf("Error opening session: %v", err)
		return a, nil
	}

	// Create trace pane on the left from the session file
	if sessionFilePath != "" {
		debuglog.Log("tui: resumeSession: parsing trace file %q", sessionFilePath)
		leftW := a.width - rightW
		claudeProvider := a.providerFor("claude")
		var parser func(string) ([]trace.Turn, error)
		if claudeProvider != nil {
			parser = claudeProvider.ParseTrace
		}
		a.splitTrace = views.NewLogsView(0, sessionFilePath, parser)
		a.splitTrace.SetSize(leftW, a.height-1)
		debuglog.Log("tui: resumeSession: trace loaded, splitTrace is set")

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
	} else {
		debuglog.Log("tui: resumeSession: no session file, splitTrace will be nil")
	}

	a.zoomed = true
	a.splitMode = true
	a.splitFocus = "session" // start with focus on the live session (right)
	a.layout.SetZoomed(true)
	debuglog.Log("tui: resumeSession complete: zoomed=%v splitMode=%v splitFocus=%q splitTrace=%v", a.zoomed, a.splitMode, a.splitFocus, a.splitTrace != nil)
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
	if strings.HasPrefix(selected.SessionID, "pod-") {
		podName := strings.TrimPrefix(selected.SessionID, "pod-")
		a.statusHint = fmt.Sprintf("Delete pod %s? y:confirm  n:cancel", podName)
	} else if selected.PID == 0 {
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
		if strings.HasPrefix(target.SessionID, "pod-") {
			// K8s session pod: scale down deployment (so it doesn't respawn)
			// and delete the pod. Run async to avoid blocking the TUI.
			podName := strings.TrimPrefix(target.SessionID, "pod-")
			namespace := "agents"
			if parts := strings.SplitN(strings.TrimPrefix(target.WorkingDir, "k8s://"), "/", 2); len(parts) == 2 {
				namespace = parts[0]
			}
			a.hideAgent(target)
			a.statusHint = fmt.Sprintf("Deleting pod %s...", podName)
			k8s := a.k8sProvider
			go func() {
				// Decrement replicas by 1 so the deployment doesn't recreate the pod.
				if k8s != nil {
					_ = k8s.ScaleDownOne(target.ProviderName, "session")
				}
				exec.Command("kubectl", "delete", "pod", podName, "-n", namespace, "--grace-period=3", "--wait=false").Run()
			}()
			return a, nil
		}
		if target.PID == 0 {
			// Session-only: hide from view by adding to hidden set
			a.hideAgent(target)
			a.statusHint = fmt.Sprintf("Removed %s from view", target.ShortProject())
		} else {
			p := a.providerFor(target.ProviderName)
			var err error
			if p != nil {
				err = p.Kill(*target)
			} else {
				err = fmt.Errorf("unknown provider %q", target.ProviderName)
			}
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
	a.tasksView.SetSize(a.width, contentHeight)
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
		a.headerView.SetHint("Enter:open  t:traces  c:costs  T:tasks  S:sessions  :new:launch  x:kill  s:sort  /:filter  ?:help")
	case viewLogs:
		a.headerView.SetHint("j/k:scroll  Enter:expand  a:annotate  N:note  :export  :export-otel  Esc:back")
	case viewCosts:
		a.headerView.SetHint("Esc:back  ?:help")
	case viewTeams:
		a.headerView.SetHint("Esc:back  ?:help")
	case viewTasks:
		a.headerView.SetHint("j/k:nav  g/G:top/bottom  :new:create  Esc:back")
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
	case viewTasks:
		a.tasksView.SetSize(a.width, contentHeight)
		content = a.tasksView.View()
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

	// Overlay the new picker if active
	if a.newPickerActive && a.newPicker != nil {
		a.newPicker.SetSize(a.width, a.height)
		return a.newPicker.View()
	}

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
	} else if a.currentView == viewTasks {
		hints = " j/k:nav  g/G:top/bottom  :new:create  Esc:back"
	} else {
		// Show group hint if selected agent is grouped
		selected := a.agentsView.Selected()
		if selected != nil && selected.GroupCount > 1 {
			hints = fmt.Sprintf(" x%d = %d grouped  Enter:open  t:traces  c:costs  T:tasks  S:sessions  x:kill  ?:help",
				selected.GroupCount, selected.GroupCount)
		} else {
			hints = " j/k:nav  Enter:open  t:traces  c:costs  T:tasks  S:sessions  s:sort  ?:help  q:quit"
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

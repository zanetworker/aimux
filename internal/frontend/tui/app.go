package tui

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"os"
	"os/exec"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/badge"
	"github.com/zanetworker/aimux/internal/cache"
	aimuxcompose "github.com/zanetworker/aimux/internal/compose"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/debuglog"
	"github.com/zanetworker/aimux/internal/correlator"
	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/evaluation"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/jump"
	"github.com/zanetworker/aimux/internal/notify"
	"github.com/zanetworker/aimux/internal/provider"
	aimuxotel "github.com/zanetworker/aimux/internal/otel"
	"github.com/zanetworker/aimux/internal/spawn"
	"github.com/zanetworker/aimux/internal/subagent"
	"github.com/zanetworker/aimux/internal/task"
	"github.com/zanetworker/aimux/internal/team"
	"github.com/zanetworker/aimux/internal/clipboard"
	"github.com/zanetworker/aimux/internal/terminal"
	"github.com/zanetworker/aimux/internal/trace"
	"github.com/zanetworker/aimux/internal/plugin"
	"github.com/zanetworker/aimux/internal/frontend/tui/views"
)

type viewType int

const (
	viewAgents viewType = iota
	viewLogs
	viewCosts
	viewTeams
	viewSessions
	viewStarred
	viewHelp
	viewTasks
	viewHealth
	viewPlugin
)

// tickMsg triggers periodic refresh.
type tickMsg time.Time

// traceRefreshMsg signals that the tailer detected new content in the session file.
type traceRefreshMsg struct{}

// sessionFilePollMsg is a fast-poll tick for discovering the session file
// after launching a new agent. Fires every 200ms for 10s, then stops.
type sessionFilePollMsg struct{ deadline time.Time }

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

// openshellStatusMsg carries the result of an async openshell status check.
type openshellStatusMsg struct{ connected bool }

// remoteLaunchResultMsg is sent when an async remote sandbox launch completes.
type remoteLaunchResultMsg struct {
	provider string
	dir      string
	model    string
	result   *aimuxcompose.LaunchResult
	err      error
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
	sessionsView   *views.SessionsView
	starredView    *views.SessionsView
	cachedSessions []history.Session
	helpView       *views.HelpView
	healthView   *views.HealthView

	// Layout
	layout *Layout
	zoomed bool

	// Split mode: trace (left) + interactive session (right)
	splitMode      bool
	splitFocus     string          // "trace" or "session"
	splitTrace     *views.LogsView // live trace pane in split mode
	splitLaunchTime time.Time      // when :new session was launched (filters old files)
	splitLoading   bool            // true while session is connecting (before first output)

	// Command palette
	commandMode   bool
	commandInput  views.TextInput
	exportConfirm bool // showing export menu
	stickyHint    bool // true = statusHint persists until keypress (not cleared by tick)

	// Preview panel focus (agents dashboard)
	previewFocused  bool   // true when right panel has focus
	previewSection  string // "trace" or "diff" — which section is active in right panel

	// Filter mode
	filterMode  bool
	filterInput views.TextInput

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

	// Tasks view
	tasksView *views.TasksView

	// Plugin system
	pluginExec       *plugin.Executor
	pluginView       *views.PluginTUIView
	pluginPicker     *views.PluginPickerView
	pluginPickerMode bool

	// Remote provider (e.g., K8s): stored separately from polling providers.
	// Only queried on-demand (tasks view, :new spawn) — never on every tick.
	// Uses the InfraProvider interface to avoid coupling to a concrete type.
	infraProvider provider.InfraProvider

	// Compose engine for OpenShell sandbox operations
	composeEngine *aimuxcompose.Engine

	// Kill confirmation
	killConfirm  bool            // true when waiting for y/n confirmation
	killTarget   *agent.Agent    // agent to kill
	hiddenAgents map[string]bool // session IDs hidden from view (session-only entries removed by user)

	// Auto-archive: hide agents idle beyond the configured threshold
	showArchived  bool // toggle to show/hide archived agents
	archivedCount int  // number of currently archived agents

	// Evaluation: annotation persistence
	evalStore      *evaluation.Store
	evalSessionID  string

	// Notifications
	prevStatuses   map[int]agent.Status // PID -> last known status for transition detection
	silenced       bool                 // TUI-level mute toggle (m key)
	doneTimestamps map[int]time.Time    // PID -> timestamp when agent finished

	// Config
	cfg       config.Config
	ctrl      *controller.Controller
	launchDir string // CWD when aimux was started; used as default session scope

	// OTEL receiver (optional)
	otelReceiver    *aimuxotel.Receiver
	otelStore       *aimuxotel.SpanStore
	lastEnrichTime  time.Time

	// Startup cache: tracks which PIDs came from cache (stale) vs fresh discovery
	staleAgents map[int]bool

	// Pending launched agents: injected immediately on spawn, preserved
	// in the instances list until discovery finds the real process.
	// Keyed by tmux session name. Removed once a discovered agent
	// matches the same tmux session or working dir.
	pendingAgents map[string]agent.Agent

	// remoteSessionIDs maps sandbox name → Claude session UUID, persisted to
	// disk (~/.aimux/remote-sessions.json) so it survives aimux restarts.
	remoteSessionIDs *remoteSessionStore

	// Live trace streaming: tailer watches the session JSONL and signals
	// traceRefresh when new lines are appended.
	activeTailer *trace.Tailer
	traceRefresh chan struct{}
}

// NewApp creates a new root TUI application.
func newOrchestrator(providers []discovery.AgentProvider, cfg config.Config) *discovery.Orchestrator {
	o := discovery.NewOrchestrator(providers...)
	if cfg.Remote.Backend == "openshell" {
		o.EnableRemoteDiscovery()
	}
	return o
}

func NewApp() App {
	cfg, _ := config.Load(config.DefaultPath())

	// Capture launch directory for session scoping and project-local config
	launchDir, _ := os.Getwd()
	if launchDir != "" {
		cfg, _ = config.LoadProject(launchDir, cfg)
	}

	ctrl := controller.New(cfg)

	allProviders := []provider.Provider{
		&provider.Claude{},
		&provider.Codex{},
		&provider.Gemini{},
	}

	// K8s provider participates in discovery (agents table) but is also
	// stored as a InfraProvider for on-demand operations (spawn, tasks,
	// health check). Concrete type used only here; stored via interface.
	var infraProv provider.InfraProvider
	if cfg.Kubernetes.IsActive() {
		k8s := provider.NewK8s(provider.K8sConfig{
			RedisURL:   cfg.Kubernetes.RedisURL,
			TeamID:     cfg.Kubernetes.TeamID,
			Namespace:  cfg.Kubernetes.Namespace,
			Kubeconfig: cfg.Kubernetes.Kubeconfig,
		})
		allProviders = append(allProviders, k8s)
		infraProv = k8s
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

	// Load cached agents for instant first paint
	cachedAgents, _ := cache.Load(cache.DefaultPath())
	staleAgents := make(map[int]bool)
	for _, a := range cachedAgents {
		staleAgents[a.PID] = true
	}

	app := App{
		currentView:  viewAgents,
		headerView:   views.NewHeaderView(),
		agentsView:   views.NewAgentsView(),
		previewPane:  views.NewPreviewPane(),
		sessionView:  views.NewSessionView(),
		costsView:     views.NewCostsView(),
		sessionsView:  views.NewSessionsView(),
		starredView:   views.NewSessionsView(),
		teamsView:    views.NewTeamsView(),
		helpView:     views.NewHelpView(),
		healthView:   views.NewHealthView(),
		tasksView:    views.NewTasksView(),
		layout:       NewLayout(0, 0),
		orchestrator: newOrchestrator(agentProviders, cfg),
		providers:    providers,
		breadcrumbs:    []string{"Agents"},
		hiddenAgents:   make(map[string]bool),
		prevStatuses:   make(map[int]agent.Status),
		doneTimestamps: make(map[int]time.Time),
		cfg:            cfg,
		ctrl:           ctrl,
		launchDir:      launchDir,
		otelStore:      aimuxotel.NewSpanStore(),
		infraProvider:  infraProv,
		instances:      cachedAgents,
		staleAgents:    staleAgents,
		pendingAgents:    make(map[string]agent.Agent),
		remoteSessionIDs: newRemoteSessionStore(aimuxConfigDir()),
		traceRefresh:   make(chan struct{}, 1),
	}

	// Start OTEL receiver if enabled
	if cfg.OTELReceiver.Enabled {
		app.otelReceiver = aimuxotel.NewReceiverWithKeys(app.otelStore, cfg.OTELReceiverPort(), keysByService)
		if cfg.Remote.Backend == "openshell" {
			app.otelReceiver.SetBindAll(true)
		}
		_ = app.otelReceiver.Start()
	}

	// Initialize compose engine for OpenShell sandbox operations
	if cfg.Remote.Backend == "openshell" {
		composeEngine, err := aimuxcompose.New(aimuxcompose.Options{
			Gateway:  cfg.Remote.Gateway,
			Insecure: true, // Remote config doesn't expose insecure flag; default to true
			Image:    cfg.Remote.Image,
		})
		if err == nil {
			app.composeEngine = composeEngine
		}
	}

	// Set cached agents as initial instances and mark them stale in AgentsView
	app.agentsView.SetStalePIDs(staleAgents)
	app.agentsView.SetHourlyRate(cfg.ROI.HourlyRate)

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

func (a App) checkOpenShellStatus() tea.Msg {
	out, err := exec.Command("openshell", "status").CombinedOutput()
	if err != nil {
		return openshellStatusMsg{connected: false}
	}
	return openshellStatusMsg{connected: strings.Contains(strings.ToLower(string(out)), "connected")}
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
		cmds := []tea.Cmd{a.discoverInstances, a.tick()}
		if a.cfg.Remote.Backend == "openshell" {
			cmds = append(cmds, a.checkOpenShellStatus)
		}
		return a, tea.Batch(cmds...)

	case openshellStatusMsg:
		if msg.connected {
			a.headerView.SetOpenShellStatus("connected")
		} else {
			a.headerView.SetOpenShellStatus("disconnected")
		}
		return a, nil

	case instancesMsg:
		a.instances = controller.FilterHidden([]agent.Agent(msg), a.hiddenAgents)

		// Merge pending launched agents that discovery hasn't found yet.
		// Match by TMuxSession OR SandboxName. When a discovered agent
		// matches by SandboxName but lacks TMuxSession, copy it from
		// the pending agent so re-entry works without creating a new session.
		for key, pending := range a.pendingAgents {
			found := false
			for i, discovered := range a.instances {
				matchTmux := discovered.TMuxSession != "" && discovered.TMuxSession == pending.TMuxSession
				matchSandbox := discovered.SandboxName != "" && discovered.SandboxName == pending.SandboxName
				if matchTmux || matchSandbox {
					if discovered.TMuxSession == "" && pending.TMuxSession != "" {
						a.instances[i].TMuxSession = pending.TMuxSession
					}
					if discovered.SessionID == "" && pending.SessionID != "" {
						a.instances[i].SessionID = pending.SessionID
					}
					found = true
					break
				}
			}
			if found {
				delete(a.pendingAgents, key)
			} else {
				a.instances = append(a.instances, pending)
			}
		}

		// Clean up orphaned aimux tmux sessions (from crashed aimux instances)
		liveTmux := make(map[string]bool)
		for _, ag := range a.instances {
			if ag.TMuxSession != "" {
				liveTmux[ag.TMuxSession] = true
			}
		}
		spawn.CleanupOrphanedSessions(liveTmux)

		// Auto-archive idle agents past the configured threshold.
		threshold := a.cfg.ArchiveThreshold()
		if threshold > 0 && !a.showArchived {
			active, archived := controller.PartitionByArchive(a.instances, threshold)
			a.instances = active
			a.archivedCount = len(archived)
		} else {
			a.archivedCount = 0
		}

		if a.otelStore.LastUpdate().After(a.lastEnrichTime) {
			a.instances = correlator.EnrichFromOTEL(a.instances, a.otelStore)
			a.lastEnrichTime = a.otelStore.LastUpdate()
		}

		// Evaluate configurable badges for each agent.
		if len(a.cfg.Badges) > 0 {
			rules := make([]badge.Rule, len(a.cfg.Badges))
			for i, b := range a.cfg.Badges {
				rules[i] = badge.Rule{Path: b.Path, JSONPath: b.JSONPath, Label: b.Label, Color: b.Color}
			}
			for i := range a.instances {
				if a.instances[i].WorkingDir != "" {
					badges := badge.Evaluate(a.instances[i].WorkingDir, rules)
					a.instances[i].Badges = make([]agent.BadgeValue, len(badges))
					for j, b := range badges {
						a.instances[i].Badges[j] = agent.BadgeValue{
							Label: b.Label, Value: b.Value, Color: b.Color,
						}
					}
				}
			}
		}

		// Save cache on discovery refresh
		go func() {
			if err := cache.Save(cache.DefaultPath(), []agent.Agent(msg)); err != nil {
				debuglog.Log("cache save failed: %v", err)
			}
		}()
		a.staleAgents = make(map[int]bool)
		a.agentsView.SetStalePIDs(a.staleAgents)

		now := time.Now()
		// Detect state transitions, fire notifications, and ring bell
		for _, inst := range a.instances {
			prev, known := a.prevStatuses[inst.PID]
			if known && prev != inst.Status {
				// Track Active → Idle transitions (agent finished)
				if prev == agent.StatusActive && inst.Status == agent.StatusIdle {
					a.doneTimestamps[inst.PID] = now
				}

				// Fire notifications
				if !a.silenced && a.cfg.Notifications.Enabled {
					a.maybeNotify(inst)
				}

				// Ring terminal bell for attention events
				shouldBell := false
				switch {
				case prev == agent.StatusActive && inst.Status == agent.StatusWaitingPermission:
					shouldBell = true
				case prev == agent.StatusActive && inst.Status == agent.StatusIdle:
					shouldBell = true
				case inst.Status == agent.StatusError && prev != agent.StatusError:
					shouldBell = true
				}
				if shouldBell && !a.silenced && a.cfg.Notifications.Bell {
					fmt.Print("\a")
				}
			}
		}

		// Update previous statuses
		a.prevStatuses = make(map[int]agent.Status, len(a.instances))
		for _, inst := range a.instances {
			a.prevStatuses[inst.PID] = inst.Status
		}

		// Calculate attention count: waiting + recently done (last 60s)
		attention := 0
		for _, inst := range a.instances {
			if inst.Status == agent.StatusWaitingPermission {
				attention++
			}
		}
		// Add recently-done agents (within 60 seconds)
		for pid, doneAt := range a.doneTimestamps {
			if now.Sub(doneAt) < 60*time.Second {
				attention++
			} else {
				delete(a.doneTimestamps, pid)
			}
		}
		a.headerView.SetAttentionCount(attention)

		a.agentsView.SetAgents(a.instances)
		starredMap := make(map[string]bool)
		for _, ag := range a.instances {
			if ag.SessionFile != "" {
				meta := history.LoadMeta(ag.SessionFile)
				if meta.Starred {
					starredMap[ag.SessionFile] = true
				}
			}
		}
		a.agentsView.SetStarredFiles(starredMap)
		a.headerView.SetAgents(a.instances)
		a.headerView.SetSilenced(a.silenced)
		a.costsView.SetAgents(a.instances)

		// Update infra status in header
		if a.infraProvider != nil {
			a.headerView.SetK8sStatus(a.infraProvider.Status())
		}
		// OpenShell status check is dispatched as an async command
		// so it never blocks the TUI event loop.

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
							if a.activeTailer == nil {
								a.activeTailer = startTraceTailer(sf, a.traceRefresh)
							}
							a.splitTrace.Reload()
							if a.activeTailer != nil {
								return a, tea.Batch(a.waitForTraceRefresh())
							}
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

	case traceRefreshMsg:
		if a.splitMode && a.splitTrace != nil {
			a.splitTrace.Reload()
		}
		if a.activeTailer != nil {
			return a, a.waitForTraceRefresh()
		}
		return a, nil

	case sessionFilePollMsg:
		if !a.splitMode || a.splitTrace == nil || a.splitTrace.FilePath() != "" {
			return a, nil
		}
		if time.Now().After(msg.deadline) {
			return a, nil
		}
		if a.sessionView != nil && a.sessionView.Agent() != nil {
			ag := a.sessionView.Agent()
			if p := a.providerFor(ag.ProviderName); p != nil {
				if sf := p.FindSessionFile(*ag); sf != "" {
					if !a.splitLaunchTime.IsZero() {
						if info, err := os.Stat(sf); err == nil && info.ModTime().Before(a.splitLaunchTime) {
							sf = ""
						}
					}
					if sf != "" {
						a.splitTrace.SetFilePath(sf)
						if a.activeTailer == nil {
							a.activeTailer = startTraceTailer(sf, a.traceRefresh)
						}
						a.splitTrace.Reload()
						if a.activeTailer != nil {
							return a, a.waitForTraceRefresh()
						}
						return a, nil
					}
				}
			}
		}
		return a, a.pollSessionFile(msg.deadline)

	case views.LaunchResumeMsg:
		a.launcherActive = false
		a.launcherView = nil
		return a.resumeSession(msg.SessionID, msg.Dir, msg.FilePath, a.cfg.DefaultMode)
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
		shell := msg.Shell
		if shell == "" {
			shell = a.cfg.ResolveShell()
		}
		sessionMgr := msg.SessionManager
		if sessionMgr == "" {
			sessionMgr = "tmux"
		}
		if msg.Runtime == "remote" {
			// Always inject OTEL for remote sessions since file-based tracing
			// doesn't work (no local session file).
			otelPort := a.cfg.OTELReceiverPort()
			otelEndpoint := fmt.Sprintf("http://localhost:%d", otelPort)
			debuglog.Log("remote launch: otel_enabled=%v port=%d endpoint=%s",
				a.cfg.OTELReceiver.Enabled, otelPort, otelEndpoint)

			// Show loading state immediately so user sees feedback
			a.splitMode = true
			a.zoomed = true
			a.splitLoading = true
			a.layout.SetZoomed(true)
			a.statusHint = fmt.Sprintf("Launching %s remotely...", msg.Provider)

			sOpts := aimuxcompose.LaunchOpts{
				Image:        a.cfg.Remote.Image,
				OTELEndpoint: otelEndpoint,
			}

			// Run launch async so the TUI stays responsive
			provider, dir, model := msg.Provider, msg.Dir, msg.Model
			return a, func() tea.Msg {
				result, err := a.composeEngine.LaunchInSandbox(provider, dir, sOpts)
				return remoteLaunchResultMsg{
					provider: provider,
					dir:      dir,
					model:    model,
					result:   result,
					err:      err,
				}
			}
		} else if msg.Runtime == "container" {
			cOpts := spawn.ContainerOpts{}
			for _, rt := range a.cfg.Runtimes {
				if rt.Type == "container" {
					cOpts.Engine = rt.Engine
					cOpts.Image = rt.Image
					break
				}
			}
			if err := spawn.LaunchInContainer(cmd, msg.Provider, msg.Dir, shell, envPrefix, cOpts); err != nil {
				a.statusHint = fmt.Sprintf("Container launch failed: %v", err)
				return a, nil
			}
		} else {
			if err := spawn.Launch(cmd, msg.Provider, msg.Dir, sessionMgr, shell, envPrefix); err != nil {
				a.statusHint = fmt.Sprintf("Launch failed: %v", err)
				return a, nil
			}
		}

		name := filepath.Base(msg.Dir)

		// Immediately open split view (both local and container use tmux)
		if msg.Runtime == "local" || msg.Runtime == "container" {
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

			newAgent := &agent.Agent{
				Name:         name,
				ProviderName: msg.Provider,
				WorkingDir:   msg.Dir,
				TMuxSession:  tmuxName,
				Status:       agent.StatusActive,
				Model:        msg.Model,
				StartTime:    time.Now(),
				LastActivity: time.Now(),
				GroupCount:    1,
				GroupPIDs:     []int{},
			}

			// Track as pending so it survives instancesMsg replacements
			// until discovery finds the real process.
			a.pendingAgents[tmuxName] = *newAgent
			a.instances = append(a.instances, *newAgent)
			a.agentsView.SetAgents(a.instances)

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
			a.splitMode = true
			a.splitFocus = "session"
			a.splitLoading = true
			a.layout.SetZoomed(true)
			a.statusHint = fmt.Sprintf("Launched %s in %s", msg.Provider, name)
			pollDeadline := time.Now().Add(10 * time.Second)
			return a, tea.Batch(teaCmd, a.pollSessionFile(pollDeadline))
		}

		a.statusHint = fmt.Sprintf("Launched %s in %s (%s)", msg.Provider, name, msg.Runtime)
		return a, nil

	case views.LaunchCancelMsg:
		a.launcherActive = false
		a.launcherView = nil
		a.statusHint = "Launch cancelled"
		return a, nil

	case views.PTYOutputMsg:
		if a.splitLoading {
			a.splitLoading = false
		}
		if a.sessionView != nil {
			cmd := a.sessionView.HandleOutput(msg.Data)
			return a, cmd
		}
		return a, nil

	case views.PTYExitMsg:
		debuglog.Log("tui: PTYExitMsg received — exiting zoom")
		a.stopActiveTailer()
		a.zoomed = false
		a.splitMode = false
		a.splitTrace = nil
		a.splitLoading = false
		a.layout.SetZoomed(false)
		if a.sessionView != nil {
			agentName := ""
			if ag := a.sessionView.Agent(); ag != nil {
				agentName = ag.ShortProject()
			}
			a.sessionView.Close()
			a.statusHint = fmt.Sprintf("Session ended: %s", agentName)
			a.stickyHint = true
		}
		return a, nil

	case views.SessionTogglePermsMsg:
		if a.sessionView == nil || !a.sessionView.Active() {
			return a, nil
		}
		ag := a.sessionView.Agent()
		if ag == nil || ag.SessionID == "" {
			a.statusHint = "Cannot toggle: no session ID"
			return a, nil
		}
		newMode := controller.ToggleBypass(a.sessionView.PermMode())
		a.sessionView.Close()
		sessionFile := ""
		if a.splitTrace != nil {
			sessionFile = a.splitTrace.FilePath()
		}
		return a.resumeSession(ag.SessionID, ag.WorkingDir, sessionFile, newMode)

	case views.SessionToggleScopeMsg:
		dir := ""
		if !msg.ShowAll {
			dir = a.sessionsView.CurrentDir()
		}
		sessions, _ := history.Discover(history.DiscoverOpts{Dir: dir}, "")
		a.cachedSessions = sessions
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
	case views.SessionStarMsg:
		meta := history.LoadMeta(msg.Session.FilePath)
		meta.Starred = msg.Starred
		_ = history.SaveMeta(msg.Session.FilePath, meta)
		a.cachedSessions = nil
		if msg.Starred {
			a.statusHint = "Session pinned ★"
		} else {
			a.statusHint = "Session unpinned"
		}
	case views.SessionTagMsg:
		meta := history.LoadMeta(msg.Session.FilePath)
		meta.Tags = msg.Tags
		_ = history.SaveMeta(msg.Session.FilePath, meta)
		a.statusHint = fmt.Sprintf("Session: tags updated (%d)", len(msg.Tags))
	case views.SessionGenerateTitlesMsg:
		a.statusHint = "Generating titles..."
		return a, func() tea.Msg {
			cfg := history.TitleConfig{
				Enabled: true,
				Model:   a.cfg.Sessions.TitleModel,
				APIKey:  a.cfg.Sessions.APIKey,
				Output:  io.Discard,
			}
			sessions, _ := history.Discover(history.DiscoverOpts{}, "")
			count, err := history.GenerateTitles(sessions, cfg)
			return views.SessionTitlesGeneratedMsg{Count: count, Err: err}
		}
	case views.SessionTitlesGeneratedMsg:
		a.sessionsView.SetGeneratingTitles(false)
		a.starredView.SetGeneratingTitles(false)
		if msg.Err != nil {
			a.statusHint = fmt.Sprintf("Generated %d titles (stopped: %v)", msg.Count, msg.Err)
		} else {
			a.statusHint = fmt.Sprintf("Generated %d titles", msg.Count)
		}
		a.cachedSessions = nil
		sessions, _ := history.Discover(history.DiscoverOpts{}, "")
		a.cachedSessions = sessions
		a.sessionsView.SetSessions(sessions)
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
	case views.SessionROIMsg:
		meta := history.LoadMeta(msg.Session.FilePath)
		meta.ROIMultiplier = msg.Multiplier
		meta.TaskType = msg.TaskType
		_ = history.SaveMeta(msg.Session.FilePath, meta)
		if msg.Multiplier > 0 {
			a.statusHint = fmt.Sprintf("Session: ROI set to %.1fx", msg.Multiplier)
		} else {
			a.statusHint = "Session: ROI cleared"
		}
	case views.SessionContentSearchResultMsg:
		a.sessionsView.HandleContentSearchResult(msg)
		count := len(msg.Matches)
		if count == 0 {
			a.statusHint = fmt.Sprintf("No sessions match '%s'", msg.Query)
		} else {
			a.statusHint = fmt.Sprintf("Found %d sessions matching '%s'", count, msg.Query)
		}
		return a, nil
	case views.SessionResumeMsg:
		debuglog.Log("tui: SessionResumeMsg received: id=%q dir=%q file=%q mode=%q", msg.SessionID, msg.WorkingDir, msg.FilePath, msg.Mode)
		if msg.SessionID == "" {
			a.statusHint = "No session ID to resume"
			return a, nil
		}
		return a.resumeSession(msg.SessionID, msg.WorkingDir, msg.FilePath, msg.Mode)
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
					tv.ScrollUp(1)
					return a, nil
				case tea.MouseButtonWheelDown:
					tv.ScrollDown(1)
					return a, nil
				}
			}
		}
		return a, nil

	case remoteLaunchResultMsg:
		if msg.err != nil {
			debuglog.Log("remote launch FAILED: %v", msg.err)
			a.splitLoading = false
			a.splitMode = false
			a.zoomed = false
			a.layout.SetZoomed(false)
			a.statusHint = fmt.Sprintf("Remote launch failed: %v", msg.err)
			return a, nil
		}

		result := msg.result
		debuglog.Log("remote launch OK: sandbox=%s otel=%s", result.SandboxName, result.OTELSessionID)

		name := filepath.Base(msg.dir)
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

		// Establish the interactive terminal by running "openshell sandbox
		// connect" in a real PTY (no tmux). Gateway is resolved by the
		// openshell CLI's own config, matching how create/delete work.
		backend, err := terminal.NewOpenShellExec(result.SandboxName, "", false, contentW, contentH)
		if err != nil {
			debuglog.Log("remote launch: openshell connect FAILED for %s: %v", result.SandboxName, err)
			a.splitLoading = false
			a.statusHint = fmt.Sprintf("Launched %s in %s but connect failed: %v", msg.provider, name, err)
			return a, nil
		}

		newAgent := &agent.Agent{
			Name:         result.SandboxName,
			ProviderName: msg.provider,
			WorkingDir:   msg.dir,
			SessionID:    result.OTELSessionID,
			Status:       agent.StatusActive,
			Model:        msg.model,
			StartTime:    time.Now(),
			LastActivity: time.Now(),
			GroupCount:   1,
			GroupPIDs:    []int{},
			Location:     "remote",
			SandboxName:  result.SandboxName,
		}

		a.pendingAgents[result.SandboxName] = *newAgent
		a.remoteSessionIDs.Put(result.SandboxName, result.OTELSessionID)
		a.instances = append(a.instances, *newAgent)
		a.agentsView.SetAgents(a.instances)

		teaCmd, err := a.sessionView.Open(newAgent, backend)
		if err != nil {
			a.statusHint = fmt.Sprintf("Launched %s remotely in %s", msg.provider, name)
			return a, nil
		}

		// Auto-start the agent inside the sandbox once the shell is ready,
		// pinning the Claude session id to the OTEL session id for trace
		// continuity across reconnects.
		go sendAgentCommand(backend, remoteAgentCommand(msg.provider, result.OTELSessionID, false))

		leftW := a.width - rightW - 1
		a.splitLaunchTime = time.Now()
		a.evalSessionID = result.OTELSessionID

		remoteParser := a.parserForRemote(result.OTELSessionID, result.SandboxName)
		a.splitTrace = views.NewLogsView(0, "", remoteParser)
		a.splitTrace.SetSize(leftW, a.height-1)

		a.zoomed = true
		a.splitMode = true
		a.splitFocus = "session"
		a.splitLoading = true
		a.layout.SetZoomed(true)
		a.statusHint = fmt.Sprintf("Launched %s remotely in sandbox %s", msg.provider, result.SandboxName)
		return a, teaCmd

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
		// Launcher overlay active — route all keys to it
		if a.launcherActive && a.launcherView != nil {
			cmd := a.launcherView.Update(msg)
			return a, cmd
		}
		// Plugin picker overlay
		if a.pluginPickerMode && a.pluginPicker != nil {
			switch msg.String() {
			case "j", "down":
				a.pluginPicker.CursorDown()
			case "k", "up":
				a.pluginPicker.CursorUp()
			case "enter":
				if sel := a.pluginPicker.Selected(); sel != nil {
					return a.openPlugin(*sel)
				}
			case "esc":
				a.pluginPickerMode = false
				a.statusHint = ""
			}
			return a, nil
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

	// Ctrl+b toggles permission mode: close PTY, relaunch with toggled mode
	if key == "ctrl+b" {
		return a, func() tea.Msg { return views.SessionTogglePermsMsg{} }
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

	// Tab switches focus
	if key == "tab" && a.splitMode {
		if a.splitFocus == "trace" {
			a.splitFocus = "session"
		} else {
			a.splitFocus = "trace"
		}
		return a, nil
	}
	if key == "tab" && a.currentView == viewAgents && !a.commandMode && !a.filterMode {
		a.previewFocused = !a.previewFocused
		if a.previewFocused {
			if a.previewSection == "" {
				a.previewSection = "trace"
			}
			a.statusHint = fmt.Sprintf("Preview [%s] (Tab:back, j/k:scroll, up/down:switch section)", a.previewSection)
		} else {
			a.statusHint = ""
		}
		return a, nil
	}

	// Command palette -- intercept ":" before routing to trace or PTY
	if key == ":" {
		a.commandMode = true
		a.commandInput.Reset()
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
		// Intercept "$" for cost-per-turn toggle
		if key == "$" {
			a.splitTrace.ToggleCostPerTurn()
			return a, nil
		}
		if key == "*" {
			return a.starFromTrace(a.splitTrace.FilePath())
		}
		if key == "C" {
			return a.copySessionIDFromTrace()
		}
		cmd := a.splitTrace.Update(msg)
		return a, cmd
	}

	// Intercept scroll keys in session view
	if tv := a.sessionView.TermView(); tv != nil {
		switch key {
		case "pgup":
			tv.ScrollUp(tv.Height() / 2)
			return a, nil
		case "pgdown":
			tv.ScrollDown(tv.Height() / 2)
			return a, nil
		case "shift+up":
			tv.ScrollUp(1)
			return a, nil
		case "shift+down":
			tv.ScrollDown(1)
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
	a.stopActiveTailer()
	a.zoomed = false
	a.splitMode = false
	a.splitTrace = nil
	a.splitLaunchTime = time.Time{}
	a.splitLoading = false
	a.layout.SetZoomed(false)
	a.sessionView.Close()
	return a, nil
}

// returnToAgentsIfZoomed exits zoom/split mode after a kill and returns to the
// agents list. If the user is already on the agents list, it's a no-op.
func (a App) returnToAgentsIfZoomed() (tea.Model, tea.Cmd) {
	if a.zoomed || a.splitMode || a.splitTrace != nil {
		a.stopActiveTailer()
		a.zoomed = false
		a.splitMode = false
		a.splitTrace = nil
		a.splitLaunchTime = time.Time{}
		a.splitLoading = false
		a.layout.SetZoomed(false)
		a.sessionView.Close()
	}
	a.currentView = viewAgents
	a.stickyHint = true
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
		a.commandInput.Reset()
		return a, nil
	case "/":
		if a.currentView == viewAgents {
			a.filterMode = true
			a.filterInput.Reset()
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
	case "*":
		if a.currentView == viewAgents {
			selected := a.agentsView.Selected()
			if selected != nil && selected.SessionFile != "" {
				starred, err := controller.ToggleStar(selected.SessionFile)
				if err != nil {
					a.statusHint = fmt.Sprintf("Star toggle failed: %v", err)
					return a, nil
				}
				starredMap := make(map[string]bool)
				for _, ag := range a.instances {
					if ag.SessionFile != "" {
						m := history.LoadMeta(ag.SessionFile)
						if m.Starred {
							starredMap[ag.SessionFile] = true
						}
					}
				}
				a.agentsView.SetStarredFiles(starredMap)
				a.cachedSessions = nil
				if starred {
					a.statusHint = "Session pinned ★"
				} else {
					a.statusHint = "Session unpinned"
				}
			}
			return a, nil
		}
		if a.currentView == viewLogs && a.logsView != nil {
			return a.starFromTrace(a.logsView.FilePath())
		}
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
	case "B":
		if a.currentView == viewAgents {
			return a.openStarred()
		}
	case "P":
		if a.currentView == viewAgents {
			return a.openPlugins()
		}
	case "S":
		if a.currentView == viewAgents {
			return a.openSessions()
		}
	case "C":
		if a.currentView == viewAgents {
			return a.copySessionID()
		}
		if a.currentView == viewSessions {
			return a.copySessionIDFromSessions()
		}
		if a.currentView == viewLogs {
			return a.copySessionIDFromTrace()
		}
	case "m":
		if a.currentView == viewAgents {
			a.silenced = !a.silenced
			a.headerView.SetSilenced(a.silenced)
			if a.silenced {
				a.statusHint = "Notifications silenced"
			} else {
				a.statusHint = "Notifications enabled"
			}
			a.stickyHint = false
			return a, nil
		}
	case "o":
		if a.currentView == viewAgents {
			a.showArchived = !a.showArchived
			if a.showArchived {
				a.statusHint = "Showing all agents (including archived)"
			} else {
				a.statusHint = fmt.Sprintf("Hiding %d archived agents", a.archivedCount)
			}
			return a, nil
		}
	case "a":
		if a.currentView == viewAgents {
			idx := controller.NextAttend(a.instances, a.agentsView.Cursor())
			if idx >= 0 {
				a.agentsView.SetCursor(idx)
				ag := a.instances[idx]
				a.statusHint = fmt.Sprintf("Attend: %s (%s)", ag.ShortProject(), ag.Status)
			} else {
				a.statusHint = "No agents need attention"
			}
			return a, nil
		}
	case "H":
		if a.currentView == viewAgents {
			return a.openHealth()
		}
	case "d":
		if a.currentView == viewAgents && a.previewPane != nil {
			if a.previewPane.IsDiffExpanded() {
				a.previewPane.ToggleDiff()
				a.statusHint = "Diff view closed"
			} else if a.previewPane.IsDiffPickerMode() {
				a.previewPane.ToggleDiff()
				a.statusHint = ""
			} else {
				a.previewPane.ToggleDiff()
				if a.previewPane.IsDiffPickerMode() {
					a.statusHint = "Select file (j/k:navigate, Enter:view, Esc:close)"
				} else {
					a.statusHint = "No git changes"
				}
			}
			return a, nil
		}
	case "esc":
		if a.filterInput.Value() != "" {
			a.filterInput.Reset()
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
		if a.currentView == viewStarred {
			if a.starredView.HasActiveInput() || a.starredView.HasActiveFilter() {
				cmd := a.starredView.Update(msg)
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
		// Enter in file diff view -> back to file picker
		if a.currentView == viewAgents && a.previewPane != nil && a.previewPane.IsDiffExpanded() {
			a.previewPane.DiffPickerBack()
			a.statusHint = "Select file (j/k:navigate, Enter:view, Esc:close)"
			return a, nil
		}
		// Enter in diff file picker -> select file
		if a.currentView == viewAgents && a.previewPane != nil && a.previewPane.IsDiffPickerMode() {
			a.previewPane.DiffPickerSelect()
			if a.previewPane.IsDiffExpanded() {
				a.statusHint = "File diff (j/k:scroll, Enter/Esc:back, d:close)"
			}
			return a, nil
		}
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
	case "$":
		if a.currentView == viewLogs && a.logsView != nil {
			a.logsView.ToggleCostPerTurn()
			return a, nil
		}
	}

	// Right panel focused: j/k navigate within current section, boundary crossing switches sections
	if a.currentView == viewAgents && a.previewFocused && a.previewPane != nil {
		switch msg.String() {
		case "j", "down":
			switch a.previewSection {
			case "trace":
				if a.previewPane.TraceIsAtBottom() && a.previewPane.HasDiffChanges() {
					a.previewSection = "diff"
					a.previewPane.ToggleDiff()
					a.statusHint = fmt.Sprintf("Preview [%s] (Tab:back, j/k:scroll)", a.previewSection)
				} else {
					a.previewPane.TraceScrollDown()
				}
			case "diff":
				if a.previewPane.IsDiffPickerMode() {
					a.previewPane.DiffPickerDown()
				} else if a.previewPane.IsDiffExpanded() {
					a.previewPane.ScrollDiff(1)
				}
			}
			return a, nil
		case "k", "up":
			switch a.previewSection {
			case "trace":
				if a.previewPane.TraceIsAtTop() && a.previewPane.HasDiffChanges() {
					a.previewSection = "diff"
					a.previewPane.ToggleDiff()
					a.statusHint = fmt.Sprintf("Preview [%s] (Tab:back, j/k:scroll)", a.previewSection)
				} else {
					a.previewPane.TraceScrollUp()
				}
			case "diff":
				if a.previewPane.IsDiffPickerMode() {
					a.previewPane.DiffPickerUp()
				} else if a.previewPane.IsDiffExpanded() {
					a.previewPane.ScrollDiff(-1)
				}
			}
			return a, nil
		case "enter", " ":
			if a.previewSection == "diff" {
				if a.previewPane.IsDiffExpanded() {
					a.previewPane.DiffPickerBack()
					a.statusHint = "Preview [diff] (j/k:navigate, Enter:view, Esc:back)"
				} else if a.previewPane.IsDiffPickerMode() {
					a.previewPane.DiffPickerSelect()
					if a.previewPane.IsDiffExpanded() {
						a.statusHint = "Preview [diff] (j/k:scroll, Enter/Esc:back)"
					}
				}
				return a, nil
			}
			// In trace section, switch to diff section
			a.previewSection = "diff"
			a.previewPane.ToggleDiff()
			a.statusHint = fmt.Sprintf("Preview [%s] (Tab:back, j/k:scroll)", a.previewSection)
			return a, nil
		case "esc":
			if a.previewSection == "diff" {
				if a.previewPane.IsDiffExpanded() {
					a.previewPane.DiffPickerBack()
					a.statusHint = "Preview [diff] (j/k:navigate, Enter:view)"
					return a, nil
				}
				if a.previewPane.IsDiffPickerMode() {
					a.previewPane.DiffPickerBack()
				}
				// Switch back to trace
				a.previewSection = "trace"
				a.statusHint = fmt.Sprintf("Preview [%s] (Tab:back, j/k:scroll)", a.previewSection)
				return a, nil
			}
			// Esc from trace in right panel -> return focus to left
			a.previewFocused = false
			a.statusHint = ""
			return a, nil
		case "tab":
			a.previewFocused = false
			a.statusHint = ""
			return a, nil
		}
	}

	// Intercept j/k for diff scrolling when diff is expanded in preview pane
	if a.currentView == viewAgents && a.previewPane != nil && a.previewPane.IsDiffPickerMode() {
		switch msg.String() {
		case "j", "down":
			a.previewPane.DiffPickerDown()
			return a, nil
		case "k", "up":
			a.previewPane.DiffPickerUp()
			return a, nil
		case "esc":
			a.previewPane.DiffPickerBack()
			a.statusHint = ""
			return a, nil
		}
	}

	if a.currentView == viewAgents && a.previewPane != nil && a.previewPane.IsDiffExpanded() {
		switch msg.String() {
		case "j", "down":
			a.previewPane.ScrollDiff(1)
			return a, nil
		case "k", "up":
			a.previewPane.ScrollDiff(-1)
			return a, nil
		case "esc":
			a.previewPane.DiffPickerBack()
			a.statusHint = "Select file (j/k:navigate, Enter:view, Esc:close)"
			return a, nil
		}
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
	case viewStarred:
		cmd := a.starredView.Update(msg)
		return a, cmd
	case viewTasks:
		if a.tasksView != nil {
			a.tasksView.HandleKey(msg.String())
		}
	case viewPlugin:
		if a.pluginView != nil {
			switch msg.String() {
			case "j", "down":
				a.pluginView.ScrollDown(1)
			case "k", "up":
				a.pluginView.ScrollUp(1)
			case "d":
				a.pluginView.ScrollDown(10)
			case "u":
				a.pluginView.ScrollUp(10)
			case "r":
				return a.refreshPlugin()
			}
		}
	}
	return a, nil
}

// syncPreview updates the preview pane with the currently selected agent.
func (a *App) syncPreview() {
	selected := a.agentsView.Selected()
	if selected != nil {
		if selected.Location == "remote" {
			sandboxName := selected.SandboxName
			if sandboxName == "" {
				sandboxName = selected.Name
			}
			sessionID := selected.SessionID
			if !uuidValid(sessionID) {
				if mapped := a.remoteSessionIDs.Get(sandboxName); mapped != "" {
					sessionID = mapped
					debuglog.Log("syncPreview: recovered session %s for sandbox %s", sessionID, sandboxName)
				} else {
					debuglog.Log("syncPreview: no session in map for sandbox %q", sandboxName)
				}
			}
			a.previewPane.SetParser(a.parserForRemote(sessionID, sandboxName))
		} else if p := a.providerFor(selected.ProviderName); p != nil {
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
	if a.infraProvider != nil && a.currentView == viewTasks {
		tasks, _ := a.infraProvider.ListTasks()
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

// parserForRemote returns a TraceParser that reads from the OTEL store
// using a known session ID. The session ID is injected into the sandbox
// as OTEL_RESOURCE_ATTRIBUTES=aimux.session_id=<id> at creation time,
// so the OTEL store can index spans by this ID.
func (a App) parserForRemote(otelSessionID, sandboxName string) views.TraceParser {
	return func(_ string) ([]trace.Turn, error) {
		if a.otelStore == nil || !a.otelStore.HasData() {
			// No OTEL data (e.g., aimux restarted, in-memory store is empty).
			// Fall back to building turns directly from the sandbox's session
			// file, which persists on disk inside the sandbox.
			return aimuxotel.FetchSessionTurns(sandboxName, otelSessionID), nil
		}

		// Enrich turns with assistant replies from the sandbox's session file.
		// Claude Code's OTEL telemetry does not emit model responses, but the
		// session JSONL file contains the full transcript. This is a lazy
		// fetch: nil if the file doesn't exist or the sandbox is gone.
		enrich := func(turns []trace.Turn) []trace.Turn {
			if len(turns) > 0 && sandboxName != "" {
				replies := aimuxotel.FetchSessionReplies(sandboxName, otelSessionID)
				aimuxotel.EnrichTurnsWithReplies(turns, replies)
			}
			return turns
		}

		// Direct lookup by our injected session ID
		if otelSessionID != "" {
			if root := a.otelStore.GetByConversation(otelSessionID); root != nil {
				turns := aimuxotel.SpansToTurns(root)
				if len(turns) > 0 {
					return enrich(turns), nil
				}
			}
		}

		// Fallback: Claude Code's SDK ignores OTEL_RESOURCE_ATTRIBUTES,
		// may strip query params from OTEL_EXPORTER_OTLP_LOGS_ENDPOINT,
		// and may not honor OTEL_EXPORTER_OTLP_HEADERS. When all three
		// injection channels fail, aimux.session_id never reaches the
		// store, so the alias is never created. Scan all conversations
		// and exclude known local sessions to find the remote one.
		localIDs := make(map[string]bool)
		if a.agentsView != nil {
			for _, ag := range a.agentsView.Agents() {
				if ag.Location != "remote" && ag.SessionID != "" {
					localIDs[ag.SessionID] = true
				}
			}
		}
		for _, convID := range a.otelStore.ConversationIDs() {
			if localIDs[convID] {
				continue
			}
			root := a.otelStore.GetByConversation(convID)
			if root == nil {
				continue
			}
			turns := aimuxotel.SpansToTurns(root)
			if len(turns) > 0 {
				return enrich(turns), nil
			}
		}

		// OTEL store exists but has no matching conversation for this
		// session. Fall back to the session file.
		return aimuxotel.FetchSessionTurns(sandboxName, otelSessionID), nil
	}
}

func (a App) handleCommandInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		raw := strings.TrimSpace(a.commandInput.Value())
		a.commandMode = false
		a.commandInput.Reset()
		// Handle commands that take arguments (e.g. "send hello world").
		if strings.HasPrefix(raw, "send ") {
			return a.sendMessageToSelected(strings.TrimPrefix(raw, "send "))
		}
		cmd := resolveCommand(raw)
		return a.executeCommand(cmd)
	case "esc":
		a.commandMode = false
		a.commandInput.Reset()
		return a, nil
	case "tab":
		completions := commandCompletions(a.commandInput.Value())
		if len(completions) == 1 {
			a.commandInput.SetValue(completions[0])
		}
		return a, nil
	default:
		a.commandInput.HandleKey(msg)
		return a, nil
	}
}

func (a App) handleFilterInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		a.filterMode = false
		a.agentsView.SetFilter(a.filterInput.Value())
		return a, nil
	case "esc":
		a.filterMode = false
		a.filterInput.Reset()
		a.agentsView.SetFilter("")
		return a, nil
	default:
		a.filterInput.HandleKey(msg)
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
	case "plugins":
		return a.openPlugins()
	case "health":
		return a.openHealth()
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

func (a App) openHealth() (tea.Model, tea.Cmd) {
	// Count active agents per provider from current instances.
	counts := make(map[string]int)
	for _, ag := range a.instances {
		counts[ag.ProviderName]++
	}

	health := provider.GatherHealthWithRemote(a.providers, a.infraProvider, counts, provider.RemoteHealthConfig{
		Backend: a.cfg.Remote.Backend,
		Gateway: a.cfg.Remote.Gateway,
	})
	a.healthView.SetHealth(health)
	a.healthView.SetSize(a.width, a.height)
	return a.navigateTo(viewHealth, "Health")
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

	// Build provider options from CLI agent providers only.
	// K8s is a runtime (where agents run), not a provider (which agent).
	providerOpts := make(map[string]views.ProviderOptions)
	for _, p := range a.providers {
		if p.Name() == "k8s" {
			continue
		}
		sa := p.SpawnArgs()
		providerOpts[p.Name()] = views.ProviderOptions{
			Models: sa.Models,
			Modes:  sa.Modes,
		}
	}

	a.launcherView = views.NewLauncherView(entries, providerOpts, a.cfg.OTELReceiver.Enabled, views.LauncherConfig{
			DefaultRuntime:        a.cfg.Runtime,
			DefaultExecution:      a.cfg.Execution,
			DefaultShell:          a.cfg.ResolveShell(),
			DefaultSessionManager: a.cfg.SessionManager,
			DefaultMode:           a.cfg.DefaultMode,
		})
	if len(a.cfg.QuickLaunch.Directories) > 0 {
		a.launcherView.SetQuickDirs(a.cfg.QuickLaunch.Directories)
	}
	a.launcherView.SetSize(a.width, a.height)
	a.launcherActive = true
	return a, nil
}

// NOTE: The former NewPicker overlay (openNewPicker, handleNewSession, handleNewTask,
// dismissPicker, pickerError, buildRecentDirs) has been removed. The :new command now
// opens the Launcher directly. The NewPicker view file (views/newpicker.go) is kept
// for reference but is no longer wired.
//
// Capabilities that existed in the NewPicker but are NOT yet in the Launcher:
//   - Task mode: fire-and-forget prompt execution (local + remote via K8s)
//   - Remote (pod) session launch via infraProvider.SpawnSession
//   - K8s health status bar display
// These should be added as new Launcher states/axes in a future pass.

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

	// Remote sandbox sessions: re-enter the tmux session.
	// Guard against racing an in-flight launch: while a remote launch is
	// still setting up (splitLoading), the sandbox already shows up as a
	// discovered agent. Opening it here would create a second tmux session
	// that competes with the launch's session, and the two stomp each other
	// (orphaned backends, killed sessions, "no current target" on keys).
	if selected.Location == "remote" {
		if a.splitLoading {
			a.statusHint = "Sandbox is still launching, please wait..."
			return a, nil
		}
		return a.openRemoteSession(selected)
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

	// Set perm mode indicator from the running agent's known mode
	permMode := selected.PermissionMode
	if permMode == "" || permMode == "default" {
		permMode = "default"
	}
	a.sessionView.SetPermMode(permMode)

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

		// Start live file tailer for real-time trace updates.
		a.activeTailer = startTraceTailer(sessionFile, a.traceRefresh)
	}

	a.zoomed = true
	a.splitMode = true
	a.splitFocus = "trace" // start with focus on the trace pane (left)
	a.splitLoading = true   // show loading placeholder until first PTY output
	a.layout.SetZoomed(true)
	cmds := []tea.Cmd{teaCmd}
	if a.activeTailer != nil {
		cmds = append(cmds, a.waitForTraceRefresh())
	}
	return a, tea.Batch(cmds...)
}

// openK8sSession attaches to a K8s session pod via kubectl exec + tmux.
// The pod runs `sleep infinity` with a tmux session named "main" inside.
func (a App) openK8sSession(selected *agent.Agent) (tea.Model, tea.Cmd) {
	// Don't try to exec into unhealthy pods.
	if selected.Status == agent.StatusError {
		a.statusHint = fmt.Sprintf("Cannot attach: pod is unhealthy (%s)", selected.LastAction)
		a.stickyHint = true
		return a, nil
	}

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

	// Set up the remote session: env vars, tmux, then claude.
	// kubectl exec gives us a bash shell. We set env vars, start tmux
	// (for session persistence), then launch claude inside tmux.
	go func() {
		time.Sleep(500 * time.Millisecond)

		// Set TERM for color support.
		if _, err := backend.Write([]byte("export TERM=xterm-256color\n")); err != nil {
			debuglog.Log("k8s setup: write TERM failed: %v", err)
		}
		time.Sleep(100 * time.Millisecond)

		// Forward non-secret config env vars from local shell.
		// Credentials (API keys, GCP ADC) are NOT forwarded here.
		// They're injected via K8s secrets (created by auto-provisioning
		// in ensureAuthSecrets or manually via kubectl create secret).
		// Only non-sensitive configuration values are sent via terminal.
		configEnvVars := []string{
			"CLAUDE_CODE_USE_VERTEX",
			"CLOUD_ML_REGION",
			"ANTHROPIC_VERTEX_PROJECT_ID",
			"ANTHROPIC_VERTEX_REGION",
		}
		for _, key := range configEnvVars {
			if val := os.Getenv(key); val != "" {
				_, _ = fmt.Fprintf(backend, "export %s=%q\n", key, val)
				time.Sleep(50 * time.Millisecond)
			}
		}

		// Launch claude. Use exec to replace the shell so there's no
		// command echo or leftover shell prompt. The clear removes
		// any env export output from the screen.
		if _, err := backend.Write([]byte("cd /workspace 2>/dev/null\n")); err != nil {
			debuglog.Log("k8s setup: write cd failed: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
		if _, err := backend.Write([]byte("clear && exec claude\n")); err != nil {
			debuglog.Log("k8s setup: write claude launch failed: %v", err)
		}
	}()

	a.zoomed = true
	a.splitMode = false
	a.layout.SetZoomed(true)
	return a, teaCmd
}

// openRemoteSession re-enters a remote sandbox by opening a fresh
// "openshell sandbox connect" PTY (no tmux). The sandbox is a gateway
// resource that persists across connects, so a new connection reattaches to
// the same sandbox each time.
func (a App) openRemoteSession(selected *agent.Agent) (tea.Model, tea.Cmd) {
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

	sandboxName := selected.SandboxName
	if sandboxName == "" {
		sandboxName = selected.Name
	}

	backend, err := terminal.NewOpenShellExec(sandboxName, "", false, contentW, contentH)
	if err != nil {
		debuglog.Log("remote session: openshell connect FAILED for %s: %v", sandboxName, err)
		a.statusHint = fmt.Sprintf("Cannot connect to %s: %v", selected.Name, err)
		return a, nil
	}
	debuglog.Log("remote session: connected to sandbox %s", sandboxName)

	// Resolve the pinned Claude session UUID. The selected agent is usually the
	// orchestrator-discovered record, which lacks it, so recover it from the
	// launch-time map keyed by sandbox name.
	sessionID := selected.SessionID
	if !uuidValid(sessionID) {
		if mapped := a.remoteSessionIDs.Get(sandboxName); mapped != "" {
			sessionID = mapped
			debuglog.Log("remote session: recovered pinned session id %s for %s", sessionID, sandboxName)
		}
	}

	teaCmd, err := a.sessionView.Open(selected, backend)
	if err != nil {
		a.statusHint = fmt.Sprintf("Error: %v", err)
		return a, nil
	}

	// A fresh connect gives a bare shell (the previous agent process ended
	// when the last connection closed). Resume the same Claude session so the
	// conversation and telemetry session.id continue, keeping the trace pane's
	// history. With the pinned UUID we resume it explicitly; without it we fall
	// back to --continue (Claude resumes its most recent conversation on disk).
	resumeCmd := remoteAgentCommand(selected.ProviderName, sessionID, true)
	if resumeCmd == selected.ProviderName && selected.ProviderName == "claude" {
		resumeCmd = "claude --continue"
	}
	go sendAgentCommand(backend, resumeCmd)

	// Reattaching to a full-screen agent leaves a stale screen; nudge a
	// redraw once the reconnect settles.
	go nudgeRedraw(backend, contentW, contentH)

	// Set up split view with trace pane on the left
	leftW := a.width - rightW - 1
	a.splitLaunchTime = time.Now()
	a.evalSessionID = sessionID

	// Use OTEL parser for remote sessions (no local session file)
	remoteParser := a.parserForRemote(sessionID, sandboxName)
	a.splitTrace = views.NewLogsView(0, "", remoteParser)
	a.splitTrace.SetSize(leftW, a.height-1)

	a.zoomed = true
	a.splitMode = true
	a.splitFocus = "session"
	a.splitLoading = true
	a.layout.SetZoomed(true)
	a.statusHint = fmt.Sprintf("Attached to %s", selected.Name)
	return a, teaCmd
}

// remoteAgentCommand builds the shell command that starts the agent inside the
// sandbox. For Claude, the session id is pinned to sessionID so telemetry and
// conversation stay continuous across reconnects: --session-id creates it on
// first launch, --resume reattaches to it on re-entry (same session.id, so the
// trace pane accumulates all turns). Other providers, or a missing/invalid
// UUID, fall back to the bare command.
func remoteAgentCommand(provider, sessionID string, resume bool) string {
	if provider == "claude" && uuidValid(sessionID) {
		if resume {
			return "claude --resume " + sessionID
		}
		return "claude --session-id " + sessionID
	}
	return provider
}

func uuidValid(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

// sendAgentCommand waits briefly for the sandbox shell to be ready, then types
// the given command into the PTY. Runs in its own goroutine so the TUI stays
// responsive while the connection establishes.
func sendAgentCommand(backend terminal.SessionBackend, cmd string) {
	time.Sleep(3 * time.Second)
	if _, err := backend.Write([]byte(cmd + "\n")); err != nil {
		debuglog.Log("remote: failed to send agent command %q: %v", cmd, err)
		return
	}
	debuglog.Log("remote: sent agent command %q", cmd)
}

// nudgeRedraw forces a full repaint of a full-screen TUI (e.g. claude) that is
// being reattached to after a reconnect. Reattaching to a running TUI leaves a
// stale/garbled screen until the app receives a window-size change; toggling
// the PTY size by one column and back delivers two SIGWINCHs that trigger a
// clean redraw. Runs in its own goroutine after the connection settles.
func nudgeRedraw(backend terminal.SessionBackend, cols, rows int) {
	if cols < 2 || rows < 1 {
		return
	}
	time.Sleep(1500 * time.Millisecond)
	_ = backend.Resize(cols-1, rows)
	time.Sleep(150 * time.Millisecond)
	_ = backend.Resize(cols, rows)
	debuglog.Log("remote: sent redraw nudge (%dx%d)", cols, rows)
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
		tmuxCmd := exec.Command("tmux", "split-window", "-h", cmdStr) // #nosec G204
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
// The mode parameter controls permission mode ("bypass", "plan", etc.); empty or "default" means no flag.
func (a App) resumeSession(sessionID, workingDir, sessionFilePath, mode string) (tea.Model, tea.Cmd) {
	debuglog.Log("tui: resumeSession start: id=%q dir=%q file=%q mode=%q", sessionID, workingDir, sessionFilePath, mode)

	claudeBin := "claude"
	if path, err := exec.LookPath("claude"); err == nil {
		claudeBin = path
	}

	if workingDir != "" {
		if info, err := os.Stat(workingDir); err == nil && info.IsDir() {
			// valid
		} else {
			debuglog.Log("tui: resumeSession: workingDir %q not found", workingDir)
			a.statusHint = "Cannot resolve project directory for resume"
			return a, nil
		}
	}
	cmd := controller.ResumeCommand(claudeBin, sessionID, workingDir, mode)

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

	a.sessionView.SetPermMode(mode)
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

		// Start live file tailer for real-time trace updates.
		a.activeTailer = startTraceTailer(sessionFilePath, a.traceRefresh)
	} else {
		debuglog.Log("tui: resumeSession: no session file, splitTrace will be nil")
	}

	a.zoomed = true
	a.splitMode = true
	a.splitFocus = "session" // start with focus on the live session (right)
	a.splitLoading = true      // show loading placeholder until first PTY output
	a.layout.SetZoomed(true)
	debuglog.Log("tui: resumeSession complete: zoomed=%v splitMode=%v splitFocus=%q splitTrace=%v", a.zoomed, a.splitMode, a.splitFocus, a.splitTrace != nil)
	cmds := []tea.Cmd{teaCmd}
	if a.activeTailer != nil {
		cmds = append(cmds, a.waitForTraceRefresh())
	}
	return a, tea.Batch(cmds...)
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

	action := controller.DetermineKillAction(*selected)
	switch action.Type {
	case controller.KillSandbox:
		a.statusHint = fmt.Sprintf("Delete sandbox %s? y:confirm  n:cancel", action.SandboxName)
	case controller.KillPod:
		a.statusHint = fmt.Sprintf("Delete pod %s? y:confirm  n:cancel", action.PodName)
	case controller.KillRemoveOnly:
		a.statusHint = fmt.Sprintf("Remove %s? y:remove  d:remove+delete trace  n:cancel", selected.ShortProject())
	default:
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

	action := controller.DetermineKillAction(*target)

	switch msg.String() {
	case "y", "Y":
		switch action.Type {
		case controller.KillSandbox:
			a.hideAgent(target)
			a.statusHint = fmt.Sprintf("Deleting sandbox %s...", action.SandboxName)
			go func() {
				if err := controller.ExecuteKillSandbox(action, a.composeEngine); err != nil {
					debuglog.Log("tui: sandbox delete failed: %v", err)
				}
			}()
			return a.returnToAgentsIfZoomed()
		case controller.KillPod:
			a.hideAgent(target)
			a.statusHint = fmt.Sprintf("Deleting pod %s...", action.PodName)
			k8s := a.infraProvider
			go func() {
				if k8s != nil {
					if err := k8s.ScaleDownOne(target.ProviderName, "session"); err != nil {
						debuglog.Log("tui: ScaleDownOne failed (non-fatal): %v", err)
					}
				}
				if err := exec.Command("kubectl", "delete", "pod", action.PodName, "-n", action.Namespace, "--grace-period=3", "--wait=false").Run(); err != nil { // #nosec G204
					debuglog.Log("kubectl delete pod %q failed: %v", action.PodName, err)
				}
			}()
			return a.returnToAgentsIfZoomed()
		case controller.KillRemoveOnly:
			a.hideAgent(target)
			a.statusHint = fmt.Sprintf("Removed %s from view", target.ShortProject())
		default:
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
				a.hideAgent(target)
				a.statusHint = fmt.Sprintf("Killed %s (PID %d)", target.ShortProject(), target.PID)
			}
		}
		return a.returnToAgentsIfZoomed()
	case "d", "D":
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
		return a.returnToAgentsIfZoomed()
	default:
		a.statusHint = "Cancelled"
		return a, nil
	}
}

// hideAgent adds an agent to the hidden set so it doesn't appear in the list.
func (a *App) hideAgent(ag *agent.Agent) {
	key := ag.SessionID
	if key == "" && ag.SandboxName != "" {
		key = "sandbox-" + ag.SandboxName
	}
	if key == "" && ag.SessionFile != "" {
		key = ag.SessionFile
	}
	if key == "" {
		key = fmt.Sprintf("pid-%d", ag.PID)
	}
	a.hiddenAgents[key] = true
}


// maybeNotify fires a macOS notification for an agent that changed state.
// The decision logic lives in controller.ShouldNotify; this method only
// delivers the notification via the platform-specific notify package.
func (a *App) maybeNotify(inst agent.Agent) {
	name := inst.ShortProject()
	if name == "" {
		name = inst.ProviderName
	}
	n := controller.ShouldNotify(inst.Status, name, a.cfg.Notifications)
	if n == nil {
		return
	}
	if n.Sound {
		notify.SendWithSound(n.Title, n.Message)
	} else {
		notify.Send(n.Title, n.Message)
	}
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

// SetPluginExecutor wires the plugin executor into the TUI for rendering plugin tabs.
func (a *App) SetPluginExecutor(exec *plugin.Executor) {
	a.pluginExec = exec
}

// openPlugins opens the plugin picker or goes directly to a single plugin.
func (a App) openPlugins() (tea.Model, tea.Cmd) {
	if a.pluginExec == nil {
		a.statusHint = "No plugins configured"
		return a, nil
	}
	plugins := a.pluginExec.Plugins()
	if len(plugins) == 0 {
		a.statusHint = "No plugins available"
		return a, nil
	}

	views.SortPlugins(plugins)

	if len(plugins) == 1 {
		return a.openPlugin(plugins[0])
	}

	a.pluginPicker = views.NewPluginPickerView(plugins)
	a.pluginPickerMode = true
	a.statusHint = "Select a plugin"
	return a, nil
}

// openPlugin opens a specific plugin, executes its command, and navigates to its view.
func (a App) openPlugin(p plugin.Plugin) (tea.Model, tea.Cmd) {
	data, err := a.pluginExec.Execute(p.Name)
	if err != nil {
		a.statusHint = fmt.Sprintf("Plugin error: %v", err)
		return a, nil
	}
	a.pluginView = views.NewPluginTUIView(p)
	a.pluginView.SetData(data)
	a.pluginPickerMode = false
	return a.navigateTo(viewPlugin, p.Tab)
}

// refreshPlugin re-executes the current plugin's command and updates the view.
func (a App) refreshPlugin() (tea.Model, tea.Cmd) {
	if a.pluginView == nil || a.pluginExec == nil {
		return a, nil
	}
	data, err := a.pluginExec.Execute(a.pluginView.Manifest().Name)
	if err != nil {
		a.statusHint = fmt.Sprintf("Refresh error: %v", err)
		return a, nil
	}
	a.pluginView.SetData(data)
	a.statusHint = "Refreshed"
	return a, nil
}

// openSessions discovers past sessions and navigates to the sessions browser.
func (a App) openSessions() (tea.Model, tea.Cmd) {
	agentDir := ""
	if sel := a.agentsView.Selected(); sel != nil {
		agentDir = sel.WorkingDir
	}
	dir := controller.DefaultSessionDir(agentDir, a.launchDir)
	a.sessionsView.SetCurrentDir(dir)

	// Set up trace parser (use Claude's parser as default)
	for _, p := range a.providers {
		if p.Name() == "claude" {
			a.sessionsView.SetTraceParser(p.ParseTrace)
			break
		}
	}

	opts := history.DiscoverOpts{Dir: dir}
	sessions, _ := history.Discover(opts, "")
	a.cachedSessions = sessions
	a.sessionsView.SetSessions(sessions)
	a.sessionsView.SetTagVocab(history.CollectTags(""))
	a.sessionsView.SetHourlyRate(a.cfg.ROI.HourlyRate)
	if a.sessionsView.ResumeMode() == "" {
		a.sessionsView.SetResumeMode(controller.ResolveMode("", a.cfg.DefaultMode))
	}

	return a.navigateTo(viewSessions, "Sessions")
}

func (a App) openStarred() (tea.Model, tea.Cmd) {
	for _, p := range a.providers {
		if p.Name() == "claude" {
			a.starredView.SetTraceParser(p.ParseTrace)
			break
		}
	}

	allSessions := a.cachedSessions
	if len(allSessions) == 0 {
		allSessions, _ = history.Discover(history.DiscoverOpts{}, "")
		a.cachedSessions = allSessions
	}
	var starred []history.Session
	for _, s := range allSessions {
		if s.Starred {
			starred = append(starred, s)
		}
	}
	a.starredView.SetSessions(starred)
	a.starredView.SetShowAll(true)
	return a.navigateTo(viewStarred, "Starred")
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
		a.headerView.SetHint("Enter:open  a:attend  *:pin  B:starred  t:traces  c:costs  T:tasks  S:sessions  P:plugins  H:health  C:copy-id  d:diff  :new:launch  x:kill  s:sort  /:filter  ?:help")
	case viewLogs:
		a.headerView.SetHint("j/k:scroll  Enter:expand  a:annotate  N:note  *:pin  C:copy-id  $:costs  :export  :export-otel  Esc:back")
	case viewCosts:
		a.headerView.SetHint("Esc:back  ?:help")
	case viewTeams:
		a.headerView.SetHint("Esc:back  ?:help")
	case viewTasks:
		a.headerView.SetHint("j/k:nav  g/G:top/bottom  :new:create  Esc:back")
	case viewSessions:
		hint := "j/k:nav  Enter:resume  B:toggle-perms  *:pin  t:titles  C:copy-id  P:path-filter  F:find-content  s:sort  /:filter  A:all  a:annotate  f:failure-mode  N:note  R:roi  I:roi-detail  d:delete  D:cleanup  p:preview"
		if a.sessionsView.ShowSubagents() {
			hint += "  H:hide-agents"
		} else {
			hint += "  H:show-agents"
		}
		hint += "  Esc:back"
		a.headerView.SetHint(hint)
	case viewStarred:
		a.headerView.SetHint("j/k:nav  Enter:resume  *:unpin  C:copy-id  /:filter  s:sort  p:preview  Esc:back")
	case viewHealth:
		a.headerView.SetHint("Esc:back  :health to refresh")
	case viewPlugin:
		a.headerView.SetHint("j/k:scroll  d/u:page  r:refresh  Esc:back")
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
	case viewStarred:
		a.starredView.SetSize(a.width, contentHeight)
		content = a.starredView.View()
	case viewHealth:
		a.healthView.SetSize(a.width, contentHeight)
		content = a.healthView.View()
	case viewPlugin:
		if a.pluginView != nil {
			a.pluginView.SetSize(a.width, contentHeight)
			content = a.pluginView.View()
		} else {
			content = "  No plugin selected"
		}
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

	// Overlay the plugin picker if active
	if a.pluginPickerMode && a.pluginPicker != nil {
		a.pluginPicker.SetSize(a.width, a.height)
		return header + "\n" + a.pluginPicker.View()
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
	if a.sessionView != nil && a.sessionView.Agent() != nil && a.sessionView.Agent().Location == "remote" {
		traceLabel = " TRACE [OTEL] "
		if a.otelReceiver != nil {
			_, logs, _ := a.otelReceiver.Stats()
			if logs > 0 {
				traceLabel = fmt.Sprintf(" TRACE [OTEL] (%d spans) ", logs)
			}
		}
	} else if a.otelReceiver != nil {
		_, logs, _ := a.otelReceiver.Stats()
		if logs > 0 {
			traceLabel = fmt.Sprintf(" TRACE [FILE] (otel:%d) ", logs)
		}
	}
	traceHeader := traceHeaderStyle.Render(padRight(traceLabel, leftW))
	leftLines = append(leftLines, traceHeader)

	if a.splitTrace != nil {
		traceContent := a.splitTrace.View()
		leftLines = append(leftLines, strings.Split(traceContent, "\n")...)
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
	var sessionContent string
	if a.splitLoading {
		// Show loading placeholder while session is connecting
		sessionContent = lipgloss.Place(
			rightW, contentH,
			lipgloss.Center, lipgloss.Center,
			lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Render("Loading session..."),
		)
	} else {
		sessionContent = a.sessionView.View()
	}
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
		focusHint = " :" + a.commandInput.BeforeCursor() + "█" + a.commandInput.AfterCursor()
	} else if focus == "trace" && a.splitTrace != nil && a.splitTrace.NoteMode() {
		noteText, noteTurn := a.splitTrace.NoteInput()
		noteStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true)
		focusHint = noteStyle.Render(fmt.Sprintf(" Note [Turn %d]: ", noteTurn)) + noteText + noteStyle.Render("|")
	} else if focus == "trace" {
		focusHint = " [TRACE] j/k:turns  a:annotate  N:note  $:costs  e:export"
	} else {
		focusHint = " [SESSION] typing goes to agent"
	}
	hints := hintStyle.Render(focusHint + "  Tab:switch  Ctrl+b:toggle-perms  Ctrl+f:fullscreen  Esc:exit")
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
				Render(" :") + a.commandInput.BeforeCursor() + lipgloss.NewStyle().
				Foreground(colorLogo).Render("█") + a.commandInput.AfterCursor())
	}
	if a.filterMode {
		return lipgloss.NewStyle().
			Background(lipgloss.Color("#111827")).
			Width(a.width).
			Render(lipgloss.NewStyle().
				Foreground(colorWaiting).
				Bold(true).
				Render(" /") + a.filterInput.BeforeCursor() + lipgloss.NewStyle().
				Foreground(colorWaiting).Render("█") + a.filterInput.AfterCursor())
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
		hints = " j/k:turns  Enter:expand  a:annotate  N:note  $:costs  /:filter  :export  :export-otel  Esc:back"
	} else if a.currentView == viewSessions {
		hints = " j/k:nav  Enter:resume  C:copy-id  F:find-content  s:sort  /:filter  A:all  a:annotate  f:failure-mode  N:note  d:delete  D:cleanup  p:preview  Esc:back"
		if a.sessionsView.HasActiveFilter() {
			hints += "  [Esc clears filter]"
		}
	} else if a.currentView == viewTasks {
		hints = " j/k:nav  g/G:top/bottom  :new:create  Esc:back"
	} else {
		// Show group hint if selected agent is grouped
		selected := a.agentsView.Selected()
		if selected != nil && selected.GroupCount > 1 {
			hints = fmt.Sprintf(" x%d = %d grouped  Enter:open  t:traces  c:costs  T:tasks  S:sessions  H:health  x:kill  ?:help",
				selected.GroupCount, selected.GroupCount)
		} else {
			hints = " j/k:nav  Enter:open  t:traces  c:costs  T:tasks  S:sessions  H:health  s:sort  ?:help  q:quit"
		}
		if a.filterInput.Value() != "" {
			hints += fmt.Sprintf("  [filter: %s]", a.filterInput.Value())
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

// pollSessionFile returns a tea.Cmd that fires a sessionFilePollMsg after 200ms.
func (a App) pollSessionFile(deadline time.Time) tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(_ time.Time) tea.Msg {
		return sessionFilePollMsg{deadline: deadline}
	})
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

// startTraceTailer creates a Tailer for the given session file. When new lines
// are appended, it sends a non-blocking signal on the channel so the Bubble Tea
// event loop can re-parse the trace. Returns nil if the file cannot be watched.
func startTraceTailer(path string, ch chan struct{}) *trace.Tailer {
	tailer, err := trace.NewTailer(path, func(_ string) {
		// Non-blocking send: if a signal is already pending, skip.
		select {
		case ch <- struct{}{}:
		default:
		}
	})
	if err != nil {
		return nil
	}
	return tailer
}

// waitForTraceRefresh returns a tea.Cmd that blocks until the traceRefresh
// channel receives a signal, then delivers a traceRefreshMsg.
func (a App) waitForTraceRefresh() tea.Cmd {
	return func() tea.Msg {
		<-a.traceRefresh
		return traceRefreshMsg{}
	}
}

// stopActiveTailer stops the active file tailer and drains the channel.
func (a *App) stopActiveTailer() {
	if a.activeTailer != nil {
		a.activeTailer.Stop()
		a.activeTailer = nil
	}
	// Drain any pending signal so it doesn't fire after split exit.
	select {
	case <-a.traceRefresh:
	default:
	}
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

// copySessionID copies the selected agent's session ID (as a resume command) to the clipboard.
func (a App) copySessionID() (tea.Model, tea.Cmd) {
	sel := a.agentsView.Selected()
	if sel == nil || sel.SessionID == "" {
		a.statusHint = "No session ID available"
		return a, nil
	}
	cmd := clipboard.ResumeCommand(sel.SessionID, sel.WorkingDir)
	if err := clipboard.Copy(cmd); err != nil {
		a.statusHint = fmt.Sprintf("Copy failed: %v", err)
		return a, nil
	}
	a.statusHint = fmt.Sprintf("Copied: %s", cmd)
	return a, nil
}

// copySessionIDFromSessions copies the selected past session's ID (as a resume command) to the clipboard.
func (a App) copySessionIDFromSessions() (tea.Model, tea.Cmd) {
	sel := a.sessionsView.SelectedSession()
	if sel == nil || sel.ID == "" {
		a.statusHint = "No session ID available"
		return a, nil
	}
	cmd := clipboard.ResumeCommand(sel.ID, sel.Project)
	if err := clipboard.Copy(cmd); err != nil {
		a.statusHint = fmt.Sprintf("Copy failed: %v", err)
		return a, nil
	}
	a.statusHint = fmt.Sprintf("Copied: %s", cmd)
	return a, nil
}

// starFromTrace toggles star on a session identified by its file path.
// Used from both the standalone trace view (viewLogs) and the split-mode trace pane.
func (a App) starFromTrace(filePath string) (tea.Model, tea.Cmd) {
	if filePath == "" {
		a.statusHint = "No session file available"
		return a, nil
	}
	starred, err := controller.ToggleStar(filePath)
	if err != nil {
		a.statusHint = fmt.Sprintf("Star toggle failed: %v", err)
		return a, nil
	}
	a.cachedSessions = nil
	if starred {
		a.statusHint = "Session pinned ★"
	} else {
		a.statusHint = "Session unpinned"
	}
	return a, nil
}

// copySessionIDFromTrace copies the session ID from the currently viewed trace.
// Works in both standalone trace view and split-mode trace pane.
func (a App) copySessionIDFromTrace() (tea.Model, tea.Cmd) {
	var ag *agent.Agent
	if a.sessionView != nil && a.sessionView.Agent() != nil {
		ag = a.sessionView.Agent()
	} else {
		ag = a.agentForLogsView()
	}
	if ag == nil || ag.SessionID == "" {
		a.statusHint = "No session ID available"
		return a, nil
	}
	cmd := clipboard.ResumeCommand(ag.SessionID, ag.WorkingDir)
	if err := clipboard.Copy(cmd); err != nil {
		a.statusHint = fmt.Sprintf("Copy failed: %v", err)
		return a, nil
	}
	a.statusHint = fmt.Sprintf("Copied: %s", cmd)
	return a, nil
}

// agentForLogsView finds the agent matching the current logsView by session file path.
func (a *App) agentForLogsView() *agent.Agent {
	if a.logsView == nil {
		return nil
	}
	fp := a.logsView.FilePath()
	for i := range a.instances {
		if a.instances[i].SessionFile == fp {
			return &a.instances[i]
		}
	}
	return nil
}

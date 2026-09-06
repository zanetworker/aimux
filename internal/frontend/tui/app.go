package tui

import (
	"fmt"
	"io"
	"strings"
	"time"

	"os"
	"os/exec"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/badge"
	"github.com/zanetworker/aimux/internal/cache"
	aimuxcompose "github.com/zanetworker/aimux/internal/compose"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/coordination"
	"github.com/zanetworker/aimux/internal/correlator"
	"github.com/zanetworker/aimux/internal/debuglog"
	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/environment"
	"github.com/zanetworker/aimux/internal/evaluation"
	"github.com/zanetworker/aimux/internal/frontend/tui/views"
	"github.com/zanetworker/aimux/internal/history"
	aimuxotel "github.com/zanetworker/aimux/internal/otel"
	"github.com/zanetworker/aimux/internal/plugin"
	"github.com/zanetworker/aimux/internal/provider"
	"github.com/zanetworker/aimux/internal/session"
	"github.com/zanetworker/aimux/internal/spawn"
	"github.com/zanetworker/aimux/internal/subagent"
	"github.com/zanetworker/aimux/internal/team"
	"github.com/zanetworker/aimux/internal/terminal"
	"github.com/zanetworker/aimux/internal/trace"
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
	headerView     *views.HeaderView
	agentsView     *views.AgentsView
	previewPane    *views.PreviewPane
	sessionView    *views.SessionView
	logsView       *views.LogsView
	costsView      *views.CostsView
	teamsView      *views.TeamsView
	sessionsView   *views.SessionsView
	starredView    *views.SessionsView
	cachedSessions []history.Session
	helpView       *views.HelpView
	healthView     *views.HealthView

	// Layout
	layout *Layout
	zoomed bool

	// Split mode: trace (left) + interactive session (right)
	splitMode       bool
	splitFocus      string          // "trace" or "session"
	splitTrace      *views.LogsView // live trace pane in split mode
	splitLaunchTime time.Time       // when :new session was launched (filters old files)
	splitLoading    bool            // true while session is connecting (before first output)

	// Command palette
	commandMode   bool
	commandInput  views.TextInput
	exportConfirm bool // showing export menu
	stickyHint    bool // true = statusHint persists until keypress (not cleared by tick)

	// Preview panel focus (agents dashboard)
	previewFocused bool   // true when right panel has focus
	previewSection string // "trace" or "diff" — which section is active in right panel

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

	// Registered environments for lifecycle operations (kill, health, tasks, messaging).
	environments []environment.Environment

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
	evalStore     *evaluation.Store
	evalSessionID string

	// Notifications
	prevStatuses   map[int]agent.Status // PID -> last known status for transition detection
	silenced       bool                 // TUI-level mute toggle (m key)
	doneTimestamps map[int]time.Time    // PID -> timestamp when agent finished

	// Config
	cfg       config.Config
	ctrl      *controller.Controller
	coord     coordination.Coordinator
	launchDir string // CWD when aimux was started; used as default session scope

	// OTEL receiver (optional)
	otelReceiver   *aimuxotel.Receiver
	otelStore      *aimuxotel.SpanStore
	lastEnrichTime time.Time

	// Startup cache: tracks which PIDs came from cache (stale) vs fresh discovery
	staleAgents map[int]bool

	// Pending launched agents: injected immediately on spawn, preserved
	// in the instances list until discovery finds the real process.
	// Keyed by tmux session name. Removed once a discovered agent
	// matches the same tmux session or working dir.
	pendingAgents map[string]agent.Agent

	remoteSessionIDs *controller.SessionStore
	sessionMgr       *session.Manager

	// Live trace streaming: tailer watches the session JSONL and signals
	// traceRefresh when new lines are appended.
	activeTailer *trace.Tailer
	traceRefresh chan struct{}
}

// NewApp creates a new root TUI application.
func newOrchestrator(providers []discovery.AgentProvider, envs []environment.Environment) *discovery.Orchestrator {
	o := discovery.NewOrchestrator(providers...)
	for _, env := range envs {
		o.AddEnvironment(env)
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

	// Create coordinator based on config
	var coord coordination.Coordinator
	if cfg.Coordination.RedisURL != "" {
		coord, _ = coordination.NewRedisCoordinator(cfg.Coordination.RedisURL, cfg.Coordination.TeamID)
	} else if cfg.Kubernetes.RedisURL != "" {
		coord, _ = coordination.NewRedisCoordinator(cfg.Kubernetes.RedisURL, cfg.Kubernetes.TeamID)
	}
	if coord == nil {
		coord = coordination.NewLocalCoordinator()
	}

	allProviders := []provider.Provider{
		&provider.Claude{},
		&provider.Codex{},
	}

	// K8s provider participates in provider-level operations (ParseTrace).
	// Discovery and lifecycle are handled by K8sEnvironment.
	if cfg.Kubernetes.IsActive() {
		k8s := provider.NewK8s(provider.K8sConfig{
			RedisURL:   cfg.Kubernetes.RedisURL,
			TeamID:     cfg.Kubernetes.TeamID,
			Namespace:  cfg.Kubernetes.Namespace,
			Kubeconfig: cfg.Kubernetes.Kubeconfig,
		})
		allProviders = append(allProviders, k8s)
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

	// Build environments for remote agent discovery and lifecycle.
	var envs []environment.Environment
	if cfg.Remote.Backend == "openshell" {
		osEnv := environment.NewOpenShellEnvironment("openshell", environment.OpenShellConfig{
			Gateway:  cfg.Remote.Gateway,
			Insecure: true,
			Image:    cfg.Remote.Image,
		})
		envs = append(envs, osEnv)
	}
	if cfg.Kubernetes.IsActive() {
		k8sEnv := environment.NewK8sEnvironment("k8s", environment.K8sEnvironmentConfig{
			RedisURL:   cfg.Kubernetes.RedisURL,
			TeamID:     cfg.Kubernetes.TeamID,
			Namespace:  cfg.Kubernetes.Namespace,
			Kubeconfig: cfg.Kubernetes.Kubeconfig,
		})
		envs = append(envs, k8sEnv)
	}

	orch := newOrchestrator(agentProviders, envs)

	app := App{
		currentView:      viewAgents,
		headerView:       views.NewHeaderView(),
		agentsView:       views.NewAgentsView(),
		previewPane:      views.NewPreviewPane(),
		sessionView:      views.NewSessionView(),
		costsView:        views.NewCostsView(),
		sessionsView:     views.NewSessionsView(),
		starredView:      views.NewSessionsView(),
		teamsView:        views.NewTeamsView(),
		helpView:         views.NewHelpView(),
		healthView:       views.NewHealthView(),
		tasksView:        views.NewTasksView(),
		layout:           NewLayout(0, 0),
		orchestrator:     orch,
		providers:        providers,
		breadcrumbs:      []string{"Agents"},
		hiddenAgents:     make(map[string]bool),
		prevStatuses:     make(map[int]agent.Status),
		doneTimestamps:   make(map[int]time.Time),
		cfg:              cfg,
		ctrl:             ctrl,
		coord:            coord,
		launchDir:        launchDir,
		otelStore:        aimuxotel.NewSpanStore(),
		environments:     envs,
		instances:        cachedAgents,
		staleAgents:      staleAgents,
		pendingAgents:    make(map[string]agent.Agent),
		remoteSessionIDs: controller.NewSessionStore(aimuxConfigDir()),
		sessionMgr:       session.NewManager(session.NewFileStore(filepath.Join(aimuxConfigDir(), "sessions.json"))),
		traceRefresh:     make(chan struct{}, 1),
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
			Insecure: true,
			Image:    cfg.Remote.Image,
		})
		if err != nil {
			debuglog.Log("compose: failed to initialize OpenShell engine: %v", err)
		} else {
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

		// Environment health checks dispatched as async commands.

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

		// Resolve runtime from environment when set
		runtime := msg.Runtime
		if msg.Environment != "" {
			if envCfg, ok := a.cfg.Environments[msg.Environment]; ok {
				runtime = controller.ResolveLaunchRuntime(envCfg)
			} else {
				a.statusHint = fmt.Sprintf("Launch failed: unknown environment %q", msg.Environment)
				return a, nil
			}
		}

		// Remote path: async launch with TUI-specific loading state
		if runtime == "remote" {
			if a.composeEngine == nil {
				a.statusHint = "Remote launch requires OpenShell backend — check config remote.backend and gateway"
				return a, nil
			}
			otelEndpoint := fmt.Sprintf("http://localhost:%d", a.cfg.OTELReceiverPort())
			a.splitMode = true
			a.zoomed = true
			a.splitLoading = true
			a.layout.SetZoomed(true)
			a.statusHint = fmt.Sprintf("Launching %s remotely...", msg.Provider)

			sOpts := aimuxcompose.LaunchOpts{
				Image:        a.cfg.Remote.Image,
				OTELEndpoint: otelEndpoint,
			}
			provider, dir, model := msg.Provider, msg.Dir, msg.Model
			return a, func() tea.Msg {
				result, err := a.composeEngine.LaunchInSandbox(provider, dir, sOpts)
				return remoteLaunchResultMsg{
					provider: provider, dir: dir, model: model,
					result: result, err: err,
				}
			}
		}

		// Local/container: build spec via shared controller, execute
		otelEndpoint := ""
		if msg.OTELEnabled && a.cfg.OTELReceiver.Enabled {
			otelEndpoint = fmt.Sprintf("http://localhost:%d", a.cfg.OTELReceiverPort())
		}
		cOpts := spawn.ContainerOpts{}
		if runtime == "container" {
			for _, rt := range a.cfg.Runtimes {
				if rt.Type == "container" {
					cOpts.Engine = rt.Engine
					cOpts.Image = rt.Image
					break
				}
			}
		}
		spec := controller.BuildLaunchSpec(p, controller.LaunchRequest{
			Dir:            msg.Dir,
			Model:          msg.Model,
			Mode:           msg.Mode,
			Shell:          msg.Shell,
			SessionManager: msg.SessionManager,
			OTELEnabled:    msg.OTELEnabled,
			OTELEndpoint:   otelEndpoint,
			Runtime:        runtime,
			ContainerOpts:  cOpts,
		})
		if spec.Shell == "/bin/sh" && a.cfg.ResolveShell() != "" {
			spec.Shell = a.cfg.ResolveShell()
		}
		if _, err := controller.ExecuteLocalLaunch(spec); err != nil {
			a.statusHint = fmt.Sprintf("Launch failed: %v", err)
			return a, nil
		}

		envName := msg.Environment
		if envName == "" {
			envName = runtime
		}
		if _, err := a.sessionMgr.CreateSession(msg.Provider, envName, msg.Dir, msg.Model, ""); err != nil {
			debuglog.Log("session create failed: %v", err)
		}
		name := filepath.Base(msg.Dir)

		// Immediately open split view (both local and container use tmux)
		if runtime == "local" || runtime == "container" {
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
				GroupCount:   1,
				GroupPIDs:    []int{},
			}

			// Track as pending so it survives instancesMsg replacements
			// until discovery finds the real process.
			a.pendingAgents[tmuxName] = *newAgent
			a.instances = append(a.instances, *newAgent)
			a.agentsView.SetAgents(a.instances)

			teaCmd, err := a.sessionView.Open(newAgent, backend)
			if err != nil {
				runtimeLabel := runtime
				if msg.Environment != "" {
					runtimeLabel = msg.Environment
				}
				a.statusHint = fmt.Sprintf("Launched %s in %s (%s)", msg.Provider, name, runtimeLabel)
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

			a.zoomed = true
			a.splitMode = true
			a.splitFocus = "session"
			a.splitLoading = true
			a.layout.SetZoomed(true)
			a.statusHint = fmt.Sprintf("Launched %s in %s", msg.Provider, name)
			pollDeadline := time.Now().Add(10 * time.Second)
			return a, tea.Batch(teaCmd, a.pollSessionFile(pollDeadline))
		}

		runtimeLabel := runtime
		if msg.Environment != "" {
			runtimeLabel = msg.Environment
		}
		a.statusHint = fmt.Sprintf("Launched %s in %s (%s)", msg.Provider, name, runtimeLabel)
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
			Name:         name,
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
		a.remoteSessionIDs.PutMeta(result.SandboxName, controller.LaunchMeta{
			SessionID: result.OTELSessionID,
			Provider:  msg.provider,
			Dir:       msg.dir,
		})
		if _, err := a.sessionMgr.CreateSession(msg.provider, "remote", msg.dir, msg.model, result.SandboxName); err != nil {
			debuglog.Log("session create failed: %v", err)
		}
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
		go sendAgentCommand(backend, controller.RemoteAgentCommand(msg.provider, result.OTELSessionID, false))

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

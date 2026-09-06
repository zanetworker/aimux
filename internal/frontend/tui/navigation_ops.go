package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/clipboard"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/debuglog"
	"github.com/zanetworker/aimux/internal/environment"
	"github.com/zanetworker/aimux/internal/frontend/tui/views"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/notify"
	"github.com/zanetworker/aimux/internal/plugin"
	"github.com/zanetworker/aimux/internal/provider"
	"github.com/zanetworker/aimux/internal/task"
)

func (a *App) syncPreview() {
	selected := a.agentsView.Selected()
	if selected != nil {
		if selected.Location == "remote" {
			sandboxName := selected.SandboxName
			if sandboxName == "" {
				sandboxName = selected.Name
			}
			sessionID := selected.SessionID
			if !controller.UUIDValid(sessionID) {
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

// refreshTasks updates the tasks view and header summary.

func (a *App) refreshTasks() {
	var allTasks []task.Task
	if k8s := a.k8sEnvironment(); k8s != nil {
		if tasks, err := k8s.ListTasks(); err == nil {
			allTasks = tasks
		}
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
	if k8s := a.k8sEnvironment(); k8s != nil && selected.SessionID != "" {
		if err := k8s.SendMessage(selected.SessionID, text); err != nil {
			a.statusHint = fmt.Sprintf("Send failed: %v", err)
		} else {
			a.statusHint = fmt.Sprintf("Sent to %s", selected.ShortProject())
		}
		return a, nil
	}
	a.statusHint = selected.ProviderName + " does not support messaging yet"
	return a, nil
}

func (a App) openHealth() (tea.Model, tea.Cmd) {
	// Count active agents per provider from current instances.
	counts := make(map[string]int)
	for _, ag := range a.instances {
		counts[ag.ProviderName]++
	}

	health := provider.GatherHealthWithRemote(a.providers, counts, provider.RemoteHealthConfig{
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

	var agentEntries []views.AgentConfigEntry
	for _, ac := range a.cfg.AgentConfigs {
		agentEntries = append(agentEntries, views.AgentConfigEntry{
			Name:    ac.Name,
			Runtime: ac.Runtime,
			Model:   ac.Model,
			Prompt:  ac.Prompt,
		})
	}

	envNames := controller.EnvironmentNames(a.cfg.Environments)
	envHealth := make(map[string]string, len(envNames))
	for _, name := range envNames {
		ec := a.cfg.Environments[name]
		switch ec.Type {
		case "local":
			envHealth[name] = "ready"
		case "openshell":
			if a.composeEngine != nil {
				envHealth[name] = "ready"
			} else {
				envHealth[name] = "unreachable"
			}
		default:
			envHealth[name] = "configured"
		}
	}

	a.launcherView = views.NewLauncherView(entries, providerOpts, a.cfg.OTELReceiver.Enabled, views.LauncherConfig{
		DefaultRuntime:        a.cfg.Runtime,
		DefaultExecution:      a.cfg.Execution,
		DefaultShell:          a.cfg.ResolveShell(),
		DefaultSessionManager: a.cfg.SessionManager,
		DefaultMode:           a.cfg.DefaultMode,
		RemoteAvailable:       a.composeEngine != nil,
		Environments:          envNames,
		EnvironmentHealth:     envHealth,
		AgentConfigs:          agentEntries,
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
//   - Remote (pod) session launch via K8sEnvironment.SpawnSession
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

func (a App) providerFor(name string) provider.Provider {
	for _, p := range a.providers {
		if p.Name() == name {
			return p
		}
	}
	return nil
}

func (a App) environmentFor(ag *agent.Agent) environment.Environment {
	if ag == nil {
		return nil
	}
	for _, env := range a.environments {
		if ag.Location == "remote" && env.Type() == "openshell" {
			return env
		}
		if ag.Location == "" && env.Type() == "local" {
			return env
		}
	}
	return nil
}

func (a App) k8sEnvironment() *environment.K8sEnvironment {
	for _, env := range a.environments {
		if k8s, ok := env.(*environment.K8sEnvironment); ok {
			return k8s
		}
	}
	return nil
}

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
			go func() {
				if k8s := a.k8sEnvironment(); k8s != nil {
					if err := k8s.Kill(*target); err != nil {
						debuglog.Log("k8s kill pod %q failed: %v", action.PodName, err)
					}
				}
			}()
			return a.returnToAgentsIfZoomed()
		case controller.KillRemoveOnly:
			a.hideAgent(target)
			a.statusHint = fmt.Sprintf("Removed %s from view", target.ShortProject())
		default:
			env := a.environmentFor(target)
			if env != nil {
				if err := env.Kill(*target); err != nil {
					a.statusHint = fmt.Sprintf("Kill failed: %v", err)
				} else {
					a.hideAgent(target)
					a.statusHint = fmt.Sprintf("Killed %s (PID %d)", target.ShortProject(), target.PID)
				}
			} else {
				err := provider.KillLocalAgent(*target)
				if err != nil {
					a.statusHint = fmt.Sprintf("Kill failed: %v", err)
				} else {
					a.hideAgent(target)
					a.statusHint = fmt.Sprintf("Killed %s (PID %d)", target.ShortProject(), target.PID)
				}
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

func aimuxConfigDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".aimux")
	}
	return ".aimux"
}

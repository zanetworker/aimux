package tui

import (
	"fmt"
	"strings"
	"time"

	"os/exec"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/agentmux/internal/agent"
	"github.com/zanetworker/agentmux/internal/discovery"
	"github.com/zanetworker/agentmux/internal/evaluation"
	"github.com/zanetworker/agentmux/internal/jump"
	"github.com/zanetworker/agentmux/internal/provider"
	"github.com/zanetworker/agentmux/internal/team"
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

	// Evaluation: annotation persistence
	evalStore      *evaluation.Store
	evalSessionID  string
}

// NewApp creates a new root TUI application.
func NewApp() App {
	providers := []provider.Provider{
		&provider.Claude{},
		&provider.Codex{},
		&provider.Gemini{},
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
		a.instances = []agent.Agent(msg)
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
		// Persist annotation to disk
		if a.evalStore != nil {
			if msg.Label == "" {
				_ = a.evalStore.Remove(msg.Turn)
				a.statusHint = fmt.Sprintf("Turn %d: annotation removed", msg.Turn)
			} else {
				_ = a.evalStore.Save(evaluation.Annotation{
					Turn:      msg.Turn,
					Label:     msg.Label,
					Timestamp: time.Now(),
				})
				a.statusHint = fmt.Sprintf("Turn %d: [%s] saved. a:cycle  :export to save all as JSONL",
					msg.Turn, strings.ToUpper(msg.Label))
			}
		}
		return a, nil

	case tea.KeyMsg:
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

	// Ctrl+f toggles split/fullscreen — works in ANY zoomed state
	if key == "ctrl+f" && a.splitTrace != nil {
		a.splitMode = !a.splitMode
		if !a.splitMode {
			a.sessionView.SetSize(a.width, a.height)
		} else {
			leftW := a.width * 40 / 100
			rightW := a.width - leftW - 1
			a.sessionView.SetSize(rightW, a.height)
			a.splitTrace.SetSize(leftW, a.height-3)
			a.splitFocus = "trace"
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
	a.previewPane.SetAgent(selected)
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
	case "quit":
		return a, tea.Quit
	}
	return a, nil
}

func (a App) handleEnter() (tea.Model, tea.Cmd) {
	if a.currentView != viewAgents {
		return a, nil
	}
	selected := a.agentsView.Selected()
	if selected == nil {
		return a, nil
	}

	// Resolve session file for the trace pane
	sessionFile := selected.SessionFile
	if sessionFile == "" {
		sessionFile = discovery.FindSessionFileDefault(selected.SessionID)
		if sessionFile == "" {
			files := discovery.SessionFilesForDir(selected.WorkingDir)
			if len(files) > 0 {
				sessionFile = files[len(files)-1]
			}
		}
	}

	// For non-Claude providers (Codex, Gemini), their TUIs can't be embedded
	// in a PTY reliably. Open trace-only view instead; use J to jump out.
	if selected.ProviderName != "claude" {
		if sessionFile == "" {
			a.statusHint = "No session data available for this instance"
			return a, nil
		}
		return a.openLogsForAgent(selected, sessionFile)
	}

	// Claude: open split view with embedded PTY
	p := a.providerFor(selected.ProviderName)
	if p == nil {
		a.statusHint = "No provider for " + selected.ProviderName
		return a, nil
	}

	cmd := p.ResumeCommand(*selected)
	if cmd == nil {
		a.statusHint = "No session data available for this instance"
		return a, nil
	}

	// Size the session view for the right half
	rightW := a.width * 60 / 100
	a.sessionView.SetSize(rightW, a.height)

	teaCmd, err := a.sessionView.Open(selected, cmd)
	if err != nil {
		a.statusHint = fmt.Sprintf("Error: %v", err)
		return a, nil
	}

	// Create live trace pane
	if sessionFile != "" {
		leftW := a.width - rightW
		a.splitTrace = views.NewLogsView(selected.PID, sessionFile)
		a.splitTrace.SetSize(leftW, a.height-1)
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

// openLogsForAgent opens the trace viewer for a specific agent and session file.
// Used for non-Claude providers where embedding a PTY isn't possible.
func (a App) openLogsForAgent(ag *agent.Agent, sessionFile string) (tea.Model, tea.Cmd) {
	a.logsView = views.NewLogsView(ag.PID, sessionFile)
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
	for _, ann := range annotations {
		annotMap[ann.Turn] = ann.Label
	}
	a.logsView.SetAnnotations(annotMap)

	label := fmt.Sprintf("Trace [%s: %s]", ag.ProviderName, ag.ShortProject())
	a.statusHint = "J:jump to session in terminal"
	return a.navigateTo(viewLogs, label)
}

func (a App) openLogsForSelected() (tea.Model, tea.Cmd) {
	selected := a.agentsView.Selected()
	if selected == nil {
		return a, nil
	}
	sessionFile := discovery.FindSessionFileDefault(selected.SessionID)
	if sessionFile == "" {
		files := discovery.SessionFilesForDir(selected.WorkingDir)
		if len(files) > 0 {
			sessionFile = files[len(files)-1]
		}
	}
	a.logsView = views.NewLogsView(selected.PID, sessionFile)
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
	for _, ann := range annotations {
		annotMap[ann.Turn] = ann.Label
	}
	a.logsView.SetAnnotations(annotMap)

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
		return a.sessionView.View()
	}

	// Set contextual hints based on current view
	switch a.currentView {
	case viewAgents:
		a.headerView.SetHint("Enter:open  l:trace  /:filter  ?:help  q:quit")
	case viewLogs:
		a.headerView.SetHint("j/k:turns  Enter:expand  a:label  /:search  c:collapse  J:jump  :export  Esc:back")
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

	// Pad content to fill the screen
	contentLines := strings.Count(content, "\n") + 1
	availableHeight := a.height - headerHeight - 1
	if contentLines < availableHeight {
		content += strings.Repeat("\n", availableHeight-contentLines)
	}

	return header + "\n" + content + "\n" + statusBar
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
		focusHint = " [TRACE] j/k:turns  Enter:expand  a:annotate  /:filter"
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

	var hints string
	if a.statusHint != "" {
		hints = " " + lipgloss.NewStyle().Foreground(colorWaiting).Render(a.statusHint)
	} else if a.currentView == viewLogs {
		hints = " j/k:turns  Enter:expand  a:label(GOOD/BAD/WASTE)  /:filter  c:collapse  :export  Esc:back"
	} else {
		// Show group hint if selected agent is grouped
		selected := a.agentsView.Selected()
		if selected != nil && selected.GroupCount > 1 {
			hints = fmt.Sprintf(" x%d = %d processes grouped (same dir+model)  Enter:zoom  l:trace  ?:help",
				selected.GroupCount, selected.GroupCount)
		} else {
			hints = " :cmd  j/k:nav  Enter:zoom  l:trace  /:filter  ?:help"
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

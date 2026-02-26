package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/agentmux/internal/agent"
	"github.com/zanetworker/agentmux/internal/discovery"
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
		a.layout.SetZoomed(false)
		if a.sessionView != nil {
			a.sessionView.Close()
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
// Only Ctrl+] zooms out; everything else goes to the PTY.
func (a App) handleZoomedKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+]":
		a.zoomed = false
		a.layout.SetZoomed(false)
		return a, nil
	default:
		a.sessionView.SendKey(msg.String())
		return a, nil
	}
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
		return a.navigateBack()
	case "enter":
		return a.handleEnter()
	case "J":
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
			a.logsView.Update(msg)
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

	// Find the full provider.Provider for ResumeCommand access.
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

	// Open PTY session in the session view and zoom in.
	teaCmd, err := a.sessionView.Open(selected, cmd)
	if err != nil {
		a.statusHint = fmt.Sprintf("Error: %v", err)
		return a, nil
	}

	a.zoomed = true
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

	// Zoomed: full-screen session — no header, no status bar.
	if a.zoomed && a.sessionView != nil && a.sessionView.Active() {
		return a.sessionView.View()
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
	} else {
		hints = " :cmd  j/k:nav  Enter:zoom  l:trace  /:filter  ?:help"
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

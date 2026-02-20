package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/claudetopus/internal/discovery"
	"github.com/zanetworker/claudetopus/internal/jump"
	"github.com/zanetworker/claudetopus/internal/model"
	"github.com/zanetworker/claudetopus/internal/team"
	"github.com/zanetworker/claudetopus/internal/tui/views"
)

type viewType int

const (
	viewInstances viewType = iota
	viewLogs
	viewCosts
	viewTeams
	viewHelp
)

// tickMsg triggers periodic refresh.
type tickMsg time.Time

// instancesMsg carries discovered instances.
type instancesMsg []model.Instance

// teamsMsg carries team configs.
type teamsMsg []team.TeamConfig

// App is the root Bubble Tea model that wires all views together.
type App struct {
	// State
	currentView viewType
	instances   []model.Instance
	teams       []team.TeamConfig
	width       int
	height      int

	// Sub-views
	instancesView *views.InstancesView
	logsView      *views.LogsView
	costsView     *views.CostsView
	teamsView     *views.TeamsView
	helpView      *views.HelpView

	// Command palette
	commandMode  bool
	commandInput string

	// Filter mode
	filterMode  bool
	filterInput string

	// Discovery
	orchestrator *discovery.Orchestrator

	// Breadcrumb trail
	breadcrumbs []string
}

// NewApp creates a new root TUI application.
func NewApp() App {
	return App{
		currentView:   viewInstances,
		instancesView: views.NewInstancesView(),
		costsView:     views.NewCostsView(),
		teamsView:     views.NewTeamsView(),
		helpView:      views.NewHelpView(),
		orchestrator:  discovery.NewOrchestrator(),
		breadcrumbs:   []string{"Instances"},
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
		a.instances = []model.Instance(msg)
		a.instancesView.SetInstances(a.instances)
		a.costsView.SetInstances(a.instances)
		if a.currentView == viewLogs && a.logsView != nil {
			a.logsView.Reload()
		}
		return a, nil

	case teamsMsg:
		a.teams = []team.TeamConfig(msg)
		a.teamsView.SetTeams(a.teams)
		return a, nil

	case tea.KeyMsg:
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

func (a App) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		if a.currentView == viewInstances {
			return a, tea.Quit
		}
		return a.navigateBack()
	case ":":
		a.commandMode = true
		a.commandInput = ""
		return a, nil
	case "/":
		if a.currentView == viewInstances {
			a.filterMode = true
			a.filterInput = ""
			return a, nil
		}
	case "?":
		return a.navigateTo(viewHelp, "Help")
	case "esc":
		if a.filterInput != "" {
			a.filterInput = ""
			a.instancesView.SetFilter("")
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
	case viewInstances:
		a.instancesView.Update(msg)
	case viewLogs:
		if a.logsView != nil {
			a.logsView.Update(msg)
		}
	}
	return a, nil
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
		a.instancesView.SetFilter(a.filterInput)
		return a, nil
	case "esc":
		a.filterMode = false
		a.filterInput = ""
		a.instancesView.SetFilter("")
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
		return a.navigateTo(viewInstances, "Instances")
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
	if a.currentView == viewInstances {
		// Enter = jump to session (like k9s Enter on a pod)
		return a.handleJump()
	}
	return a, nil
}

func (a App) openLogsForSelected() (tea.Model, tea.Cmd) {
	selected := a.instancesView.Selected()
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
	contentHeight := a.height - 4
	if contentHeight < 1 {
		contentHeight = 10
	}
	a.logsView.SetSize(a.width, contentHeight)
	return a.navigateTo(viewLogs, fmt.Sprintf("Logs [PID %d]", selected.PID))
}

func (a App) handleJump() (tea.Model, tea.Cmd) {
	selected := a.instancesView.Selected()
	if selected == nil {
		return a, nil
	}

	// Try tmux — check matched session first, then try "claude-<project>" convention
	tmuxTarget := selected.TMuxSession
	if tmuxTarget == "" && selected.WorkingDir != "" {
		// Try the convention: claude-<last-dir-segment>
		project := selected.ShortProject()
		if project != "" && jump.TmuxHasSession("claude-"+project) {
			tmuxTarget = "claude-" + project
		} else if project != "" && jump.TmuxHasSession(project) {
			tmuxTarget = project
		}
	}
	if tmuxTarget != "" && jump.TmuxHasSession(tmuxTarget) {
		cmd := jump.SuspendAndAttach(tmuxTarget)
		return a, tea.ExecProcess(cmd, func(err error) tea.Msg { return nil })
	}

	// Try iTerm2
	if jump.IsITerm2() {
		_ = jump.ITerm2FocusByPID(selected.PID)
		return a, nil
	}

	// Fallback: open logs view
	return a.openLogsForSelected()
}

func (a App) navigateTo(v viewType, label string) (tea.Model, tea.Cmd) {
	a.currentView = v
	if v == viewInstances {
		a.breadcrumbs = []string{"Instances"}
	} else {
		a.breadcrumbs = []string{"Instances", label}
	}
	return a, nil
}

func (a App) navigateBack() (tea.Model, tea.Cmd) {
	if a.currentView != viewInstances {
		a.currentView = viewInstances
		a.breadcrumbs = []string{"Instances"}
	}
	return a, nil
}

func (a *App) resizeViews() {
	contentHeight := a.height - 4
	if contentHeight < 1 {
		contentHeight = 1
	}
	a.instancesView.SetSize(a.width, contentHeight)
	a.costsView.SetSize(a.width, contentHeight)
	a.teamsView.SetSize(a.width, contentHeight)
	a.helpView.SetSize(a.width, contentHeight)
	if a.logsView != nil {
		a.logsView.SetSize(a.width, contentHeight)
	}
}

// --- View rendering ---

func (a App) View() string {
	if a.width == 0 {
		return "Loading..."
	}

	header := a.renderHeader()

	var content string
	switch a.currentView {
	case viewInstances:
		content = a.instancesView.View()
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
	availableHeight := a.height - 3
	if contentLines < availableHeight {
		content += strings.Repeat("\n", availableHeight-contentLines)
	}

	return header + "\n" + content + "\n" + statusBar
}

func (a App) renderHeader() string {
	active, idle, waiting := 0, 0, 0
	var totalCost float64
	for _, inst := range a.instances {
		switch inst.Status {
		case model.StatusActive:
			active++
		case model.StatusIdle:
			idle++
		case model.StatusWaitingPermission:
			waiting++
		}
		totalCost += inst.EstCostUSD
	}

	viewLabel := strings.Join(a.breadcrumbs, " > ")
	stats := fmt.Sprintf("%d instances (%s%d %s%d %s%d)  $%.2f",
		len(a.instances),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E")).Render("●"), active,
		lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Render("○"), idle,
		lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Render("◐"), waiting,
		totalCost,
	)

	left := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#7C3AED")).
		Render(" claudetopus")

	middle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#E5E7EB")).
		Render("  " + viewLabel)

	right := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#9CA3AF")).
		Render(stats + " ")

	gap := a.width - lipgloss.Width(left) - lipgloss.Width(middle) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}

	return lipgloss.NewStyle().
		Background(lipgloss.Color("#111827")).
		Width(a.width).
		Render(left + middle + strings.Repeat(" ", gap) + right)
}

func (a App) renderStatusBar() string {
	if a.commandMode {
		return lipgloss.NewStyle().
			Background(lipgloss.Color("#111827")).
			Width(a.width).
			Render(lipgloss.NewStyle().
				Foreground(lipgloss.Color("#7C3AED")).
				Bold(true).
				Render(" :") + a.commandInput + lipgloss.NewStyle().
				Foreground(lipgloss.Color("#7C3AED")).Render("|"))
	}
	if a.filterMode {
		return lipgloss.NewStyle().
			Background(lipgloss.Color("#111827")).
			Width(a.width).
			Render(lipgloss.NewStyle().
				Foreground(lipgloss.Color("#F59E0B")).
				Bold(true).
				Render(" /") + a.filterInput + lipgloss.NewStyle().
				Foreground(lipgloss.Color("#F59E0B")).Render("|"))
	}

	hints := " :command  j/k:nav  Enter:attach  /:filter  :l logs  ?:help"
	if a.filterInput != "" {
		hints += fmt.Sprintf("  [filter: %s]", a.filterInput)
	}
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color("#6B7280")).
		Background(lipgloss.Color("#111827")).
		Width(a.width).
		Render(hints)
}

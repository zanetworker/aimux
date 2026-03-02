package views

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/aimux/internal/agent"
)

// Column widths for the agents table.
const (
	colName  = 22
	colAgent = 8
	colModel = 12
	colDir   = 16
	colLast  = 14
	colAge   = 6
	colCostA = 8
)

var (
	// Table header: blue text on dark blue background.
	tableHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#5F87FF")).
				Background(lipgloss.Color("#1E293B"))

	// Selected row: dark blue background.
	agentSelectedStyle = lipgloss.NewStyle().Background(lipgloss.Color("#1E3A5F"))

	// Status icon styles.
	agentActiveIcon  = lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E"))
	agentIdleIcon    = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	agentWaitingIcon = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))
	agentMutedIcon   = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
)

// costColor returns a lipgloss style for the cost value based on thresholds.
func costColor(cost float64) lipgloss.Style {
	switch {
	case cost <= 0:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")) // dim gray
	case cost < 10:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#E5E7EB")) // default
	case cost < 50:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")) // yellow
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")) // red
	}
}

// AgentsView renders the main agents table with columns.
type AgentsView struct {
	agents      []agent.Agent
	cursor      int
	selectedPID int    // track selection by PID across refreshes
	width       int
	height      int
	filter      string
	sortField   string // "", "name", "cost", "age", "model"
}

// NewAgentsView creates a new AgentsView.
func NewAgentsView() *AgentsView {
	return &AgentsView{}
}

// SetAgents updates the list of agents with stable sort order.
// Preserves cursor position by tracking the selected PID across refreshes.
func (v *AgentsView) SetAgents(agents []agent.Agent) {
	// Sort with stable sort to prevent flickering between ticks.
	// Default: active agents first, then by name alphabetically.
	switch v.sortField {
	case "name":
		sort.SliceStable(agents, func(i, j int) bool {
			return strings.ToLower(agents[i].ShortProject()) < strings.ToLower(agents[j].ShortProject())
		})
	case "cost":
		sort.SliceStable(agents, func(i, j int) bool {
			return agents[i].EstCostUSD > agents[j].EstCostUSD
		})
	case "age":
		sort.SliceStable(agents, func(i, j int) bool {
			return agents[i].LastActivity.After(agents[j].LastActivity)
		})
	case "model":
		sort.SliceStable(agents, func(i, j int) bool {
			return agents[i].ShortModel() < agents[j].ShortModel()
		})
	default:
		// Default: status priority (active > waiting > idle > unknown), then name
		sort.SliceStable(agents, func(i, j int) bool {
			si, sj := agents[i].Status, agents[j].Status
			if si != sj {
				return si < sj // Active=0, Idle=1, Waiting=2, Unknown=3
			}
			return strings.ToLower(agents[i].ShortProject()) < strings.ToLower(agents[j].ShortProject())
		})
	}
	v.agents = agents

	// Restore cursor to the same PID if it still exists
	if v.selectedPID != 0 {
		f := v.filtered()
		for i, a := range f {
			if a.PID == v.selectedPID {
				v.cursor = i
				return
			}
		}
	}
	// PID gone or no previous selection - clamp cursor
	if v.cursor >= len(v.filtered()) {
		v.cursor = max(0, len(v.filtered())-1)
	}
}

// SetSize sets the available width and height.
func (v *AgentsView) SetSize(w, h int) {
	v.width = w
	v.height = h
}

// SetFilter sets a filter string for agents.
func (v *AgentsView) SetFilter(f string) {
	v.filter = f
	v.cursor = 0
}

// Selected returns the currently selected agent, or nil.
func (v *AgentsView) Selected() *agent.Agent {
	f := v.filtered()
	if v.cursor >= 0 && v.cursor < len(f) {
		return &f[v.cursor]
	}
	return nil
}

// Cursor returns the current cursor position.
func (v *AgentsView) Cursor() int {
	return v.cursor
}

// SortField returns the current sort field name.
func (v *AgentsView) SortField() string {
	return v.sortField
}

// Update handles key messages for navigation.
func (v *AgentsView) Update(msg tea.Msg) {
	f := v.filtered()
	if len(f) == 0 {
		return
	}
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if v.cursor < len(f)-1 {
				v.cursor++
			}
		case "k", "up":
			if v.cursor > 0 {
				v.cursor--
			}
		case "g":
			v.cursor = 0
		case "G":
			v.cursor = len(f) - 1
		case "s":
			// Cycle sort field
			switch v.sortField {
			case "":
				v.sortField = "name"
			case "name":
				v.sortField = "cost"
			case "cost":
				v.sortField = "age"
			case "age":
				v.sortField = "model"
			default:
				v.sortField = ""
			}
		}
	}
	// Track selected PID for cursor preservation across refreshes
	if v.cursor >= 0 && v.cursor < len(f) {
		v.selectedPID = f[v.cursor].PID
	}
}

// padRight pads a string with spaces so its visual (display) width reaches
// the target. Unlike fmt's %-*s, this correctly handles multi-byte UTF-8
// characters and ANSI escape sequences.
func padRight(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

// View renders the agents table with headers and status icons.
func (v *AgentsView) View() string {
	var b strings.Builder

	// Build sort-indicator-aware column headers.
	nameHeader := "NAME"
	if v.sortField == "name" {
		nameHeader = "NAME \u25bc"
	}
	modelHeader := "MODEL"
	if v.sortField == "model" {
		modelHeader = "MODEL \u25bc"
	}
	ageHeader := "AGE"
	if v.sortField == "age" {
		ageHeader = "AGE \u25bc"
	}
	costHeader := "COST"
	if v.sortField == "cost" {
		costHeader = "COST \u25bc"
	}

	// Header row: blue on dark blue — plain ASCII, so padRight
	// and fmt produce the same result, but we use padRight for consistency.
	header := " " + padRight(nameHeader, colName) + " " +
		padRight("AGENT", colAgent) + " " +
		padRight(modelHeader, colModel) + " " +
		padRight("DIR", colDir) + " " +
		padRight("LAST", colLast) + " " +
		padRight(ageHeader, colAge) + " " +
		padRight(costHeader, colCostA)
	// Pad header to full width
	if lipgloss.Width(header) < v.width {
		header += strings.Repeat(" ", v.width-lipgloss.Width(header))
	}
	b.WriteString(tableHeaderStyle.Render(header))
	b.WriteString("\n")

	f := v.filtered()
	if len(f) == 0 {
		b.WriteString(agentMutedIcon.Render("  No agents found."))
		return b.String()
	}

	// Determine visible range based on height (reserve 2 for header + border).
	visibleHeight := v.height - 2
	if visibleHeight < 1 {
		visibleHeight = len(f)
	}
	start := 0
	if v.cursor >= visibleHeight {
		start = v.cursor - visibleHeight + 1
	}
	end := start + visibleHeight
	if end > len(f) {
		end = len(f)
	}

	for idx := start; idx < end; idx++ {
		a := f[idx]
		icon := v.renderStatusIcon(a.Status)

		// Format: ▸● name — use padRight because icon contains ANSI codes
		name := a.ShortProject()
		if a.GroupCount > 1 {
			badge := agentMutedIcon.Render(fmt.Sprintf("x%d", a.GroupCount))
			name = truncate(name, colName-7) + " " + badge
		} else {
			name = truncate(name, colName-3)
		}
		nameCol := "▸" + icon + " " + name

		costRendered := costColor(a.EstCostUSD).Render(a.FormatCost())

		row := " " + padRight(nameCol, colName) + " " +
			padRight(truncate(a.ProviderName, colAgent), colAgent) + " " +
			padRight(truncate(a.ShortModel(), colModel), colModel) + " " +
			padRight(truncate(a.ShortDir(), colDir), colDir) + " " +
			padRight(truncate(a.LastAction, colLast), colLast) + " " +
			padRight(a.FormatAge(), colAge) + " " +
			padRight(costRendered, colCostA)

		if idx == v.cursor {
			// Pad to full width for selected background
			if lipgloss.Width(row) < v.width {
				row += strings.Repeat(" ", v.width-lipgloss.Width(row))
			}
			b.WriteString(agentSelectedStyle.Render(row))
		} else {
			b.WriteString(row)
		}
		b.WriteString("\n")
	}

	return b.String()
}

func (v *AgentsView) renderStatusIcon(s agent.Status) string {
	icon := s.Icon()
	switch s {
	case agent.StatusActive:
		return agentActiveIcon.Render(icon)
	case agent.StatusIdle:
		return agentIdleIcon.Render(icon)
	case agent.StatusWaitingPermission:
		return agentWaitingIcon.Render(icon)
	default:
		return agentMutedIcon.Render(icon)
	}
}

func (v *AgentsView) filtered() []agent.Agent {
	if v.filter == "" {
		return v.agents
	}
	f := strings.ToLower(v.filter)
	var out []agent.Agent
	for _, a := range v.agents {
		if strings.Contains(strings.ToLower(a.ShortProject()), f) ||
			strings.Contains(strings.ToLower(a.ShortModel()), f) ||
			strings.Contains(strings.ToLower(a.Status.String()), f) ||
			strings.Contains(strings.ToLower(a.Source.String()), f) ||
			strings.Contains(strings.ToLower(a.ProviderName), f) ||
			strings.Contains(strings.ToLower(a.ShortDir()), f) ||
			strings.Contains(strings.ToLower(a.LastAction), f) {
			out = append(out, a)
		}
	}
	return out
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

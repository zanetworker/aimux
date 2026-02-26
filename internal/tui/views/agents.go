package views

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/agentmux/internal/agent"
)

// Column widths for the agents table — k9s-style.
const (
	colName  = 22
	colAgent = 10
	colModel = 14
	colMode  = 14
	colAge   = 8
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

// AgentsView renders the main agents table with k9s-style columns.
type AgentsView struct {
	agents      []agent.Agent
	cursor      int
	selectedPID int // track selection by PID across refreshes
	width       int
	height      int
	filter      string
}

// NewAgentsView creates a new AgentsView.
func NewAgentsView() *AgentsView {
	return &AgentsView{}
}

// SetAgents updates the list of agents with stable sort order.
// Preserves cursor position by tracking the selected PID across refreshes.
func (v *AgentsView) SetAgents(agents []agent.Agent) {
	// Sort by PID for stable ordering
	sort.Slice(agents, func(i, j int) bool {
		return agents[i].PID < agents[j].PID
	})
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
		}
	}
	// Track selected PID for cursor preservation across refreshes
	if v.cursor >= 0 && v.cursor < len(f) {
		v.selectedPID = f[v.cursor].PID
	}
}

// View renders the agents table with k9s-style headers and status icons.
func (v *AgentsView) View() string {
	var b strings.Builder

	// Header row: k9s-style blue on dark blue
	header := fmt.Sprintf(" %-*s %-*s %-*s %-*s %-*s %-*s",
		colName, "NAME",
		colAgent, "AGENT",
		colModel, "MODEL",
		colMode, "MODE",
		colAge, "AGE",
		colCostA, "COST",
	)
	// Pad header to full width
	if len(header) < v.width {
		header += strings.Repeat(" ", v.width-len(header))
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

		// Format: ▸● name
		nameCol := fmt.Sprintf("▸%s %s", icon, truncate(a.ShortProject(), colName-3))

		row := fmt.Sprintf(" %-*s %-*s %-*s %-*s %-*s %-*s",
			colName, nameCol,
			colAgent, truncate(a.ProviderName, colAgent),
			colModel, truncate(a.ShortModel(), colModel),
			colMode, truncate(a.PermissionMode, colMode),
			colAge, a.FormatAge(),
			colCostA, a.FormatCost(),
		)

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
			strings.Contains(strings.ToLower(a.ProviderName), f) {
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

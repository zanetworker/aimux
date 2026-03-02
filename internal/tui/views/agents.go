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

// treeRow represents a single row in the agents table. Parent rows show the
// session; child rows show individual sub-processes when expanded.
type treeRow struct {
	agent   agent.Agent // the session agent (always set)
	isChild bool        // true for sub-process rows
	childID int         // index into GroupPIDs (only for child rows)
	isLast  bool        // last child in the group (for └─ vs ├─)
}

// AgentsView renders the main agents table with columns.
type AgentsView struct {
	agents      []agent.Agent
	rows        []treeRow // flattened tree rows for rendering
	cursor      int
	selectedPID int           // track selection by PID across refreshes
	expanded    map[int]bool  // PID -> expanded state
	width       int
	height      int
	filter      string
	sortField   string // "", "name", "cost", "age", "model"
}

// NewAgentsView creates a new AgentsView.
func NewAgentsView() *AgentsView {
	return &AgentsView{
		expanded: make(map[int]bool),
	}
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
	v.buildTreeRows()

	// Restore cursor to the same PID if it still exists
	if v.selectedPID != 0 {
		for i, r := range v.rows {
			if !r.isChild && r.agent.PID == v.selectedPID {
				v.cursor = i
				return
			}
		}
	}
	// PID gone or no previous selection - clamp cursor
	if v.cursor >= len(v.rows) {
		v.cursor = max(0, len(v.rows)-1)
	}
}

// buildTreeRows builds the flat list of treeRows from the filtered agents.
// Parent rows are always present; child rows appear only for expanded agents.
func (v *AgentsView) buildTreeRows() {
	filtered := v.filtered()
	v.rows = make([]treeRow, 0, len(filtered))
	for _, a := range filtered {
		v.rows = append(v.rows, treeRow{agent: a})
		if v.expanded[a.PID] && a.GroupCount > 1 && len(a.GroupPIDs) > 0 {
			for i, pid := range a.GroupPIDs {
				if pid == a.PID {
					continue // skip the representative PID (already shown as parent)
				}
				v.rows = append(v.rows, treeRow{
					agent:   a,
					isChild: true,
					childID: i,
					isLast:  i == len(a.GroupPIDs)-1,
				})
			}
		}
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
// If the cursor is on a child row, the parent session agent is returned.
func (v *AgentsView) Selected() *agent.Agent {
	if v.cursor >= 0 && v.cursor < len(v.rows) {
		r := &v.rows[v.cursor]
		return &r.agent
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
	if len(v.rows) == 0 {
		return
	}
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if v.cursor < len(v.rows)-1 {
				v.cursor++
			}
		case "k", "up":
			if v.cursor > 0 {
				v.cursor--
			}
		case "g":
			v.cursor = 0
		case "G":
			v.cursor = len(v.rows) - 1
		case "tab", "x":
			// Toggle expand/collapse for the selected agent's process tree.
			if v.cursor >= 0 && v.cursor < len(v.rows) {
				r := v.rows[v.cursor]
				pid := r.agent.PID
				if r.agent.GroupCount > 1 {
					v.expanded[pid] = !v.expanded[pid]
					v.buildTreeRows()
					// Keep cursor on the parent row after rebuild.
					for i, row := range v.rows {
						if !row.isChild && row.agent.PID == pid {
							v.cursor = i
							break
						}
					}
				}
			}
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
	if v.cursor >= 0 && v.cursor < len(v.rows) {
		v.selectedPID = v.rows[v.cursor].agent.PID
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

	if len(v.rows) == 0 {
		b.WriteString(agentMutedIcon.Render("  No agents found."))
		return b.String()
	}

	// Determine visible range based on height (reserve 2 for header + border).
	visibleHeight := v.height - 2
	if visibleHeight < 1 {
		visibleHeight = len(v.rows)
	}
	start := 0
	if v.cursor >= visibleHeight {
		start = v.cursor - visibleHeight + 1
	}
	end := start + visibleHeight
	if end > len(v.rows) {
		end = len(v.rows)
	}

	for idx := start; idx < end; idx++ {
		r := v.rows[idx]
		var row string

		if r.isChild {
			row = v.renderChildRow(r)
		} else {
			row = v.renderParentRow(r)
		}

		if idx == v.cursor {
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

// renderParentRow renders a session row with status icon, name, and columns.
func (v *AgentsView) renderParentRow(r treeRow) string {
	a := r.agent
	icon := v.renderStatusIcon(a.Status)

	name := a.Name
	if name == "" {
		name = a.ShortProject()
	}
	if a.GroupCount > 1 {
		badge := agentMutedIcon.Render(fmt.Sprintf("x%d", a.GroupCount))
		name = truncate(name, colName-7) + " " + badge
	} else {
		name = truncate(name, colName-3)
	}
	nameCol := "▸" + icon + " " + name

	costRendered := costColor(a.EstCostUSD).Render(a.FormatCost())

	return " " + padRight(nameCol, colName) + " " +
		padRight(truncate(a.ProviderName, colAgent), colAgent) + " " +
		padRight(truncate(a.ShortModel(), colModel), colModel) + " " +
		padRight(truncate(a.ShortDir(), colDir), colDir) + " " +
		padRight(truncate(a.LastAction, colLast), colLast) + " " +
		padRight(a.FormatAge(), colAge) + " " +
		padRight(costRendered, colCostA)
}

// renderChildRow renders a sub-process row with tree glyphs and process info.
func (v *AgentsView) renderChildRow(r treeRow) string {
	glyph := "├─"
	if r.isLast {
		glyph = "└─"
	}
	treeGlyph := agentMutedIcon.Render("   " + glyph + " ")

	pid := 0
	if r.childID >= 0 && r.childID < len(r.agent.GroupPIDs) {
		pid = r.agent.GroupPIDs[r.childID]
	}

	pidStr := agentIdleIcon.Render(fmt.Sprintf("PID %d", pid))
	info := processInfo(pid)
	if info != "" {
		info = agentMutedIcon.Render("  " + info)
	}
	return treeGlyph + pidStr + info
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

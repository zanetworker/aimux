package views

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/controller"
)

// Column widths for the agents table.
const (
	colName   = 22
	colAgent  = 8
	colModel  = 12
	colLoc    = 8  // location: "local" or "k8s"
	colDir    = 8
	colBranch = 12
	colLast   = 14
	colCPU    = 5
	colMem    = 6
	colAge    = 6
	colCostA  = 8
	colROIA   = 8
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
	agentActiveIcon  = lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E")).Bold(true)
	agentIdleIcon    = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
	agentWaitingIcon = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true)
	agentErrorIcon   = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Bold(true)
	agentMutedIcon   = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
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

func cpuColor(pct float64) lipgloss.Style {
	switch {
	case pct < 10:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	case pct < 50:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))
	}
}

func memColor(mb uint64) lipgloss.Style {
	switch {
	case mb < 500:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	case mb < 1000:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))
	}
}

// treeRow represents a single row in the agents table. Parent rows show the
// session; child rows show individual sub-processes when expanded.
type treeRow struct {
	agent      agent.Agent // the session agent (always set)
	isChild    bool        // true for sub-process rows
	childID    int         // index into GroupPIDs (only for child rows)
	isLast     bool        // last child in the group (for └─ vs ├─)
	isSubagent bool        // true for subagent rows
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
	sortField   string        // "", "name", "cost", "age", "model"
	stalePIDs    map[int]bool    // PIDs that came from cache (stale)
	starredFiles map[string]bool // session file paths that are starred
	hourlyRate   float64
}

// NewAgentsView creates a new AgentsView.
func NewAgentsView() *AgentsView {
	return &AgentsView{
		expanded:     make(map[int]bool),
		stalePIDs:    make(map[int]bool),
		starredFiles: make(map[string]bool),
	}
}

// SetAgents updates the list of agents with stable sort order.
// Preserves cursor position by tracking the selected PID across refreshes.
func (v *AgentsView) SetAgents(agents []agent.Agent) {
	// Sort with stable sort to prevent flickering between ticks.
	controller.SortAgents(agents, v.sortField)
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
// Subagent rows are nested under their parent agent.
func (v *AgentsView) buildTreeRows() {
	filtered := v.filtered()
	v.rows = make([]treeRow, 0, len(filtered))

	// Separate parents and subagents
	var parents []agent.Agent
	subByParent := make(map[int][]agent.Agent)
	for _, a := range filtered {
		if a.IsSubagent() {
			subByParent[a.ParentPID] = append(subByParent[a.ParentPID], a)
		} else {
			parents = append(parents, a)
		}
	}

	for _, a := range parents {
		v.rows = append(v.rows, treeRow{agent: a})

		// Existing process group expansion
		if v.expanded[a.PID] && a.GroupCount > 1 && len(a.GroupPIDs) > 0 {
			for i, pid := range a.GroupPIDs {
				if pid == a.PID {
					continue
				}
				v.rows = append(v.rows, treeRow{
					agent:   a,
					isChild: true,
					childID: i,
					isLast:  i == len(a.GroupPIDs)-1,
				})
			}
		}

		// Subagent children (always shown)
		subs := subByParent[a.PID]
		for i, sub := range subs {
			v.rows = append(v.rows, treeRow{
				agent:      sub,
				isChild:    true,
				isSubagent: true,
				isLast:     i == len(subs)-1,
			})
		}
	}
}

// SetSize sets the available width and height.
func (v *AgentsView) SetSize(w, h int) {
	v.width = w
	v.height = h
}

// SetFilter sets a filter string for agents and rebuilds the visible rows
// immediately so the table reflects the filter without waiting for the next
// discovery tick.
func (v *AgentsView) SetFilter(f string) {
	v.filter = f
	v.cursor = 0
	v.buildTreeRows()
}

// SetStalePIDs sets the map of PIDs that came from cache (stale).
func (v *AgentsView) SetStarredFiles(files map[string]bool) {
	v.starredFiles = files
}

func (v *AgentsView) SetHourlyRate(rate float64) {
	v.hourlyRate = rate
}

func (v *AgentsView) SetStalePIDs(pids map[int]bool) {
	v.stalePIDs = pids
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

// Agents returns the current unfiltered agents list.
func (v *AgentsView) Agents() []agent.Agent {
	return v.agents
}

// Cursor returns the current cursor position.
func (v *AgentsView) Cursor() int {
	return v.cursor
}

// SetCursor moves the cursor to the given index, clamping to valid bounds,
// and updates the selected PID for cursor preservation across refreshes.
func (v *AgentsView) SetCursor(idx int) {
	if idx < 0 {
		idx = 0
	}
	if idx >= len(v.rows) {
		idx = len(v.rows) - 1
	}
	v.cursor = idx
	if idx >= 0 && idx < len(v.rows) {
		v.selectedPID = v.rows[idx].agent.PID
	}
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
				v.sortField = "cpu"
			case "cpu":
				v.sortField = "mem"
			case "mem":
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
	cpuHeader := "CPU"
	if v.sortField == "cpu" {
		cpuHeader = "CPU\u25bc"
	}
	memHeader := "MEM"
	if v.sortField == "mem" {
		memHeader = "MEM\u25bc"
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
		padRight("LOC", colLoc) + " " +
		padRight("DIR", colDir) + " " +
		padRight("BRANCH", colBranch) + " " +
		padRight("LAST", colLast) + " " +
		padRight(cpuHeader, colCPU) + " " +
		padRight(memHeader, colMem) + " " +
		padRight(ageHeader, colAge) + " " +
		padRight(costHeader, colCostA) + " " +
		padRight("ROI", colROIA)
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
	icon := v.renderStatusIcon(a.Status, a.LastActivity)

	starPrefix := " "
	if v.starredFiles[a.SessionFile] {
		starPrefix = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Render("★")
	}

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
	nameCol := starPrefix + "▸" + icon + " " + name

	// Render project badges after the name column.
	var badgeStr string
	for _, b := range a.Badges {
		style := lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
		if b.Color != "" {
			style = style.Foreground(lipgloss.Color(b.Color))
		}
		badgeStr += style.Render(" [" + b.Value + "]")
	}
	nameCol += badgeStr

	// Clamp nameCol to colName visual width so subsequent columns align.
	if lipgloss.Width(nameCol) > colName {
		nameCol = lipgloss.NewStyle().MaxWidth(colName).Render(nameCol)
	}

	costRendered := costColor(a.EstCostUSD).Render(a.FormatCost())

	loc := agentLocation(a)

	branchDisplay := truncate(a.GitBranch, colBranch)
	if branchDisplay == "" {
		branchDisplay = padRight("", colBranch)
	} else if a.GitBranch == "main" || a.GitBranch == "master" {
		branchDisplay = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Render(padRight(branchDisplay, colBranch))
	} else {
		branchDisplay = lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA")).Render(padRight(branchDisplay, colBranch))
	}

	cpuRendered := cpuColor(a.CPUPercent).Render(a.FormatCPU())
	memRendered := memColor(a.MemoryMB).Render(a.FormatMemory())

	row := " " + padRight(nameCol, colName) + " " +
		padRight(truncate(a.ProviderName, colAgent), colAgent) + " " +
		padRight(truncate(a.ShortModel(), colModel), colModel) + " " +
		padRight(truncate(loc, colLoc), colLoc) + " " +
		padRight(truncate(a.ShortDir(), colDir), colDir) + " " +
		branchDisplay + " " +
		padRight(truncate(a.LastAction, colLast), colLast) + " " +
		padRight(cpuRendered, colCPU) + " " +
		padRight(memRendered, colMem) + " " +
		padRight(a.FormatAge(), colAge) + " " +
		padRight(costRendered, colCostA) + " " +
		padRight(v.renderAgentROI(a), colROIA)

	// Apply dim styling if this agent is stale (from cache)
	if v.stalePIDs[a.PID] {
		dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#4B5563"))
		row = dimStyle.Render(row)
	}

	return row
}

// renderAgentROI computes and renders the ROI value for an active agent.
// Uses baseline 1.5x multiplier and active time from StartTime.
func (v *AgentsView) renderAgentROI(a agent.Agent) string {
	if a.EstCostUSD <= 0 || a.StartTime.IsZero() {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Render("--")
	}
	rate := v.hourlyRate
	if rate <= 0 {
		rate = 150.0
	}
	durationMin := time.Since(a.StartTime).Minutes()
	mult := 1.5
	timeSavedMin := durationMin*mult - durationMin
	valueUSD := timeSavedMin * (rate / 60.0)
	netROI := valueUSD - a.EstCostUSD
	var roiStr string
	if netROI >= 1000 {
		roiStr = fmt.Sprintf("$%.1fK", netROI/1000)
	} else {
		roiStr = fmt.Sprintf("$%.0f", netROI)
	}
	if netROI >= 0 {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#34D399")).Render(roiStr)
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Render(roiStr)
}

// renderChildRow renders a sub-process row with tree glyphs and process info.
func (v *AgentsView) renderChildRow(r treeRow) string {
	glyph := "├─"
	if r.isLast {
		glyph = "└─"
	}

	if r.isSubagent {
		label := r.agent.Subagent.Type
		if label == "" {
			label = "subagent"
		}
		// Use the same column layout as parent but indented with glyph
		status := r.agent.Status.Icon()
		model := r.agent.ShortModel()
		age := r.agent.FormatAge()
		cost := r.agent.FormatCost()
		return fmt.Sprintf("     %s %s  %s  %s  %s  %s",
			glyph, label, model, status, age, cost)
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

func (v *AgentsView) renderStatusIcon(s agent.Status, lastActivity time.Time) string {
	icon := s.Icon()

	// Check for fading status color first.
	if fadeColor := agent.StatusFadeColor(s, lastActivity); fadeColor != "" {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(fadeColor)).Render(icon)
	}

	switch s {
	case agent.StatusActive:
		return agentActiveIcon.Render(icon)
	case agent.StatusIdle:
		return agentIdleIcon.Render(icon)
	case agent.StatusWaitingPermission:
		return agentWaitingIcon.Render(icon)
	case agent.StatusError:
		return agentErrorIcon.Render(icon)
	default:
		return agentMutedIcon.Render(icon)
	}
}

func (v *AgentsView) filtered() []agent.Agent {
	return controller.FilterAgents(v.agents, v.filter)
}

func agentLocation(a agent.Agent) string {
	if a.Location != "" {
		return a.Location
	}
	if strings.HasPrefix(a.WorkingDir, "k8s://") {
		return "k8s"
	}
	return "local"
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

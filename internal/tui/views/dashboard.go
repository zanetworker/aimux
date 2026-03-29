package views

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/aimux/internal/agent"
)

// statusPriority returns a sort key for agent status. Lower values appear first.
// WaitingPermission(0) > Error(1) > Active(2) > Idle(3) > Unknown(4)
func statusPriority(s agent.Status) int {
	switch s {
	case agent.StatusWaitingPermission:
		return 0
	case agent.StatusError:
		return 1
	case agent.StatusActive:
		return 2
	case agent.StatusIdle:
		return 3
	default:
		return 4
	}
}

// minLinesPerPreview is the minimum number of lines allocated to each
// mini-preview: 1 header + 1 separator + 1 content line.
const minLinesPerPreview = 3

var (
	dashHeaderActive  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#22C55E"))
	dashHeaderIdle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	dashHeaderWaiting = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F59E0B"))
	dashHeaderError   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#EF4444"))
	dashHeaderUnknown = lipgloss.NewStyle().Foreground(lipgloss.Color("#4B5563"))
	dashDim           = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	dashSep           = lipgloss.NewStyle().Foreground(lipgloss.Color("#374151"))
	dashSelectedBorder = lipgloss.NewStyle().Foreground(lipgloss.Color("#5F87FF"))
	dashOverflow      = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF")).Italic(true)
)

// DashboardView renders stacked mini-previews for all agents with auto-sizing
// and priority sorting.
type DashboardView struct {
	agents       []agent.Agent
	tmuxCaptures map[string]string // tmux session name -> raw pane output
	traceSnips   map[int]string    // PID -> abbreviated trace text
	selectedPID  int
	width        int
	height       int
}

// NewDashboardView creates a new DashboardView.
func NewDashboardView() *DashboardView {
	return &DashboardView{}
}

// SetAgents updates the agent list. The view sorts them internally by priority.
func (d *DashboardView) SetAgents(agents []agent.Agent) {
	d.agents = agents
}

// SetTmuxCaptures updates the tmux session captures.
func (d *DashboardView) SetTmuxCaptures(captures map[string]string) {
	d.tmuxCaptures = captures
}

// SetTraceSnippets updates the trace snippet map.
func (d *DashboardView) SetTraceSnippets(snips map[int]string) {
	d.traceSnips = snips
}

// SetSelected sets the PID of the currently highlighted agent.
func (d *DashboardView) SetSelected(pid int) {
	d.selectedPID = pid
}

// SetSize sets the available width and height for the dashboard.
func (d *DashboardView) SetSize(w, h int) {
	d.width = w
	d.height = h
}

// View renders the dashboard.
func (d *DashboardView) View() string {
	if len(d.agents) == 0 {
		return dashDim.Render("No agents running")
	}

	// Sort agents by priority.
	sorted := make([]agent.Agent, len(d.agents))
	copy(sorted, d.agents)
	sort.SliceStable(sorted, func(i, j int) bool {
		return statusPriority(sorted[i].Status) < statusPriority(sorted[j].Status)
	})

	// Determine how many agents fit and how many lines each gets.
	maxAgents := d.height / minLinesPerPreview
	if maxAgents < 1 {
		maxAgents = 1
	}

	showOverflow := false
	visible := sorted
	overflowCount := 0
	if len(sorted) > maxAgents {
		// Reserve 1 line for the overflow indicator.
		availableForAgents := d.height - 1
		maxAgents = availableForAgents / minLinesPerPreview
		if maxAgents < 1 {
			maxAgents = 1
		}
		if len(sorted) > maxAgents {
			overflowCount = len(sorted) - maxAgents
			visible = sorted[:maxAgents]
			showOverflow = true
		}
	}

	linesPerAgent := d.height / len(visible)
	if showOverflow {
		linesPerAgent = (d.height - 1) / len(visible)
	}
	if linesPerAgent < minLinesPerPreview {
		linesPerAgent = minLinesPerPreview
	}

	// Content lines = total lines minus 1 header and 1 separator.
	contentLines := linesPerAgent - 2
	if contentLines < 1 {
		contentLines = 1
	}

	var b strings.Builder
	for _, a := range visible {
		d.renderPreview(&b, a, contentLines)
	}

	if showOverflow {
		b.WriteString(dashOverflow.Render(fmt.Sprintf("  +%d more agents", overflowCount)))
		b.WriteString("\n")
	}

	return b.String()
}

// renderPreview writes a single mini-preview for an agent into the builder.
func (d *DashboardView) renderPreview(b *strings.Builder, a agent.Agent, contentLines int) {
	isSelected := a.PID == d.selectedPID
	contentWidth := d.width - 2 // leave room for left border

	// Build header line: icon + name + cost + age
	name := a.Name
	if name == "" {
		name = a.ShortProject()
	}
	if name == "" {
		name = fmt.Sprintf("PID %d", a.PID)
	}

	headerStyle := d.headerStyleForStatus(a.Status)
	icon := a.Status.Icon()
	cost := a.FormatCost()
	age := a.FormatAge()

	// Right-aligned cost + age.
	rightPart := fmt.Sprintf("%s  %s", cost, age)
	nameMax := contentWidth - len(rightPart) - len(icon) - 3 // icon + space + padding
	if nameMax < 4 {
		nameMax = 4
	}
	if len(name) > nameMax {
		name = name[:nameMax-1] + "…"
	}
	gap := contentWidth - len(icon) - 1 - len(name) - len(rightPart)
	if gap < 1 {
		gap = 1
	}

	headerLine := headerStyle.Render(icon+" "+name) + strings.Repeat(" ", gap) + dashDim.Render(rightPart)

	// Separator line.
	sepLine := dashSep.Render(strings.Repeat("─", contentWidth))

	// Content: prefer tmux capture, then trace snippet, then placeholder.
	content := d.contentForAgent(a, contentLines, contentWidth)

	// Determine left border character.
	var borderChar string
	if isSelected {
		borderChar = dashSelectedBorder.Render("│")
	} else {
		borderChar = dashDim.Render(" ")
	}

	// Write header.
	b.WriteString(borderChar + " " + headerLine + "\n")

	// Write content lines.
	for _, line := range content {
		if len(line) > contentWidth {
			line = line[:contentWidth]
		}
		b.WriteString(borderChar + " " + line + "\n")
	}

	// Write separator.
	b.WriteString(borderChar + " " + sepLine + "\n")
}

// contentForAgent returns content lines for a mini-preview.
func (d *DashboardView) contentForAgent(a agent.Agent, maxLines, maxWidth int) []string {
	// Try tmux capture first.
	if a.TMuxSession != "" && d.tmuxCaptures != nil {
		if raw, ok := d.tmuxCaptures[a.TMuxSession]; ok && raw != "" {
			return lastNLines(raw, maxLines)
		}
	}

	// Try trace snippet.
	if d.traceSnips != nil {
		if snip, ok := d.traceSnips[a.PID]; ok && snip != "" {
			return lastNLines(snip, maxLines)
		}
	}

	// Placeholder.
	placeholder := []string{dashDim.Render("No session data")}
	for len(placeholder) < maxLines {
		placeholder = append(placeholder, "")
	}
	return placeholder
}

// lastNLines splits text on newlines and returns the last n lines.
// If the text has fewer lines, all lines are returned (padded to n with empty strings).
func lastNLines(text string, n int) []string {
	all := strings.Split(text, "\n")
	// Remove trailing empty line from split.
	if len(all) > 0 && all[len(all)-1] == "" {
		all = all[:len(all)-1]
	}
	if len(all) <= n {
		result := make([]string, n)
		// Pad at the start with empty lines.
		offset := n - len(all)
		for i, line := range all {
			result[offset+i] = line
		}
		return result
	}
	return all[len(all)-n:]
}

// headerStyleForStatus returns the lipgloss style for the header based on status.
func (d *DashboardView) headerStyleForStatus(s agent.Status) lipgloss.Style {
	switch s {
	case agent.StatusActive:
		return dashHeaderActive
	case agent.StatusIdle:
		return dashHeaderIdle
	case agent.StatusWaitingPermission:
		return dashHeaderWaiting
	case agent.StatusError:
		return dashHeaderError
	default:
		return dashHeaderUnknown
	}
}

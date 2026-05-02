package views

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/aimux/internal/agent"
)

var (
	colorLogo       = lipgloss.Color("#5F87FF")
	colorActive     = lipgloss.Color("#22C55E")
	colorIdle       = lipgloss.Color("#6B7280")
	colorWaiting    = lipgloss.Color("#F59E0B")
	colorCost       = lipgloss.Color("#34D399")
	colorInfoBox    = lipgloss.Color("#1C1C2E")
	colorInfoBorder = lipgloss.Color("#3B3B5C")
	colorHeaderText = lipgloss.Color("#E5E7EB")
	colorMutedText  = lipgloss.Color("#9CA3AF")
	colorCrumb      = lipgloss.Color("#5F87FF")
	colorCrumbSep   = lipgloss.Color("#374151")
)

// ASCII art logo for the right side of the header.
var asciiLogo = []string{
	"       _                    ",
	"  __ _(_)_ __ _  ___ __    ",
	" / _` | | '  \\ || \\ \\ /   ",
	" \\__,_|_|_|_|_\\_,_/_\\_\\   ",
}

// HeaderView renders a header with info boxes and ASCII logo.
type HeaderView struct {
	agents   []agent.Agent
	crumbs   []string
	hintText string // contextual key hints for the current view
	width    int

	// Task summary counts
	taskPending   int
	taskActive    int
	taskCompleted int
	taskFailed    int

	// K8s status
	k8sStatus string // e.g. "connected", "disconnected (retry in 25s)", ""

	// Notification state
	silenced bool

	// Attention counter
	attentionCount int
}

// NewHeaderView creates a new HeaderView.
func NewHeaderView() *HeaderView {
	return &HeaderView{
		crumbs: []string{"Agents"},
	}
}

// SetAgents updates the agent list used for stats.
func (h *HeaderView) SetAgents(agents []agent.Agent) {
	h.agents = agents
}

// SetCrumbs updates the breadcrumb trail.
func (h *HeaderView) SetCrumbs(crumbs []string) {
	h.crumbs = crumbs
}

// SetWidth sets the available width.
func (h *HeaderView) SetWidth(w int) {
	h.width = w
}

// SetHint sets the contextual key hint bar text.
func (h *HeaderView) SetHint(hint string) {
	h.hintText = hint
}

// SetK8sStatus updates the K8s connection status displayed in the header.
func (h *HeaderView) SetK8sStatus(status string) {
	h.k8sStatus = status
}

// SetTaskSummary updates the task counts displayed in the header.
func (h *HeaderView) SetTaskSummary(pending, active, completed, failed int) {
	h.taskPending = pending
	h.taskActive = active
	h.taskCompleted = completed
	h.taskFailed = failed
}

// SetSilenced updates the notification mute state.
func (h *HeaderView) SetSilenced(silenced bool) {
	h.silenced = silenced
}

// SetAttentionCount updates the attention counter (agents waiting + recently done).
func (h *HeaderView) SetAttentionCount(n int) {
	h.attentionCount = n
}

// View renders the header.
func (h *HeaderView) View() string {
	infoBoxes := h.renderInfoBoxes()
	logo := h.renderLogo()
	crumbBar := h.renderCrumbs()

	// Hide logo if terminal is too narrow to fit both info boxes and logo
	infoW := lipgloss.Width(infoBoxes)
	logoW := lipgloss.Width(logo)
	if infoW+logoW+2 > h.width {
		logo = "" // hide logo for narrow terminals
	}

	// Join info boxes and logo horizontally
	topRow := lipgloss.JoinHorizontal(lipgloss.Top, infoBoxes, h.fillGap(infoBoxes, logo), logo)

	// Ensure the top row fills the width
	topRow = lipgloss.NewStyle().Width(h.width).Render(topRow)

	result := topRow + "\n" + crumbBar
	if h.hintText != "" {
		sep := lipgloss.NewStyle().Foreground(lipgloss.Color("#374151")).Render(strings.Repeat("─", h.width))
		result += "\n" + h.renderHintBar() + "\n" + sep
	}
	return result
}

// Height returns the rendered height of the header (for layout calculations).
// Must match the actual number of newlines in View() output.
func (h *HeaderView) Height() int {
	// Info boxes / logo row:  4 lines (rounded border top + 2 content + bottom)
	// Breadcrumbs:            1 line
	// Hint bar + separator:   2 lines (when hint is set)
	if h.hintText != "" {
		return 4 + 1 + 2 // = 7
	}
	return 4 + 1 // = 5
}

func (h *HeaderView) renderHintBar() string {
	keyStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#5F87FF")).
		Bold(true)
	descStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#6B7280"))

	// Parse hint text: "Key:desc  Key:desc" -> styled output
	parts := strings.Fields(h.hintText)
	var rendered []string
	for _, part := range parts {
		if idx := strings.Index(part, ":"); idx > 0 {
			key := part[:idx]
			desc := part[idx+1:]
			rendered = append(rendered, keyStyle.Render(key)+" "+descStyle.Render(desc))
		} else {
			rendered = append(rendered, descStyle.Render(part))
		}
	}

	prefix := lipgloss.NewStyle().Foreground(lipgloss.Color("#374151")).Render(" ❯ ")
	return prefix + strings.Join(rendered, "  ")
}

func (h *HeaderView) renderInfoBoxes() string {
	active, idle, waiting, errors := 0, 0, 0, 0
	var totalCost float64
	providers := make(map[string]int)

	for _, a := range h.agents {
		switch a.Status {
		case agent.StatusActive:
			active++
		case agent.StatusIdle:
			idle++
		case agent.StatusWaitingPermission:
			waiting++
		case agent.StatusError:
			errors++
		}
		totalCost += a.EstCostUSD
		if a.ProviderName != "" {
			providers[a.ProviderName]++
		}
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorInfoBorder).
		Background(colorInfoBox).
		Padding(0, 1)

	labelStyle := lipgloss.NewStyle().Foreground(colorMutedText)
	valueStyle := lipgloss.NewStyle().Foreground(colorHeaderText).Bold(true)

	// Agent count box with labeled status icons
	activeStr := lipgloss.NewStyle().Foreground(colorActive).Render(fmt.Sprintf("●%d active", active))
	waitingStr := lipgloss.NewStyle().Foreground(colorWaiting).Render(fmt.Sprintf("◐%d wait", waiting))
	idleStr := lipgloss.NewStyle().Foreground(colorIdle).Render(fmt.Sprintf("○%d idle", idle))
	errorStr := lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Render(fmt.Sprintf("✕%d err", errors))

	agentBox := boxStyle.Render(
		labelStyle.Render("Agents") + " " +
			valueStyle.Render(fmt.Sprintf("%d", len(h.agents))) + "\n" +
			activeStr + " " + waitingStr + " " + idleStr + " " + errorStr,
	)

	// Cost box — color-coded by threshold
	costStyle := lipgloss.NewStyle().Foreground(colorCost).Bold(true)
	var costStyled string
	switch {
	case totalCost <= 0:
		costStyled = lipgloss.NewStyle().Foreground(colorIdle).Render(fmt.Sprintf("$%.2f", totalCost))
	case totalCost < 10:
		costStyled = costStyle.Render(fmt.Sprintf("$%.2f", totalCost))
	case totalCost < 50:
		costStyled = lipgloss.NewStyle().Foreground(colorWaiting).Bold(true).Render(fmt.Sprintf("$%.2f", totalCost))
	default:
		costStyled = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Bold(true).Render(fmt.Sprintf("$%.2f", totalCost))
	}
	costBox := boxStyle.Render(
		labelStyle.Render("Cost") + "\n" +
			costStyled,
	)

	// Provider box
	var providerLines []string
	for name, count := range providers {
		providerLines = append(providerLines, fmt.Sprintf("%s:%d", name, count))
	}
	providerStr := "-"
	if len(providerLines) > 0 {
		providerStr = strings.Join(providerLines, " ")
	}
	providerBox := boxStyle.Render(
		labelStyle.Render("Providers") + "\n" +
			valueStyle.Render(providerStr),
	)

	boxes := lipgloss.JoinHorizontal(lipgloss.Top, agentBox, " ", costBox, " ", providerBox)

	if h.k8sStatus != "" {
		k8sBox := boxStyle.Render(h.renderK8sStatus())
		boxes = lipgloss.JoinHorizontal(lipgloss.Top, boxes, " ", k8sBox)
	}

	if taskSummary := h.renderTaskSummary(); taskSummary != "" {
		taskBox := boxStyle.Render(taskSummary)
		boxes = lipgloss.JoinHorizontal(lipgloss.Top, boxes, " ", taskBox)
	}

	// Attention box (only show when count > 0)
	if h.attentionCount > 0 {
		attentionStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true)
		attentionBox := boxStyle.Render(
			labelStyle.Render("Attention") + "\n" +
				attentionStyle.Render(fmt.Sprintf("⚠ %d need action", h.attentionCount)),
		)
		boxes = lipgloss.JoinHorizontal(lipgloss.Top, boxes, " ", attentionBox)
	}

	return boxes
}

// renderTaskSummary returns the formatted task summary content, or "" if no tasks exist.
func (h *HeaderView) renderTaskSummary() string {
	total := h.taskPending + h.taskActive + h.taskCompleted + h.taskFailed
	if total == 0 {
		return ""
	}

	labelStyle := lipgloss.NewStyle().Foreground(colorMutedText)
	var parts []string

	if h.taskPending > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(colorIdle).Render(fmt.Sprintf("○ %d pending", h.taskPending)))
	}
	if h.taskActive > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(colorWaiting).Render(fmt.Sprintf("● %d running", h.taskActive)))
	}
	if h.taskCompleted > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(colorActive).Render(fmt.Sprintf("✓ %d done", h.taskCompleted)))
	}
	if h.taskFailed > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Render(fmt.Sprintf("✗ %d failed", h.taskFailed)))
	}

	return labelStyle.Render("Tasks") + "\n" + strings.Join(parts, "  ")
}

// renderK8sStatus returns the formatted K8s connection status.
func (h *HeaderView) renderK8sStatus() string {
	labelStyle := lipgloss.NewStyle().Foreground(colorMutedText)
	var statusColor lipgloss.Color
	var icon string
	switch {
	case strings.HasPrefix(h.k8sStatus, "connected"):
		statusColor = colorActive
		icon = "●"
	case strings.HasPrefix(h.k8sStatus, "connecting"):
		statusColor = colorWaiting
		icon = "◐"
	default:
		statusColor = lipgloss.Color("#EF4444")
		icon = "○"
	}
	return labelStyle.Render("K8s") + "\n" +
		lipgloss.NewStyle().Foreground(statusColor).Render(icon+" "+h.k8sStatus)
}

func (h *HeaderView) renderLogo() string {
	logoStyle := lipgloss.NewStyle().
		Foreground(colorLogo).
		Bold(true).
		Padding(0, 1)

	return logoStyle.Render(strings.Join(asciiLogo, "\n"))
}

func (h *HeaderView) fillGap(left, right string) string {
	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)
	gap := h.width - leftW - rightW
	if gap < 1 {
		gap = 1
	}
	return strings.Repeat(" ", gap)
}

func (h *HeaderView) renderCrumbs() string {
	crumbStyle := lipgloss.NewStyle().Foreground(colorCrumb).Bold(true)
	sepStyle := lipgloss.NewStyle().Foreground(colorCrumbSep)

	var parts []string
	for i, c := range h.crumbs {
		parts = append(parts, crumbStyle.Render(c))
		if i < len(h.crumbs)-1 {
			parts = append(parts, sepStyle.Render(" > "))
		}
	}

	return " " + strings.Join(parts, "")
}

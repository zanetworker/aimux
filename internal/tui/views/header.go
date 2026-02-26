package views

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/agentmux/internal/agent"
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
	"         _                       ",
	"  ___ _ | | ___  _  _  _ __  ___ ",
	" / _|| || |/ _ \\| || || '_ \\/ -_)",
	" \\__|_||_|\\___/ \\_,_|| .__/\\___|",
	"                     |_|         ",
}

// HeaderView renders a k9s-style header with info boxes and ASCII logo.
type HeaderView struct {
	agents []agent.Agent
	crumbs []string
	width  int
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

// View renders the header.
func (h *HeaderView) View() string {
	infoBoxes := h.renderInfoBoxes()
	logo := h.renderLogo()
	crumbBar := h.renderCrumbs()

	// Join info boxes and logo horizontally
	topRow := lipgloss.JoinHorizontal(lipgloss.Top, infoBoxes, h.fillGap(infoBoxes, logo), logo)

	// Ensure the top row fills the width
	topRow = lipgloss.NewStyle().Width(h.width).Render(topRow)

	return topRow + "\n" + crumbBar
}

// Height returns the rendered height of the header (for layout calculations).
func (h *HeaderView) Height() int {
	// Logo is 5 lines + 1 for border/padding, plus 1 for crumb bar
	return 8
}

func (h *HeaderView) renderInfoBoxes() string {
	active, idle, waiting := 0, 0, 0
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

	// Agent count box
	activeStr := lipgloss.NewStyle().Foreground(colorActive).Render(fmt.Sprintf("●%d", active))
	waitingStr := lipgloss.NewStyle().Foreground(colorWaiting).Render(fmt.Sprintf("◐%d", waiting))
	idleStr := lipgloss.NewStyle().Foreground(colorIdle).Render(fmt.Sprintf("○%d", idle))

	agentBox := boxStyle.Render(
		labelStyle.Render("Agents") + " " +
			valueStyle.Render(fmt.Sprintf("%d", len(h.agents))) + "\n" +
			activeStr + " " + waitingStr + " " + idleStr,
	)

	// Cost box
	costStyle := lipgloss.NewStyle().Foreground(colorCost).Bold(true)
	costBox := boxStyle.Render(
		labelStyle.Render("Cost") + "\n" +
			costStyle.Render(fmt.Sprintf("$%.2f", totalCost)),
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

	return lipgloss.JoinHorizontal(lipgloss.Top, agentBox, " ", costBox, " ", providerBox)
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

package views

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/controller"
)

var (
	costValueStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#34D399"))
	separatorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#374151"))
	costHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#E5E7EB"))
	costMutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
)

// CostsView renders a cost dashboard aggregated by project.
type CostsView struct {
	instances []agent.Agent
	width     int
	height    int
}

// NewCostsView creates a new CostsView.
func NewCostsView() *CostsView {
	return &CostsView{}
}

// SetAgents updates the agents used for cost aggregation.
func (v *CostsView) SetAgents(agents []agent.Agent) {
	v.instances = agents
}

// SetSize sets the available width and height.
func (v *CostsView) SetSize(w, h int) {
	v.width = w
	v.height = h
}

// View renders the costs table.
func (v *CostsView) View() string {
	if len(v.instances) == 0 {
		return costMutedStyle.Render("  No cost data available.")
	}

	rows := controller.CostsByProject(v.instances)
	for i := range rows {
		if rows[i].Provider == "" {
			rows[i].Provider = "unknown" // the TUI labels an empty provider; the web sends it empty
		}
	}

	var b strings.Builder

	// Header
	header := fmt.Sprintf("%-20s %-10s %-14s %12s %12s %10s",
		"PROJECT", "AGENT", "MODEL", "TOKENS IN", "TOKENS OUT", "COST",
	)
	b.WriteString(costHeaderStyle.Render(header))
	b.WriteString("\n")

	var totalIn, totalOut int64
	var totalCost float64

	for _, row := range rows {
		totalIn += row.TokensIn
		totalOut += row.TokensOut
		totalCost += row.CostUSD

		costStr := fmt.Sprintf("%10s", fmt.Sprintf("$%.2f", row.CostUSD))
		line := fmt.Sprintf("%-20s %-10s %-14s %12s %12s ",
			truncate(row.Project, 20),
			truncate(row.Provider, 10),
			truncate(row.Model, 14),
			formatTokens(row.TokensIn),
			formatTokens(row.TokensOut),
		)
		b.WriteString(line)
		b.WriteString(costValueStyle.Render(costStr))
		b.WriteString("\n")
	}

	// Separator
	sep := strings.Repeat("─", 80)
	b.WriteString(separatorStyle.Render(sep))
	b.WriteString("\n")

	// Total
	totalCostStr := fmt.Sprintf("%10s", fmt.Sprintf("$%.2f", totalCost))
	total := fmt.Sprintf("%-20s %-10s %-14s %12s %12s ",
		"TOTAL", "", "",
		formatTokens(totalIn),
		formatTokens(totalOut),
	)
	b.WriteString(costHeaderStyle.Render(total))
	b.WriteString(costValueStyle.Render(totalCostStr))
	b.WriteString("\n")

	return b.String()
}

// formatTokens formats a token count with K/M suffixes.
func formatTokens(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

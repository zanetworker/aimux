package views

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/aimux/internal/agent"
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

type projectCost struct {
	project   string
	provider  string
	model     string
	tokensIn  int64
	tokensOut int64
	cost      float64
}

// View renders the costs table.
func (v *CostsView) View() string {
	if len(v.instances) == 0 {
		return costMutedStyle.Render("  No cost data available.")
	}

	// Aggregate by project
	agg := make(map[string]*projectCost)
	for _, inst := range v.instances {
		proj := inst.ShortProject()
		if proj == "" {
			proj = "(unknown)"
		}
		pc, ok := agg[proj]
		if !ok {
			prov := inst.ProviderName
			if prov == "" {
				prov = "unknown"
			}
			pc = &projectCost{project: proj, provider: prov, model: inst.ShortModel()}
			agg[proj] = pc
		}
		pc.tokensIn += inst.TokensIn
		pc.tokensOut += inst.TokensOut
		pc.cost += inst.EstCostUSD
	}

	// Sort by cost descending
	rows := make([]*projectCost, 0, len(agg))
	for _, pc := range agg {
		rows = append(rows, pc)
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].cost > rows[j].cost
	})

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
		totalIn += row.tokensIn
		totalOut += row.tokensOut
		totalCost += row.cost

		costStr := fmt.Sprintf("%10s", fmt.Sprintf("$%.2f", row.cost))
		line := fmt.Sprintf("%-20s %-10s %-14s %12s %12s ",
			truncate(row.project, 20),
			truncate(row.provider, 10),
			truncate(row.model, 14),
			formatTokens(row.tokensIn),
			formatTokens(row.tokensOut),
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

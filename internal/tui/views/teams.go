package views

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/aimux/internal/team"
)

var (
	teamHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7C3AED"))
	memberStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#E5E7EB"))
	agentTypeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
	teamMutedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
)

// TeamsView renders the teams overview.
type TeamsView struct {
	teams  []team.TeamConfig
	width  int
	height int
}

// NewTeamsView creates a new TeamsView.
func NewTeamsView() *TeamsView {
	return &TeamsView{}
}

// SetTeams updates the list of teams.
func (v *TeamsView) SetTeams(teams []team.TeamConfig) {
	v.teams = teams
}

// SetSize sets the available width and height.
func (v *TeamsView) SetSize(w, h int) {
	v.width = w
	v.height = h
}

// View renders the teams display.
func (v *TeamsView) View() string {
	if len(v.teams) == 0 {
		return teamMutedStyle.Render("  No teams configured.")
	}

	var b strings.Builder

	for _, t := range v.teams {
		header := fmt.Sprintf("\u25b8 %s (%d members)", t.Name, len(t.Members))
		b.WriteString(teamHeaderStyle.Render(header))
		b.WriteString("\n")

		for _, m := range t.Members {
			name := memberStyle.Render(fmt.Sprintf("    %-20s", m.Name))
			atype := agentTypeStyle.Render(m.AgentType)
			b.WriteString(fmt.Sprintf("%s %s\n", name, atype))
		}
		b.WriteString("\n")
	}

	return b.String()
}

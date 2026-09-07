package views

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/aimux/internal/config"
)

// EnvironmentsView shows configured environments.
type EnvironmentsView struct {
	names  []string
	envs   map[string]config.EnvironmentConfig
	health map[string]string // name -> status
	width  int
	height int
	cursor int
}

// NewEnvironmentsView creates a new environments view.
func NewEnvironmentsView() *EnvironmentsView {
	return &EnvironmentsView{}
}

// SetEnvironments updates the environments list.
func (v *EnvironmentsView) SetEnvironments(envs map[string]config.EnvironmentConfig) {
	v.envs = envs
	v.names = make([]string, 0, len(envs))
	for name := range envs {
		v.names = append(v.names, name)
	}
	sort.Strings(v.names)
	if v.cursor >= len(v.names) {
		v.cursor = len(v.names) - 1
	}
	if v.cursor < 0 {
		v.cursor = 0
	}
}

// SetHealth updates the health status for environments.
func (v *EnvironmentsView) SetHealth(health map[string]string) {
	v.health = health
}

// SetSize sets the available width and height.
func (v *EnvironmentsView) SetSize(w, h int) {
	v.width = w
	v.height = h
}

// CursorUp moves the cursor up.
func (v *EnvironmentsView) CursorUp() {
	if v.cursor > 0 {
		v.cursor--
	}
}

// CursorDown moves the cursor down.
func (v *EnvironmentsView) CursorDown() {
	if v.cursor < len(v.names)-1 {
		v.cursor++
	}
}

// SelectedName returns the name of the currently selected environment.
func (v *EnvironmentsView) SelectedName() string {
	if v.cursor < 0 || v.cursor >= len(v.names) {
		return ""
	}
	return v.names[v.cursor]
}

// View renders the environments table.
func (v *EnvironmentsView) View() string {
	w := v.width
	if w < 40 {
		w = 80
	}

	var b strings.Builder

	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#E5E7EB"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	selectedStyle := lipgloss.NewStyle().Background(lipgloss.Color("#1E3A5F"))

	b.WriteString(headerStyle.Render("  Environments") + "\n")
	b.WriteString(dimStyle.Render(fmt.Sprintf("  %d configured", len(v.names))) + "\n\n")

	if len(v.names) == 0 {
		b.WriteString(dimStyle.Render("  No environments configured.") + "\n")
		b.WriteString(dimStyle.Render("  Add environments to ~/.aimux/config.yaml") + "\n")
		return b.String()
	}

	colName := 20
	colType := 12
	colStatus := 16
	colDetails := w - colName - colType - colStatus - 16
	if colDetails < 20 {
		colDetails = 20
	}

	tableHeader := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#5F87FF")).
		Background(lipgloss.Color("#1E293B"))

	header := fmt.Sprintf("  %-*s   %-*s   %-*s   %-*s",
		colName, "NAME",
		colType, "TYPE",
		colStatus, "STATUS",
		colDetails, "DETAILS")
	b.WriteString(tableHeader.Render(padToWidth(header, w)) + "\n")

	maxRows := v.height - 6
	if maxRows < 1 {
		maxRows = 10
	}

	start := 0
	if v.cursor >= maxRows {
		start = v.cursor - maxRows + 1
	}
	end := start + maxRows
	if end > len(v.names) {
		end = len(v.names)
	}

	nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#E5E7EB")).Bold(true)
	typeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA"))
	detailStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))

	for i := start; i < end; i++ {
		name := v.names[i]
		ec := v.envs[name]

		status := "configured"
		if v.health != nil {
			if s, ok := v.health[name]; ok {
				status = s
			}
		}

		details := envDetails(ec)

		statusText := status
		if len(statusText) > colStatus {
			statusText = statusText[:colStatus-3] + "..."
		}
		statusColor := lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
		switch status {
		case "ready":
			statusColor = lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E"))
			statusText = "● " + statusText
		case "unreachable":
			statusColor = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))
			statusText = "○ " + statusText
		default:
			statusText = "◐ " + statusText
		}

		row := "  " +
			nameStyle.Render(padToWidth(truncate(name, colName), colName)) + "   " +
			typeStyle.Render(padToWidth(truncate(ec.Type, colType), colType)) + "   " +
			statusColor.Render(padToWidth(truncate(statusText, colStatus), colStatus)) + "   " +
			detailStyle.Render(padToWidth(truncate(details, colDetails), colDetails))

		if i == v.cursor {
			row = selectedStyle.Render(padToWidth(row, w))
		}

		b.WriteString(row + "\n")
	}

	return b.String()
}

func envDetails(ec config.EnvironmentConfig) string {
	var parts []string
	if ec.Gateway != "" {
		parts = append(parts, "gw:"+ec.Gateway)
	}
	if ec.Image != "" {
		parts = append(parts, "img:"+ec.Image)
	}
	if ec.Namespace != "" {
		parts = append(parts, "ns:"+ec.Namespace)
	}
	if ec.RedisURL != "" {
		parts = append(parts, "redis:yes")
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, "  ")
}

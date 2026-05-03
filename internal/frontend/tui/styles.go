package tui

import "github.com/charmbracelet/lipgloss"

// Color constants used throughout the TUI — terminal dashboard palette.
const (
	colorLogo       = lipgloss.Color("#5F87FF")
	colorActive     = lipgloss.Color("#22C55E")
	colorIdle       = lipgloss.Color("#6B7280")
	colorWaiting    = lipgloss.Color("#F59E0B")
	colorError      = lipgloss.Color("#EF4444")
	colorBorder     = lipgloss.Color("#374151")
	colorHeader     = lipgloss.Color("#E5E7EB")
	colorMuted      = lipgloss.Color("#9CA3AF")
	colorCost       = lipgloss.Color("#34D399")
	colorTableHead  = lipgloss.Color("#5F87FF")
	colorSelected   = lipgloss.Color("#1E3A5F")
	colorInfoBox    = lipgloss.Color("#1C1C2E")
	colorInfoBorder = lipgloss.Color("#3B3B5C")
)

// StatusStyle returns a lipgloss style colored for the given status string.
func StatusStyle(status string) lipgloss.Style {
	switch status {
	case "Active":
		return lipgloss.NewStyle().Foreground(colorActive)
	case "Idle":
		return lipgloss.NewStyle().Foreground(colorIdle)
	case "Waiting":
		return lipgloss.NewStyle().Foreground(colorWaiting)
	default:
		return lipgloss.NewStyle().Foreground(colorMuted)
	}
}

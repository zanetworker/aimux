package tui

import "github.com/charmbracelet/lipgloss"

// Color constants used throughout the TUI.
const (
	colorPrimary   = lipgloss.Color("#7C3AED") // Purple
	colorSecondary = lipgloss.Color("#06B6D4") // Cyan
	colorActive    = lipgloss.Color("#22C55E") // Green
	colorIdle      = lipgloss.Color("#6B7280") // Gray
	colorWaiting   = lipgloss.Color("#F59E0B") // Amber
	colorError     = lipgloss.Color("#EF4444") // Red
	colorBorder    = lipgloss.Color("#374151") // Dark gray
	colorHeader    = lipgloss.Color("#E5E7EB") // Light gray
	colorMuted     = lipgloss.Color("#9CA3AF") // Medium gray
	colorCost      = lipgloss.Color("#34D399") // Emerald
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

package views

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	helpTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7C3AED"))
	helpKeyStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#06B6D4"))
	helpDescStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#E5E7EB"))
)

// HelpView renders the help overlay.
type HelpView struct {
	width  int
	height int
}

// NewHelpView creates a new HelpView.
func NewHelpView() *HelpView {
	return &HelpView{}
}

// SetSize sets the available width and height.
func (v *HelpView) SetSize(w, h int) {
	v.width = w
	v.height = h
}

// View renders the help screen.
func (v *HelpView) View() string {
	var b strings.Builder

	b.WriteString(helpTitleStyle.Render("Navigation"))
	b.WriteString("\n")
	writeHelp(&b, "j/k", "Move cursor down/up")
	writeHelp(&b, "Enter", "Zoom into session (interactive PTY)")
	writeHelp(&b, "Ctrl+]", "Zoom out of session")
	writeHelp(&b, "Esc", "Go back")
	writeHelp(&b, "g/G", "Jump to top / bottom")
	writeHelp(&b, "/", "Filter instances")
	writeHelp(&b, "q", "Quit")
	b.WriteString("\n")

	b.WriteString(helpTitleStyle.Render("Commands"))
	b.WriteString("\n")
	writeHelp(&b, ":instances :i", "Instance list")
	writeHelp(&b, ":logs :l", "Log viewer")
	writeHelp(&b, ":session :s", "Session detail")
	writeHelp(&b, ":teams :t", "Teams overview")
	writeHelp(&b, ":costs :c", "Cost dashboard")
	writeHelp(&b, ":new :n", "Launch new instance")
	writeHelp(&b, ":kill", "Kill selected instance")
	writeHelp(&b, ":quit :q", "Quit")
	b.WriteString("\n")

	b.WriteString(helpTitleStyle.Render("Actions"))
	b.WriteString("\n")
	writeHelp(&b, "Enter / J", "Zoom into interactive PTY session")
	writeHelp(&b, "Ctrl+]", "Zoom out (keep session alive)")
	writeHelp(&b, "l", "View conversation trace")
	writeHelp(&b, "d / u", "Page down / up in trace")

	return b.String()
}

func writeHelp(b *strings.Builder, key, desc string) {
	b.WriteString("  ")
	b.WriteString(helpKeyStyle.Render(padRight(key, 16)))
	b.WriteString(helpDescStyle.Render(desc))
	b.WriteString("\n")
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

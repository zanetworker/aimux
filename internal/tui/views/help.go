package views

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	helpTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7C3AED"))
	helpKeyStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#06B6D4"))
	helpDescStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#E5E7EB"))
	helpDimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
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

	activeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E"))
	idleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	waitingStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))

	b.WriteString(helpTitleStyle.Render("Status Icons"))
	b.WriteString("\n")
	writeHelp(&b, activeStyle.Render("●")+" Active", "Processing (activity in last 30s)")
	writeHelp(&b, idleStyle.Render("○")+" Idle", "Waiting for user input")
	writeHelp(&b, waitingStyle.Render("◐")+" Waiting", "Blocked on permission prompt")
	b.WriteString("\n")

	b.WriteString(helpTitleStyle.Render("Agent List"))
	b.WriteString("\n")
	writeHelp(&b, "j/k", "Move cursor down/up")
	writeHelp(&b, "Enter", "Open split view (trace + session)")
	writeHelp(&b, "t", "Open trace viewer (full screen)")
	writeHelp(&b, "c", "Cost dashboard")
	writeHelp(&b, "T", "Teams overview")
	writeHelp(&b, ":new / :n", "Launch new agent (provider, dir, options)")
	writeHelp(&b, "x", "Kill selected agent (with confirmation)")
	writeHelp(&b, "s", "Sort: cycle Name/Cost/Age/Model/PID")
	writeHelp(&b, "/", "Filter agents by name, dir, model")
	writeHelp(&b, "Esc", "Clear filter / go back")
	writeHelp(&b, "g/G", "Jump to top / bottom")
	writeHelp(&b, "?", "This help screen")
	writeHelp(&b, "q", "Quit aimux")
	b.WriteString("\n")

	b.WriteString(helpTitleStyle.Render("Split View (Enter on agent)"))
	b.WriteString("\n")
	writeHelp(&b, "Tab", "Switch focus: TRACE <-> SESSION")
	writeHelp(&b, "Ctrl+f", "Toggle fullscreen / split view")
	writeHelp(&b, "Ctrl+g", "Exit back to agent list")
	b.WriteString(helpDimStyle.Render("  When TRACE focused: j/k, Enter, +, / work on turns\n"))
	b.WriteString(helpDimStyle.Render("  When SESSION focused: all keys go to the agent\n"))
	b.WriteString("\n")

	b.WriteString(helpTitleStyle.Render("Trace Viewer"))
	b.WriteString("\n")
	writeHelp(&b, "Enter", "Expand/collapse turn")
	writeHelp(&b, "Space / n / p", "Next / next / previous turn")
	b.WriteString(helpDimStyle.Render("  Scrolling within expanded turn:\n"))
	writeHelp(&b, "j / k", "1 line down / up")
	writeHelp(&b, "J / K", "10 lines down / up")
	writeHelp(&b, "d / u", "Half page down / up")
	writeHelp(&b, "g / G", "First / last turn")
	writeHelp(&b, "c", "Collapse all turns")
	writeHelp(&b, "/", "Search turns by keyword, Esc to clear")
	b.WriteString("\n")

	b.WriteString(helpTitleStyle.Render("Annotations (in trace viewer)"))
	b.WriteString("\n")
	writeHelp(&b, "a", "Annotate: cycle GOOD -> BAD -> WASTE -> remove")
	b.WriteString(helpDimStyle.Render("  Labels appear as colored badges in the turn header.\n"))
	b.WriteString(helpDimStyle.Render("  Annotations auto-save to ~/.aimux/evaluations/\n"))
	b.WriteString(helpDimStyle.Render("  Use :export to write all turns + labels as JSONL.\n"))
	b.WriteString("\n")

	b.WriteString(helpTitleStyle.Render("Commands"))
	b.WriteString("\n")
	writeHelp(&b, ":new / :n", "Launch new agent")
	writeHelp(&b, ":export", "Export trace + annotations as JSONL")
	writeHelp(&b, ":instances :i", "Agent list")
	writeHelp(&b, ":logs :l", "Trace viewer")
	writeHelp(&b, ":teams :t", "Teams overview")
	writeHelp(&b, ":costs :c", "Cost dashboard")
	writeHelp(&b, ":quit :q", "Quit")
	b.WriteString("\n")

	b.WriteString(helpTitleStyle.Render("Providers"))
	b.WriteString("\n")
	writeHelp(&b, "Claude", "Full support (discover, resume, trace, eval)")
	writeHelp(&b, "Codex", "Discovery, resume, session files")
	writeHelp(&b, "Gemini", "Planned")

	return b.String()
}

func writeHelp(b *strings.Builder, key, desc string) {
	b.WriteString("  ")
	b.WriteString(helpKeyStyle.Render(padRight(key, 18)))
	b.WriteString(helpDescStyle.Render(desc))
	b.WriteString("\n")
}

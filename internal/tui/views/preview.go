package views

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/agentmux/internal/agent"
)

var (
	previewBorderStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#374151"))
	previewHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#E5E7EB")).
				Background(lipgloss.Color("#1E293B"))
	previewLabelStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#9CA3AF"))
	previewValueStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#E5E7EB")).
				Bold(true)
	previewDimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280"))
)

// PreviewPane renders a read-only conversation preview for the right side of a
// split pane layout. It shows the conversation trace of the currently selected
// agent, parsed from its session JSONL file.
type PreviewPane struct {
	agent    *agent.Agent
	logsView *LogsView
	width    int
	height   int
}

// NewPreviewPane creates a new preview pane.
func NewPreviewPane() *PreviewPane {
	return &PreviewPane{}
}

// SetAgent updates the agent whose conversation is displayed. It reloads the
// conversation from the agent's SessionFile only if the agent changed.
// If the agent is nil or has no SessionFile, the pane shows a placeholder.
func (p *PreviewPane) SetAgent(a *agent.Agent) {
	if a == nil {
		p.agent = nil
		p.logsView = nil
		return
	}
	// Skip reload if same agent (by PID)
	if p.agent != nil && p.agent.PID == a.PID {
		return
	}
	p.agent = a
	if a.SessionFile == "" {
		p.logsView = nil
		return
	}
	p.logsView = NewLogsView(a.PID, a.SessionFile)
	p.resizeLogsView()
}

// Reload re-reads the session file and refreshes the conversation trace.
func (p *PreviewPane) Reload() {
	if p.logsView != nil {
		p.logsView.Reload()
	}
}

// SetSize sets the available width and height for the preview pane.
func (p *PreviewPane) SetSize(w, h int) {
	p.width = w
	p.height = h
	p.resizeLogsView()
}

func (p *PreviewPane) resizeLogsView() {
	if p.logsView == nil {
		return
	}
	// Reserve 4 lines for header (agent info) and 1 line for the border char
	contentHeight := p.height - 4
	if contentHeight < 1 {
		contentHeight = 1
	}
	// Subtract 2 from width for left border padding
	contentWidth := p.width - 2
	if contentWidth < 1 {
		contentWidth = 1
	}
	p.logsView.SetSize(contentWidth, contentHeight)
}

// View renders the preview pane with a left border, header, and conversation.
func (p *PreviewPane) View() string {
	if p.width < 4 {
		return ""
	}

	var b strings.Builder

	// Left border character for visual separation
	border := previewBorderStyle.Render("│")

	// Header
	header := p.renderHeader()
	for _, line := range strings.Split(header, "\n") {
		b.WriteString(border + " " + line + "\n")
	}

	// Content
	if p.logsView == nil {
		emptyMsg := previewDimStyle.Render("No conversation data")
		b.WriteString(border + " " + emptyMsg + "\n")
		// Fill remaining height with empty bordered lines
		usedLines := strings.Count(header, "\n") + 2
		remaining := p.height - usedLines
		for i := 0; i < remaining; i++ {
			b.WriteString(border + "\n")
		}
	} else {
		content := p.logsView.View()
		for _, line := range strings.Split(content, "\n") {
			b.WriteString(border + " " + line + "\n")
		}
	}

	return b.String()
}

func (p *PreviewPane) renderHeader() string {
	if p.agent == nil {
		return previewDimStyle.Render("No agent selected")
	}

	a := p.agent
	maxW := p.width - 3 // account for border + padding
	if maxW < 1 {
		maxW = 1
	}

	// Agent name line
	name := a.ShortProject()
	if name == "" {
		name = "(unknown)"
	}
	nameLine := previewHeaderStyle.Render(truncatePreview(name, maxW))

	// Info line: provider | model | mode
	var infoParts []string
	if a.ProviderName != "" {
		infoParts = append(infoParts, previewLabelStyle.Render("Provider: ")+previewValueStyle.Render(a.ProviderName))
	}
	if a.Model != "" {
		infoParts = append(infoParts, previewLabelStyle.Render("Model: ")+previewValueStyle.Render(a.ShortModel()))
	}
	if a.PermissionMode != "" {
		infoParts = append(infoParts, previewLabelStyle.Render("Mode: ")+previewValueStyle.Render(a.PermissionMode))
	}

	infoLine := strings.Join(infoParts, "  ")

	// Status line
	statusIcon := a.Icon()
	statusText := a.Status.String()
	statusLine := fmt.Sprintf("%s %s  %s  %s",
		statusIcon,
		statusText,
		previewLabelStyle.Render("Age: ")+previewValueStyle.Render(a.FormatAge()),
		previewLabelStyle.Render("Cost: ")+previewValueStyle.Render(a.FormatCost()),
	)

	separator := previewBorderStyle.Render(strings.Repeat("─", maxW))

	return nameLine + "\n" + infoLine + "\n" + statusLine + "\n" + separator
}

func truncatePreview(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

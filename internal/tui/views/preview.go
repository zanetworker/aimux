package views

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/aimux/internal/agent"
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

	// Preview-specific accent styles
	previewSuccessStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E"))
	previewFailStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))
	previewToolStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))
	previewTokenBarStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#06B6D4"))
)

// PreviewPane renders a read-only conversation preview for the right side of a
// split pane layout. It shows the conversation trace of the currently selected
// agent, parsed from its session JSONL file.
type PreviewPane struct {
	agent    *agent.Agent
	logsView *LogsView
	parser   TraceParser // parser function set by app.go for the current agent
	width    int
	height   int
}

// NewPreviewPane creates a new preview pane.
func NewPreviewPane() *PreviewPane {
	return &PreviewPane{}
}

// SetParser updates the trace parser used for creating LogsViews.
// Called by app.go when the selected agent changes and the provider's
// ParseTrace method should be used.
func (p *PreviewPane) SetParser(parser TraceParser) {
	p.parser = parser
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
	if a.SessionFile == "" || p.parser == nil {
		p.logsView = nil
		return
	}
	p.logsView = NewLogsView(a.PID, a.SessionFile, p.parser)
	p.logsView.SetSessionCost(a.EstCostUSD)
	p.logsView.compact = true // no interactive hints in preview
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
	// Reserve 10 lines for header (agent info, tokens, actions) and 1 line for the border char
	contentHeight := p.height - 10
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

	// Dir line
	var dirLine string
	if a.WorkingDir != "" {
		dirLine = previewLabelStyle.Render("Dir: ") + previewValueStyle.Render(truncatePreview(a.WorkingDir, maxW-5))
	}

	// Source line
	var sourceLine string
	if a.Source.String() != "CLI" {
		sourceLine = previewLabelStyle.Render("Source: ") + previewValueStyle.Render(a.Source.String())
	}

	// Grouped processes section
	var groupLines string
	if a.GroupCount > 1 {
		groupStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true)
		pidStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#5F87FF"))
		groupLines = groupStyle.Render(fmt.Sprintf("  %d Grouped Processes", a.GroupCount)) + "\n"
		for i, pid := range a.GroupPIDs {
			if i >= 6 {
				groupLines += previewDimStyle.Render(fmt.Sprintf("  ... and %d more", len(a.GroupPIDs)-6)) + "\n"
				break
			}
			cmdInfo := processInfo(pid)
			groupLines += pidStyle.Render(fmt.Sprintf("  PID %d", pid))
			if cmdInfo != "" {
				groupLines += previewDimStyle.Render("  " + cmdInfo)
			}
			groupLines += "\n"
		}
	}

	// Git branch line
	var branchLine string
	if a.GitBranch != "" {
		branchLine = previewLabelStyle.Render("Branch: ") + previewValueStyle.Render(a.GitBranch)
	}

	// Memory line
	var memLine string
	if a.MemoryMB > 0 {
		memLine = previewLabelStyle.Render("Mem: ") + previewValueStyle.Render(a.FormatMemory())
	}

	// Token bar line
	var tokenLine string
	if a.TokensIn > 0 || a.TokensOut > 0 {
		inBar := renderTokenBar("IN ", a.TokensIn, 200000, 10)
		outBar := renderTokenBar("OUT", a.TokensOut, 200000, 10)
		tokenLine = inBar + "    " + outBar
	}

	// Session ID line
	var sessionLine string
	if a.SessionID != "" {
		sid := a.SessionID
		if len(sid) > 12 {
			sid = sid[:12] + "..."
		}
		sessionLine = previewLabelStyle.Render("Session: ") + previewDimStyle.Render(sid)
	}

	// Tmux line
	var tmuxLine string
	if a.TMuxSession != "" {
		tmuxLine = previewLabelStyle.Render("Tmux: ") + previewValueStyle.Render(a.TMuxSession)
	}

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

	result := nameLine + "\n" + infoLine + "\n"
	if dirLine != "" {
		result += dirLine + "\n"
	}
	if sourceLine != "" {
		result += sourceLine + "\n"
	}
	if branchLine != "" {
		result += branchLine + "\n"
	}
	// Combine memory, session, tmux on concise lines
	var extraParts []string
	if memLine != "" {
		extraParts = append(extraParts, memLine)
	}
	if sessionLine != "" {
		extraParts = append(extraParts, sessionLine)
	}
	if tmuxLine != "" {
		extraParts = append(extraParts, tmuxLine)
	}
	if len(extraParts) > 0 {
		result += strings.Join(extraParts, "  ") + "\n"
	}
	if tokenLine != "" {
		result += tokenLine + "\n"
	}
	result += statusLine + "\n"

	// Grouped processes
	if groupLines != "" {
		result += groupLines
	}

	// Recent Actions section
	actionsSection := p.renderRecentActions()
	if actionsSection != "" {
		result += actionsSection + "\n"
	}

	result += separator
	return result
}

// renderRecentActions returns the last 3 tool calls from the session trace.
func (p *PreviewPane) renderRecentActions() string {
	if p.logsView == nil {
		return ""
	}
	turns := p.logsView.Turns()
	if len(turns) == 0 {
		return ""
	}

	// Collect up to 3 tool spans from the last turns backwards
	var actions []ToolSpan
	for i := len(turns) - 1; i >= 0 && len(actions) < 3; i-- {
		t := turns[i]
		for j := len(t.Actions) - 1; j >= 0 && len(actions) < 3; j-- {
			actions = append(actions, t.Actions[j])
		}
	}

	if len(actions) == 0 {
		return ""
	}

	var lines []string
	lines = append(lines, previewLabelStyle.Render("Recent Actions"))
	for _, action := range actions {
		var indicator string
		if action.Success {
			indicator = previewSuccessStyle.Render("✓")
		} else {
			indicator = previewFailStyle.Render("✗")
		}
		toolName := previewToolStyle.Render(padRight(action.Name, 6))
		snippet := action.Snippet
		if len(snippet) > 40 {
			snippet = snippet[:37] + "..."
		}
		line := fmt.Sprintf("  %s %s %s", indicator, toolName, previewDimStyle.Render(snippet))
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// renderTokenBar creates a visual bar showing token usage relative to a maximum.
func renderTokenBar(label string, tokens int64, maxTokens int64, width int) string {
	if maxTokens <= 0 {
		maxTokens = 200000
	}
	ratio := float64(tokens) / float64(maxTokens)
	if ratio > 1 {
		ratio = 1
	}
	filled := int(ratio * float64(width))
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	return fmt.Sprintf("%s %s %s",
		previewTokenBarStyle.Render(label),
		bar,
		previewValueStyle.Render(formatTokens(tokens)),
	)
}

// processInfo returns a short description of a process by PID: its command
// name and RSS memory. Returns "" if the process can't be inspected.
func processInfo(pid int) string {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "rss=,comm=").Output()
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(out))
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return ""
	}
	rss, _ := strconv.ParseUint(fields[0], 10, 64)
	cmd := fields[len(fields)-1]
	// Show just the binary basename
	parts := strings.Split(cmd, "/")
	binary := parts[len(parts)-1]

	if rss > 0 {
		mb := rss / 1024
		return fmt.Sprintf("%s (%dMB)", binary, mb)
	}
	return binary
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

package views

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/diff"
	"github.com/zanetworker/aimux/internal/history"
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
	agent         *agent.Agent
	logsView      *LogsView
	parser        TraceParser // parser function set by app.go for the current agent
	width         int
	height        int
	cachedPodLogs string // cached kubectl logs output
	cachedPodPID  int    // PID (or 0) the cached logs belong to
	diffStat       string   // cached git diff --stat output
	diffFull       string   // cached git diff output (for expanded view)
	diffExpanded   bool     // true when showing full diff instead of trace
	diffScroll     int      // scroll position within full diff view
	diffPickerMode bool     // true when showing file picker
	diffFiles      []string // list of changed files
	diffFileCursor int      // cursor position in file picker
	diffMu         sync.Mutex
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

	// Reset diff state when agent changes
	p.diffStat = ""
	p.diffFull = ""
	p.diffExpanded = false
	p.diffScroll = 0

	// Fetch diff stat if working dir is a local git repo
	if a.WorkingDir != "" && !strings.HasPrefix(a.WorkingDir, "k8s://") {
		go func(cwd string) {
			stat, _ := diff.GetDiffStat(cwd)
			p.diffMu.Lock()
			p.diffStat = stat
			p.diffMu.Unlock()
		}(a.WorkingDir)
	}

	// Fetch pod error logs once on agent change (not on every render)
	pid := a.PID
	if a.Status == agent.StatusError && strings.HasPrefix(a.WorkingDir, "k8s://") && p.cachedPodPID != pid {
		p.cachedPodLogs = fetchPodErrorLogs(a)
		p.cachedPodPID = pid
	} else if !strings.HasPrefix(a.WorkingDir, "k8s://") || a.Status != agent.StatusError {
		p.cachedPodLogs = ""
		p.cachedPodPID = 0
	}
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
	// Reserve 11 lines for header (agent info, title, tokens, actions) and 1 line for the border char
	contentHeight := p.height - 11
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

	// Diff summary (compact, between header and content)
	p.diffMu.Lock()
	compact := diff.FormatCompact(p.diffStat)
	diffExpanded := p.diffExpanded
	diffFull := p.diffFull
	diffScroll := p.diffScroll
	diffPickerMode := p.diffPickerMode
	diffFiles := append([]string{}, p.diffFiles...)
	diffFileCursor := p.diffFileCursor
	p.diffMu.Unlock()

	if compact != "" && !diffExpanded && !diffPickerMode {
		diffStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E"))
		for _, line := range strings.Split(strings.TrimSpace(compact), "\n") {
			b.WriteString(border + " " + diffStyle.Render(line) + "\n")
		}
	}

	// File picker mode: show navigable file list
	if diffPickerMode {
		titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E")).Bold(true)
		hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
		selectedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#E5E7EB")).Bold(true).Background(lipgloss.Color("#374151"))
		normalStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E"))

		b.WriteString(border + " " + titleStyle.Render("Select file to diff:") + "\n")
		for i, file := range diffFiles {
			prefix := "  "
			style := normalStyle
			if i == diffFileCursor {
				prefix = "> "
				style = selectedStyle
			}
			b.WriteString(border + " " + style.Render(prefix+"M "+file) + "\n")
		}
		b.WriteString(border + " " + hintStyle.Render("j/k:navigate  Enter:view  Esc:close") + "\n")

		contentHeight := p.height - strings.Count(b.String(), "\n") - 1
		for i := 0; i < contentHeight && i < 20; i++ {
			b.WriteString(border + "\n")
		}
	} else if diffExpanded && diffFull != "" {
		// Render expanded diff
		diffLines := strings.Split(diffFull, "\n")
		contentHeight := p.height - strings.Count(header, "\n") - 2
		if contentHeight < 1 {
			contentHeight = 1
		}
		visibleStart := diffScroll
		visibleEnd := diffScroll + contentHeight
		if visibleEnd > len(diffLines) {
			visibleEnd = len(diffLines)
		}
		if visibleStart >= len(diffLines) {
			visibleStart = 0
		}

		for i := visibleStart; i < visibleEnd; i++ {
			line := diffLines[i]
			styledLine := p.styleDiffLine(line)
			b.WriteString(border + " " + styledLine + "\n")
		}

		// Fill remaining lines with empty borders
		rendered := visibleEnd - visibleStart
		for i := rendered; i < contentHeight; i++ {
			b.WriteString(border + "\n")
		}
	} else if p.logsView == nil {
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

	// Session title (from LLM-generated meta)
	var titleLine string
	if a.SessionFile != "" {
		title := history.TitleForSessionFile(a.SessionFile)
		if title != "" {
			titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#06B6D4")).Italic(true)
			titleLine = titleStyle.Render(truncatePreview(title, maxW))
		}
	}

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

	// Error banner — shown prominently when agent is in error state
	var errorBanner string
	if a.Status == agent.StatusError && a.LastAction != "" {
		errorStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(lipgloss.Color("#991B1B")).
			Bold(true).
			Padding(0, 1)
		errorBanner = errorStyle.Render("ERROR: " + a.LastAction)
	}

	separator := previewBorderStyle.Render(strings.Repeat("─", maxW))

	result := nameLine + "\n"
	if titleLine != "" {
		result += titleLine + "\n"
	}
	result += infoLine + "\n"
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

	// Error banner with cached pod logs for K8s agents
	if errorBanner != "" {
		result += errorBanner + "\n"
		if p.cachedPodLogs != "" {
			logStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FCA5A5"))
			result += logStyle.Render(p.cachedPodLogs) + "\n"
		}
	}

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

// fetchPodErrorLogs gets the last few lines of init container logs for a K8s pod.
// Returns empty string if logs can't be fetched (non-blocking, 2s timeout).
func fetchPodErrorLogs(a *agent.Agent) string {
	parts := strings.SplitN(strings.TrimPrefix(a.WorkingDir, "k8s://"), "/", 2)
	if len(parts) != 2 {
		return ""
	}
	namespace := parts[0]
	podName := parts[1]

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Try init container logs first (most common failure point)
	out, err := exec.CommandContext(ctx, "kubectl", "logs", podName,
		"-n", namespace,
		"--init-container=clone-repo",
		"--tail=5",
	).CombinedOutput()
	if err == nil && len(out) > 0 {
		return strings.TrimSpace(string(out))
	}

	// Fall back to main container logs
	out, err = exec.CommandContext(ctx, "kubectl", "logs", podName,
		"-n", namespace,
		"--tail=5",
	).CombinedOutput()
	if err == nil && len(out) > 0 {
		return strings.TrimSpace(string(out))
	}
	return ""
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

// ToggleDiff cycles through diff states: trace -> file picker -> trace.
// When in file picker, Enter selects a file. When viewing a file diff, Esc
// returns to file picker.
func (p *PreviewPane) ToggleDiff() {
	p.diffMu.Lock()
	defer p.diffMu.Unlock()

	if p.diffExpanded {
		// Viewing a file diff -> back to trace
		p.diffExpanded = false
		p.diffPickerMode = false
		p.diffScroll = 0
		return
	}

	if p.diffPickerMode {
		// File picker visible -> close it
		p.diffPickerMode = false
		return
	}

	// Trace view -> open file picker
	if p.agent != nil && p.agent.WorkingDir != "" && !strings.HasPrefix(p.agent.WorkingDir, "k8s://") {
		files, _ := diff.ListChangedFiles(p.agent.WorkingDir)
		if len(files) > 0 {
			p.diffFiles = files
			p.diffFileCursor = 0
			p.diffPickerMode = true
			return
		}
	}
}

// DiffPickerSelect selects the currently highlighted file in the picker
// and switches to showing that file's diff.
func (p *PreviewPane) DiffPickerSelect() {
	p.diffMu.Lock()
	defer p.diffMu.Unlock()

	if !p.diffPickerMode || len(p.diffFiles) == 0 {
		return
	}

	file := p.diffFiles[p.diffFileCursor]
	fileDiff, _ := diff.GetFileDiff(p.agent.WorkingDir, file)
	if fileDiff != "" {
		p.diffFull = fileDiff
		p.diffExpanded = true
		p.diffPickerMode = false
		p.diffScroll = 0
	}
}

// DiffPickerUp moves the file picker cursor up.
func (p *PreviewPane) DiffPickerUp() {
	p.diffMu.Lock()
	defer p.diffMu.Unlock()
	if p.diffFileCursor > 0 {
		p.diffFileCursor--
	}
}

// DiffPickerDown moves the file picker cursor down.
func (p *PreviewPane) DiffPickerDown() {
	p.diffMu.Lock()
	defer p.diffMu.Unlock()
	if p.diffFileCursor < len(p.diffFiles)-1 {
		p.diffFileCursor++
	}
}

// DiffPickerBack returns from file diff view to the file picker.
func (p *PreviewPane) DiffPickerBack() {
	p.diffMu.Lock()
	defer p.diffMu.Unlock()
	if p.diffExpanded {
		p.diffExpanded = false
		p.diffScroll = 0
		p.diffPickerMode = true
		return
	}
	if p.diffPickerMode {
		p.diffPickerMode = false
	}
}

// TraceScrollDown scrolls the preview trace down one turn.
func (p *PreviewPane) TraceScrollDown() {
	if p.logsView != nil {
		p.logsView.ScrollCursorDown()
	}
}

// TraceScrollUp scrolls the preview trace up one turn.
func (p *PreviewPane) TraceScrollUp() {
	if p.logsView != nil {
		p.logsView.ScrollCursorUp()
	}
}

// TraceIsAtBottom returns true if the trace cursor is at the last turn.
func (p *PreviewPane) TraceIsAtBottom() bool {
	if p.logsView == nil {
		return true
	}
	return p.logsView.IsAtBottom()
}

// TraceIsAtTop returns true if the trace cursor is at the first turn.
func (p *PreviewPane) TraceIsAtTop() bool {
	if p.logsView == nil {
		return true
	}
	return p.logsView.IsAtTop()
}

// HasDiffChanges returns true if the agent's CWD has uncommitted git changes.
func (p *PreviewPane) HasDiffChanges() bool {
	p.diffMu.Lock()
	defer p.diffMu.Unlock()
	return p.diffStat != ""
}

// IsDiffPickerMode returns true if the file picker is active.
func (p *PreviewPane) IsDiffPickerMode() bool {
	p.diffMu.Lock()
	defer p.diffMu.Unlock()
	return p.diffPickerMode
}

// ScrollDiff scrolls the expanded diff view by the given number of lines.
// Positive values scroll down, negative values scroll up.
func (p *PreviewPane) ScrollDiff(n int) {
	p.diffMu.Lock()
	defer p.diffMu.Unlock()

	if !p.diffExpanded || p.diffFull == "" {
		return
	}

	diffLines := strings.Split(p.diffFull, "\n")
	p.diffScroll += n

	if p.diffScroll < 0 {
		p.diffScroll = 0
	}
	maxScroll := len(diffLines) - 1
	if maxScroll < 0 {
		maxScroll = 0
	}
	if p.diffScroll > maxScroll {
		p.diffScroll = maxScroll
	}
}

// IsDiffExpanded returns true if the diff view is currently expanded.
func (p *PreviewPane) IsDiffExpanded() bool {
	p.diffMu.Lock()
	defer p.diffMu.Unlock()
	return p.diffExpanded
}

// styleDiffLine applies syntax highlighting to a diff line.
func (p *PreviewPane) styleDiffLine(line string) string {
	if strings.HasPrefix(line, "+") {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E")).Render(line)
	}
	if strings.HasPrefix(line, "-") {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Render(line)
	}
	if strings.HasPrefix(line, "@@") {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#06B6D4")).Render(line)
	}
	if strings.HasPrefix(line, "diff ") || strings.HasPrefix(line, "index ") {
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#E5E7EB")).Render(line)
	}
	return previewDimStyle.Render(line)
}

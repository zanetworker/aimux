package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (a App) View() string {
	if a.width == 0 {
		return "Loading..."
	}

	// Zoomed modes — no header, no outer status bar.
	if a.zoomed && a.sessionView != nil && a.sessionView.Active() {
		if a.splitMode {
			return a.renderSplitView()
		}
		// Full-screen whichever pane was focused
		if a.splitFocus == "trace" && a.splitTrace != nil {
			return a.splitTrace.View()
		}
		return a.sessionView.View()
	}

	// Set contextual hints based on current view
	switch a.currentView {
	case viewAgents:
		a.headerView.SetHint("Enter:open  a:attend  *:pin  B:starred  t:traces  c:costs  T:tasks  S:sessions  P:plugins  H:health  C:copy-id  d:diff  :new:launch  x:kill  s:sort  /:filter  ?:help")
	case viewLogs:
		a.headerView.SetHint("j/k:scroll  Enter:expand  a:annotate  N:note  *:pin  C:copy-id  $:costs  :export  :export-otel  Esc:back")
	case viewCosts:
		a.headerView.SetHint("Esc:back  ?:help")
	case viewTeams:
		a.headerView.SetHint("Esc:back  ?:help")
	case viewTasks:
		a.headerView.SetHint("j/k:nav  g/G:top/bottom  :new:create  Esc:back")
	case viewSessions:
		hint := "j/k:nav  Enter:resume  B:toggle-perms  *:pin  t:titles  C:copy-id  P:path-filter  F:find-content  s:sort  /:filter  A:all  a:annotate  f:failure-mode  N:note  R:roi  I:roi-detail  d:delete  D:cleanup  p:preview"
		if a.sessionsView.ShowSubagents() {
			hint += "  H:hide-agents"
		} else {
			hint += "  H:show-agents"
		}
		hint += "  Esc:back"
		a.headerView.SetHint(hint)
	case viewStarred:
		a.headerView.SetHint("j/k:nav  Enter:resume  *:unpin  C:copy-id  /:filter  s:sort  p:preview  Esc:back")
	case viewHealth:
		a.headerView.SetHint("Esc:back  :health to refresh")
	case viewPlugin:
		a.headerView.SetHint("j/k:scroll  d/u:page  r:refresh  Esc:back")
	case viewHelp:
		a.headerView.SetHint("Esc:back  q:quit")
	}

	header := a.headerView.View()
	headerHeight := a.headerView.Height()
	contentHeight := a.layout.ContentHeight(headerHeight)

	var content string
	switch a.currentView {
	case viewAgents:
		leftW, rightW := a.layout.SplitVertical(35)
		a.agentsView.SetSize(leftW, contentHeight)
		a.previewPane.SetSize(rightW, contentHeight)

		// Update preview with currently selected agent
		selected := a.agentsView.Selected()
		a.previewPane.SetAgent(selected)

		content = lipgloss.JoinHorizontal(lipgloss.Top,
			a.agentsView.View(),
			a.previewPane.View(),
		)
	case viewLogs:
		if a.logsView != nil {
			content = a.logsView.View()
		} else {
			content = "  No logs available"
		}
	case viewCosts:
		content = a.costsView.View()
	case viewTeams:
		content = a.teamsView.View()
	case viewTasks:
		a.tasksView.SetSize(a.width, contentHeight)
		content = a.tasksView.View()
	case viewSessions:
		a.sessionsView.SetSize(a.width, contentHeight)
		content = a.sessionsView.View()
	case viewStarred:
		a.starredView.SetSize(a.width, contentHeight)
		content = a.starredView.View()
	case viewHealth:
		a.healthView.SetSize(a.width, contentHeight)
		content = a.healthView.View()
	case viewPlugin:
		if a.pluginView != nil {
			a.pluginView.SetSize(a.width, contentHeight)
			content = a.pluginView.View()
		} else {
			content = "  No plugin selected"
		}
	case viewHelp:
		content = a.helpView.View()
	}

	statusBar := a.renderStatusBar()

	// Fit content to exact available height: pad if short, truncate if long
	availableHeight := a.height - headerHeight - 1
	if availableHeight < 1 {
		availableHeight = 1
	}
	lines := strings.Split(content, "\n")
	if len(lines) > availableHeight {
		lines = lines[:availableHeight]
	}
	for len(lines) < availableHeight {
		lines = append(lines, "")
	}
	content = strings.Join(lines, "\n")

	result := header + "\n" + content + "\n" + statusBar

	// Overlay the launcher if active
	if a.launcherActive && a.launcherView != nil {
		a.launcherView.SetSize(a.width, a.height)
		return a.launcherView.View()
	}

	// Overlay the plugin picker if active
	if a.pluginPickerMode && a.pluginPicker != nil {
		a.pluginPicker.SetSize(a.width, a.height)
		return header + "\n" + a.pluginPicker.View()
	}

	return result
}

// renderSplitView renders the split layout: live trace (left) + session (right).

func (a App) renderSplitView() string {
	leftW := a.width * 40 / 100
	rightW := a.width - leftW - 1 // -1 for divider

	contentH := a.height - 1 // reserve 1 for status bar

	// Resize panes
	if a.splitTrace != nil {
		a.splitTrace.SetSize(leftW, contentH-2) // -2 for trace header + status
	}
	a.sessionView.SetSize(rightW, contentH)

	// Styles for pane headers
	focusedHeaderStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#111827")).
		Background(lipgloss.Color("#5F87FF"))
	unfocusedHeaderStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#9CA3AF")).
		Background(lipgloss.Color("#1E293B"))
	dividerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#374151"))

	// Left pane: trace
	var leftLines []string
	traceHeaderStyle := unfocusedHeaderStyle
	if a.splitFocus == "trace" {
		traceHeaderStyle = focusedHeaderStyle
	}
	// Show data source indicator in trace header
	traceLabel := " TRACE [FILE] "
	if a.sessionView != nil && a.sessionView.Agent() != nil && a.sessionView.Agent().Location == "remote" {
		traceLabel = " TRACE [OTEL] "
		if a.otelReceiver != nil {
			_, logs, _ := a.otelReceiver.Stats()
			if logs > 0 {
				traceLabel = fmt.Sprintf(" TRACE [OTEL] (%d spans) ", logs)
			}
		}
	} else if a.otelReceiver != nil {
		_, logs, _ := a.otelReceiver.Stats()
		if logs > 0 {
			traceLabel = fmt.Sprintf(" TRACE [FILE] (otel:%d) ", logs)
		}
	}
	traceHeader := traceHeaderStyle.Render(padRight(traceLabel, leftW))
	leftLines = append(leftLines, traceHeader)

	if a.splitTrace != nil {
		traceContent := a.splitTrace.View()
		leftLines = append(leftLines, strings.Split(traceContent, "\n")...)
	} else {
		leftLines = append(leftLines, lipgloss.NewStyle().Foreground(colorMuted).Render("  No trace data"))
	}

	// Pad left pane to fill height
	for len(leftLines) < contentH {
		leftLines = append(leftLines, "")
	}
	if len(leftLines) > contentH {
		leftLines = leftLines[:contentH]
	}

	// Right pane: session (rendered by SessionView with its own header/status)
	var sessionContent string
	if a.splitLoading {
		// Show loading placeholder while session is connecting
		sessionContent = lipgloss.Place(
			rightW, contentH,
			lipgloss.Center, lipgloss.Center,
			lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Render("Loading session..."),
		)
	} else {
		sessionContent = a.sessionView.View()
	}
	rightLines := strings.Split(sessionContent, "\n")
	// Replace session header with our focused/unfocused version
	sessionHeaderStyle := unfocusedHeaderStyle
	if a.splitFocus == "session" {
		sessionHeaderStyle = focusedHeaderStyle
	}
	agentName := "(session)"
	if a.sessionView.Agent() != nil {
		agentName = a.sessionView.Agent().ShortProject()
	}
	rightLines[0] = sessionHeaderStyle.Render(padRight(" SESSION: "+agentName+" ", rightW))

	// Pad right pane
	for len(rightLines) < contentH {
		rightLines = append(rightLines, "")
	}
	if len(rightLines) > contentH {
		rightLines = rightLines[:contentH]
	}

	// Join left and right with divider
	divider := dividerStyle.Render("│")
	var b strings.Builder
	for i := 0; i < contentH; i++ {
		left := leftLines[i]
		right := ""
		if i < len(rightLines) {
			right = rightLines[i]
		}
		// Pad left to exact width
		leftPad := leftW - lipgloss.Width(left)
		if leftPad > 0 {
			left += strings.Repeat(" ", leftPad)
		}
		b.WriteString(left)
		b.WriteString(divider)
		b.WriteString(right)
		b.WriteString("\n")
	}

	// Status bar
	badge := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#111827")).
		Background(lipgloss.Color("#5F87FF")).
		Render(" aimux ")
	focus := a.splitFocus
	hintStyle := lipgloss.NewStyle().Foreground(colorMuted)
	var focusHint string
	if a.statusHint != "" {
		// Show export menu or other status messages
		focusHint = " " + a.statusHint
	} else if a.commandMode {
		focusHint = " :" + a.commandInput.BeforeCursor() + "█" + a.commandInput.AfterCursor()
	} else if focus == "trace" && a.splitTrace != nil && a.splitTrace.NoteMode() {
		noteText, noteTurn := a.splitTrace.NoteInput()
		noteStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true)
		focusHint = noteStyle.Render(fmt.Sprintf(" Note [Turn %d]: ", noteTurn)) + noteText + noteStyle.Render("|")
	} else if focus == "trace" {
		focusHint = " [TRACE] j/k:turns  a:annotate  N:note  $:costs  e:export"
	} else {
		focusHint = " [SESSION] typing goes to agent"
	}
	hints := hintStyle.Render(focusHint + "  Tab:switch  Ctrl+b:toggle-perms  Ctrl+f:fullscreen  Esc:exit")
	statusGap := a.width - lipgloss.Width(badge) - lipgloss.Width(hints)
	if statusGap < 0 {
		statusGap = 0
	}
	b.WriteString(badge + hints + strings.Repeat(" ", statusGap))

	return b.String()
}

func padRight(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

func (a App) renderStatusBar() string {
	if a.commandMode {
		return lipgloss.NewStyle().
			Background(lipgloss.Color("#111827")).
			Width(a.width).
			Render(lipgloss.NewStyle().
				Foreground(colorLogo).
				Bold(true).
				Render(" :") + a.commandInput.BeforeCursor() + lipgloss.NewStyle().
				Foreground(colorLogo).Render("█") + a.commandInput.AfterCursor())
	}
	if a.filterMode {
		return lipgloss.NewStyle().
			Background(lipgloss.Color("#111827")).
			Width(a.width).
			Render(lipgloss.NewStyle().
				Foreground(colorWaiting).
				Bold(true).
				Render(" /") + a.filterInput.BeforeCursor() + lipgloss.NewStyle().
				Foreground(colorWaiting).Render("█") + a.filterInput.AfterCursor())
	}
	if a.currentView == viewLogs && a.logsView != nil && a.logsView.NoteMode() {
		noteText, noteTurn := a.logsView.NoteInput()
		return lipgloss.NewStyle().
			Background(lipgloss.Color("#111827")).
			Width(a.width).
			Render(lipgloss.NewStyle().
				Foreground(lipgloss.Color("#F59E0B")).
				Bold(true).
				Render(fmt.Sprintf(" Note [Turn %d]: ", noteTurn)) + noteText + lipgloss.NewStyle().
				Foreground(lipgloss.Color("#F59E0B")).Render("|"))
	}

	var hints string
	if a.statusHint != "" {
		hintColor := colorWaiting
		if strings.Contains(a.statusHint, "failed") || strings.Contains(a.statusHint, "Error") {
			hintColor = lipgloss.Color("#EF4444") // red for errors
		}
		hints = " " + lipgloss.NewStyle().Foreground(hintColor).Bold(true).Render(a.statusHint)
	} else if a.currentView == viewLogs {
		hints = " j/k:turns  Enter:expand  a:annotate  N:note  $:costs  /:filter  :export  :export-otel  Esc:back"
	} else if a.currentView == viewSessions {
		hints = " j/k:nav  Enter:resume  C:copy-id  F:find-content  s:sort  /:filter  A:all  a:annotate  f:failure-mode  N:note  d:delete  D:cleanup  p:preview  Esc:back"
		if a.sessionsView.HasActiveFilter() {
			hints += "  [Esc clears filter]"
		}
	} else if a.currentView == viewTasks {
		hints = " j/k:nav  g/G:top/bottom  :new:create  Esc:back"
	} else {
		// Show group hint if selected agent is grouped
		selected := a.agentsView.Selected()
		if selected != nil && selected.GroupCount > 1 {
			hints = fmt.Sprintf(" x%d = %d grouped  Enter:open  t:traces  c:costs  T:tasks  S:sessions  H:health  x:kill  ?:help",
				selected.GroupCount, selected.GroupCount)
		} else {
			hints = " j/k:nav  Enter:open  t:traces  c:costs  T:tasks  S:sessions  H:health  s:sort  ?:help  q:quit"
		}
		if a.filterInput.Value() != "" {
			hints += fmt.Sprintf("  [filter: %s]", a.filterInput.Value())
		}
	}
	return lipgloss.NewStyle().
		Foreground(colorIdle).
		Background(lipgloss.Color("#111827")).
		Width(a.width).
		Render(hints)
}

// activeTraceTurns returns turns from whichever trace view is active:
// standalone logs view (via `l`) or split trace pane (via Enter).

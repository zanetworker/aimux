package views

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/aimux/internal/session"
)

var (
	tsTimestampStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
	tsToolNameStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true)
	tsTextStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#E5E7EB"))
	tsErrorStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Bold(true)
	tsStatusStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	tsResultStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#D1FAE5"))
	tsSelectedStyle  = lipgloss.NewStyle().Background(lipgloss.Color("#1E3A5F"))
	tsDimStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	tsHeaderStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#E5E7EB"))
	tsIconTool       = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Render("◆")
	tsIconText       = lipgloss.NewStyle().Foreground(lipgloss.Color("#06B6D4")).Render("◇")
	tsIconError      = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Render("✗")
	tsIconStatus     = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Render("■")
)

// ToolStreamBackMsg is emitted when the user presses q/Esc to go back.
type ToolStreamBackMsg struct{}

// SessionOpenToolStreamMsg is emitted when the user opens a managed session's event stream.
type SessionOpenToolStreamMsg struct {
	SessionID string
}

// ToolStreamView renders a scrollable timeline of session events.
type ToolStreamView struct {
	events   []session.SessionEvent
	cursor   int
	offset   int
	width    int
	height   int
	expanded map[int]bool
}

// NewToolStreamView creates a new tool stream view from session events.
func NewToolStreamView(events []session.SessionEvent, width, height int) *ToolStreamView {
	return &ToolStreamView{
		events:   events,
		width:    width,
		height:   height,
		expanded: make(map[int]bool),
	}
}

// SetEvents replaces the event list (for live streaming).
func (v *ToolStreamView) SetEvents(events []session.SessionEvent) {
	v.events = events
	if v.cursor >= len(events) && len(events) > 0 {
		v.cursor = len(events) - 1
	}
	if v.cursor < 0 {
		v.cursor = 0
	}
}

// SetSize sets the available rendering dimensions.
func (v *ToolStreamView) SetSize(w, h int) {
	v.width = w
	v.height = h
}

// Update handles key messages for navigation.
func (v *ToolStreamView) Update(msg tea.Msg) (*ToolStreamView, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if v.cursor < len(v.events)-1 {
				v.cursor++
			}
		case "k", "up":
			if v.cursor > 0 {
				v.cursor--
			}
		case "enter":
			if v.cursor >= 0 && v.cursor < len(v.events) {
				v.expanded[v.cursor] = !v.expanded[v.cursor]
			}
		case "home", "g":
			v.cursor = 0
		case "end", "G":
			if len(v.events) > 0 {
				v.cursor = len(v.events) - 1
			}
		case "pgup", "ctrl+u":
			pageSize := v.height - 4
			if pageSize < 1 {
				pageSize = 1
			}
			v.cursor -= pageSize
			if v.cursor < 0 {
				v.cursor = 0
			}
		case "pgdown", "ctrl+d":
			pageSize := v.height - 4
			if pageSize < 1 {
				pageSize = 1
			}
			v.cursor += pageSize
			if v.cursor >= len(v.events) {
				v.cursor = len(v.events) - 1
			}
			if v.cursor < 0 {
				v.cursor = 0
			}
		case "q", "esc":
			return v, func() tea.Msg { return ToolStreamBackMsg{} }
		}
	}
	return v, nil
}

// toolCallData holds parsed JSON from a tool_call event.
type toolCallData struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// toolResultData holds parsed JSON from a tool_result event.
type toolResultData struct {
	ID      string `json:"id"`
	Output  string `json:"output"`
	IsError bool   `json:"is_error"`
}

// textData holds parsed JSON from a text event.
type textData struct {
	Text string `json:"text"`
}

// View renders the tool stream timeline.
func (v *ToolStreamView) View() string {
	var b strings.Builder
	w := v.width
	if w < 20 {
		w = 80
	}

	b.WriteString(tsHeaderStyle.Render("  Tool Stream") + "\n")
	b.WriteString(tsDimStyle.Render(fmt.Sprintf("  %d events", len(v.events))) + "\n\n")

	if len(v.events) == 0 {
		b.WriteString(tsDimStyle.Render("  No events yet.") + "\n")
		return b.String()
	}

	listHeight := v.height - 4
	if listHeight < 1 {
		listHeight = 1
	}

	// Scroll window: count rendered lines per event to compute visible range.
	// Pre-compute line counts per event.
	lineCounts := make([]int, len(v.events))
	for i := range v.events {
		lineCounts[i] = v.eventLineCount(i)
	}

	// Find a scroll window that keeps cursor visible.
	start := v.offset
	if v.cursor < start {
		start = v.cursor
	}

	// Calculate how many events fit from start to fill listHeight.
	linesUsed := 0
	end := start
	for end < len(v.events) && linesUsed+lineCounts[end] <= listHeight {
		linesUsed += lineCounts[end]
		end++
	}
	if end <= v.cursor {
		// Cursor is past visible range; adjust start forward.
		end = v.cursor + 1
		linesUsed = 0
		start = end
		for start > 0 && linesUsed+lineCounts[start-1] <= listHeight {
			start--
			linesUsed += lineCounts[start]
		}
	}
	v.offset = start

	for i := start; i < end; i++ {
		selected := i == v.cursor
		lines := v.renderEvent(i, w)
		if selected {
			for _, line := range lines {
				b.WriteString(tsSelectedStyle.Render(padRight(line, w)) + "\n")
			}
		} else {
			for _, line := range lines {
				b.WriteString(line + "\n")
			}
		}
	}

	return b.String()
}

// eventLineCount returns the number of rendered lines for an event at index i.
func (v *ToolStreamView) eventLineCount(i int) int {
	if i < 0 || i >= len(v.events) {
		return 1
	}
	lines := 1
	if v.expanded[i] {
		lines += v.expandedLineCount(i)
	}
	// Check if next event is a matching tool_result for this tool_call.
	if v.events[i].Type == "tool_call" && !v.expanded[i] {
		if result := v.findResult(i); result != nil {
			lines++
		}
	}
	return lines
}

// expandedLineCount returns the number of extra lines when an event is expanded.
func (v *ToolStreamView) expandedLineCount(i int) int {
	evt := v.events[i]
	switch evt.Type {
	case "tool_call":
		var tc toolCallData
		if json.Unmarshal([]byte(evt.Data), &tc) != nil {
			return 1
		}
		inputStr := string(tc.Input)
		lines := strings.Count(inputStr, "\n") + 1
		if lines > 20 {
			lines = 20
		}
		return lines
	case "tool_result":
		var tr toolResultData
		if json.Unmarshal([]byte(evt.Data), &tr) != nil {
			return 1
		}
		lines := strings.Count(tr.Output, "\n") + 1
		if lines > 20 {
			lines = 20
		}
		return lines
	case "text":
		var td textData
		if json.Unmarshal([]byte(evt.Data), &td) != nil {
			return 1
		}
		lines := strings.Count(td.Text, "\n") + 1
		if lines > 20 {
			lines = 20
		}
		return lines
	}
	return 1
}

// findResult finds a matching tool_result for a tool_call at index i.
func (v *ToolStreamView) findResult(i int) *session.SessionEvent {
	var tc toolCallData
	if json.Unmarshal([]byte(v.events[i].Data), &tc) != nil {
		return nil
	}
	for j := i + 1; j < len(v.events) && j <= i+5; j++ {
		if v.events[j].Type == "tool_result" {
			var tr toolResultData
			if json.Unmarshal([]byte(v.events[j].Data), &tr) == nil && tr.ID == tc.ID {
				return &v.events[j]
			}
		}
	}
	return nil
}

// renderEvent renders a single event as one or more lines.
func (v *ToolStreamView) renderEvent(i int, w int) []string {
	evt := v.events[i]
	ts := tsTimestampStyle.Render(evt.Timestamp.Format("15:04:05"))
	contentWidth := w - 14 // timestamp(8) + spaces(4) + icon(2)

	var lines []string

	switch evt.Type {
	case "tool_call":
		var tc toolCallData
		if json.Unmarshal([]byte(evt.Data), &tc) != nil {
			lines = append(lines, fmt.Sprintf("  %s  %s %s", ts, tsIconTool, tsDimStyle.Render("(invalid json)")))
			break
		}
		summary := toolCallSummary(tc, contentWidth-len(tc.Name)-1)
		lines = append(lines, fmt.Sprintf("  %s  %s %s  %s", ts, tsIconTool, tsToolNameStyle.Render(tc.Name), tsDimStyle.Render(summary)))

		if !v.expanded[i] {
			if result := v.findResult(i); result != nil {
				var tr toolResultData
				if json.Unmarshal([]byte(result.Data), &tr) == nil {
					output := firstLine(tr.Output, contentWidth-4)
					prefix := "          └ "
					if tr.IsError {
						lines = append(lines, prefix+tsErrorStyle.Render(output))
					} else {
						lines = append(lines, prefix+tsResultStyle.Render(output))
					}
				}
			}
		}

		if v.expanded[i] {
			inputStr := formatJSON(string(tc.Input), contentWidth)
			for _, line := range splitLines(inputStr, 20) {
				lines = append(lines, "            "+tsDimStyle.Render(line))
			}
		}

	case "tool_result":
		var tr toolResultData
		if json.Unmarshal([]byte(evt.Data), &tr) != nil {
			lines = append(lines, fmt.Sprintf("  %s  %s %s", ts, tsIconTool, tsDimStyle.Render("(invalid json)")))
			break
		}
		output := firstLine(tr.Output, contentWidth)
		if tr.IsError {
			lines = append(lines, fmt.Sprintf("  %s  %s %s", ts, tsIconError, tsErrorStyle.Render(output)))
		} else {
			lines = append(lines, fmt.Sprintf("  %s  %s %s", ts, tsIconTool, tsResultStyle.Render(output)))
		}
		if v.expanded[i] {
			for _, line := range splitLines(tr.Output, 20) {
				lines = append(lines, "            "+tsResultStyle.Render(line))
			}
		}

	case "text":
		var td textData
		if json.Unmarshal([]byte(evt.Data), &td) != nil {
			lines = append(lines, fmt.Sprintf("  %s  %s %s", ts, tsIconText, tsDimStyle.Render("(invalid json)")))
			break
		}
		text := firstLine(td.Text, contentWidth)
		lines = append(lines, fmt.Sprintf("  %s  %s %s", ts, tsIconText, tsTextStyle.Render(text)))
		if v.expanded[i] {
			for _, line := range splitLines(td.Text, 20) {
				lines = append(lines, "            "+tsTextStyle.Render(line))
			}
		}

	case "error":
		text := firstLine(evt.Data, contentWidth)
		lines = append(lines, fmt.Sprintf("  %s  %s %s", ts, tsIconError, tsErrorStyle.Render(text)))

	case "status_change":
		text := evt.Data
		if text == "" {
			text = "status changed"
		}
		lines = append(lines, fmt.Sprintf("  %s  %s %s", ts, tsIconStatus, tsStatusStyle.Render(text)))

	default:
		lines = append(lines, fmt.Sprintf("  %s  %s %s", ts, tsDimStyle.Render("·"), tsDimStyle.Render(evt.Type+": "+firstLine(evt.Data, contentWidth-len(evt.Type)-2))))
	}

	return lines
}

// toolCallSummary produces a one-line summary of a tool call's input.
func toolCallSummary(tc toolCallData, maxLen int) string {
	var m map[string]interface{}
	if json.Unmarshal(tc.Input, &m) != nil {
		return truncate(string(tc.Input), maxLen)
	}
	switch tc.Name {
	case "Bash":
		if cmd, ok := m["command"].(string); ok {
			return truncate(cmd, maxLen)
		}
	case "Read":
		if fp, ok := m["file_path"].(string); ok {
			return truncate(fp, maxLen)
		}
	case "Edit":
		if fp, ok := m["file_path"].(string); ok {
			return truncate(fp, maxLen)
		}
	case "Write":
		if fp, ok := m["file_path"].(string); ok {
			return truncate(fp, maxLen)
		}
	}
	// Generic: show first key=value pair.
	for k, val := range m {
		s := fmt.Sprintf("%s=%v", k, val)
		return truncate(s, maxLen)
	}
	return ""
}

// firstLine returns the first line of s, truncated to maxLen.
func firstLine(s string, maxLen int) string {
	idx := strings.IndexByte(s, '\n')
	if idx >= 0 {
		s = s[:idx]
	}
	return truncate(s, maxLen)
}

// splitLines splits s into lines, capping at maxLines.
func splitLines(s string, maxLines int) []string {
	lines := strings.Split(s, "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines = append(lines, "...")
	}
	return lines
}

// formatJSON tries to pretty-format JSON, falling back to raw string.
func formatJSON(s string, _ int) string {
	var v interface{}
	if json.Unmarshal([]byte(s), &v) != nil {
		return s
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return s
	}
	return string(b)
}

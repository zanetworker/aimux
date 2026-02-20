package views

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	timestampStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
	userLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#06B6D4")).Bold(true)
	asstLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E")).Bold(true)
	toolLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true)
	progLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	textStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#E5E7EB"))
	toolNameStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))
	dimStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
)

// TraceLine represents a single rendered line in the trace view.
type TraceLine struct {
	Timestamp time.Time
	Label     string // styled label like "USER", "ASST", "TOOL"
	Content   string // the actual text content
	EntryType string // raw type for filtering
}

// LogsView renders a conversation trace for a specific instance.
type LogsView struct {
	lines      []TraceLine
	filePath   string
	width      int
	height     int
	scrollPos  int
	autoScroll bool
	pid        int
}

// NewLogsView creates a new LogsView for the given PID and log file path.
func NewLogsView(pid int, filePath string) *LogsView {
	v := &LogsView{
		pid:        pid,
		filePath:   filePath,
		autoScroll: true,
	}
	v.Reload()
	return v
}

// SetSize sets the available width and height.
func (v *LogsView) SetSize(w, h int) {
	v.width = w
	v.height = h
}

// Reload reads and parses the JSONL log file into trace lines.
func (v *LogsView) Reload() {
	data, err := os.ReadFile(v.filePath)
	if err != nil {
		v.lines = nil
		return
	}

	var lines []TraceLine
	for _, rawLine := range strings.Split(string(data), "\n") {
		rawLine = strings.TrimSpace(rawLine)
		if rawLine == "" {
			continue
		}
		parsed := parseJSONLEntry(rawLine)
		lines = append(lines, parsed...)
	}
	v.lines = lines

	if v.autoScroll {
		v.scrollToBottom()
	}
}

// Update handles key messages for scrolling.
func (v *LogsView) Update(msg tea.Msg) {
	if len(v.lines) == 0 {
		return
	}
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if v.scrollPos < len(v.lines)-1 {
				v.scrollPos++
				v.autoScroll = false
			}
		case "k", "up":
			if v.scrollPos > 0 {
				v.scrollPos--
				v.autoScroll = false
			}
		case "g":
			v.scrollPos = 0
			v.autoScroll = false
		case "G":
			v.scrollToBottom()
			v.autoScroll = true
		case "d":
			// Page down
			v.scrollPos += v.height / 2
			if v.scrollPos >= len(v.lines) {
				v.scrollPos = len(v.lines) - 1
			}
			v.autoScroll = false
		case "u":
			// Page up
			v.scrollPos -= v.height / 2
			if v.scrollPos < 0 {
				v.scrollPos = 0
			}
			v.autoScroll = false
		}
	}
}

// View renders the trace lines.
func (v *LogsView) View() string {
	if len(v.lines) == 0 {
		return dimStyle.Render("  No trace entries found for this session.")
	}

	var b strings.Builder

	visibleHeight := v.height
	if visibleHeight < 1 {
		visibleHeight = len(v.lines)
	}

	end := v.scrollPos + 1
	start := end - visibleHeight
	if start < 0 {
		start = 0
	}
	if end > len(v.lines) {
		end = len(v.lines)
	}

	for i := start; i < end; i++ {
		line := v.lines[i]
		ts := ""
		if !line.Timestamp.IsZero() {
			ts = timestampStyle.Render(line.Timestamp.Format("15:04:05")) + " "
		}

		content := line.Content
		maxContent := v.width - 16 // leave room for timestamp + label
		if maxContent > 0 && len(content) > maxContent {
			content = content[:maxContent-3] + "..."
		}

		b.WriteString(fmt.Sprintf(" %s%s %s\n", ts, line.Label, content))
	}

	// Scroll indicator
	pos := fmt.Sprintf(" [%d/%d]", min(v.scrollPos+1, len(v.lines)), len(v.lines))
	b.WriteString(dimStyle.Render(pos))
	if v.autoScroll {
		b.WriteString(dimStyle.Render(" (following)"))
	}

	return b.String()
}

func (v *LogsView) scrollToBottom() {
	v.scrollPos = len(v.lines) - 1
	if v.scrollPos < 0 {
		v.scrollPos = 0
	}
}

// parseJSONLEntry parses a JSONL line and returns one or more trace lines.
// A single entry can produce multiple lines (e.g., assistant with text + tool calls).
func parseJSONLEntry(line string) []TraceLine {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return nil
	}

	var entryType string
	json.Unmarshal(raw["type"], &entryType)

	var ts time.Time
	var tsStr string
	if err := json.Unmarshal(raw["timestamp"], &tsStr); err == nil {
		ts, _ = time.Parse(time.RFC3339Nano, tsStr)
	}

	switch entryType {
	case "user":
		return parseUserEntry(raw, ts)
	case "assistant":
		return parseAssistantEntry(raw, ts)
	case "progress":
		return parseProgressEntry(raw, ts)
	default:
		// Skip file-history-snapshot, result, etc.
		return nil
	}
}

// extractMessageContent extracts the text content from a message field.
// Handles both JSON object format {"role":"user","content":"text"} and
// Python-style string format "{'role': 'user', 'content': 'text'}".
func extractMessageContent(msgRaw json.RawMessage) string {
	// Try as JSON object first
	var msgObj map[string]interface{}
	if err := json.Unmarshal(msgRaw, &msgObj); err == nil {
		if content, ok := msgObj["content"].(string); ok {
			return content
		}
	}
	// Try as string (Python-style repr)
	var msgStr string
	if err := json.Unmarshal(msgRaw, &msgStr); err == nil {
		if idx := strings.Index(msgStr, "'content':"); idx != -1 {
			rest := msgStr[idx+len("'content':"):]
			rest = strings.TrimSpace(rest)
			if len(rest) > 1 && (rest[0] == '\'' || rest[0] == '"') {
				quote := rest[0]
				end := strings.Index(rest[1:], string(quote))
				if end > 0 {
					return rest[1 : end+1]
				}
			}
		}
	}
	return ""
}

func parseUserEntry(raw map[string]json.RawMessage, ts time.Time) []TraceLine {
	content := extractMessageContent(raw["message"])
	if content == "" {
		return nil
	}
	// Split multi-line user messages into separate trace lines
	var lines []TraceLine
	for i, line := range strings.Split(content, "\n") {
		label := userLabelStyle.Render("USER")
		if i > 0 {
			label = "    " // continuation indent
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lines = append(lines, TraceLine{
			Timestamp: ts,
			Label:     label,
			Content:   textStyle.Render(trimmed),
			EntryType: "user",
		})
		if i == 0 {
			ts = time.Time{} // only show timestamp on first line
		}
	}
	return lines
}

func parseAssistantEntry(raw map[string]json.RawMessage, ts time.Time) []TraceLine {
	// Try to parse message as a JSON object
	var msgObj map[string]json.RawMessage
	if err := json.Unmarshal(raw["message"], &msgObj); err != nil {
		// Message might be a string (Python-style repr) — try to extract what we can
		var msgStr string
		if err := json.Unmarshal(raw["message"], &msgStr); err == nil {
			return parseStringMessage(msgStr, ts)
		}
		return nil
	}

	// Parse the content array
	var contentBlocks []map[string]interface{}
	if err := json.Unmarshal(msgObj["content"], &contentBlocks); err != nil {
		return nil
	}

	var lines []TraceLine
	for _, block := range contentBlocks {
		blockType, _ := block["type"].(string)
		switch blockType {
		case "text":
			text, _ := block["text"].(string)
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			// Show first few lines of assistant text
			for i, textLine := range strings.Split(text, "\n") {
				trimmed := strings.TrimSpace(textLine)
				if trimmed == "" {
					continue
				}
				label := asstLabelStyle.Render("ASST")
				if i > 0 {
					label = "    "
				}
				entryTS := ts
				if i > 0 {
					entryTS = time.Time{}
				}
				lines = append(lines, TraceLine{
					Timestamp: entryTS,
					Label:     label,
					Content:   textStyle.Render(trimmed),
					EntryType: "assistant",
				})
				if len(lines) > 5 {
					// Truncate very long responses to keep trace readable
					lines = append(lines, TraceLine{
						Label:     "    ",
						Content:   dimStyle.Render("... (truncated)"),
						EntryType: "assistant",
					})
					break
				}
			}

		case "tool_use":
			name, _ := block["name"].(string)
			toolLine := TraceLine{
				Timestamp: ts,
				Label:     toolLabelStyle.Render("TOOL"),
				Content:   toolNameStyle.Render(name),
				EntryType: "tool",
			}
			// Try to extract a short snippet of the input
			if input, ok := block["input"].(map[string]interface{}); ok {
				snippet := toolInputSnippet(name, input)
				if snippet != "" {
					toolLine.Content += " " + dimStyle.Render(snippet)
				}
			}
			lines = append(lines, toolLine)
			ts = time.Time{} // only first block gets timestamp

		case "tool_result":
			// Show tool results briefly
			if content, ok := block["content"].(string); ok {
				content = strings.TrimSpace(content)
				if len(content) > 80 {
					content = content[:77] + "..."
				}
				if content != "" {
					lines = append(lines, TraceLine{
						Label:     dimStyle.Render(" RES"),
						Content:   dimStyle.Render(content),
						EntryType: "result",
					})
				}
			}
		}
	}
	return lines
}

func parseProgressEntry(raw map[string]json.RawMessage, ts time.Time) []TraceLine {
	// Try to extract progress info
	var dataStr string
	if err := json.Unmarshal(raw["data"], &dataStr); err == nil {
		// data is a string — try to extract hook name
		if strings.Contains(dataStr, "hook_progress") {
			return nil // skip hook progress noise
		}
	}

	var dataObj map[string]interface{}
	if err := json.Unmarshal(raw["data"], &dataObj); err == nil {
		if hookName, ok := dataObj["hookName"].(string); ok {
			return []TraceLine{{
				Timestamp: ts,
				Label:     progLabelStyle.Render("HOOK"),
				Content:   dimStyle.Render(hookName),
				EntryType: "progress",
			}}
		}
	}

	return nil // skip uninteresting progress entries
}

// parseStringMessage handles the case where message is stored as a Python-style
// string representation rather than proper JSON.
func parseStringMessage(msg string, ts time.Time) []TraceLine {
	// Try to extract content from Python-style dict string
	// e.g., "{'role': 'user', 'content': \"hello\"}"
	msg = strings.TrimSpace(msg)

	// Look for 'content': patterns
	if idx := strings.Index(msg, "'content':"); idx != -1 {
		rest := msg[idx+len("'content':"):]
		rest = strings.TrimSpace(rest)

		// Try to extract the value
		if strings.HasPrefix(rest, "'") || strings.HasPrefix(rest, "\"") {
			// Simple string value
			quote := rest[0]
			end := strings.Index(rest[1:], string(quote))
			if end > 0 {
				content := rest[1 : end+1]
				if content != "" {
					return []TraceLine{{
						Timestamp: ts,
						Label:     asstLabelStyle.Render("ASST"),
						Content:   textStyle.Render(content),
						EntryType: "assistant",
					}}
				}
			}
		}
	}

	// Look for tool_use in string representation
	if strings.Contains(msg, "'type': 'tool_use'") {
		if idx := strings.Index(msg, "'name':"); idx != -1 {
			rest := msg[idx+len("'name':"):]
			rest = strings.TrimSpace(rest)
			if strings.HasPrefix(rest, "'") {
				end := strings.Index(rest[1:], "'")
				if end > 0 {
					name := rest[1 : end+1]
					return []TraceLine{{
						Timestamp: ts,
						Label:     toolLabelStyle.Render("TOOL"),
						Content:   toolNameStyle.Render(name),
						EntryType: "tool",
					}}
				}
			}
		}
	}

	return nil
}

// toolInputSnippet returns a short description of what a tool call is doing.
func toolInputSnippet(toolName string, input map[string]interface{}) string {
	switch toolName {
	case "Bash":
		if cmd, ok := input["command"].(string); ok {
			cmd = strings.TrimSpace(cmd)
			if len(cmd) > 60 {
				cmd = cmd[:57] + "..."
			}
			return "$ " + cmd
		}
	case "Read":
		if path, ok := input["file_path"].(string); ok {
			return path
		}
	case "Write":
		if path, ok := input["file_path"].(string); ok {
			return path
		}
	case "Edit":
		if path, ok := input["file_path"].(string); ok {
			return path
		}
	case "Grep":
		if pattern, ok := input["pattern"].(string); ok {
			return "/" + pattern + "/"
		}
	case "Glob":
		if pattern, ok := input["pattern"].(string); ok {
			return pattern
		}
	case "Task":
		if desc, ok := input["description"].(string); ok {
			return desc
		}
	case "WebSearch":
		if query, ok := input["query"].(string); ok {
			return query
		}
	case "WebFetch":
		if url, ok := input["url"].(string); ok {
			if len(url) > 50 {
				url = url[:47] + "..."
			}
			return url
		}
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

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
	userStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#06B6D4")).Bold(true)
	assistantStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#7C3AED")).Bold(true)
	progressStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))
)

// LogEntry represents a single parsed log line.
type LogEntry struct {
	Timestamp time.Time
	Type      string
	Summary   string
}

// LogsView renders the log viewer for a specific instance.
type LogsView struct {
	entries    []LogEntry
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

// Reload reads and parses the JSONL log file.
func (v *LogsView) Reload() {
	data, err := os.ReadFile(v.filePath)
	if err != nil {
		v.entries = nil
		return
	}

	var entries []LogEntry
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		entry := parseLine(line)
		entries = append(entries, entry)
	}
	v.entries = entries

	if v.autoScroll {
		v.scrollToBottom()
	}
}

// Update handles key messages for scrolling.
func (v *LogsView) Update(msg tea.Msg) {
	if len(v.entries) == 0 {
		return
	}
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "j", "down":
			if v.scrollPos < len(v.entries)-1 {
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
		}
	}
}

// View renders the log entries.
func (v *LogsView) View() string {
	if len(v.entries) == 0 {
		return mutedIcon.Render("  No log entries.")
	}

	var b strings.Builder

	visibleHeight := v.height
	if visibleHeight < 1 {
		visibleHeight = len(v.entries)
	}

	end := v.scrollPos + 1
	start := end - visibleHeight
	if start < 0 {
		start = 0
	}
	if end > len(v.entries) {
		end = len(v.entries)
	}

	for i := start; i < end; i++ {
		e := v.entries[i]
		ts := timestampStyle.Render(e.Timestamp.Format("15:04:05"))
		tag := v.renderTag(e.Type)
		b.WriteString(fmt.Sprintf("%s %s %s\n", ts, tag, e.Summary))
	}

	return b.String()
}

func (v *LogsView) scrollToBottom() {
	v.scrollPos = len(v.entries) - 1
	if v.scrollPos < 0 {
		v.scrollPos = 0
	}
}

func (v *LogsView) renderTag(t string) string {
	switch t {
	case "user", "human":
		return userStyle.Render("USR")
	case "assistant":
		return assistantStyle.Render("AST")
	case "progress":
		return progressStyle.Render("PRG")
	default:
		return mutedIcon.Render(fmt.Sprintf("%-3s", strings.ToUpper(t)))
	}
}

// parseLine parses a single JSONL line into a LogEntry.
func parseLine(line string) LogEntry {
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return LogEntry{Summary: line, Type: "raw"}
	}

	entry := LogEntry{}

	// Parse timestamp
	if ts, ok := raw["timestamp"].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			entry.Timestamp = t
		}
	}

	// Determine type
	if t, ok := raw["type"].(string); ok {
		entry.Type = t
	}

	// Build summary based on type
	switch entry.Type {
	case "user", "human":
		entry.Summary = extractContent(raw)
	case "assistant":
		entry.Summary = extractAssistantSummary(raw)
	case "progress":
		if hookName, ok := raw["hook"].(string); ok {
			entry.Summary = hookName
		} else if msg, ok := raw["message"].(string); ok {
			entry.Summary = msg
		}
	default:
		if msg, ok := raw["message"].(string); ok {
			entry.Summary = msg
		}
	}

	if len(entry.Summary) > 80 {
		entry.Summary = entry.Summary[:77] + "..."
	}

	return entry
}

func extractContent(raw map[string]interface{}) string {
	if content, ok := raw["content"].(string); ok {
		return content
	}
	if message, ok := raw["message"].(string); ok {
		return message
	}
	return ""
}

func extractAssistantSummary(raw map[string]interface{}) string {
	// Check for tool_call
	if toolName, ok := raw["tool"].(string); ok {
		return fmt.Sprintf("[tool: %s]", toolName)
	}
	if toolCall, ok := raw["tool_call"].(map[string]interface{}); ok {
		if name, ok := toolCall["name"].(string); ok {
			return fmt.Sprintf("[tool: %s]", name)
		}
	}
	// Fall back to text content
	return extractContent(raw)
}

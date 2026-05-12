// Package sessions provides interactive session picking via fzf or bubbletea.
package sessions

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zanetworker/aimux/internal/history"
)

var ansiRegexp = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// ErrCancelled is returned when the user cancels the picker.
var ErrCancelled = fmt.Errorf("selection cancelled")

// PickSession presents an interactive picker for the given sessions.
// Uses fzf if available, falls back to a bubbletea list.
// Returns the selected session or ErrCancelled.
func PickSession(sessions []history.Session) (history.Session, error) {
	if len(sessions) == 0 {
		return history.Session{}, fmt.Errorf("no sessions to pick from")
	}
	if hasFzf() {
		return fzfPick(sessions)
	}
	return bubbleteaPick(sessions)
}

const (
	ansiCyan  = "\033[36m"
	ansiGreen = "\033[32m"
	ansiDim   = "\033[2m"
	ansiReset = "\033[0m"
)

// FormatLine formats a session as a single display line for the picker.
func FormatLine(s history.Session) string {
	proj := shortProject(s.Project)
	age := shortAge(s.LastActive)
	title := s.Title
	if title == "" {
		title = s.FirstPrompt
	}
	if len(title) > 60 {
		title = title[:57] + "..."
	}
	if title == "" {
		title = "-"
	}
	return fmt.Sprintf("%s%-38s%s  %s%-14s%s  %-7s  %4dT  $%6.2f  %s",
		ansiCyan, s.ID, ansiReset,
		ansiGreen, truncStr(proj, 14), ansiReset,
		age, s.TurnCount, s.CostUSD, title)
}

// FormatPreview returns a second line with the first prompt in dim text,
// shown below the main FormatLine for extra context.
func FormatPreview(s history.Session) string {
	preview := s.FirstPrompt
	if preview == "" {
		return ""
	}
	if s.Title != "" && preview == s.Title {
		return ""
	}
	if len(preview) > 90 {
		preview = preview[:87] + "..."
	}
	return fmt.Sprintf("%s    %s%s", ansiDim, preview, ansiReset)
}

// ParseSelectedID extracts the session ID from a picker output line.
// Strips ANSI escape codes first, then takes the first whitespace-delimited field.
func ParseSelectedID(line string) string {
	line = ansiRegexp.ReplaceAllString(line, "")
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func hasFzf() bool {
	_, err := exec.LookPath("fzf")
	return err == nil
}

func fzfPick(sessions []history.Session) (history.Session, error) {
	var input bytes.Buffer
	for _, s := range sessions {
		line := FormatLine(s)
		if preview := FormatPreview(s); preview != "" {
			line += "\n    " + strings.TrimSpace(preview)
		}
		input.WriteString(line)
		input.WriteByte(0)
	}

	fzfBin, _ := exec.LookPath("fzf")
	cmd := exec.Command(fzfBin, // #nosec G204
		"--ansi",
		"--no-multi",
		"--read0",
		"--header", "ID                                      PROJECT         AGE      TURNS    COST  PROMPT",
		"--prompt", "session> ",
	)
	cmd.Stdin = &input
	cmd.Stderr = os.Stderr

	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 130 {
			return history.Session{}, ErrCancelled
		}
		return history.Session{}, ErrCancelled
	}

	selectedID := ParseSelectedID(string(out))
	if selectedID == "" {
		return history.Session{}, ErrCancelled
	}

	for _, s := range sessions {
		if s.ID == selectedID {
			return s, nil
		}
	}
	return history.Session{}, fmt.Errorf("session %q not found", selectedID)
}

type pickerModel struct {
	sessions  []history.Session
	filter    string
	cursor    int
	selected  history.Session
	done      bool
	cancelled bool
	width     int
	height    int
}

func newPickerModel(sessions []history.Session) pickerModel {
	return pickerModel{
		sessions: sessions,
		width:    120,
		height:   24,
	}
}

func (m pickerModel) filteredSessions() []history.Session {
	if m.filter == "" {
		return m.sessions
	}
	needle := strings.ToLower(m.filter)
	var result []history.Session
	for _, s := range m.sessions {
		if strings.Contains(strings.ToLower(s.Title), needle) ||
			strings.Contains(strings.ToLower(s.FirstPrompt), needle) ||
			strings.Contains(strings.ToLower(s.Project), needle) ||
			strings.Contains(strings.ToLower(s.ID), needle) {
			result = append(result, s)
		}
	}
	return result
}

func (m pickerModel) Init() tea.Cmd { return nil }

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancelled = true
			m.done = true
			return m, tea.Quit
		case "enter":
			filtered := m.filteredSessions()
			if len(filtered) > 0 && m.cursor < len(filtered) {
				m.selected = filtered[m.cursor]
				m.done = true
				return m, tea.Quit
			}
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			filtered := m.filteredSessions()
			if m.cursor < len(filtered)-1 {
				m.cursor++
			}
		case "backspace":
			if len(m.filter) > 0 {
				m.filter = m.filter[:len(m.filter)-1]
				m.cursor = 0
			}
		default:
			if len(msg.String()) == 1 {
				m.filter += msg.String()
				m.cursor = 0
			}
		}
	}
	return m, nil
}

func (m pickerModel) View() string {
	var b strings.Builder
	fmt.Fprintf(&b, " session> %s\n", m.filter)
	b.WriteString(" ──────────────────────────────────────────────────────────────────────\n")

	filtered := m.filteredSessions()
	if len(filtered) == 0 {
		b.WriteString(" No matching sessions.\n")
		return b.String()
	}

	maxVisible := m.height - 4
	if maxVisible < 5 {
		maxVisible = 5
	}
	start := 0
	if m.cursor >= maxVisible {
		start = m.cursor - maxVisible + 1
	}
	end := start + maxVisible
	if end > len(filtered) {
		end = len(filtered)
	}

	for i := start; i < end; i++ {
		prefix := "  "
		if i == m.cursor {
			prefix = "> "
		}
		b.WriteString(prefix)
		b.WriteString(FormatLine(filtered[i]))
		b.WriteString("\n")
		if preview := FormatPreview(filtered[i]); preview != "" {
			b.WriteString("  ")
			b.WriteString(preview)
			b.WriteString("\n")
		}
	}

	fmt.Fprintf(&b, "\n %d/%d sessions  |  type to filter  |  enter to resume  |  esc to cancel",
		len(filtered), len(m.sessions))
	return b.String()
}

func bubbleteaPick(sessions []history.Session) (history.Session, error) {
	m := newPickerModel(sessions)
	p := tea.NewProgram(m)
	result, err := p.Run()
	if err != nil {
		return history.Session{}, fmt.Errorf("picker error: %w", err)
	}

	final := result.(pickerModel)
	if final.cancelled || !final.done {
		return history.Session{}, ErrCancelled
	}
	return final.selected, nil
}

func shortProject(path string) string {
	if path == "" {
		return "(unknown)"
	}
	return filepath.Base(path)
}

func shortAge(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dmo", int(d.Hours()/24/30))
	}
}

func truncStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

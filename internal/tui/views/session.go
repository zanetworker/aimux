package views

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/terminal"
)

var (
	sessionHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#E5E7EB")).
				Background(lipgloss.Color("#1E293B"))
	sessionBadgeStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#111827")).
				Background(lipgloss.Color("#5F87FF"))
	sessionStatusStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#22C55E")).
				Background(lipgloss.Color("#111827")).
				Bold(true)
	sessionHintStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#9CA3AF")).
				Background(lipgloss.Color("#111827"))
	sessionModeStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#F59E0B")).
				Background(lipgloss.Color("#111827")).
				Bold(true)
)

// PTYOutputMsg carries raw output data from the PTY subprocess.
type PTYOutputMsg struct {
	Data []byte
}

// PTYExitMsg signals that the PTY subprocess has exited.
type PTYExitMsg struct{}

// SessionView provides a full-screen interactive terminal view. It wraps a
// session backend (direct PTY or tmux mirror) and a VT terminal emulator to
// render output within the Bubble Tea TUI. The user interacts with the
// subprocess directly; keystrokes are forwarded to the backend.
type SessionView struct {
	agent    *agent.Agent
	session  terminal.SessionBackend
	termView *terminal.TermView
	width    int
	height   int
	active   bool
}

// NewSessionView creates a new SessionView in an inactive state.
func NewSessionView() *SessionView {
	return &SessionView{}
}

// Open starts an interactive session using the given backend (direct PTY or
// tmux mirror). It starts a background goroutine to read output and returns
// a tea.Cmd that delivers the first PTYOutputMsg.
func (sv *SessionView) Open(a *agent.Agent, backend terminal.SessionBackend) (tea.Cmd, error) {
	sv.agent = a
	sv.session = backend
	sv.active = true

	// Create the VT emulator sized for the content area (minus header + status bars)
	contentHeight := sv.height - 2
	if contentHeight < 1 {
		contentHeight = 24
	}
	contentWidth := sv.width
	if contentWidth < 1 {
		contentWidth = 80
	}
	sv.termView = terminal.NewTermView(contentWidth, contentHeight)

	// Resize the backend to match
	_ = backend.Resize(contentWidth, contentHeight)

	// Return a command that reads the first chunk of PTY output
	return sv.readPTY(), nil
}

// HandleOutput feeds raw data into the VT emulator (for direct PTY backends)
// and returns a tea.Cmd to continue reading. For DirectRenderer backends
// (tmux), the data is ignored since Render() provides the content directly.
func (sv *SessionView) HandleOutput(data []byte) tea.Cmd {
	if !sv.active {
		return nil
	}
	// Only feed through VT emulator if the backend isn't a DirectRenderer
	if _, isDirect := sv.session.(terminal.DirectRenderer); !isDirect && sv.termView != nil {
		sv.termView.Write(data)
	}
	return sv.readPTY()
}

// SendKey forwards a keystroke to the PTY subprocess, translating Bubble Tea
// key names to their actual terminal byte sequences.
func (sv *SessionView) SendKey(key string) {
	if sv.session == nil || !sv.active {
		return
	}
	data := keyToBytes(key)
	if len(data) > 0 {
		_, _ = sv.session.Write(data)
	}
}

// keyToBytes converts a Bubble Tea key string to the raw bytes a terminal
// would send. Single printable characters pass through as-is. Named keys
// are mapped to their ANSI escape sequences or control codes.
func keyToBytes(key string) []byte {
	// Single printable character — pass through directly
	if len(key) == 1 {
		return []byte(key)
	}

	switch key {
	case "enter":
		return []byte{'\r'}
	case "tab":
		return []byte{'\t'}
	case "backspace":
		return []byte{0x7f}
	case "esc", "escape":
		return []byte{0x1b}
	case "space":
		return []byte{' '}
	case "up":
		return []byte("\x1b[A")
	case "down":
		return []byte("\x1b[B")
	case "right":
		return []byte("\x1b[C")
	case "left":
		return []byte("\x1b[D")
	case "home":
		return []byte("\x1b[H")
	case "end":
		return []byte("\x1b[F")
	case "pgup":
		return []byte("\x1b[5~")
	case "pgdown":
		return []byte("\x1b[6~")
	case "delete":
		return []byte("\x1b[3~")
	case "insert":
		return []byte("\x1b[2~")
	}

	// Ctrl+letter: ctrl+a=0x01, ctrl+b=0x02, ..., ctrl+z=0x1a
	if strings.HasPrefix(key, "ctrl+") {
		ch := key[5:]
		if len(ch) == 1 && ch[0] >= 'a' && ch[0] <= 'z' {
			return []byte{ch[0] - 'a' + 1}
		}
		// Ctrl+[ = ESC (0x1b), Ctrl+] = 0x1d, Ctrl+\ = 0x1c
		switch ch {
		case "[":
			return []byte{0x1b}
		case "\\":
			return []byte{0x1c}
		case "]":
			return []byte{0x1d}
		}
	}

	// Function keys
	switch key {
	case "f1":
		return []byte("\x1bOP")
	case "f2":
		return []byte("\x1bOQ")
	case "f3":
		return []byte("\x1bOR")
	case "f4":
		return []byte("\x1bOS")
	}

	// Unknown key — try sending as-is (covers multi-byte UTF-8 chars)
	return []byte(key)
}

// SetSize resizes the PTY and VT emulator to fit the new dimensions.
func (sv *SessionView) SetSize(w, h int) {
	sv.width = w
	sv.height = h
	if !sv.active || sv.session == nil {
		return
	}
	contentHeight := h - 2 // header + status bar
	if contentHeight < 1 {
		contentHeight = 1
	}
	contentWidth := w
	if contentWidth < 1 {
		contentWidth = 1
	}
	sv.termView.Resize(contentWidth, contentHeight)
	_ = sv.session.Resize(contentWidth, contentHeight)
}

// Close terminates the PTY session and marks the view as inactive.
func (sv *SessionView) Close() {
	sv.active = false
	if sv.session != nil {
		sv.session.Close()
		sv.session = nil
	}
}

// Active returns true if the session view is currently running.
func (sv *SessionView) Active() bool {
	return sv.active
}

// Agent returns the agent associated with this session, or nil.
func (sv *SessionView) Agent() *agent.Agent {
	return sv.agent
}

// TermView returns the underlying terminal view, or nil for DirectRenderer backends.
func (sv *SessionView) TermView() *terminal.TermView {
	return sv.termView
}

// View renders the session view with a header bar, terminal content, and a
// status bar at the bottom.
func (sv *SessionView) View() string {
	if !sv.active {
		return ""
	}

	var b strings.Builder

	// Header bar
	header := sv.renderHeader()
	b.WriteString(header)
	b.WriteString("\n")

	// Terminal content — use DirectRenderer if available, else VT emulator
	var termContent string
	if dr, ok := sv.session.(terminal.DirectRenderer); ok {
		termContent = dr.Render()
	} else if sv.termView != nil {
		termContent = sv.termView.Render()
	}

	// Show scroll indicator when viewing history
	if sv.termView != nil && sv.termView.IsScrolled() {
		scrollIndicator := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F59E0B")).Bold(true).
			Render("-- scrolled (PgDn to resume) --")
		lines := strings.Split(termContent, "\n")
		if len(lines) > 0 {
			lines[len(lines)-1] = scrollIndicator
			termContent = strings.Join(lines, "\n")
		}
	}

	b.WriteString(termContent)

	// Pad to fill height if needed
	termLines := strings.Count(termContent, "\n") + 1
	contentHeight := sv.height - 2
	if termLines < contentHeight {
		b.WriteString(strings.Repeat("\n", contentHeight-termLines))
	}
	b.WriteString("\n")

	// Status bar
	b.WriteString(sv.renderStatusBar())

	return b.String()
}

func (sv *SessionView) renderHeader() string {
	name := "(unknown)"
	model := ""
	provider := ""
	if sv.agent != nil {
		if p := sv.agent.ShortProject(); p != "" {
			name = p
		}
		model = sv.agent.ShortModel()
		provider = sv.agent.ProviderName
	}

	badge := sessionBadgeStyle.Render(" aimux ")
	left := badge + sessionHeaderStyle.Render(fmt.Sprintf(" %s ", name))
	if provider != "" {
		left += " " + sessionHintStyle.Render(provider)
	}
	if model != "" {
		left += " " + sessionHintStyle.Render(model)
	}

	right := sessionHintStyle.Render(" PgUp/PgDn:scroll  Tab:trace  Ctrl+f:split  Ctrl+]:exit ")

	gap := sv.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}
	fill := sessionHeaderStyle.Render(strings.Repeat(" ", gap))

	return left + fill + right
}

func (sv *SessionView) renderStatusBar() string {
	badge := sessionBadgeStyle.Render(" aimux ")
	mode := sessionModeStyle.Render(" INTERACTIVE ")
	hint := sessionHintStyle.Render(" PgUp/PgDn:scroll  Ctrl+f:split  Ctrl+]:exit ")

	gap := sv.width - lipgloss.Width(badge) - lipgloss.Width(mode) - lipgloss.Width(hint)
	if gap < 0 {
		gap = 0
	}
	fill := sessionStatusStyle.Render(strings.Repeat(" ", gap))

	return badge + mode + fill + hint
}

// readPTY returns a tea.Cmd that reads the next chunk from the PTY. When the
// read fails (process exit or close), it sends a PTYExitMsg instead.
func (sv *SessionView) readPTY() tea.Cmd {
	sess := sv.session
	if sess == nil {
		return nil
	}
	return func() tea.Msg {
		buf := make([]byte, 4096)
		n, err := sess.Read(buf)
		if err != nil || n == 0 {
			return PTYExitMsg{}
		}
		// Copy data to avoid buffer reuse issues
		data := make([]byte, n)
		copy(data, buf[:n])
		return PTYOutputMsg{Data: data}
	}
}

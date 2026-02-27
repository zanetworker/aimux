package views

import (
	"fmt"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/zanetworker/agentmux/internal/agent"
	"github.com/zanetworker/agentmux/internal/terminal"
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

// SessionView provides a full-screen interactive terminal view. It wraps a PTY
// session and a VT terminal emulator to render the subprocess output within the
// Bubble Tea TUI. The user interacts with the subprocess directly; keystrokes
// are forwarded to the PTY.
type SessionView struct {
	agent    *agent.Agent
	session  *terminal.Session
	termView *terminal.TermView
	width    int
	height   int
	active   bool
}

// NewSessionView creates a new SessionView in an inactive state.
func NewSessionView() *SessionView {
	return &SessionView{}
}

// Open spawns a PTY session for the given agent and command, and starts a
// background goroutine to read PTY output. It returns a tea.Cmd that delivers
// the first PTYOutputMsg.
func (sv *SessionView) Open(a *agent.Agent, cmd *exec.Cmd) (tea.Cmd, error) {
	sess, err := terminal.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("starting PTY session: %w", err)
	}

	sv.agent = a
	sv.session = sess
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

	// Resize the PTY to match
	_ = sess.Resize(contentWidth, contentHeight)

	// Return a command that reads the first chunk of PTY output
	return sv.readPTY(), nil
}

// HandleOutput feeds raw PTY data into the VT emulator and returns a tea.Cmd
// to continue reading. Returns nil when the session is no longer active.
func (sv *SessionView) HandleOutput(data []byte) tea.Cmd {
	if sv.termView == nil || !sv.active {
		return nil
	}
	sv.termView.Write(data)
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

// View renders the session view with a header bar, terminal content, and a
// status bar at the bottom.
func (sv *SessionView) View() string {
	if !sv.active || sv.termView == nil {
		return ""
	}

	var b strings.Builder

	// Header bar
	header := sv.renderHeader()
	b.WriteString(header)
	b.WriteString("\n")

	// Terminal content
	termContent := sv.termView.Render()
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

	badge := sessionBadgeStyle.Render(" agentmux ")
	left := badge + sessionHeaderStyle.Render(fmt.Sprintf(" %s ", name))
	if provider != "" {
		left += " " + sessionHintStyle.Render(provider)
	}
	if model != "" {
		left += " " + sessionHintStyle.Render(model)
	}

	right := sessionHintStyle.Render(" Tab:trace  Ctrl+f:split  Esc:exit ")

	gap := sv.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}
	fill := sessionHeaderStyle.Render(strings.Repeat(" ", gap))

	return left + fill + right
}

func (sv *SessionView) renderStatusBar() string {
	badge := sessionBadgeStyle.Render(" agentmux ")
	mode := sessionModeStyle.Render(" INTERACTIVE ")
	hint := sessionHintStyle.Render(" Ctrl+f:split  Esc:exit ")

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

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
	dataCh   chan []byte // goroutine-based reader pushes data here
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
	sv.termView = terminal.NewTermView(contentWidth, contentHeight, backend)

	// Resize the backend to match
	_ = backend.Resize(contentWidth, contentHeight)

	// Start a dedicated reader goroutine that pushes data to a channel.
	// This prevents blocking reads from stalling Bubble Tea's command pool.
	//
	// IMPORTANT: capture the channel and backend as local variables so that
	// if Close()+Open() races with this goroutine, the old goroutine closes
	// its own channel (not the new one) and reads from its own backend.
	sv.dataCh = make(chan []byte, 16)
	go readerLoop(backend, sv.dataCh)

	// Return a command that waits for the first chunk from the channel
	return sv.readPTY(), nil
}

// readerLoop runs in a dedicated goroutine and reads from the session backend.
// Data is pushed to dataCh. When the backend closes or errors, the channel is closed.
// The backend and channel are passed as parameters (not accessed via the SessionView
// pointer) so that a subsequent Open() call cannot accidentally cause this goroutine
// to close the new session's channel or read from the new session's backend.
func readerLoop(backend terminal.SessionBackend, ch chan<- []byte) {
	defer close(ch)
	for {
		buf := make([]byte, 4096)
		n, err := backend.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			ch <- data
		}
		if err != nil {
			return
		}
	}
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
// Skips the resize if dimensions haven't changed to avoid sending
// spurious SIGWINCH signals that cause the subprocess to re-draw.
func (sv *SessionView) SetSize(w, h int) {
	if w == sv.width && h == sv.height {
		return
	}
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
		if len(strings.TrimSpace(termContent)) == 0 {
			termContent = fmt.Sprintf("[VT emulator: %dx%d, waiting for content...]", sv.width, sv.height)
		}
	}

	// Scroll indicator is shown in the status bar below, not overlaid on content

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

	right := sessionHintStyle.Render(" Shift+↑↓:scroll  Tab:trace  Ctrl+f:split  Ctrl+]:exit ")

	gap := sv.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}
	fill := sessionHeaderStyle.Render(strings.Repeat(" ", gap))

	return left + fill + right
}

func (sv *SessionView) renderStatusBar() string {
	badge := sessionBadgeStyle.Render(" aimux ")

	// Show scroll state in the mode indicator
	var mode string
	if sv.termView != nil && sv.termView.IsScrolled() {
		scrollStyle := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#111827")).
			Background(lipgloss.Color("#F59E0B"))
		mode = scrollStyle.Render(" SCROLLED ")
	} else {
		mode = sessionModeStyle.Render(" INTERACTIVE ")
	}

	hint := sessionHintStyle.Render(" Shift+↑↓:scroll  PgUp/PgDn:page  Ctrl+f:split  Ctrl+]:exit ")

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
	ch := sv.dataCh
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		// Wait for at least one chunk of data.
		data, ok := <-ch
		if !ok {
			return PTYExitMsg{}
		}

		// Drain any additional buffered data to batch it into one message.
		// This prevents flooding Bubble Tea's message queue with many small
		// PTYOutputMsg messages that starve key event processing.
		for {
			select {
			case more, ok := <-ch:
				if !ok {
					return PTYOutputMsg{Data: data}
				}
				data = append(data, more...)
			default:
				return PTYOutputMsg{Data: data}
			}
		}
	}
}

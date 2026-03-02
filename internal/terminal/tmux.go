package terminal

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zanetworker/aimux/internal/config"
)

// TmuxSession mirrors a tmux session's pane content. It polls
// `tmux capture-pane` for output and forwards keystrokes via
// `tmux send-keys`. This allows embedding any TUI (Codex, Gemini)
// inside aimux's split view.
type TmuxSession struct {
	sessionName string
	created     bool // true if we created the session (kill on Close)

	mu       sync.Mutex
	closed   bool
	prev     []byte // previous capture to detect changes
	rendered string // latest rendered pane content for DirectRenderer
	pending  []byte // signal bytes to keep readPTY loop alive
	signal   chan struct{}

	cancel context.CancelFunc
}

// StartTmux creates a new tmux session running cmd, then mirrors it.
// The session is killed when Close is called.
func StartTmux(cmd *exec.Cmd, cols, rows int, shell, envPrefix string) (*TmuxSession, error) {
	if cmd == nil {
		return nil, fmt.Errorf("tmux: nil command")
	}

	name := fmt.Sprintf("aimux-embed-%d", time.Now().UnixNano())

	// Build the command string for the user's shell with RC file sourced.
	// Use the command name (not absolute path) so shell functions take
	// precedence over the raw binary (e.g., gemini() wrapper).
	var cmdParts []string
	cmdParts = append(cmdParts, filepath.Base(cmd.Args[0]))
	cmdParts = append(cmdParts, cmd.Args[1:]...)
	innerCmd := strings.Join(cmdParts, " ")
	shellCmd := config.ShellRCPrefix(shell) + envPrefix + innerCmd

	args := []string{"new-session", "-d", "-s", name,
		"-x", fmt.Sprintf("%d", cols), "-y", fmt.Sprintf("%d", rows),
		"--", shell, "-lc", shellCmd}

	tmuxCmd := exec.Command("tmux", args...)
	if cmd.Dir != "" {
		tmuxCmd.Dir = cmd.Dir
	}
	tmuxCmd.Env = append(cmd.Environ(), "TERM=xterm-256color")

	if err := tmuxCmd.Run(); err != nil {
		return nil, fmt.Errorf("tmux new-session: %w", err)
	}

	ts := &TmuxSession{
		sessionName: name,
		created:     true,
		signal:      make(chan struct{}, 1),
	}

	ctx, cancel := context.WithCancel(context.Background())
	ts.cancel = cancel
	go ts.poll(ctx)

	return ts, nil
}

// AttachTmux mirrors an existing tmux session without creating it.
// The session is left running when Close is called.
func AttachTmux(sessionName string, cols, rows int) (*TmuxSession, error) {
	// Verify the session exists
	if err := exec.Command("tmux", "has-session", "-t", sessionName).Run(); err != nil {
		return nil, fmt.Errorf("tmux session %q not found: %w", sessionName, err)
	}

	ts := &TmuxSession{
		sessionName: sessionName,
		created:     false,
		signal:      make(chan struct{}, 1),
	}

	ctx, cancel := context.WithCancel(context.Background())
	ts.cancel = cancel
	go ts.poll(ctx)

	return ts, nil
}

// poll periodically captures the tmux pane content and stores it for
// Render(). Also signals Read() so the readPTY loop stays alive.
func (ts *TmuxSession) poll(ctx context.Context) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			out, err := exec.Command("tmux", "capture-pane", "-e", "-p", "-t", ts.sessionName).Output()
			if err != nil {
				continue
			}

			ts.mu.Lock()
			if ts.closed {
				ts.mu.Unlock()
				return
			}

			// Store rendered content for Render() and signal Read()
			if string(out) != string(ts.prev) {
				ts.prev = out
				ts.rendered = string(out)
				// Put a single byte in pending so Read() returns and the
				// readPTY loop triggers a view refresh via PTYOutputMsg.
				ts.pending = append(ts.pending, '.')

				select {
				case ts.signal <- struct{}{}:
				default:
				}
			}
			ts.mu.Unlock()
		}
	}
}

// Read returns captured pane content. Blocks until data is available or
// the session is closed.
func (ts *TmuxSession) Read(buf []byte) (int, error) {
	for {
		ts.mu.Lock()
		if ts.closed {
			ts.mu.Unlock()
			return 0, io.EOF
		}
		if len(ts.pending) > 0 {
			n := copy(buf, ts.pending)
			ts.pending = ts.pending[n:]
			ts.mu.Unlock()
			return n, nil
		}
		ts.mu.Unlock()

		// Wait for new data or closure
		select {
		case <-ts.signal:
			// New data available, loop back to read it
		case <-time.After(200 * time.Millisecond):
			// Periodic check in case we missed a signal
		}
	}
}

// Write sends input to the tmux session via send-keys.
func (ts *TmuxSession) Write(data []byte) (int, error) {
	ts.mu.Lock()
	if ts.closed {
		ts.mu.Unlock()
		return 0, io.EOF
	}
	name := ts.sessionName
	ts.mu.Unlock()

	// Handle special control characters that need tmux key names
	for i := 0; i < len(data); i++ {
		b := data[i]

		// Check for escape sequences
		if b == 0x1b && i+2 < len(data) && data[i+1] == '[' {
			var key string
			switch data[i+2] {
			case 'A':
				key = "Up"
			case 'B':
				key = "Down"
			case 'C':
				key = "Right"
			case 'D':
				key = "Left"
			case 'H':
				key = "Home"
			case 'F':
				key = "End"
			}
			if key != "" {
				exec.Command("tmux", "send-keys", "-t", name, key).Run()
				i += 2
				continue
			}
		}

		// Single control characters
		switch b {
		case '\r':
			exec.Command("tmux", "send-keys", "-t", name, "Enter").Run()
		case '\t':
			exec.Command("tmux", "send-keys", "-t", name, "Tab").Run()
		case 0x7f:
			exec.Command("tmux", "send-keys", "-t", name, "BSpace").Run()
		case 0x1b:
			exec.Command("tmux", "send-keys", "-t", name, "Escape").Run()
		case 0x03:
			exec.Command("tmux", "send-keys", "-t", name, "C-c").Run()
		case 0x04:
			exec.Command("tmux", "send-keys", "-t", name, "C-d").Run()
		default:
			if b >= 1 && b <= 26 {
				// Ctrl+A through Ctrl+Z
				letter := string(rune('a' + b - 1))
				exec.Command("tmux", "send-keys", "-t", name, "C-"+letter).Run()
			} else if b >= 32 && b < 127 {
				// Printable ASCII — use literal mode
				exec.Command("tmux", "send-keys", "-t", name, "-l", string(b)).Run()
			}
		}
	}

	return len(data), nil
}

// Resize changes the tmux window dimensions.
func (ts *TmuxSession) Resize(cols, rows int) error {
	ts.mu.Lock()
	if ts.closed {
		ts.mu.Unlock()
		return nil
	}
	name := ts.sessionName
	ts.mu.Unlock()

	// Resize both the window and the pane to ensure they match
	exec.Command("tmux", "resize-window", "-t", name,
		"-x", fmt.Sprintf("%d", cols), "-y", fmt.Sprintf("%d", rows)).Run()

	return nil
}

// Close stops polling. If the session was created by StartTmux, it kills
// the tmux session. If it was attached via AttachTmux, the session
// continues running.
func (ts *TmuxSession) Close() error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.closed {
		return nil
	}
	ts.closed = true
	ts.cancel()

	// Signal any blocked Read calls
	select {
	case ts.signal <- struct{}{}:
	default:
	}

	if ts.created {
		exec.Command("tmux", "kill-session", "-t", ts.sessionName).Run()
	}
	return nil
}

// Alive returns true if the tmux session still exists.
func (ts *TmuxSession) Alive() bool {
	ts.mu.Lock()
	if ts.closed {
		ts.mu.Unlock()
		return false
	}
	name := ts.sessionName
	ts.mu.Unlock()

	err := exec.Command("tmux", "has-session", "-t", name).Run()
	return err == nil
}

// SessionName returns the tmux session name for external reference.
func (ts *TmuxSession) SessionName() string {
	return ts.sessionName
}

// Render returns the latest captured pane content. This implements the
// DirectRenderer interface, allowing SessionView to skip the VT emulator
// and display the tmux output directly (it's already rendered terminal text).
func (ts *TmuxSession) Render() string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.rendered
}

// tmuxEscapeText escapes text for tmux send-keys literal mode.
func tmuxEscapeText(s string) string {
	return strings.ReplaceAll(s, ";", "\\;")
}

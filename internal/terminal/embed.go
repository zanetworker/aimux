package terminal

import (
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
)

// Session manages a PTY-backed subprocess. It provides thread-safe read, write,
// and resize operations for embedding interactive terminal processes.
type Session struct {
	cmd    *exec.Cmd
	ptmx   *os.File
	mu     sync.Mutex
	closed bool
}

// Start spawns a command inside a new PTY and returns a Session.
func Start(cmd *exec.Cmd) (*Session, error) {
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}
	return &Session{cmd: cmd, ptmx: ptmx}, nil
}

// Read reads output from the PTY. It returns io.EOF if the session is closed.
func (s *Session) Read(buf []byte) (int, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, io.EOF
	}
	ptmx := s.ptmx
	s.mu.Unlock()
	return ptmx.Read(buf)
}

// Write sends input to the PTY. It returns io.EOF if the session is closed.
func (s *Session) Write(data []byte) (int, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, io.EOF
	}
	ptmx := s.ptmx
	s.mu.Unlock()
	return ptmx.Write(data)
}

// Resize changes the PTY window size. It is safe to call after Close.
func (s *Session) Resize(cols, rows int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	return pty.Setsize(s.ptmx, &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	})
}

// termResetSeq restores standard terminal modes that the subprocess may have
// changed. This prevents terminal corruption when the subprocess is killed
// without getting a chance to clean up.
//
// Sequences: disable CSI u mode, disable bracketed paste, disable focus
// reporting, disable mouse reporting, reset character set, show cursor.
var termResetSeq = []byte(
	"\x1b[<u" + // pop kitty keyboard protocol mode (CSI u)
		"\x1b[<u" + // pop again (in case multiple levels were pushed)
		"\x1b[<u" + // pop a third time for safety
		"\x1b[?2004l" + // disable bracketed paste
		"\x1b[?1004l" + // disable focus reporting
		"\x1b[?1000l" + // disable mouse click reporting
		"\x1b[?1002l" + // disable mouse drag reporting
		"\x1b[?1003l" + // disable mouse all-motion reporting
		"\x1b[?1006l" + // disable SGR mouse mode
		"\x1b[?25h" + // show cursor
		"\x1b[?1049l" + // exit alternate screen
		"\x1b>", // reset character set (DECKPNM)
)

// Close terminates the PTY session. It sends terminal reset sequences to
// restore standard modes, then closes the PTY file descriptor and signals
// the process to exit. Cleanup runs in a background goroutine so the TUI
// returns immediately. It is safe to call multiple times.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true

	// Write reset sequences before closing to prevent terminal corruption
	_, _ = s.ptmx.Write(termResetSeq)

	_ = s.ptmx.Close()
	cmd := s.cmd
	go func() {
		if cmd.Process != nil {
			_ = cmd.Process.Signal(os.Interrupt)
		}
		_ = cmd.Wait()
	}()
	return nil
}

// Alive returns true if the session is still open and the process has not exited.
func (s *Session) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && s.cmd.ProcessState == nil
}

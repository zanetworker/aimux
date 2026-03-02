package terminal

import (
	"sync"

	"github.com/charmbracelet/x/vt"
)

// TermView wraps a charmbracelet/x/vt terminal emulator, providing a
// thread-safe interface for writing PTY output and rendering the screen buffer
// as an ANSI-styled string.
type TermView struct {
	term   *vt.SafeEmulator
	mu     sync.Mutex
	width  int
	height int
}

// NewTermView creates a new TermView with the given column and row dimensions.
func NewTermView(cols, rows int) *TermView {
	return &TermView{
		term:   vt.NewSafeEmulator(cols, rows),
		width:  cols,
		height: rows,
	}
}

// Write feeds raw PTY output into the VT emulator. This processes ANSI escape
// sequences and updates the internal screen buffer.
func (tv *TermView) Write(data []byte) {
	tv.term.Write(data)
}

// Resize changes the dimensions of the virtual terminal.
func (tv *TermView) Resize(cols, rows int) {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	tv.width = cols
	tv.height = rows
	tv.term.Resize(cols, rows)
}

// Render returns the current screen buffer as a string with ANSI styling.
func (tv *TermView) Render() string {
	return tv.term.Render()
}

// Width returns the current terminal width in columns.
func (tv *TermView) Width() int {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	return tv.width
}

// Height returns the current terminal height in rows.
func (tv *TermView) Height() int {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	return tv.height
}

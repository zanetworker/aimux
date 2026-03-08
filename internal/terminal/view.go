package terminal

import (
	"strings"
	"sync"

	"github.com/charmbracelet/x/vt"
)

// TermView wraps a charmbracelet/x/vt terminal emulator, providing a
// thread-safe interface for writing PTY output and rendering the screen buffer
// as an ANSI-styled string.
type TermView struct {
	term       *vt.SafeEmulator
	mu         sync.Mutex
	width      int
	height     int
	history    []string // scroll-back buffer: past screen renders
	scrollBack int      // 0 = live bottom, >0 = lines scrolled up
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
// sequences and updates the internal screen buffer. Before writing, the current
// screen is snapshotted into the scroll-back history if the content changes.
func (tv *TermView) Write(data []byte) {
	tv.mu.Lock()
	preRender := tv.term.Render()
	tv.mu.Unlock()

	tv.term.Write(data)

	tv.mu.Lock()
	defer tv.mu.Unlock()

	postRender := tv.term.Render()

	// If content changed, capture pre-render lines into history
	if preRender != postRender {
		for _, line := range strings.Split(preRender, "\n") {
			tv.history = append(tv.history, line)
		}
		// Cap history at 10000 lines to prevent unbounded growth
		if len(tv.history) > 10000 {
			tv.history = tv.history[len(tv.history)-10000:]
		}
	}

	// Auto-snap to bottom on new output
	tv.scrollBack = 0
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
// When scrolled back, it returns lines from the history buffer instead of the
// live terminal output.
func (tv *TermView) Render() string {
	tv.mu.Lock()
	scrollBack := tv.scrollBack
	histLen := len(tv.history)
	height := tv.height
	tv.mu.Unlock()

	if scrollBack == 0 || histLen == 0 {
		return tv.term.Render()
	}

	tv.mu.Lock()
	// Take lines from history
	end := histLen
	start := end - scrollBack
	if start < 0 {
		start = 0
	}
	visible := make([]string, 0, height)
	for i := start; i < end && len(visible) < height; i++ {
		visible = append(visible, tv.history[i])
	}
	tv.mu.Unlock()

	return strings.Join(visible, "\n")
}

// ScrollUp moves the viewport up by n lines.
func (tv *TermView) ScrollUp(n int) {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	tv.scrollBack += n
	if tv.scrollBack > len(tv.history) {
		tv.scrollBack = len(tv.history)
	}
}

// ScrollDown moves the viewport down by n lines. Snaps to live at 0.
func (tv *TermView) ScrollDown(n int) {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	tv.scrollBack -= n
	if tv.scrollBack < 0 {
		tv.scrollBack = 0
	}
}

// IsScrolled returns true if viewing history (not live bottom).
func (tv *TermView) IsScrolled() bool {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	return tv.scrollBack > 0
}

// SnapToBottom resets scroll to live view.
func (tv *TermView) SnapToBottom() {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	tv.scrollBack = 0
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

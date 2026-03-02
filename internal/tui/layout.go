package tui

// Layout manages split pane dimensions and zoom state for the TUI.
type Layout struct {
	width  int
	height int
	zoomed bool
}

// NewLayout creates a new Layout with the given dimensions.
func NewLayout(w, h int) *Layout {
	return &Layout{width: w, height: h}
}

// SetSize updates the layout dimensions.
func (l *Layout) SetSize(w, h int) { l.width = w; l.height = h }

// SplitVertical divides the width at the given percentage, returning left and right widths.
func (l *Layout) SplitVertical(percent int) (left, right int) {
	left = l.width * percent / 100
	right = l.width - left
	return
}

// ContentHeight returns the available height for content after subtracting the header
// and one line for the status bar.
func (l *Layout) ContentHeight(headerHeight int) int {
	h := l.height - headerHeight - 1
	if h < 1 {
		return 1
	}
	return h
}

// SetZoomed sets the zoom state.
func (l *Layout) SetZoomed(z bool) { l.zoomed = z }

// IsZoomed returns whether a pane is currently zoomed.
func (l *Layout) IsZoomed() bool { return l.zoomed }

// Width returns the current layout width.
func (l *Layout) Width() int { return l.width }

// Height returns the current layout height.
func (l *Layout) Height() int { return l.height }

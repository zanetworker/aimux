package tui

import "testing"

func TestSplitVertical(t *testing.T) {
	l := NewLayout(100, 40)

	tests := []struct {
		percent   int
		wantLeft  int
		wantRight int
	}{
		{50, 50, 50},
		{30, 30, 70},
		{70, 70, 30},
		{0, 0, 100},
		{100, 100, 0},
	}
	for _, tt := range tests {
		left, right := l.SplitVertical(tt.percent)
		if left != tt.wantLeft || right != tt.wantRight {
			t.Errorf("SplitVertical(%d) = (%d, %d), want (%d, %d)",
				tt.percent, left, right, tt.wantLeft, tt.wantRight)
		}
	}
}

func TestSplitVerticalSumsToWidth(t *testing.T) {
	l := NewLayout(123, 40)
	left, right := l.SplitVertical(33)
	if left+right != 123 {
		t.Errorf("SplitVertical(33) left+right = %d, want 123", left+right)
	}
}

func TestContentHeight(t *testing.T) {
	tests := []struct {
		height       int
		headerHeight int
		want         int
	}{
		{40, 8, 31},  // 40 - 8 - 1 = 31
		{10, 8, 1},   // 10 - 8 - 1 = 1
		{5, 8, 1},    // 5 - 8 - 1 = -4, clamped to 1
		{2, 0, 1},    // 2 - 0 - 1 = 1
		{1, 0, 1},    // 1 - 0 - 1 = 0, clamped to 1
		{50, 10, 39}, // 50 - 10 - 1 = 39
	}
	for _, tt := range tests {
		l := NewLayout(100, tt.height)
		got := l.ContentHeight(tt.headerHeight)
		if got != tt.want {
			t.Errorf("ContentHeight(%d) with height=%d = %d, want %d",
				tt.headerHeight, tt.height, got, tt.want)
		}
	}
}

func TestZoomToggle(t *testing.T) {
	l := NewLayout(100, 40)

	if l.IsZoomed() {
		t.Error("new Layout should not be zoomed")
	}

	l.SetZoomed(true)
	if !l.IsZoomed() {
		t.Error("expected zoomed after SetZoomed(true)")
	}

	l.SetZoomed(false)
	if l.IsZoomed() {
		t.Error("expected not zoomed after SetZoomed(false)")
	}
}

func TestSetSize(t *testing.T) {
	l := NewLayout(100, 40)

	l.SetSize(200, 80)
	if l.Width() != 200 || l.Height() != 80 {
		t.Errorf("after SetSize(200, 80): Width()=%d, Height()=%d", l.Width(), l.Height())
	}
}

func TestWidthHeight(t *testing.T) {
	l := NewLayout(120, 50)
	if l.Width() != 120 {
		t.Errorf("Width() = %d, want 120", l.Width())
	}
	if l.Height() != 50 {
		t.Errorf("Height() = %d, want 50", l.Height())
	}
}

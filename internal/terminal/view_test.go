package terminal

import (
	"fmt"
	"testing"
)

func TestTermView_ScrollMethods(t *testing.T) {
	tv := NewTermView(80, 24)

	if tv.IsScrolled() {
		t.Error("should not be scrolled initially")
	}

	// Add some history manually
	tv.mu.Lock()
	for i := 0; i < 50; i++ {
		tv.history = append(tv.history, fmt.Sprintf("history line %d", i))
	}
	tv.mu.Unlock()

	tv.ScrollUp(10)
	if !tv.IsScrolled() {
		t.Error("should be scrolled after ScrollUp")
	}

	tv.ScrollDown(10)
	if tv.IsScrolled() {
		t.Error("should not be scrolled after ScrollDown back to 0")
	}

	// Scroll past history — should clamp
	tv.ScrollUp(100)
	tv.mu.Lock()
	if tv.scrollBack != 50 {
		t.Errorf("scrollBack = %d, want 50 (clamped)", tv.scrollBack)
	}
	tv.mu.Unlock()

	tv.SnapToBottom()
	if tv.IsScrolled() {
		t.Error("should not be scrolled after SnapToBottom")
	}
}

func TestTermView_ScrollDown_Clamp(t *testing.T) {
	tv := NewTermView(80, 24)

	// Scroll down when already at 0 — should stay at 0
	tv.ScrollDown(5)
	tv.mu.Lock()
	if tv.scrollBack != 0 {
		t.Errorf("scrollBack = %d, want 0 (can't go below 0)", tv.scrollBack)
	}
	tv.mu.Unlock()
}

func TestTermView_RenderLive(t *testing.T) {
	tv := NewTermView(80, 5)

	// When not scrolled, should return term.Render()
	result := tv.Render()
	if result == "" {
		// Empty terminal still renders something
		t.Log("empty render is ok")
	}
}

func TestTermView_RenderScrolled(t *testing.T) {
	tv := NewTermView(80, 5)

	// Add history
	tv.mu.Lock()
	for i := 0; i < 20; i++ {
		tv.history = append(tv.history, fmt.Sprintf("line %d", i))
	}
	tv.mu.Unlock()

	// Scroll up 10 lines
	tv.ScrollUp(10)

	result := tv.Render()
	if result == "" {
		t.Error("scrolled render should not be empty with history")
	}

	// Should contain history lines, not live terminal
	// The visible window should be history[10:15] (5 lines from end-10)
	if result == tv.term.Render() {
		t.Error("scrolled render should differ from live render when history exists")
	}
}

func TestTermView_HistoryCap(t *testing.T) {
	tv := NewTermView(80, 24)

	// Manually add more than 10000 lines
	tv.mu.Lock()
	for i := 0; i < 10500; i++ {
		tv.history = append(tv.history, fmt.Sprintf("line %d", i))
	}
	tv.mu.Unlock()

	// Write data to trigger the cap logic
	tv.Write([]byte("trigger\n"))

	tv.mu.Lock()
	histLen := len(tv.history)
	tv.mu.Unlock()

	if histLen > 10000 {
		t.Errorf("history length = %d, want <= 10000", histLen)
	}
}

func TestTermView_WriteSnapsToBottom(t *testing.T) {
	tv := NewTermView(80, 24)

	// Add history and scroll up
	tv.mu.Lock()
	for i := 0; i < 20; i++ {
		tv.history = append(tv.history, fmt.Sprintf("line %d", i))
	}
	tv.mu.Unlock()

	tv.ScrollUp(5)
	if !tv.IsScrolled() {
		t.Fatal("should be scrolled after ScrollUp")
	}

	// Write new data — should NOT snap to bottom when user is scrolled back.
	// User must explicitly scroll down to return to live view.
	tv.Write([]byte("new output\n"))

	if !tv.IsScrolled() {
		t.Error("should stay scrolled when user is reading history")
	}

	// Explicitly scroll to bottom
	tv.SnapToBottom()
	if tv.IsScrolled() {
		t.Error("should be at bottom after SnapToBottom")
	}
}

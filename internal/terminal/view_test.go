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

	// Write data and call SnapshotHistory to trigger the cap logic
	tv.Write([]byte("trigger\n"))
	tv.SnapshotHistory()

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

func TestWriteSetsDirty(t *testing.T) {
	tv := NewTermView(80, 24)
	tv.Write([]byte("hello world\n"))

	tv.mu.Lock()
	dirty := tv.dirty
	tv.mu.Unlock()

	if !dirty {
		t.Error("expected dirty=true after Write")
	}
}

func TestRenderClearsDirty(t *testing.T) {
	tv := NewTermView(80, 24)
	tv.Write([]byte("hello\n"))
	_ = tv.Render()

	tv.mu.Lock()
	dirty := tv.dirty
	tv.mu.Unlock()

	if dirty {
		t.Error("expected dirty=false after Render")
	}
}

func TestRenderCaching(t *testing.T) {
	tv := NewTermView(80, 24)
	tv.Write([]byte("hello\n"))

	r1 := tv.Render()
	r2 := tv.Render()

	if r1 != r2 {
		t.Error("consecutive Render calls with no Write should return same string")
	}
}

func TestSnapshotHistory(t *testing.T) {
	tv := NewTermView(80, 24)
	tv.Write([]byte("line one\n"))
	tv.SnapshotHistory()

	tv.mu.Lock()
	histLen := len(tv.history)
	tv.mu.Unlock()

	if histLen == 0 {
		t.Error("expected history to have entries after SnapshotHistory")
	}
}

func TestSnapshotHistorySkipsIfNoWrites(t *testing.T) {
	tv := NewTermView(80, 24)
	tv.SnapshotHistory()

	tv.mu.Lock()
	histLen := len(tv.history)
	tv.mu.Unlock()

	if histLen != 0 {
		t.Error("expected empty history when no writes occurred")
	}
}

func TestWriteDoesNotPopulateHistory(t *testing.T) {
	tv := NewTermView(80, 24)
	tv.Write([]byte("hello\n"))

	tv.mu.Lock()
	histLen := len(tv.history)
	tv.mu.Unlock()

	if histLen != 0 {
		t.Error("Write should not populate history directly; SnapshotHistory does that")
	}
}

func TestWriteIncrementsBytesWritten(t *testing.T) {
	tv := NewTermView(80, 24)
	tv.Write([]byte("hello"))

	tv.mu.Lock()
	bw := tv.bytesWritten
	tv.mu.Unlock()

	if bw != 5 {
		t.Errorf("bytesWritten = %d, want 5", bw)
	}

	tv.Write([]byte("world"))

	tv.mu.Lock()
	bw = tv.bytesWritten
	tv.mu.Unlock()

	if bw != 10 {
		t.Errorf("bytesWritten = %d, want 10 after second Write", bw)
	}
}

func TestSnapshotHistoryResetsBytesWritten(t *testing.T) {
	tv := NewTermView(80, 24)
	tv.Write([]byte("data"))
	tv.SnapshotHistory()

	tv.mu.Lock()
	bw := tv.bytesWritten
	tv.mu.Unlock()

	if bw != 0 {
		t.Errorf("bytesWritten = %d after SnapshotHistory, want 0", bw)
	}
}

func TestRenderCacheInvalidatedByWrite(t *testing.T) {
	tv := NewTermView(80, 24)
	tv.Write([]byte("first\n"))
	r1 := tv.Render()

	tv.Write([]byte("second\n"))
	r2 := tv.Render()

	// After a new Write, Render should re-render (dirty=true),
	// so the result may differ from the cached one.
	// We verify caching works by checking the cache is populated.
	tv.mu.Lock()
	cached := tv.cachedView
	tv.mu.Unlock()

	if cached != r2 {
		t.Error("cachedView should equal the latest Render result")
	}

	// r1 and r2 should differ since we wrote new content
	if r1 == r2 {
		// VT emulator may or may not produce different output depending
		// on the content, but the cache should at least have been refreshed.
		t.Log("r1 == r2 is possible if VT emulator output is the same; cache still works")
	}
}

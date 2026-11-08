package history

import (
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// A read of one transcript (seconds for a large one) must not hold up a
// title lookup for another: the TUI's preview asks for its title in View
// while a refresh warms the others.
func TestShownTitleForFile_OtherFileReadDoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.jsonl"), filepath.Join(dir, "b.jsonl")
	writeTitleSession(t, a, titleUserLine)
	writeTitleSession(t, b, titleUserLine)
	WarmTitles([]string{a, b})

	tb := titleFileFor(b)
	tb.mu.Lock() // b is being read
	defer tb.mu.Unlock()
	done := make(chan string, 1)
	go func() { done <- ShownTitleForFile(a) }()
	select {
	case got := <-done:
		if got != "fix the login bug" {
			t.Errorf("got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("title lookup for a waited for the read of b")
	}
}

func TestWarmTitles_ManyFilesInParallel(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for i := 0; i < 40; i++ {
		p := filepath.Join(dir, strconv.Itoa(i)+".jsonl")
		writeTitleSession(t, p, titleUserLine, titleAILine)
		paths = append(paths, p, p) // duplicates read concurrently too
	}
	WarmTitles(paths)
	for _, p := range paths {
		tf := titleFileFor(p)
		tf.mu.Lock()
		got := tf.s.AITitle
		tf.mu.Unlock()
		if got != "Login bug fix" {
			t.Fatalf("%s not warmed: %q", filepath.Base(p), got)
		}
		if title := ShownTitleForFile(p); title != "Login bug fix" {
			t.Fatalf("%s: title %q", filepath.Base(p), title)
		}
	}
}

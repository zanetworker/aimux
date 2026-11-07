package history

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestTitleRule pins the one title rule every surface uses: the .meta.json
// title (the owner's or the namer's), then Claude Code's ai-title, then the
// first message; DisplayTitle is the list form of the same choice.
func TestTitleRule(t *testing.T) {
	long := strings.Repeat("x", 100)
	tests := []struct {
		name          string
		s             Session
		shown, listed string
	}{
		{"meta title wins", Session{Title: "Meta", AITitle: "AI", FirstPrompt: "first"}, "Meta", "Meta"},
		{"owner title over /rename", Session{Title: "Owner", TitleBy: "owner", CustomTitle: "renamed", AITitle: "AI"}, "Owner", "Owner"},
		{"/rename over the namer", Session{Title: "Namer", TitleBy: "model", CustomTitle: "renamed", AITitle: "AI"}, "renamed", "renamed"},
		{"/rename over an unmarked (model) meta title", Session{Title: "Namer", CustomTitle: "renamed"}, "renamed", "renamed"},
		{"/rename over ai-title", Session{CustomTitle: "renamed", AITitle: "AI", FirstPrompt: "first"}, "renamed", "renamed"},
		{"namer over ai-title", Session{Title: "Namer", TitleBy: "model", AITitle: "AI"}, "Namer", "Namer"},
		{"owner title over ai-title", Session{Title: "Owner", TitleBy: "owner", AITitle: "AI"}, "Owner", "Owner"},
		{"ai-title over first message", Session{AITitle: "AI", FirstPrompt: "first"}, "AI", "AI"},
		{"first message only", Session{FirstPrompt: "fix the cache"}, "fix the cache", "fix the cache"},
		{"long first message is cut at 80 bytes in the list form", Session{FirstPrompt: long}, long, strings.Repeat("x", 80) + "…"},
		{"first message is labelled in the list form", Session{FirstPrompt: "Stop hook feedback: x"}, "Stop hook feedback: x", "(hook feedback)"},
		{"first message is cleaned in the list form", Session{FirstPrompt: "[Image #1] fix it"}, "[Image #1] fix it", "fix it"},
		{"a notification is named in the list form", Session{FirstPrompt: "<task-notification>"}, "<task-notification>", "(notification)"},
		{"long titles are kept as written", Session{AITitle: long}, long, long},
		{"nothing", Session{}, "", ""},
	}
	for _, tt := range tests {
		if got := ShownTitle(tt.s); got != tt.shown {
			t.Errorf("%s: ShownTitle = %q, want %q", tt.name, got, tt.shown)
		}
		if got := DisplayTitle(tt.s); got != tt.listed {
			t.Errorf("%s: DisplayTitle = %q, want %q", tt.name, got, tt.listed)
		}
	}
}

func writeTitleSession(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

const (
	titleUserLine = `{"type":"user","message":{"role":"user","content":"fix the login bug"},"timestamp":"2026-10-01T10:00:00Z"}`
	titleAILine   = `{"type":"ai-title","aiTitle":"Login bug fix"}`
	titleAILine2  = `{"type":"ai-title","aiTitle":"Login and session fix"}`
)

func TestShownTitleForFile(t *testing.T) {
	dir := t.TempDir()

	first := filepath.Join(dir, "a.jsonl")
	writeTitleSession(t, first, titleUserLine)
	if got := ShownTitleForFile(first); got != "fix the login bug" {
		t.Errorf("first prompt only: got %q", got)
	}

	ai := filepath.Join(dir, "b.jsonl")
	writeTitleSession(t, ai, titleUserLine, titleAILine)
	if got := ShownTitleForFile(ai); got != "Login bug fix" {
		t.Errorf("ai-title: got %q", got)
	}

	meta := filepath.Join(dir, "c.jsonl")
	writeTitleSession(t, meta, titleUserLine, titleAILine)
	if err := UpdateMeta(meta, func(m *Meta) { m.Title = "Owner title"; m.TitleBy = "owner" }); err != nil {
		t.Fatal(err)
	}
	if got := ShownTitleForFile(meta); got != "Owner title" {
		t.Errorf("meta title: got %q", got)
	}

	if got := ShownTitleForFile(""); got != "" {
		t.Errorf("empty path: got %q", got)
	}
	if got := ShownTitleForFile(filepath.Join(dir, "missing.jsonl")); got != "" {
		t.Errorf("missing file: got %q", got)
	}
}

// A live session grows: a later ai-title replaces the earlier one, and a
// meta title written or removed later shows on the next call.
func TestShownTitleForFile_FollowsChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.jsonl")
	writeTitleSession(t, path, titleUserLine)
	if got := ShownTitleForFile(path); got != "fix the login bug" {
		t.Fatalf("start: got %q", got)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- a temp file
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(titleAILine + "\n" + titleAILine2 + "\n" + `{"type":"ai-title","aiTitle":"half writ`) // last line still being written
	_ = f.Close()
	future := time.Now().Add(time.Second)
	_ = os.Chtimes(path, future, future)
	if got := ShownTitleForFile(path); got != "Login and session fix" {
		t.Errorf("after growth: got %q", got)
	}

	if err := UpdateMeta(path, func(m *Meta) { m.Title = "Named" }); err != nil {
		t.Fatal(err)
	}
	if got := ShownTitleForFile(path); got != "Named" {
		t.Errorf("after meta: got %q", got)
	}
	if err := UpdateMeta(path, func(m *Meta) { m.Title = "" }); err != nil {
		t.Fatal(err)
	}
	if got := ShownTitleForFile(path); got != "Login and session fix" {
		t.Errorf("after meta cleared: got %q", got)
	}

	// a rewritten (shorter) file is read again from the start
	writeTitleSession(t, path, `{"type":"user","message":{"role":"user","content":"new start"}}`)
	if got := ShownTitleForFile(path); got != "new start" {
		t.Errorf("after rewrite: got %q", got)
	}
}

func TestShownTitleForFile_CustomTitleAndOwner(t *testing.T) {
	dir := t.TempDir()
	renamed := filepath.Join(dir, "r.jsonl")
	writeTitleSession(t, renamed, titleUserLine, titleAILine, `{"type":"custom-title","customTitle":"login-work"}`)
	if got := ShownTitleForFile(renamed); got != "login-work" {
		t.Errorf("/rename: got %q", got)
	}
	if err := UpdateMeta(renamed, func(m *Meta) { m.Title, m.TitleBy = "Namer title", "model" }); err != nil {
		t.Fatal(err)
	}
	if got := ShownTitleForFile(renamed); got != "login-work" {
		t.Errorf("/rename over the namer: got %q", got)
	}
	if err := UpdateMeta(renamed, func(m *Meta) { m.Title, m.TitleBy = "Owner title", "owner" }); err != nil {
		t.Fatal(err)
	}
	if got := ShownTitleForFile(renamed); got != "Owner title" {
		t.Errorf("owner over /rename: got %q", got)
	}
}

// The parse cache keeps at most titleFilesMax files, dropping the oldest.
func TestShownTitleForFile_CacheIsCapped(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "0.jsonl")
	for i := 0; i < titleFilesMax+20; i++ {
		p := filepath.Join(dir, strconv.Itoa(i)+".jsonl")
		writeTitleSession(t, p, titleUserLine)
		if got := ShownTitleForFile(p); got != "fix the login bug" {
			t.Fatalf("file %d: got %q", i, got)
		}
	}
	titleFiles.Lock()
	n, kept := len(titleFiles.m), titleFiles.m[first] != nil
	titleFiles.Unlock()
	if n > titleFilesMax {
		t.Errorf("%d cached files, want at most %d", n, titleFilesMax)
	}
	if kept {
		t.Error("the oldest file is still cached")
	}
	if got := ShownTitleForFile(first); got != "fix the login bug" {
		t.Errorf("a dropped file is read again: got %q", got)
	}
}

// WarmTitles parses transcripts ahead of a render, so ShownTitleForFile on
// the render path finds them read.
func TestWarmTitles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "warm.jsonl")
	writeTitleSession(t, path, titleUserLine, titleAILine)
	WarmTitles([]string{path, "", filepath.Join(t.TempDir(), "missing.jsonl")})
	info, _ := os.Stat(path)
	titleFiles.Lock()
	tf := titleFiles.m[path]
	titleFiles.Unlock()
	if tf == nil || tf.offset != info.Size() || tf.s.AITitle != "Login bug fix" {
		t.Fatalf("not warmed: %+v", tf)
	}
}

// ShownTitleForFileMeta takes a .meta.json the caller already read.
func TestShownTitleForFileMeta(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.jsonl")
	writeTitleSession(t, path, titleUserLine, titleAILine)
	if got := ShownTitleForFileMeta(path, Meta{Title: "Given", TitleBy: "owner"}); got != "Given" {
		t.Errorf("given meta: got %q", got)
	}
	if got := ShownTitleForFileMeta(path, Meta{}); got != "Login bug fix" {
		t.Errorf("empty meta: got %q", got)
	}
	if got := ShownTitleForFileMeta("", Meta{Title: "x"}); got != "" {
		t.Errorf("no path: got %q", got)
	}
}

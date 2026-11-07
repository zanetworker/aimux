package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/harness"
	"github.com/zanetworker/aimux/internal/history"
)

func TestClaudeSessionStore(t *testing.T) {
	projects := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projects, "-w-app"), 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(projects, "-w-app", "s-1.jsonl")
	if err := os.WriteFile(file, []byte(`{"type":"user","cwd":"/w/app"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Claude{ProjectsDir: projects}

	if got := c.SessionFile("s-1"); got != file {
		t.Fatalf("SessionFile = %q, want %q", got, file)
	}
	for _, id := range []string{"missing", "../s-1", "*"} {
		if got := c.SessionFile(id); got != "" {
			t.Fatalf("SessionFile(%q) = %q, want none", id, got)
		}
	}
	if cwd, err := c.SessionCwd(file); err != nil || cwd != "/w/app" {
		t.Fatalf("SessionCwd = %q, %v", cwd, err)
	}

	fp := c.Fingerprint("s-1")
	if fp == "" {
		t.Fatal("no fingerprint for an existing session")
	}
	if again := c.Fingerprint("s-1"); again != fp {
		t.Fatalf("fingerprint moved without a change: %q then %q", fp, again)
	}
	if err := os.WriteFile(file, []byte(`{"type":"user","cwd":"/w/app"}`+"\n"+`{"type":"assistant"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatal(err)
	}
	if changed := c.Fingerprint("s-1"); changed == fp || changed == "" {
		t.Fatalf("fingerprint %q did not change with the transcript (was %q)", changed, fp)
	}
	if got := c.Fingerprint("missing"); got != "" {
		t.Fatalf("Fingerprint(missing) = %q, want none", got)
	}
}

// Sessions lists the transcripts under ProjectsDir as Claude sessions, and
// ParseTail reads a transcript's latest turns as ParseTraceTail does.
func TestClaudeSessionStore_SessionsAndTail(t *testing.T) {
	projects := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projects, "-w-app"), 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(projects, "-w-app", "s-1.jsonl")
	lines := `{"type":"user","cwd":"/w/app","sessionId":"s-1","timestamp":"2026-01-01T10:00:00Z","message":{"role":"user","content":"first"}}` + "\n" +
		`{"type":"user","cwd":"/w/app","sessionId":"s-1","timestamp":"2026-01-01T10:01:00Z","message":{"role":"user","content":"second"}}` + "\n"
	if err := os.WriteFile(file, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Claude{ProjectsDir: projects}

	all, err := c.Sessions(history.DiscoverOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].ID != "s-1" || all[0].Provider != c.Name() || all[0].FilePath != file {
		t.Fatalf("Sessions = %+v, want s-1 as a %s session", all, c.Name())
	}
	if other, err := c.Sessions(history.DiscoverOpts{Provider: "codex"}); err != nil || len(other) != 0 {
		t.Fatalf("Sessions(codex) = %+v, %v; want none", other, err)
	}
	if none, err := (&Claude{ProjectsDir: filepath.Join(projects, "missing")}).Sessions(history.DiscoverOpts{}); err != nil || len(none) != 0 {
		t.Fatalf("Sessions without a projects dir = %+v, %v; want none", none, err)
	}

	tail, err := c.ParseTail(file, 1)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := c.ParseTraceTail(file, 1)
	if len(tail) == 0 || len(tail) != len(want) || strings.Join(tail[len(tail)-1].UserLines, "") != "second" {
		t.Fatalf("ParseTail = %+v, want the last turn (%+v)", tail, want)
	}
	if _, err := c.ParseTail(filepath.Join(projects, "nope.jsonl"), 1); err == nil {
		t.Fatal("ParseTail of a missing file: no error")
	}
}

// With no home directory known, the transcripts dir falls back to the
// relative .claude/projects, as before the registry, never "" (which would
// make a glob look in the current directory).
func TestTranscriptsDir_NoHomeFallsBackToRelative(t *testing.T) {
	t.Setenv("HOME", "")
	got := (&Claude{}).TranscriptsDir()
	if got == "" {
		t.Fatal("TranscriptsDir is empty with no home directory")
	}
	if !strings.HasSuffix(got, filepath.Join(".claude", "projects")) {
		t.Fatalf("TranscriptsDir = %q, want it to end in .claude/projects", got)
	}
	if got := (&Claude{ProjectsDir: "/w/projects"}).TranscriptsDir(); got != "/w/projects" {
		t.Fatalf("explicit ProjectsDir = %q", got)
	}
}

// One registry reader per Claude and ttl: LiveSessions returns the same
// LiveCache each time, and discovery reads the registry through it, so a
// TUI or dashboard refresh (discover, then the state service's holders)
// reads the registry and runs ps once.
func TestClaude_SharesOneLiveCache(t *testing.T) {
	dir := t.TempDir()
	me := os.Getpid()
	entry := fmt.Sprintf(`{"pid":%d,"sessionId":"shared-1","cwd":"/w","status":"busy"}`, me)
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", me)), []byte(entry), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Claude{RegistryDir: dir, ProjectsDir: t.TempDir()}
	if c.LiveSessions(discovery.LiveTTL) != c.LiveSessions(discovery.LiveTTL) {
		t.Fatal("LiveSessions built a second reader for the same ttl")
	}
	if c.LiveSessions(0) == c.LiveSessions(discovery.LiveTTL) {
		t.Fatal("another ttl shares the reader")
	}
	agents, err := c.discoverFromAgents([]agent.Agent{{PID: me}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) == 0 || agents[0].SessionID != "shared-1" {
		t.Fatalf("discovery did not read RegistryDir: %+v", agents)
	}
	// The registry is gone, but within the TTL the holders come from the
	// read discovery made.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	hs, _ := c.LiveSessions(discovery.LiveTTL).Holders(harness.List)
	if len(hs) != 1 || hs[0].SessionID != "shared-1" || hs[0].Status != "busy" {
		t.Fatalf("holders after discovery = %+v, want the read discovery made", hs)
	}
}

package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionCwd(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	first := write("a.jsonl", `{"type":"summary","summary":"x"}`+"\n"+
		`not json`+"\n"+
		`{"type":"user","cwd":"/w/app","timestamp":"2026-11-01T10:00:00Z","message":{"role":"user","content":"hi"}}`+"\n"+
		`{"type":"user","cwd":"/w/other","timestamp":"2026-11-01T10:01:00Z","message":{"role":"user","content":"again"}}`+"\n")
	got, err := SessionCwd(first)
	if err != nil || got != "/w/app" {
		t.Fatalf("SessionCwd = %q, %v; want the first recorded /w/app", got, err)
	}
	// The same rule as the session list: scanSession reads the same cwd.
	if s, err := scanSession("a", first, "p"); err != nil || s.Cwd != got {
		t.Fatalf("scanSession Cwd = %q, %v; want %q like SessionCwd", s.Cwd, err, got)
	}

	// A line scanSession cannot decode (a bad timestamp) is skipped by both.
	bad := write("d.jsonl", `{"cwd":"/a","timestamp":"bad"}`+"\n"+
		`{"type":"user","cwd":"/b","timestamp":"2026-11-01T10:00:00Z","message":{"role":"user","content":"hi"}}`+"\n")
	if got, err := SessionCwd(bad); err != nil || got != "/b" {
		t.Fatalf("SessionCwd = %q, %v; want /b, the first line scanSession decodes", got, err)
	}
	if s, err := scanSession("d", bad, "p"); err != nil || s.Cwd != "/b" {
		t.Fatalf("scanSession Cwd = %q, %v; want /b", s.Cwd, err)
	}

	none := write("b.jsonl", `{"type":"summary","summary":"x"}`+"\n")
	if _, err := SessionCwd(none); err == nil || !strings.Contains(err.Error(), "no working directory") {
		t.Fatalf("no cwd: err = %v, want records no working directory", err)
	}
	empty := write("c.jsonl", "")
	if _, err := SessionCwd(empty); err == nil {
		t.Fatal("empty file: no error")
	}
	if _, err := SessionCwd(filepath.Join(dir, "absent.jsonl")); err == nil {
		t.Fatal("missing file: no error")
	}
}

package provider

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

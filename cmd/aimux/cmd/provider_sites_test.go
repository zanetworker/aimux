package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/provider"
)

// aimux chat reads Claude's transcripts and registry under home, with an
// uncached live reader (one read per send).
func TestChatProvider_ClaudeUnderHome(t *testing.T) {
	home := t.TempDir()
	store, live, err := chatProvider(provider.NewRegistry(home, config.Config{}), "claude")
	if err != nil {
		t.Fatal(err)
	}
	lc, ok := live.(*discovery.LiveCache)
	if !ok || lc.Dir != filepath.Join(home, ".claude", "sessions") || lc.TTL != 0 {
		t.Fatalf("live = %#v, want the registry under %s with TTL 0", live, home)
	}
	file := filepath.Join(home, ".claude", "projects", "-w", "s-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"cwd":"/w"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := store.SessionFile("s-1"); got != file {
		t.Fatalf("SessionFile = %q, want %q", got, file)
	}
	if store.Fingerprint("s-1") == "" || store.Fingerprint("missing") != "" {
		t.Fatal("Fingerprint should be set for s-1 and empty for a missing session")
	}
}

func TestChatProvider_RefusesProvidersThatCannotContinue(t *testing.T) {
	r := provider.NewRegistry(t.TempDir(), config.Config{})
	for _, name := range []string{"codex", "nope", ""} {
		if _, _, err := chatProvider(r, name); err == nil {
			t.Fatalf("chatProvider(%q) gave no error", name)
		}
	}
}

// The search index reads the transcript directory the provider reports.
func TestProjectsDirOf(t *testing.T) {
	home := t.TempDir()
	r := provider.NewRegistry(home, config.Config{})
	if got, want := projectsDirOf(r, "claude"), filepath.Join(home, ".claude", "projects"); got != want {
		t.Fatalf("projectsDirOf(claude) = %q, want %q", got, want)
	}
	for _, name := range []string{"codex", "nope"} {
		if got := projectsDirOf(r, name); got != "" {
			t.Fatalf("projectsDirOf(%q) = %q, want empty", name, got)
		}
	}
}

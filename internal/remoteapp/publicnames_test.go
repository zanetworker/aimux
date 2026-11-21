package remoteapp

import (
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The run engine's public names come from ~/.config/aimux/public-names.txt
// under home, read on every call, so an edit applies to the next turn.
func TestPublicNamesLoader_ReadsHomeFileEachCall(t *testing.T) {
	home := t.TempDir()
	load := publicNamesLoader(home, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if got := load(); got != nil {
		t.Fatalf("no file: %q, want none", got)
	}
	f := filepath.Join(home, ".config", "aimux", "public-names.txt")
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte("Globex Cloud # vendor\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := load(); !reflect.DeepEqual(got, []string{"Globex Cloud"}) {
		t.Fatalf("after write: %q", got)
	}
	if err := os.WriteFile(f, []byte("Initech\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := load(); !reflect.DeepEqual(got, []string{"Initech"}) {
		t.Fatalf("after edit: %q, want the edit applied without a restart", got)
	}
}

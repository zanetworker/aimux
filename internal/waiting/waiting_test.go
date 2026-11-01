package waiting

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

func TestDirIsUnderAimux(t *testing.T) {
	if got, want := Dir("/h"), filepath.Join("/h", ".aimux", "waiting"); got != want {
		t.Fatalf("Dir = %q, want %q", got, want)
	}
}

func TestWriteThenReadAndList(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "waiting")
	if err := Write(dir, "sess-1", "Claude needs your permission to use Bash", t0); err != nil {
		t.Fatal(err)
	}
	w, ok := Read(dir, "sess-1")
	if !ok {
		t.Fatal("Read: marker not found")
	}
	want := Waiting{Kind: KindPermission, Message: "Claude needs your permission to use Bash", Since: t0}
	if w != want {
		t.Fatalf("Read = %+v, want %+v", w, want)
	}
	all := List(dir)
	if len(all) != 1 || all["sess-1"] != want {
		t.Fatalf("List = %+v", all)
	}
}

func TestWriteIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "waiting")
	if err := Write(dir, "sess-1", "m", t0); err != nil {
		t.Fatal(err)
	}
	di, _ := os.Stat(dir)
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v, want 0700", di.Mode().Perm())
	}
	fi, _ := os.Stat(filepath.Join(dir, "sess-1.json"))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, want 0600", fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("left %d files, want only the marker", len(entries))
	}
}

func TestWriteRefusesUnsafeIDs(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"", "../x", "a/b", ".hidden", strings.Repeat("a", 200), "a b"} {
		if err := Write(dir, id, "m", t0); err == nil {
			t.Errorf("Write(%q) accepted an unsafe id", id)
		}
	}
	if _, ok := Read(dir, "../x"); ok {
		t.Error("Read accepted an unsafe id")
	}
}

func TestWriteTidiesMessage(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, "s", "  two\n\tlines  "+strings.Repeat("x", 400), t0); err != nil {
		t.Fatal(err)
	}
	w, _ := Read(dir, "s")
	if !strings.HasPrefix(w.Message, "two lines ") || len([]rune(w.Message)) != MaxMessage {
		t.Fatalf("Message = %q (%d runes)", w.Message, len([]rune(w.Message)))
	}
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()
	_ = Write(dir, "s", "m", t0)
	if err := Remove(dir, "s"); err != nil {
		t.Fatal(err)
	}
	if _, ok := Read(dir, "s"); ok {
		t.Fatal("marker still there")
	}
	if err := Remove(dir, "s"); err != nil {
		t.Fatalf("second Remove = %v, want nil", err)
	}
	if err := Remove(filepath.Join(dir, "missing"), "s"); err != nil {
		t.Fatalf("Remove in a missing dir = %v, want nil", err)
	}
}

func TestListSkipsJunk(t *testing.T) {
	dir := t.TempDir()
	_ = Write(dir, "good", "m", t0)
	_ = os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{not json"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "other.txt"), []byte("{}"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, ".tmp-x.json"), []byte("{}"), 0o600)
	got := List(dir)
	if len(got) != 1 {
		t.Fatalf("List = %+v, want only good", got)
	}
	if len(List(filepath.Join(dir, "missing"))) != 0 {
		t.Fatal("missing dir should list nothing")
	}
}

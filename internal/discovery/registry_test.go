package discovery

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadClaudeRegistry(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("101.json", `{"pid":101,"sessionId":"sess-a","cwd":"/w/a","status":"busy","procStart":"Fri Oct 23 15:40:46 2026"}`)
	write("102.json", `{"pid":102,"sessionId":"sess-b","cwd":"/w/b","status":"idle"}`)
	write("103.json", `{not json`)
	write("104.json", `{"pid":0,"sessionId":"no-pid"}`)
	write("105.json", `{"pid":105}`)
	write("101.abc.key", `secret`)

	reg := ReadClaudeRegistry(dir)
	if len(reg) != 2 {
		t.Fatalf("got %d entries %v, want 2 (malformed, pid-less, id-less and non-json files skipped)", len(reg), reg)
	}
	a := reg[101]
	if a.SessionID != "sess-a" || a.CWD != "/w/a" || a.Status != "busy" {
		t.Fatalf("entry 101 = %+v", a)
	}
	if reg[102].SessionID != "sess-b" {
		t.Fatalf("entry 102 = %+v", reg[102])
	}
}

func TestReadClaudeRegistry_MissingDir(t *testing.T) {
	reg := ReadClaudeRegistry(filepath.Join(t.TempDir(), "absent"))
	if reg == nil || len(reg) != 0 {
		t.Fatalf("missing dir: got %v, want empty non-nil map", reg)
	}
}

func TestClaudeRegistryEntry_StartedAt(t *testing.T) {
	e := ClaudeRegistryEntry{ProcStart: "Fri Oct 23 15:40:46 2026"}
	got, ok := e.StartedAt()
	want := time.Date(2026, 10, 23, 15, 40, 46, 0, time.UTC)
	if !ok || !got.Equal(want) {
		t.Fatalf("StartedAt() = %v, %v; want %v (procStart is UTC)", got, ok, want)
	}
	if _, ok := (ClaudeRegistryEntry{ProcStart: "yesterday"}).StartedAt(); ok {
		t.Fatal("unparsable procStart reported ok")
	}
	if _, ok := (ClaudeRegistryEntry{}).StartedAt(); ok {
		t.Fatal("empty procStart reported ok")
	}
}

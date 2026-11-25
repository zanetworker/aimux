package ledger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadDir_MergesEveryFile(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"model-proxy", "collector"} {
		w := mustOpen(t, dir, name)
		for range 3 {
			w.Write(Line{Kind: KindModelCall, Session: "s1"})
		}
		_ = w.Close()
	}
	// A file that is not a ledger file is not read.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lines, report := readLines(t, dir)
	if len(lines) != 6 || report != (ReadReport{Files: 2}) {
		t.Fatalf("%d lines, report %+v; want 6 lines from 2 files", len(lines), report)
	}
}

// A line of a schema this build does not know is skipped and counted, never guessed at.
func TestReadDir_SkipsAnotherSchemaAndCountsIt(t *testing.T) {
	dir := t.TempDir()
	w := mustOpen(t, dir, "model-proxy")
	w.Write(Line{Kind: KindModelCall, Session: "s1"})
	_ = w.Close()
	path := filepath.Join(dir, "model-proxy.jsonl")
	old, err := os.ReadFile(path) // #nosec G304 -- test temp file
	if err != nil {
		t.Fatal(err)
	}
	other := `{"schema":"aimux.ledger/2","writer":"model-proxy","seq":2,"kind":"model_call","session":"s1","later":"field"}` + "\n"
	third := strings.Replace(string(old), `"seq":1`, `"seq":3`, 1)
	if err := os.WriteFile(path, []byte(string(old)+other+third), 0o600); err != nil { //nolint:gosec // G703: a path under t.TempDir
		t.Fatal(err)
	}
	lines, report := readLines(t, dir)
	if len(lines) != 2 || report.Skipped != 1 || report.Torn != 0 {
		t.Fatalf("%d lines, report %+v; want 2 lines and 1 skipped", len(lines), report)
	}
}

func TestReadDir_MissingFolderIsEmpty(t *testing.T) {
	lines, report, err := ReadDir(filepath.Join(t.TempDir(), "none"))
	if err != nil || len(lines) != 0 || report != (ReadReport{}) {
		t.Fatalf("lines %v, report %+v, err %v; want nothing", lines, report, err)
	}
}

// A model call keeps every field through the file, under the agreed names, and no field can hold content.
func TestReadDir_ModelCallRoundTrips(t *testing.T) {
	dir := t.TempDir()
	want := Line{
		Kind: KindModelCall, Session: "0b1c2d3e-full-session-id", Turn: "4", Call: "mc_abc", Harness: "claude-code",
		RequestModel: "claude-sonnet-5-5", ResponseModel: "codex:gpt-6.2-sol", Engine: "codex", Effort: "high",
		InputTokens: 91200, CacheRead: 90000, OutputTokens: 40, Estimated: true,
		TTFBMillis: 1900, DurationMs: 4200, Outcome: OutcomeOK, Side: true,
	}
	w := mustOpen(t, dir, "model-proxy")
	w.Write(want)
	_ = w.Close()

	lines, _ := readLines(t, dir)
	if len(lines) != 1 {
		t.Fatalf("%d lines", len(lines))
	}
	got := lines[0]
	want.Writer, want.Seq, want.Schema, want.SemConv, want.At = "model-proxy", 1, Schema, SemConv, got.At
	if got != want {
		t.Fatalf("read back\n%+v\nwant\n%+v", got, want)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "model-proxy.jsonl")) // #nosec G304 -- test temp file
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"gen_ai.request.model":"claude-sonnet-5-5"`, `"gen_ai.response.model":"codex:gpt-6.2-sol"`,
		`"gen_ai.usage.input_tokens":91200`, `"gen_ai.usage.output_tokens":40`, `"aimux.usage.cache_read_input_tokens":90000`,
		`"aimux.engine":"codex"`, `"aimux.outcome":"ok"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("the file lacks %s:\n%s", key, raw)
		}
	}
	for _, banned := range []string{`"prompt"`, `"text"`, `"path"`, `"arguments"`} {
		if strings.Contains(string(raw), banned) {
			t.Fatalf("the file holds a %s key", banned)
		}
	}
}

// The folder is read as named, also when its name has characters a pattern would treat specially, and a folder
// that cannot be listed is an error, not an empty ledger.
func TestReadDir_ReadsTheLiteralFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ledger[old]")
	w := mustOpen(t, dir, "model-proxy")
	w.Write(Line{Kind: KindModelCall, Session: "s1"})
	_ = w.Close()
	if lines, _ := readLines(t, dir); len(lines) != 1 {
		t.Fatalf("%d lines from a folder with brackets in its name, want 1", len(lines))
	}
	notAFolder := filepath.Join(dir, "model-proxy.jsonl")
	if _, _, err := ReadDir(notAFolder); err == nil {
		t.Fatal("reading a file as the ledger folder gave no error")
	}
}

// A writer reads only the end of its own file at start, however long the file is.
func TestReadTail_ReadsTheLastWholeLinesOfOneFile(t *testing.T) {
	dir := t.TempDir()
	w := mustOpen(t, dir, "model-proxy")
	for range 200 {
		w.Write(Line{Kind: KindModelCall, Session: "s1", Turn: "1"})
	}
	_ = w.Close()
	other := mustOpen(t, dir, "collector")
	other.Write(Line{Kind: KindToolCall, Session: "s1"})
	_ = other.Close()

	all, _, err := ReadTail(dir, "model-proxy", 1<<20)
	if err != nil || len(all) != 200 {
		t.Fatalf("a limit above the file's size: %d lines, %v; want all 200", len(all), err)
	}
	tail, report, err := ReadTail(dir, "model-proxy", 2000)
	if err != nil || len(tail) == 0 || len(tail) >= 200 || report.Torn != 0 {
		t.Fatalf("a 2000-byte tail: %d lines, report %+v, %v; want some whole lines and none torn", len(tail), report, err)
	}
	if last := tail[len(tail)-1]; last.Seq != 200 || last.Writer != "model-proxy" || tail[0].Seq != 200-uint64(len(tail))+1 {
		t.Fatalf("the tail is not the file's last lines in order: first %d, last %d", tail[0].Seq, last.Seq)
	}
	if none, _, err := ReadTail(dir, "nobody", 2000); err != nil || len(none) != 0 {
		t.Fatalf("a writer with no file: %d lines, %v", len(none), err)
	}
}

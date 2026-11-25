package ledger

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func readLines(t *testing.T, dir string) ([]Line, ReadReport) {
	t.Helper()
	lines, report, err := ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return lines, report
}

func mustOpen(t *testing.T, dir, name string) *Writer {
	t.Helper()
	w, err := OpenWriter(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// A writer numbers its lines from 1 and goes on from the last line on disk after a restart.
func TestWriter_NumbersLinesAndContinuesAfterReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ledger")
	w := mustOpen(t, dir, "model-proxy")
	w.Write(Line{Kind: KindModelCall, Session: "s1"})
	w.Write(Line{Kind: KindModelCall, Session: "s1"})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	w = mustOpen(t, dir, "model-proxy")
	w.Write(Line{Kind: KindModelCall, Session: "s1"})
	if got := w.Stats(); got != (WriterStats{Seq: 3}) {
		t.Fatalf("stats %+v, want seq 3 and no failures", got)
	}
	_ = w.Close()

	lines, _ := readLines(t, dir)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	for i, l := range lines {
		if l.Seq != uint64(i+1) || l.Schema != "aimux.ledger/1" || l.SemConv != "1.27.0" || l.Writer != "model-proxy" || l.At.IsZero() {
			t.Fatalf("line %d: %+v", i, l)
		}
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("folder mode %v, %v; want 0700", info.Mode().Perm(), err)
	}
	if info, err := os.Stat(filepath.Join(dir, "model-proxy.jsonl")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v, %v; want 0600", info.Mode().Perm(), err)
	}
}

// A crash or a full disk can leave half a line. The writer goes on from the last whole one, on a line of its own.
func TestWriter_ContinuesAfterATornLastLine(t *testing.T) {
	dir := t.TempDir()
	w := mustOpen(t, dir, "model-proxy")
	w.Write(Line{Kind: KindModelCall, Session: "s1"})
	w.Write(Line{Kind: KindModelCall, Session: "s1"})
	_ = w.Close()
	path := filepath.Join(dir, "model-proxy.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- test temp file
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"seq":3,"wri`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	w = mustOpen(t, dir, "model-proxy")
	w.Write(Line{Kind: KindModelCall, Session: "s1"})
	_ = w.Close()

	lines, report := readLines(t, dir)
	if report.Torn != 1 || len(lines) != 3 || lines[2].Seq != 3 {
		t.Fatalf("report %+v, %d lines, last %+v; want one torn line and seq 1, 2, 3", report, len(lines), lines[len(lines)-1])
	}
}

// A write that fails is counted. It is never an error to the caller, whose request has already been answered.
func TestWriter_FailedWriteIsCountedNotReturned(t *testing.T) {
	w := mustOpen(t, t.TempDir(), "model-proxy")
	w.Write(Line{Kind: KindModelCall, Session: "s1"})
	_ = w.Close()
	w.Write(Line{Kind: KindModelCall, Session: "s1"})
	if got := w.Stats(); got.Failed != 1 || got.Seq != 2 {
		t.Fatalf("stats %+v, want one failure and the number used up", got)
	}
}

// A file a newer aimux wrote is left alone.
func TestWriter_RefusesAFileFromANewerSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model-proxy.jsonl")
	newer := `{"schema":"aimux.ledger/2","writer":"model-proxy","seq":7,"kind":"model_call"}` + "\n"
	if err := os.WriteFile(path, []byte(newer), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenWriter(dir, "model-proxy"); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("OpenWriter: %v, want ErrNewerSchema", err)
	}
	if got, _ := os.ReadFile(path); string(got) != newer { // #nosec G304 -- test temp file
		t.Fatal("the newer file was changed")
	}
}

func TestWriter_NilWritesNothing(t *testing.T) {
	var w *Writer
	w.Write(Line{Kind: KindModelCall})
	if w.Stats() != (WriterStats{}) || w.Close() != nil {
		t.Fatal("a nil writer did something")
	}
}

func TestWriter_ConcurrentWritesKeepOneLineEach(t *testing.T) {
	dir := t.TempDir()
	w := mustOpen(t, dir, "model-proxy")
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() { w.Write(Line{Kind: KindModelCall, Session: "s1"}) })
	}
	wg.Wait()
	_ = w.Close()
	lines, report := readLines(t, dir)
	seen := map[uint64]bool{}
	for _, l := range lines {
		seen[l.Seq] = true
	}
	if len(lines) != 50 || len(seen) != 50 || report.Torn != 0 || !seen[1] || !seen[50] {
		t.Fatalf("%d lines, %d distinct numbers, report %+v", len(lines), len(seen), report)
	}
}

// A writer's name becomes a file name, so it may not leave the folder.
func TestOpenWriter_RejectsANameThatIsNotAFileName(t *testing.T) {
	for _, name := range []string{"", "../x", "a/b", "a.jsonl/.."} {
		if _, err := OpenWriter(t.TempDir(), name); err == nil || !strings.Contains(err.Error(), "writer name") {
			t.Fatalf("OpenWriter(%q): %v, want a refusal", name, err)
		}
	}
}

// shortWriter writes only the first limit bytes of one write to the real file and then fails, as a disk that
// fills up in the middle of a line does. After that it writes normally.
type shortWriter struct {
	file  *os.File
	limit int
	used  bool
}

func (s *shortWriter) Write(p []byte) (int, error) {
	if s.used {
		return s.file.Write(p)
	}
	s.used = true
	n, _ := s.file.Write(p[:s.limit])
	return n, errors.New("no space left on device")
}

// A line cut short by a full disk is counted as failed, and the next line is not glued onto its fragment.
func TestWriter_PartialWriteDoesNotSwallowTheNextLine(t *testing.T) {
	dir := t.TempDir()
	w := mustOpen(t, dir, "model-proxy")
	w.Write(Line{Kind: KindModelCall, Session: "s1"})
	w.out = &shortWriter{file: w.file, limit: 12}
	w.Write(Line{Kind: KindModelCall, Session: "s1"}) // half of it reaches the file
	w.Write(Line{Kind: KindModelCall, Session: "s1"})
	_ = w.Close()

	lines, report := readLines(t, dir)
	if w.Stats().Failed != 1 || report.Torn != 1 || len(lines) != 2 || lines[0].Seq != 1 || lines[1].Seq != 3 {
		t.Fatalf("stats %+v, report %+v, lines %+v; want lines 1 and 3 whole and one torn", w.Stats(), report, lines)
	}
}

// A last line that is whole JSON but lost its newline still counts for the numbering, so its number is not reused.
func TestWriter_CountsAFinalLineWithoutItsNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model-proxy.jsonl")
	whole := `{"schema":"aimux.ledger/1","writer":"model-proxy","seq":4,"kind":"model_call","session":"s1"}`
	if err := os.WriteFile(path, []byte(whole), 0o600); err != nil {
		t.Fatal(err)
	}
	w := mustOpen(t, dir, "model-proxy")
	w.Write(Line{Kind: KindModelCall, Session: "s1"})
	_ = w.Close()
	lines, report := readLines(t, dir)
	if len(lines) != 2 || lines[0].Seq != 4 || lines[1].Seq != 5 || report.Torn != 0 {
		t.Fatalf("lines %+v, report %+v; want seq 4 then 5", lines, report)
	}
}

// A newer schema is recognised by its name alone, whatever it did to the other fields.
func TestWriter_RefusesANewerSchemaThatChangedAFieldType(t *testing.T) {
	dir := t.TempDir()
	newer := `{"schema":"aimux.ledger/2","writer":"model-proxy","seq":"seven"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "model-proxy.jsonl"), []byte(newer), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenWriter(dir, "model-proxy"); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("OpenWriter: %v, want ErrNewerSchema", err)
	}
}

// One writer per file: a second one is refused while the first holds it, and an old file is made owner-only.
func TestOpenWriter_OneWriterPerFileAndOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model-proxy.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil { // #nosec G306 -- the too-open mode is the point
		t.Fatal(err)
	}
	first := mustOpen(t, dir, "model-proxy")
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v, %v; want 0600", info.Mode().Perm(), err)
	}
	if _, err := OpenWriter(dir, "model-proxy"); err == nil || !strings.Contains(err.Error(), "one writer per ledger file") {
		t.Fatalf("second writer on the same file: %v, want a refusal", err)
	}
	_ = first.Close()
	second := mustOpen(t, dir, "model-proxy")
	_ = second.Close()
}

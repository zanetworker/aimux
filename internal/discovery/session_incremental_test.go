package discovery

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// sessionEntry is one assistant line using tool name on file, with tokens.
func sessionEntry(i int, tool string) string {
	return fmt.Sprintf(`{"type":"assistant","sessionId":"sess-1","gitBranch":"b%d","timestamp":"2026-01-01T10:%02d:00Z","message":{"model":"claude-opus-4-6","content":[{"type":"tool_use","name":%q,"input":{"file_path":"/x/f%d.go"}}],"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":1,"cache_creation_input_tokens":2}}}`+"\n", i, i%60, tool, i, 10+i, 5+i)
}

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- test file
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

// checkSame fails when ParseSessionFile (incremental) and a full parse of
// the file disagree.
func checkSame(t *testing.T, path, when string) SessionInfo {
	t.Helper()
	got, gotErr := ParseSessionFile(path)
	want, wantErr := parseSessionFileFull(path)
	if (gotErr == nil) != (wantErr == nil) {
		t.Fatalf("%s: err = %v, full parse err = %v", when, gotErr, wantErr)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: incremental = %+v\nfull = %+v", when, got, want)
	}
	return got
}

func TestParseSessionFile_AppendMatchesFullParse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(sessionEntry(1, "Read")), 0o600); err != nil {
		t.Fatal(err)
	}
	info := checkSame(t, path, "first read")
	if info.MessageCount != 1 || info.LastAction != "Rd f1.go" {
		t.Fatalf("first read = %+v", info)
	}

	appendTo(t, path, sessionEntry(2, "Edit")+sessionEntry(3, "Bash"))
	info = checkSame(t, path, "after append")
	if info.MessageCount != 3 || info.TokensIn != 11+12+13 {
		t.Errorf("after append = %+v, want 3 messages and 36 input tokens", info)
	}

	// A line still being written (no newline yet) counts as the full parse
	// counts it, and is not lost once it is completed.
	half := sessionEntry(4, "Write")
	appendTo(t, path, half[:len(half)-1])
	checkSame(t, path, "partial last line")
	appendTo(t, path, "\n")
	info = checkSame(t, path, "line completed")
	if info.MessageCount != 4 || info.LastAction != "Wr f4.go" {
		t.Errorf("after completing the line = %+v", info)
	}

	// A torn line (not yet valid JSON) is skipped now and counted later.
	torn := sessionEntry(5, "Grep")
	appendTo(t, path, torn[:20])
	checkSame(t, path, "torn last line")
	appendTo(t, path, torn[20:])
	info = checkSame(t, path, "torn line completed")
	if info.MessageCount != 5 {
		t.Errorf("messages = %d, want 5", info.MessageCount)
	}
}

func TestParseSessionFile_UnchangedFileNotReread(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	content := sessionEntry(1, "Read")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	first, _ := ParseSessionFile(path)
	fi, _ := os.Stat(path)

	// Same size and mtime, different bytes: only a re-read would notice.
	other := sessionEntry(2, "Edit")
	if len(other) != len(content) {
		t.Fatalf("test lines differ in length: %d vs %d", len(other), len(content))
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0o600) // #nosec G304 -- test file
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte(other), 0); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := os.Chtimes(path, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}

	again, err := ParseSessionFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, first) {
		t.Errorf("unchanged size and mtime re-read the file: %+v, want the cached %+v", again, first)
	}
}

func TestParseSessionFile_TruncatedFileReadAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(sessionEntry(1, "Read")+sessionEntry(2, "Edit")+sessionEntry(3, "Bash")), 0o600); err != nil {
		t.Fatal(err)
	}
	checkSame(t, path, "before truncate")
	if err := os.WriteFile(path, []byte(sessionEntry(7, "Glob")), 0o600); err != nil {
		t.Fatal(err)
	}
	info := checkSame(t, path, "after truncate")
	if info.MessageCount != 1 || info.TokensIn != 17 {
		t.Errorf("after truncate = %+v, want only the new line", info)
	}
}

func TestParseSessionFile_ReplacedFileReadAgain(t *testing.T) {
	// A file replaced by another (rename over it), even a larger one, is a
	// new file: its first bytes are not the ones already parsed.
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(path, []byte(sessionEntry(1, "Read")), 0o600); err != nil {
		t.Fatal(err)
	}
	checkSame(t, path, "before replace")
	tmp := filepath.Join(dir, "new.jsonl")
	if err := os.WriteFile(tmp, []byte(sessionEntry(8, "Edit")+sessionEntry(9, "Bash")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	info := checkSame(t, path, "after replace")
	if info.MessageCount != 2 || info.TokensIn != 18+19 {
		t.Errorf("after replace = %+v, want the new file's two lines", info)
	}
}

func TestParseSessionFile_GoneFileErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(sessionEntry(1, "Read")), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseSessionFile(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseSessionFile(path); err == nil {
		t.Error("a removed file returned its cached parse instead of an error")
	}
}

func TestParseSessionFile_EmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if info := checkSame(t, path, "empty"); info.MessageCount != 0 {
		t.Errorf("empty file = %+v", info)
	}
	appendTo(t, path, sessionEntry(1, "Read"))
	checkSame(t, path, "first line into an empty file")
}

func TestParseSessionFile_MtimeOnlyChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(sessionEntry(1, "Read")), 0o600); err != nil {
		t.Fatal(err)
	}
	checkSame(t, path, "first")
	future := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	checkSame(t, path, "touched")
}

func BenchmarkParseSessionFile_LargeUnchanged(b *testing.B) {
	path := filepath.Join(b.TempDir(), "s.jsonl")
	f, err := os.Create(path) // #nosec G304 -- test file
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 50000; i++ {
		_, _ = f.WriteString(sessionEntry(i, "Edit"))
	}
	_ = f.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ParseSessionFile(path); err != nil {
			b.Fatal(err)
		}
	}
}

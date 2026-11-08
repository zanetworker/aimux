//go:build unix

package jsonedit

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// setValue returns an EditKey transform that sets the key to v and counts calls.
// An empty v removes the key.
func setValue(v string, calls *int) func(json.RawMessage) (json.RawMessage, error) {
	return func(old json.RawMessage) (json.RawMessage, error) {
		if calls != nil {
			*calls++
		}
		if v == "" {
			return nil, nil
		}
		return json.RawMessage(v), nil
	}
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // a test temp path
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// assertOnlyFiles fails if dir holds anything besides the named entries (no stray temp files).
func assertOnlyFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !want[e.Name()] {
			t.Errorf("unexpected file left in %s: %s", dir, e.Name())
		}
	}
}

// setHooks installs the test hooks for the duration of the test.
func setHooks(t *testing.T, before, after func(string)) {
	t.Helper()
	editHookBeforeRename, editHookAfterRename = before, after
	t.Cleanup(func() { editHookBeforeRename, editHookAfterRename = nil, nil })
}

func bigArray(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf("%d", i*7919)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func TestEditKey_OtherKeysByteIdentical(t *testing.T) {
	// Deliberately unusual but valid formatting in the other keys: escapes, raw unicode,
	// exponent and huge numbers, irregular spacing, a large array, unsorted key order.
	prefix := "{\n" +
		"  \"zeta\": {\"nested\": {\"deep\": [1, 2, {\"x\": null}]}, \"html\": \"<a&b>\"},\n" +
		"  \"unicode\": \"café \\u00e9 \\ud83d\\ude00 日本\",\n" +
		"  \"numbers\": [1e5, -0.0, 12345678901234567890, 3.14159265358979323846, 1E-7],\n" +
		"  \"spacing\" :   { \"a\" :1 ,\"b\":[ ] },\n" +
		"  \"big\": " + bigArray(5000) + ",\n" +
		"  \"mcpServers\": "
	oldVal := "{\n    \"old\": {\n      \"command\": \"x\"\n    }\n  }"
	suffix := ",\n  \"alpha\": true,\n  \"n\": 1e5,\n  \"projects\": {\"/p\": {\"allowedTools\": []}}\n}\n"

	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")
	writeFile(t, path, prefix+oldVal+suffix, 0o600)

	var got json.RawMessage
	err := EditKey(path, "mcpServers", func(old json.RawMessage) (json.RawMessage, error) {
		got = old
		return json.RawMessage(`{"mux":{"type":"http","url":"http://127.0.0.1:7381/mcp"}}`), nil
	})
	if err != nil {
		t.Fatalf("EditKey: %v", err)
	}
	if string(got) != oldVal {
		t.Errorf("fn got old = %q, want %q", got, oldVal)
	}
	newVal := "{\n    \"mux\": {\n      \"type\": \"http\",\n      \"url\": \"http://127.0.0.1:7381/mcp\"\n    }\n  }"
	want := prefix + newVal + suffix
	if out := readFile(t, path); out != want {
		t.Errorf("file not byte-identical outside mcpServers\n got: %.400q\nwant: %.400q", out, want)
	}
	assertOnlyFiles(t, dir, ".claude.json")
}

func TestEditKey_KeyOrderAndCompactStyle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	writeFile(t, path, `{"z":1,"mcpServers":{},"a":[2],"m":"x"}`, 0o600)
	if err := EditKey(path, "mcpServers", setValue("{\n \"mux\" : { \"url\": \"u\" }\n}", nil)); err != nil {
		t.Fatal(err)
	}
	want := `{"z":1,"mcpServers":{"mux":{"url":"u"}},"a":[2],"m":"x"}`
	if out := readFile(t, path); out != want {
		t.Errorf("got %s, want %s", out, want)
	}
}

func TestEditKey_NumberValueAtEnd(t *testing.T) {
	// A number value ends without a closing delimiter; make sure its span is exact.
	path := filepath.Join(t.TempDir(), "c.json")
	writeFile(t, path, "{\"a\": 10 , \"b\": 20 }", 0o600)
	if err := EditKey(path, "b", setValue("30", nil)); err != nil {
		t.Fatal(err)
	}
	if out, want := readFile(t, path), "{\"a\": 10 , \"b\": 30 }"; out != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestEditKey_ModeKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	writeFile(t, path, `{"a":1}`, 0o640)
	if err := EditKey(path, "a", setValue("2", nil)); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want 0640", fi.Mode().Perm())
	}
}

func TestEditKey_AbsentKeyAdded(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"indented", "{\n  \"a\": 1\n}\n", "{\n  \"a\": 1,\n  \"mcpServers\": {\n    \"x\": 1\n  }\n}\n"},
		{"compact", `{"a":1}`, `{"a":1,"mcpServers":{"x":1}}`},
		{"empty object", "{}", "{\n  \"mcpServers\": {\n    \"x\": 1\n  }\n}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "c.json")
			writeFile(t, path, c.in, 0o600)
			var sawNil bool
			err := EditKey(path, "mcpServers", func(old json.RawMessage) (json.RawMessage, error) {
				sawNil = old == nil
				return json.RawMessage(`{"x":1}`), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if !sawNil {
				t.Error("fn should get nil old for an absent key")
			}
			if out := readFile(t, path); out != c.want {
				t.Errorf("got %q, want %q", out, c.want)
			}
		})
	}
}

func TestEditKey_NilRemovesKey(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"middle", "{\n  \"a\": 1,\n  \"mcpServers\": {},\n  \"b\": 2\n}", "{\n  \"a\": 1,\n  \"b\": 2\n}"},
		{"first", "{\n  \"mcpServers\": {},\n  \"b\": 2\n}", "{\n  \"b\": 2\n}"},
		{"last", "{\n  \"a\": 1,\n  \"mcpServers\": {}\n}", "{\n  \"a\": 1\n}"},
		{"only", "{\n  \"mcpServers\": {}\n}\n", "{}\n"},
		{"absent is a no-op", `{"a":1}`, `{"a":1}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "c.json")
			writeFile(t, path, c.in, 0o600)
			if err := EditKey(path, "mcpServers", setValue("", nil)); err != nil {
				t.Fatal(err)
			}
			if out := readFile(t, path); out != c.want {
				t.Errorf("got %q, want %q", out, c.want)
			}
		})
	}
}

func TestEditKey_RefusesWithoutWriting(t *testing.T) {
	fnErr := errors.New("boom")
	cases := []struct {
		name string
		in   string
		fn   func(json.RawMessage) (json.RawMessage, error)
	}{
		{"invalid json", `{"a":1,`, setValue("1", nil)},
		{"empty file", ``, setValue("1", nil)},
		{"trailing garbage", `{"a":1} x`, setValue("1", nil)},
		{"two values", `{"a":1}{"b":2}`, setValue("1", nil)},
		{"top level array", `[1,2]`, setValue("1", nil)},
		{"duplicate key", `{"a":1,"a":2}`, setValue("1", nil)},
		{"fn returns invalid json", `{"a":1}`, setValue("{nope", nil)},
		{"fn returns error", `{"a":1}`, func(json.RawMessage) (json.RawMessage, error) { return nil, fnErr }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "c.json")
			writeFile(t, path, c.in, 0o600)
			err := EditKey(path, "a", c.fn)
			if err == nil {
				t.Fatal("want error, got nil")
			}
			if c.name == "fn returns error" && !errors.Is(err, fnErr) {
				t.Errorf("err = %v, want it to wrap fn's error", err)
			}
			if out := readFile(t, path); out != c.in {
				t.Errorf("file changed: %q", out)
			}
			assertOnlyFiles(t, dir, "c.json")
		})
	}
}

func TestEditKey_MissingFile(t *testing.T) {
	err := EditKey(filepath.Join(t.TempDir(), "nope.json"), "a", setValue("1", nil))
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want os.ErrNotExist", err)
	}
}

func TestEditKey_UnchangedValueDoesNotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	writeFile(t, path, `{"a":{"b":1}}`, 0o600)
	wrote := false
	setHooks(t, func(string) { wrote = true }, nil)
	if err := EditKey(path, "a", setValue(`{ "b": 1 }`, nil)); err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Error("an unchanged value should not write the file")
	}
}

func TestEditKey_WriterBeforeRenameRetriesAndKeepsBoth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	writeFile(t, path, "{\n  \"numStartups\": 1,\n  \"mcpServers\": {}\n}\n", 0o600)
	hookCalls := 0
	setHooks(t, func(target string) {
		hookCalls++
		if hookCalls == 1 { // another writer (Claude Code) bumps a counter while we edit
			writeFile(t, target, "{\n  \"numStartups\": 2,\n  \"mcpServers\": {}\n}\n", 0o600)
		}
	}, nil)
	calls := 0
	if err := EditKey(path, "mcpServers", setValue(`{"mux":1}`, &calls)); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"numStartups\": 2,\n  \"mcpServers\": {\n    \"mux\": 1\n  }\n}\n"
	if out := readFile(t, path); out != want {
		t.Errorf("got %q, want %q", out, want)
	}
	if calls != 2 {
		t.Errorf("fn called %d times, want 2 (one retry)", calls)
	}
	assertOnlyFiles(t, dir, "c.json")
}

func TestEditKey_FiveConflictsGiveErrConcurrentWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	writeFile(t, path, `{"n":0,"mcpServers":{}}`, 0o600)
	n := 0
	last := ""
	setHooks(t, func(target string) {
		n++
		last = fmt.Sprintf(`{"n":%d,"mcpServers":{}}`, n)
		writeFile(t, target, last, 0o600)
	}, nil)
	calls := 0
	err := EditKey(path, "mcpServers", setValue(`{"mux":1}`, &calls))
	if !errors.Is(err, ErrConcurrentWrite) {
		t.Fatalf("err = %v, want ErrConcurrentWrite", err)
	}
	if calls != 5 {
		t.Errorf("fn called %d times, want 5", calls)
	}
	if out := readFile(t, path); out != last {
		t.Errorf("other writer's content not intact: got %q, want %q", out, last)
	}
	assertOnlyFiles(t, dir, "c.json")
}

func TestEditKey_ClobberAfterRenameIsDetectedAndRetried(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	writeFile(t, path, `{"n":1,"mcpServers":{}}`, 0o600)
	n := 0
	setHooks(t, nil, func(target string) {
		n++
		if n == 1 { // a writer with a stale in-memory copy rewrites the whole file
			writeFile(t, target, `{"n":2,"mcpServers":{}}`, 0o600)
		}
	})
	calls := 0
	if err := EditKey(path, "mcpServers", setValue(`{"mux":1}`, &calls)); err != nil {
		t.Fatal(err)
	}
	if out, want := readFile(t, path), `{"n":2,"mcpServers":{"mux":1}}`; out != want {
		t.Errorf("got %q, want %q", out, want)
	}
	if calls != 2 {
		t.Errorf("fn called %d times, want 2", calls)
	}
}

func TestEditKey_LaterWriterKeepingOurValueIsSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	writeFile(t, path, `{"n":1,"mcpServers":{}}`, 0o600)
	setHooks(t, nil, func(target string) {
		// re-read our file and rewrote it in its own style, keeping our value
		writeFile(t, target, "{\n  \"n\": 2,\n  \"mcpServers\": {\n    \"mux\": 1\n  }\n}", 0o600)
	})
	calls := 0
	if err := EditKey(path, "mcpServers", setValue(`{"mux":1}`, &calls)); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("fn called %d times, want 1 (no retry when our value survived)", calls)
	}
}

func TestEditKey_SymlinkEditsTarget(t *testing.T) {
	linkDir, targetDir := t.TempDir(), t.TempDir()
	target := filepath.Join(targetDir, "settings.json")
	link := filepath.Join(linkDir, "settings.json")
	writeFile(t, target, `{"a":1,"b":2}`, 0o644)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := EditKey(link, "b", setValue("3", nil)); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was replaced by a regular file")
	}
	if out := readFile(t, target); out != `{"a":1,"b":3}` {
		t.Errorf("target = %q", out)
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o644 {
		t.Errorf("target mode = %v, want 0644", fi.Mode().Perm())
	}
	assertOnlyFiles(t, linkDir, "settings.json")
	assertOnlyFiles(t, targetDir, "settings.json")
}

func TestEditKey_ConcurrentEditorsAllLand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	writeFile(t, path, `{"base":true}`, 0o600)
	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- EditKey(path, fmt.Sprintf("k%d", i), setValue(fmt.Sprintf("%d", i), nil))
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("EditKey: %v", err)
		}
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readFile(t, path)), &m); err != nil {
		t.Fatal(err)
	}
	if len(m) != n+1 {
		t.Errorf("got %d keys, want %d (an edit was lost)", len(m), n+1)
	}
}

func TestEditKey_LargeIntegerChangeIsWritten(t *testing.T) {
	// Both numbers round to the same float64; the change must still be written.
	path := filepath.Join(t.TempDir(), "c.json")
	writeFile(t, path, `{"a":12345678901234567890}`, 0o600)
	if err := EditKey(path, "a", setValue("12345678901234567891", nil)); err != nil {
		t.Fatal(err)
	}
	if out, want := readFile(t, path), `{"a":12345678901234567891}`; out != want {
		t.Errorf("got %s, want %s", out, want)
	}
}

// fakeBackupClock makes backupNow return t, advancing by step after each call.
func fakeBackupClock(t *testing.T, start time.Time, step time.Duration) {
	t.Helper()
	cur := start
	backupNow = func() time.Time { now := cur; cur = cur.Add(step); return now }
	t.Cleanup(func() { backupNow = time.Now })
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func TestBackup_CopiesWithOwnerOnlyMode(t *testing.T) {
	src := filepath.Join(t.TempDir(), ".claude.json")
	writeFile(t, src, `{"a":1}`, 0o644)
	dir := filepath.Join(t.TempDir(), "backups")
	fakeBackupClock(t, time.Date(2026, 11, 1, 9, 8, 7, 0, time.FixedZone("CET", 3600)), time.Second)

	got, err := Backup(src, dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, ".claude.json.20261101T080807Z"); got != want {
		t.Errorf("path = %s, want %s (UTC stamp)", got, want)
	}
	if readFile(t, got) != `{"a":1}` {
		t.Error("backup content differs")
	}
	fi, _ := os.Stat(got)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("backup mode = %v, want 0600", fi.Mode().Perm())
	}
	di, _ := os.Stat(dir)
	if di.Mode().Perm() != 0o700 {
		t.Errorf("backup dir mode = %v, want 0700", di.Mode().Perm())
	}
}

func TestBackup_RotationKeepsNewest(t *testing.T) {
	src := filepath.Join(t.TempDir(), "settings.json")
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "other.json.20200101T000000Z"), "x", 0o600) // another file's backup
	writeFile(t, filepath.Join(dir, "settings.json.notes"), "x", 0o600)         // not a backup name
	fakeBackupClock(t, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Minute)
	var made []string
	for i := 0; i < 12; i++ {
		writeFile(t, src, fmt.Sprintf(`{"v":%d}`, i), 0o600)
		p, err := Backup(src, dir, 10)
		if err != nil {
			t.Fatal(err)
		}
		made = append(made, filepath.Base(p))
	}
	want := append([]string{"other.json.20200101T000000Z", "settings.json.notes"}, made[2:]...)
	sort.Strings(want)
	if got := listDir(t, dir); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("dir = %v\nwant  %v", got, want)
	}
	if readFile(t, filepath.Join(dir, made[11])) != `{"v":11}` {
		t.Error("newest backup has the wrong content")
	}
}

func TestBackup_SameSecondGetsUniqueName(t *testing.T) {
	src := filepath.Join(t.TempDir(), "c.json")
	dir := t.TempDir()
	fakeBackupClock(t, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), 0)
	var paths []string
	for i := 0; i < 12; i++ {
		writeFile(t, src, fmt.Sprintf("%d", i), 0o600)
		p, err := Backup(src, dir, 10)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	if got := len(listDir(t, dir)); got != 10 {
		t.Fatalf("kept %d backups, want 10", got)
	}
	// The newest ten survive, in order, despite sharing one timestamp.
	for i, p := range paths {
		_, err := os.Stat(p)
		if kept := err == nil; kept != (i >= 2) {
			t.Errorf("backup %d (%s) kept=%v", i, filepath.Base(p), kept)
		}
	}
	if readFile(t, paths[11]) != "11" {
		t.Error("newest backup has the wrong content")
	}
}

func TestBackup_Errors(t *testing.T) {
	dir := t.TempDir()
	if _, err := Backup(filepath.Join(dir, "missing.json"), dir, 10); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing source: err = %v, want os.ErrNotExist", err)
	}
	src := filepath.Join(dir, "c.json")
	writeFile(t, src, "{}", 0o600)
	if _, err := Backup(src, t.TempDir(), 0); err == nil {
		t.Error("keep 0 should be refused")
	}
}

// fakeReads makes EditKey's reads return override(n) for the n-th read (from 1)
// when it is non-nil, else the real file. It returns the read counter.
func fakeReads(t *testing.T, override func(n int) []byte) *int {
	t.Helper()
	n := 0
	editReadFile = func(name string) ([]byte, error) {
		n++
		if b := override(n); b != nil {
			return b, nil
		}
		return os.ReadFile(name) //nolint:gosec // a test temp path
	}
	t.Cleanup(func() { editReadFile = os.ReadFile })
	return &n
}

func shortLockTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	lockTimeout = d
	t.Cleanup(func() { lockTimeout = 10 * time.Second })
}

func TestEditKey_TornInitialReadWaitsAndRetries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	writeFile(t, path, `{"n":1,"mcpServers":{}}`, 0o600)
	fakeReads(t, func(n int) []byte {
		if n == 1 { // caught Claude Code mid-write
			return []byte(`{"n":1,"mcpSer`)
		}
		return nil
	})
	var olds []string
	start := time.Now()
	err := EditKey(path, "mcpServers", func(old json.RawMessage) (json.RawMessage, error) {
		olds = append(olds, string(old))
		return json.RawMessage(`{"mux":1}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(olds) != 1 || olds[0] != "{}" {
		t.Errorf("fn saw %q; it must only ever see the valid base", olds)
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Error("a torn read should wait before retrying")
	}
	if out, want := readFile(t, path), `{"n":1,"mcpServers":{"mux":1}}`; out != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestEditKey_TornPreRenameReadRetries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	writeFile(t, path, `{"n":1,"mcpServers":{}}`, 0o600)
	fakeReads(t, func(n int) []byte {
		if n == 2 {
			return []byte(`{"n":1,`)
		}
		return nil
	})
	calls := 0
	if err := EditKey(path, "mcpServers", setValue(`{"mux":1}`, &calls)); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("fn called %d times, want 2", calls)
	}
	if out, want := readFile(t, path), `{"n":1,"mcpServers":{"mux":1}}`; out != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestEditKey_TornReadBackRetries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	writeFile(t, path, `{"n":1,"mcpServers":{}}`, 0o600)
	fakeReads(t, func(n int) []byte {
		if n == 3 {
			return []byte(`{"n":1,"mcpServers":{"mu`)
		}
		return nil
	})
	calls := 0
	if err := EditKey(path, "mcpServers", setValue(`{"mux":1}`, &calls)); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("fn called %d times, want 2 (verify again from a fresh read)", calls)
	}
	if out, want := readFile(t, path), `{"n":1,"mcpServers":{"mux":1}}`; out != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestEditKey_InvalidOnlyAfterTwoIdenticalReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	writeFile(t, path, `{"a":1,`, 0o600)
	reads := fakeReads(t, func(int) []byte { return nil })
	calls := 0
	err := EditKey(path, "a", setValue("2", &calls))
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("err = %v, want invalid JSON", err)
	}
	if errors.Is(err, ErrConcurrentWrite) {
		t.Error("a steadily broken file is not a concurrent write")
	}
	if *reads != 2 || calls != 0 {
		t.Errorf("reads = %d, fn calls = %d; want 2 and 0", *reads, calls)
	}
}

func TestEditKey_ChangingInvalidReadsAreAConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	writeFile(t, path, `{"a":1}`, 0o600)
	fakeReads(t, func(n int) []byte { return []byte(fmt.Sprintf(`{"a":%d,`, n)) })
	calls := 0
	err := EditKey(path, "a", setValue("2", &calls))
	if !errors.Is(err, ErrConcurrentWrite) {
		t.Fatalf("err = %v, want ErrConcurrentWrite", err)
	}
	if calls != 0 {
		t.Errorf("fn called %d times on invalid bases", calls)
	}
	if readFile(t, path) != `{"a":1}` {
		t.Error("file changed")
	}
}

func TestEditKey_SecondLockerTimesOut(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	writeFile(t, path, `{"a":1}`, 0o600)
	unlock, err := lockDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	shortLockTimeout(t, 150*time.Millisecond)
	start := time.Now()
	err = EditKey(path, "a", setValue("2", nil))
	if err == nil || !strings.Contains(err.Error(), dir) || !strings.Contains(err.Error(), "another aimux") {
		t.Fatalf("err = %v, want a lock timeout naming %s", err, dir)
	}
	if el := time.Since(start); el < 140*time.Millisecond || el > 2*time.Second {
		t.Errorf("gave up after %v, want about 150ms", el)
	}
	if readFile(t, path) != `{"a":1}` {
		t.Error("file changed without the lock")
	}
}

func TestEditKey_WaitsForHeldLockfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	writeFile(t, path, `{"a":1}`, 0o600)
	lock := path + ".lock"
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	unchangedWhileHeld := make(chan bool, 1)
	go func() {
		time.Sleep(300 * time.Millisecond)
		b, _ := os.ReadFile(path) //nolint:gosec // a test temp path
		unchangedWhileHeld <- string(b) == `{"a":1}`
		_ = os.Remove(lock)
	}()
	start := time.Now()
	if err := EditKey(path, "a", setValue("2", nil)); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 280*time.Millisecond {
		t.Error("did not wait for the held lock")
	}
	if !<-unchangedWhileHeld {
		t.Error("wrote while another process held the lock")
	}
	if readFile(t, path) != `{"a":2}` {
		t.Error("edit missing")
	}
	assertOnlyFiles(t, dir, "c.json")
}

func TestEditKey_TakesStaleLockfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	writeFile(t, path, `{"a":1}`, 0o600)
	lock := path + ".lock"
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * time.Second)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := EditKey(path, "a", setValue("2", nil)); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("waited on a stale lock")
	}
	if readFile(t, path) != `{"a":2}` {
		t.Error("edit missing")
	}
	assertOnlyFiles(t, dir, "c.json")
}

func TestEditKey_LockfileHeldTooLongGivesUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	writeFile(t, path, `{"a":1}`, 0o600)
	if err := os.Mkdir(path+".lock", 0o700); err != nil {
		t.Fatal(err)
	}
	shortLockTimeout(t, 200*time.Millisecond)
	err := EditKey(path, "a", setValue("2", nil))
	if err == nil || !strings.Contains(err.Error(), path+".lock") {
		t.Fatalf("err = %v, want a timeout naming the lock", err)
	}
	if readFile(t, path) != `{"a":1}` {
		t.Error("file changed")
	}
	assertOnlyFiles(t, dir, "c.json", "c.json.lock") // not ours to remove
}

func TestEditKey_RefusesWhenOwnerCannotBeKept(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can chown to anyone")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	writeFile(t, path, `{"a":1}`, 0o600)
	realOwner := fileOwner
	fileOwner = func(fi os.FileInfo) (int, int) { // as if run under sudo on another user's file
		_, gid := realOwner(fi)
		return os.Geteuid() + 1, gid
	}
	t.Cleanup(func() { fileOwner = realOwner })
	err := EditKey(path, "a", setValue("2", nil))
	if err == nil || !strings.Contains(err.Error(), "owned by uid") {
		t.Fatalf("err = %v, want a refusal naming the owner", err)
	}
	if readFile(t, path) != `{"a":1}` {
		t.Error("file changed")
	}
	assertOnlyFiles(t, dir, "c.json")
}

func TestErrConcurrentWrite_DoesNotClaimNothingWritten(t *testing.T) {
	// After an attempt's rename was overwritten, "nothing written" would be false.
	msg := ErrConcurrentWrite.Error()
	if strings.Contains(msg, "nothing written") || !strings.Contains(msg, "other writer") {
		t.Errorf("ErrConcurrentWrite = %q", msg)
	}
}

func TestEditKey_OverwrittenEveryTimeGivesErrConcurrentWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	writeFile(t, path, `{"n":0,"mcpServers":{}}`, 0o600)
	n := 0
	setHooks(t, nil, func(target string) {
		n++
		writeFile(t, target, fmt.Sprintf(`{"n":%d,"mcpServers":{}}`, n), 0o600)
	})
	err := EditKey(path, "mcpServers", setValue(`{"mux":1}`, nil))
	if !errors.Is(err, ErrConcurrentWrite) {
		t.Fatalf("err = %v, want ErrConcurrentWrite", err)
	}
	if out, want := readFile(t, path), `{"n":5,"mcpServers":{}}`; out != want {
		t.Errorf("got %q, want the other writer's %q", out, want)
	}
}

func TestEditKey_RemovesStaleTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	writeFile(t, path, `{"a":1}`, 0o600)
	old := time.Now().Add(-2 * time.Minute)
	for _, name := range []string{".c.json.aimux-111", ".c.json.aimux-222", ".other.json.aimux-333", ".c.json.mine"} {
		writeFile(t, filepath.Join(dir, name), "x", 0o600)
		if name != ".c.json.aimux-222" {
			if err := os.Chtimes(filepath.Join(dir, name), old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := EditKey(path, "a", setValue("2", nil)); err != nil {
		t.Fatal(err)
	}
	// The old crash leftover goes; a fresh one (maybe another edit's) and other files stay.
	assertOnlyFiles(t, dir, "c.json", ".c.json.aimux-222", ".other.json.aimux-333", ".c.json.mine")
	if _, err := os.Stat(filepath.Join(dir, ".c.json.aimux-111")); err == nil {
		t.Error("stale temp file kept")
	}
}

func TestEditKey_RefusesHardLinkedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	writeFile(t, path, `{"a":1}`, 0o600)
	if err := os.Link(path, filepath.Join(dir, "other-name.json")); err != nil {
		t.Fatal(err)
	}
	err := EditKey(path, "a", setValue("2", nil))
	if err == nil || !strings.Contains(err.Error(), "hard link") {
		t.Fatalf("err = %v, want a hard link refusal", err)
	}
	if readFile(t, path) != `{"a":1}` {
		t.Error("file changed")
	}
	assertOnlyFiles(t, dir, "c.json", "other-name.json")
}

func TestBackup_ClockBehindNewestStillSortsNewest(t *testing.T) {
	src := filepath.Join(t.TempDir(), "c.json")
	writeFile(t, src, "new", 0o600)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "c.json.20261101T120000Z"), "old", 0o600)
	fakeBackupClock(t, time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC), 0) // clock went back two hours
	p, err := Backup(src, dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p) != "c.json.20261101T120000Z-1" {
		t.Errorf("backup = %s, want it to sort after the newest existing one", filepath.Base(p))
	}
	if got := listDir(t, dir); len(got) != 1 || readFile(t, p) != "new" {
		t.Errorf("dir = %v; rotation must keep the copy just made", got)
	}
}

func TestSetMember_AddMember(t *testing.T) {
	obj := json.RawMessage(`{"a": 1}`)
	got, err := SetMember(obj, "b", json.RawMessage(`2`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a": 1,"b": 2}`
	if string(got) != want {
		t.Errorf("SetMember add = %q, want %q", got, want)
	}
}

func TestSetMember_ReplaceMember(t *testing.T) {
	obj := json.RawMessage(`{"a": 1, "b": 2}`)
	got, err := SetMember(obj, "a", json.RawMessage(`99`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a": 99, "b": 2}`
	if string(got) != want {
		t.Errorf("SetMember replace = %q, want %q", got, want)
	}
}

func TestSetMember_RemoveMember(t *testing.T) {
	obj := json.RawMessage(`{"a": 1, "b": 2}`)
	got, err := SetMember(obj, "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"b": 2}`
	if string(got) != want {
		t.Errorf("SetMember remove = %q, want %q", got, want)
	}
}

func TestSetMember_OnlyMember(t *testing.T) {
	obj := json.RawMessage(`{"a": 1}`)
	got, err := SetMember(obj, "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `{}`
	if string(got) != want {
		t.Errorf("SetMember remove only = %q, want %q", got, want)
	}
}

func TestSetMember_FirstOfMany(t *testing.T) {
	obj := json.RawMessage(`{"a": 1, "b": 2, "c": 3}`)
	got, err := SetMember(obj, "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"b": 2, "c": 3}`
	if string(got) != want {
		t.Errorf("SetMember remove first = %q, want %q", got, want)
	}
}

func TestSetMember_LastOfMany(t *testing.T) {
	obj := json.RawMessage(`{"a": 1, "b": 2, "c": 3}`)
	got, err := SetMember(obj, "c", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a": 1, "b": 2}`
	if string(got) != want {
		t.Errorf("SetMember remove last = %q, want %q", got, want)
	}
}

func TestSetMember_NestedKeyName(t *testing.T) {
	obj := json.RawMessage(`{"a.b.c": 1}`)
	got, err := SetMember(obj, "a.b.c", json.RawMessage(`2`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a.b.c": 2}`
	if string(got) != want {
		t.Errorf("SetMember nested key name = %q, want %q", got, want)
	}
}

func TestSetMember_KeyInStringValue(t *testing.T) {
	obj := json.RawMessage(`{"a": "has \"b\" in it", "b": 2}`)
	got, err := SetMember(obj, "b", json.RawMessage(`99`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a": "has \"b\" in it", "b": 99}`
	if string(got) != want {
		t.Errorf("SetMember key in string = %q, want %q", got, want)
	}
}

func TestSetMember_DuplicateMemberRefused(t *testing.T) {
	obj := json.RawMessage(`{"a": 1, "a": 2}`)
	_, err := SetMember(obj, "a", json.RawMessage(`3`))
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("SetMember duplicate = %v, want duplicate error", err)
	}
}

func TestSetMember_PreservesIndentation(t *testing.T) {
	obj := json.RawMessage(`{
  "a": 1,
  "b": {
    "nested": true
  }
}`)
	// SetMember formats the value to match obj's style
	got, err := SetMember(obj, "c", json.RawMessage(`[1, 2, 3]`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "a": 1,
  "b": {
    "nested": true
  },
  "c": [
    1,
    2,
    3
  ]
}`
	if string(got) != want {
		t.Errorf("SetMember:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestSetMember_EmptyObject(t *testing.T) {
	obj := json.RawMessage(`{}`)
	got, err := SetMember(obj, "a", json.RawMessage(`1`))
	if err != nil {
		t.Fatal(err)
	}
	// Empty object gets default 2-space indentation
	want := `{
  "a": 1
}`
	if string(got) != want {
		t.Errorf("SetMember empty object = %q, want %q", got, want)
	}
}

func TestSetMember_NotAnObject(t *testing.T) {
	_, err := SetMember(json.RawMessage(`[]`), "a", json.RawMessage(`1`))
	if err == nil || !strings.Contains(err.Error(), "not a JSON object") {
		t.Errorf("SetMember array = %v, want object error", err)
	}
}

func TestSetMember_InvalidJSON(t *testing.T) {
	_, err := SetMember(json.RawMessage(`{bad`), "a", json.RawMessage(`1`))
	if err == nil {
		t.Error("SetMember invalid = nil, want error")
	}
}

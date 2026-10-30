package history

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newSessionFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func readMetaMap(t *testing.T, sessionFile string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(MetaPath(sessionFile))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestUpdateMeta_CreatesFileWith0600AndNewFields(t *testing.T) {
	sf := newSessionFile(t)
	err := UpdateMeta(sf, func(m *Meta) {
		m.Description = "A garden shed plan."
		m.DescriptionBy = "model"
		m.DescribedTurns = 4
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(MetaPath(sf))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	raw := readMetaMap(t, sf)
	if raw["description"] != "A garden shed plan." || raw["description_by"] != "model" || raw["described_turns"] != float64(4) {
		t.Fatalf("new fields not written in snake_case: %v", raw)
	}
	if _, ok := raw["updated_at"]; !ok {
		t.Fatal("updated_at not set")
	}
	if got := LoadMeta(sf); got.Description != "A garden shed plan." {
		t.Fatalf("LoadMeta Description = %q", got.Description)
	}
}

func TestUpdateMeta_KeepsUnknownKeysAndClearsEmptiedFields(t *testing.T) {
	sf := newSessionFile(t)
	existing := `{"title":"Old","note":"keep me","future_field":{"a":[1,2]},"other":"x"}`
	if err := os.WriteFile(MetaPath(sf), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UpdateMeta(sf, func(m *Meta) { m.Title = ""; m.Starred = true }); err != nil {
		t.Fatal(err)
	}
	raw := readMetaMap(t, sf)
	if _, ok := raw["title"]; ok {
		t.Fatal("cleared title still present")
	}
	if raw["note"] != "keep me" || raw["starred"] != true || raw["other"] != "x" {
		t.Fatalf("fields lost: %v", raw)
	}
	ff, ok := raw["future_field"].(map[string]any)
	if !ok || len(ff["a"].([]any)) != 2 {
		t.Fatalf("unknown nested key not kept: %v", raw["future_field"])
	}
}

func TestUpdateMeta_KeepsExistingMode(t *testing.T) {
	sf := newSessionFile(t)
	if err := os.WriteFile(MetaPath(sf), []byte(`{}`), 0o640); err != nil { // #nosec G306 -- the test needs a non-default mode
		t.Fatal(err)
	}
	if err := os.Chmod(MetaPath(sf), 0o640); err != nil { // #nosec G302 -- the test needs a non-default mode
		t.Fatal(err)
	}
	if err := UpdateMeta(sf, func(m *Meta) { m.Note = "n" }); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(MetaPath(sf))
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v, want 0640", info.Mode().Perm())
	}
}

// The file is replaced, never rewritten in place: a reader holding the old
// file sees the old content whole, and no temp files are left behind.
func TestUpdateMeta_AtomicReplace(t *testing.T) {
	sf := newSessionFile(t)
	if err := UpdateMeta(sf, func(m *Meta) { m.Note = "first" }); err != nil {
		t.Fatal(err)
	}
	old, err := os.Open(MetaPath(sf))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = old.Close() }()
	if err := UpdateMeta(sf, func(m *Meta) { m.Note = "second" }); err != nil {
		t.Fatal(err)
	}
	var m Meta
	if err := json.NewDecoder(old).Decode(&m); err != nil || m.Note != "first" {
		t.Fatalf("old handle saw %q (%v): file was rewritten in place", m.Note, err)
	}
	if LoadMeta(sf).Note != "second" {
		t.Fatal("new content not visible")
	}
	entries, _ := os.ReadDir(filepath.Dir(sf))
	for _, e := range entries {
		if strings.Contains(e.Name(), "tmp") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestUpdateMeta_CorruptFileIsAnErrorAndUntouched(t *testing.T) {
	sf := newSessionFile(t)
	if err := os.WriteFile(MetaPath(sf), []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := UpdateMeta(sf, func(m *Meta) { m.Starred = true })
	if err == nil || !strings.Contains(err.Error(), MetaPath(sf)) {
		t.Fatalf("err = %v, want one naming the file", err)
	}
	data, _ := os.ReadFile(MetaPath(sf))
	if string(data) != `{not json` {
		t.Fatal("corrupt file was overwritten")
	}
}

func TestUpdateMeta_MissingDirectoryIsAnError(t *testing.T) {
	err := UpdateMeta(filepath.Join(t.TempDir(), "nope", "s.jsonl"), func(m *Meta) {})
	if err == nil {
		t.Fatal("want error for a missing directory")
	}
}

// Many goroutines each set their own field; every change survives.
func TestUpdateMeta_ConcurrentGoroutinesKeepEveryChange(t *testing.T) {
	sf := newSessionFile(t)
	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- UpdateMeta(sf, func(m *Meta) {
				m.Tags = append(m.Tags, fmt.Sprintf("t%02d", i))
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := len(LoadMeta(sf).Tags); got != n {
		t.Fatalf("tags = %d, want %d: writes were lost", got, n)
	}
}

// Two processes (the TUI starring while the namer writes): both changes survive.
func TestUpdateMeta_TwoProcessesKeepBothChanges(t *testing.T) {
	sf := newSessionFile(t)
	const rounds = 30
	run := func(field string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperMetaWriter$") // #nosec G204 G702 -- the test binary itself
		cmd.Env = append(os.Environ(), "AIMUX_META_HELPER="+field, "AIMUX_META_FILE="+sf, fmt.Sprintf("AIMUX_META_ROUNDS=%d", rounds))
		return cmd
	}
	a, b := run("tags"), run("note")
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	if err := a.Wait(); err != nil {
		t.Fatalf("helper a: %v", err)
	}
	if err := b.Wait(); err != nil {
		t.Fatalf("helper b: %v", err)
	}
	m := LoadMeta(sf)
	if len(m.Tags) != rounds {
		t.Fatalf("tags = %d, want %d: the other process dropped writes", len(m.Tags), rounds)
	}
	if m.Note != strings.Repeat("n", rounds) {
		t.Fatalf("note length = %d, want %d", len(m.Note), rounds)
	}
}

// TestHelperMetaWriter is run as a child process by the test above.
func TestHelperMetaWriter(t *testing.T) {
	field := os.Getenv("AIMUX_META_HELPER")
	if field == "" {
		t.Skip("helper process only")
	}
	sf := os.Getenv("AIMUX_META_FILE")
	var rounds int
	_, _ = fmt.Sscan(os.Getenv("AIMUX_META_ROUNDS"), &rounds)
	for i := 0; i < rounds; i++ {
		err := UpdateMeta(sf, func(m *Meta) {
			if field == "tags" {
				m.Tags = append(m.Tags, fmt.Sprintf("t%02d", i))
			} else {
				m.Note += "n"
			}
			time.Sleep(time.Millisecond) // widen the read-modify-write window
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestDiscover_FillsDescriptionFromMeta(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, "-Users-test-project")
	_ = os.MkdirAll(projDir, 0o750)
	sp := minimalSession(t, projDir, "described", "some task", time.Date(2026, 3, 6, 10, 0, 0, 0, time.UTC))
	if err := UpdateMeta(sp, func(m *Meta) { m.Description = "Owner words."; m.DescriptionBy = "owner" }); err != nil {
		t.Fatal(err)
	}
	sessions, err := Discover(DiscoverOpts{}, dir)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("Discover: %v, %d sessions", err, len(sessions))
	}
	if sessions[0].Description != "Owner words." {
		t.Fatalf("Description = %q", sessions[0].Description)
	}
}

// Every .meta.json writer goes through UpdateMeta (or RemoveMeta): outside
// the files that own the sidecar, no production code even builds its path.
func TestMetaWriters_OnlyOwnersTouchMetaPath(t *testing.T) {
	allowed := map[string]bool{
		filepath.Join("internal", "history", "history.go"):   true, // MetaPath, LoadMeta
		filepath.Join("internal", "history", "metawrite.go"): true, // UpdateMeta, RemoveMeta
	}
	root := filepath.Join("..", "..")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case "node_modules", ".git", "clients", "docs-site":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if allowed[rel] {
			return nil
		}
		data, err := os.ReadFile(path) // #nosec G304 G122 -- repo source files
		if err != nil {
			return err
		}
		if strings.Contains(string(data), "MetaPath(") {
			t.Errorf("%s uses MetaPath: write session metadata with UpdateMeta", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMetaLockPath(t *testing.T) {
	if got := MetaLockPath("/p/abc.jsonl"); got != "/p/abc.meta.json.lock" {
		t.Fatalf("MetaLockPath = %q", got)
	}
}

// A file holding JSON null means no metadata.
func TestUpdateMeta_NullFileIsEmpty(t *testing.T) {
	sf := newSessionFile(t)
	if err := os.WriteFile(MetaPath(sf), []byte("null\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UpdateMeta(sf, func(m *Meta) { m.Note = "n" }); err != nil {
		t.Fatal(err)
	}
	if LoadMeta(sf).Note != "n" {
		t.Fatal("note not written over a null file")
	}
}

func TestUpdateMeta_CorruptFileErrorSaysHowToReset(t *testing.T) {
	sf := newSessionFile(t)
	if err := os.WriteFile(MetaPath(sf), []byte(`[1,`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := UpdateMeta(sf, func(m *Meta) {})
	if err == nil || !strings.Contains(err.Error(), "delete the file to reset") {
		t.Fatalf("err = %v", err)
	}
}

// Unknown keys keep their exact bytes, even numbers beyond float64.
func TestUpdateMeta_KeepsLargeNumbersExactly(t *testing.T) {
	sf := newSessionFile(t)
	if err := os.WriteFile(MetaPath(sf), []byte(`{"big":12345678901234567890}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UpdateMeta(sf, func(m *Meta) { m.Starred = true }); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(MetaPath(sf))
	if !strings.Contains(string(data), `"big": 12345678901234567890`) {
		t.Fatalf("large number changed: %s", data)
	}
}

// RemoveMeta waits for the lock, removes the metadata, then the lock file.
func TestRemoveMeta_WaitsForLockThenRemovesBoth(t *testing.T) {
	sf := newSessionFile(t)
	if err := UpdateMeta(sf, func(m *Meta) { m.Note = "n" }); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockMeta(MetaLockPath(sf))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- RemoveMeta(sf) }()
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(MetaPath(sf)); err != nil {
		t.Fatal("meta removed while another writer held the lock")
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{MetaPath(sf), MetaLockPath(sf)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s still exists", filepath.Base(p))
		}
	}
}

func TestRemoveMeta_NoMetaIsFine(t *testing.T) {
	sf := newSessionFile(t)
	if err := RemoveMeta(sf); err != nil {
		t.Fatalf("RemoveMeta without metadata: %v", err)
	}
	if _, err := os.Stat(MetaLockPath(sf)); !os.IsNotExist(err) {
		t.Fatal("lock file left behind")
	}
}

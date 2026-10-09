package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withScanCache points Discover's scan cache at a temp file for any projects
// dir and counts how many files are actually parsed.
func withScanCache(t *testing.T) (cacheFile string, scans *int) {
	t.Helper()
	cacheFile = filepath.Join(t.TempDir(), "session-scan.gob")
	origFile, origScan := scanCacheFile, scanSessionFn
	n := 0
	scanCacheFile = func(string, bool) string { return cacheFile }
	scanSessionFn = func(id, path, project string) (Session, error) {
		n++
		return origScan(id, path, project)
	}
	t.Cleanup(func() { scanCacheFile, scanSessionFn = origFile, origScan })
	return cacheFile, &n
}

func scanFixture(t *testing.T) (root, projDir string) {
	t.Helper()
	root = t.TempDir()
	projDir = filepath.Join(root, "-Users-test-myproject")
	if err := os.MkdirAll(projDir, 0o750); err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 3, 6, 10, 0, 0, 0, time.UTC)
	minimalSession(t, projDir, "aaa", "first session", ts)
	minimalSession(t, projDir, "bbb", "second session", ts.Add(time.Hour))
	return root, projDir
}

func TestDiscover_ScanCacheSkipsUnchangedFiles(t *testing.T) {
	_, scans := withScanCache(t)
	root, _ := scanFixture(t)

	first, err := Discover(DiscoverOpts{}, root)
	if err != nil || len(first) != 2 || *scans != 2 {
		t.Fatalf("first run: %d sessions, %d scans, err=%v", len(first), *scans, err)
	}
	*scans = 0
	second, err := Discover(DiscoverOpts{}, root)
	if err != nil || len(second) != 2 {
		t.Fatalf("second run: %d sessions err=%v", len(second), err)
	}
	if *scans != 0 {
		t.Errorf("second run parsed %d files, want 0 (all cached)", *scans)
	}
	if second[0].ID != first[0].ID || second[0].FirstPrompt != first[0].FirstPrompt || second[0].TokensIn != first[0].TokensIn {
		t.Errorf("cached session differs: %+v vs %+v", second[0], first[0])
	}
}

func TestDiscover_ScanCacheRescansChangedAndDropsDeleted(t *testing.T) {
	_, scans := withScanCache(t)
	root, projDir := scanFixture(t)
	if _, err := Discover(DiscoverOpts{}, root); err != nil {
		t.Fatal(err)
	}

	// change one file: only it is parsed again, and the new content shows
	ts := time.Date(2026, 3, 7, 10, 0, 0, 0, time.UTC)
	minimalSession(t, projDir, "aaa", "rewritten prompt", ts)
	future := time.Now().Add(time.Minute)
	_ = os.Chtimes(filepath.Join(projDir, "aaa.jsonl"), future, future)
	*scans = 0
	got, _ := Discover(DiscoverOpts{}, root)
	if *scans != 1 {
		t.Errorf("after one change parsed %d files, want 1", *scans)
	}
	var prompt string
	for _, s := range got {
		if s.ID == "aaa" {
			prompt = s.FirstPrompt
		}
	}
	if prompt != "rewritten prompt" {
		t.Errorf("changed session shows %q", prompt)
	}

	// delete one file: it disappears
	if err := os.Remove(filepath.Join(projDir, "bbb.jsonl")); err != nil {
		t.Fatal(err)
	}
	got, _ = Discover(DiscoverOpts{}, root)
	if len(got) != 1 || got[0].ID != "aaa" {
		t.Errorf("after delete: %v", got)
	}
}

func TestDiscover_ScanCacheKeepsSidecarMetaFresh(t *testing.T) {
	withScanCache(t)
	root, projDir := scanFixture(t)
	if _, err := Discover(DiscoverOpts{}, root); err != nil {
		t.Fatal(err)
	}
	// a title set after caching must show without the session file changing
	if err := SaveMeta(filepath.Join(projDir, "aaa.jsonl"), Meta{Title: "named later", Starred: true}); err != nil {
		t.Fatal(err)
	}
	got, _ := Discover(DiscoverOpts{}, root)
	for _, s := range got {
		if s.ID == "aaa" && (s.Title != "named later" || !s.Starred) {
			t.Errorf("sidecar meta not applied to cached session: title=%q starred=%v", s.Title, s.Starred)
		}
	}
}

func TestDiscover_ScopedRunKeepsOtherCacheEntries(t *testing.T) {
	_, scans := withScanCache(t)
	root, _ := scanFixture(t)
	other := filepath.Join(root, "-Users-test-other")
	if err := os.MkdirAll(other, 0o750); err != nil {
		t.Fatal(err)
	}
	minimalSession(t, other, "ccc", "other project", time.Date(2026, 3, 8, 10, 0, 0, 0, time.UTC))
	if _, err := Discover(DiscoverOpts{}, root); err != nil {
		t.Fatal(err)
	}
	// a run scoped to one directory must not evict the others from the cache
	if _, err := Discover(DiscoverOpts{Dir: "/Users/test/other"}, root); err != nil {
		t.Fatal(err)
	}
	*scans = 0
	if got, _ := Discover(DiscoverOpts{}, root); len(got) != 3 || *scans != 0 {
		t.Errorf("after scoped run: %d sessions, %d parsed, want 3 and 0", len(got), *scans)
	}
}

func TestDiscover_CorruptScanCacheIsIgnored(t *testing.T) {
	cacheFile, scans := withScanCache(t)
	root, _ := scanFixture(t)
	if err := os.WriteFile(cacheFile, []byte("not a gob"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Discover(DiscoverOpts{}, root)
	if err != nil || len(got) != 2 || *scans != 2 {
		t.Errorf("corrupt cache: %d sessions, %d parsed, err=%v; want a full rescan", len(got), *scans, err)
	}
}

func TestDiscover_NoCacheForCustomProjectsDirByDefault(t *testing.T) {
	// the real scanCacheFile only caches the default ~/.claude/projects
	if f := scanCacheFile(t.TempDir(), false); f != "" {
		t.Errorf("custom projects dir should not be cached, got %q", f)
	}
	if f := scanCacheFile("", true); f == "" {
		t.Error("default projects dir should be cached")
	}
}

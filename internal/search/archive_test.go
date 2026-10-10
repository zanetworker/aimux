package search

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const sidOld = "eeeeeeee-0000-0000-0000-00000000000e"

// reconstructed is the line format session-search's export writes.
func reconstructed(role, cwd, text string) string {
	if role == "user" {
		return `{"type":"user","message":{"role":"user","content":"` + text + `"},"cwd":"` + cwd + `","reconstructed":true}`
	}
	return `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` + text + `"}]},"cwd":"` + cwd + `","reconstructed":true}`
}

func sessionPath(t *testing.T, ix *Index, id string) string {
	t.Helper()
	d, err := ix.Detail(id, 0)
	if err != nil {
		t.Fatalf("Detail(%s): %v", id, err)
	}
	return d.Path
}

func TestUpdate_ArchiveAddsSessions(t *testing.T) {
	live, archive := newProjects(t), t.TempDir()
	put(t, archive, "-Users-me-old", sidOld,
		reconstructed("user", "/Users/me/old", "how did we set up the zeppelin cluster"),
		reconstructed("assistant", "/Users/me/old", "With three brokers behind a load balancer."))
	ix := openIndex(t)
	if _, err := ix.Update(live, DefaultExtractOpts(), archive); err != nil {
		t.Fatal(err)
	}
	rs, err := ix.Search("zeppelin cluster", SearchOpts{Limit: 5})
	if err != nil || len(rs) != 1 || rs[0].SessionID != sidOld || rs[0].CWD != "/Users/me/old" {
		t.Fatalf("archive session not searchable: %v err=%v", ids(rs), err)
	}
}

func TestUpdate_LiveWinsOverArchive(t *testing.T) {
	live, archive := newProjects(t), t.TempDir()
	stale := put(t, archive, "-Users-me-research", sidDeck, human("/Users/me/research", "stale archived copy"), reply("old"))
	ix := openIndex(t)
	if _, err := ix.Update(live, DefaultExtractOpts(), archive); err != nil {
		t.Fatal(err)
	}
	if p := sessionPath(t, ix, sidDeck); p == stale {
		t.Errorf("indexed the archive copy %s, want the live file", p)
	}
	if rs, _ := ix.Search("stale archived copy", SearchOpts{Limit: 5}); len(rs) != 0 {
		t.Errorf("archive content leaked into the index: %v", ids(rs))
	}
}

func TestUpdate_FallsBackToArchiveWhenLiveDeleted(t *testing.T) {
	live, archive := newProjects(t), t.TempDir()
	livePath := filepath.Join(live, "-Users-me-research", sidDeck+".jsonl")
	body, err := os.ReadFile(livePath) // #nosec G304 -- test temp dir
	if err != nil {
		t.Fatal(err)
	}
	// an rsync -a copy: same bytes, same mtime
	archived := filepath.Join(archive, "-Users-me-research", sidDeck+".jsonl")
	if err := os.MkdirAll(filepath.Dir(archived), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archived, body, 0o600); err != nil { // #nosec G703 -- test temp dir
		t.Fatal(err)
	}
	info, _ := os.Stat(livePath)
	if err := os.Chtimes(archived, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}

	ix := openIndex(t)
	if _, err := ix.Update(live, DefaultExtractOpts(), archive); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(livePath); err != nil { // Claude Code's cleanup
		t.Fatal(err)
	}
	st, err := ix.Update(live, DefaultExtractOpts(), archive)
	if err != nil {
		t.Fatal(err)
	}
	if st.Removed != 0 {
		t.Errorf("Removed = %d, want 0: the archive still has it", st.Removed)
	}
	if p := sessionPath(t, ix, sidDeck); p != archived {
		t.Errorf("path = %s, want the archive copy %s", p, archived)
	}
}

func TestUpdate_UnscannedArchiveIsNotPruned(t *testing.T) {
	live, archive := newProjects(t), t.TempDir()
	put(t, archive, "-Users-me-old", sidOld, reconstructed("user", "/Users/me/old", "zeppelin"), reconstructed("assistant", "/Users/me/old", "ok"))
	ix := openIndex(t)
	if _, err := ix.Update(live, DefaultExtractOpts(), archive); err != nil {
		t.Fatal(err)
	}
	// a caller that only knows the live dir, e.g. an older `aimux sessions index`
	st, err := ix.Update(live, DefaultExtractOpts())
	if err != nil {
		t.Fatal(err)
	}
	if st.Removed != 0 {
		t.Errorf("Removed = %d: an Update that did not scan the archive dropped its sessions", st.Removed)
	}
	if _, err := ix.Resolve(sidOld); err != nil {
		t.Errorf("archive session gone: %v", err)
	}
}

func TestUpdate_DeletedEverywhereIsPruned(t *testing.T) {
	live, archive := newProjects(t), t.TempDir()
	old := put(t, archive, "-Users-me-old", sidOld, reconstructed("user", "/Users/me/old", "zeppelin"), reconstructed("assistant", "/Users/me/old", "ok"))
	ix := openIndex(t)
	if _, err := ix.Update(live, DefaultExtractOpts(), archive); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	if st, _ := ix.Update(live, DefaultExtractOpts(), archive); st.Removed != 1 {
		t.Errorf("Removed = %d, want 1", st.Removed)
	}
}

func TestService_SearchesArchive(t *testing.T) {
	svc, _ := newService(t, nil)
	svc.ArchiveDirs = []string{t.TempDir()}
	put(t, svc.ArchiveDirs[0], "-Users-me-old", sidOld,
		reconstructed("user", "/Users/me/old", "zeppelin cluster brokers"), reconstructed("assistant", "/Users/me/old", "three"))
	old := time.Now().AddDate(-1, 0, 0)
	_ = os.Chtimes(filepath.Join(svc.ArchiveDirs[0], "-Users-me-old", sidOld+".jsonl"), old, old)
	rs, _, err := svc.Query(context.Background(), "zeppelin", QueryOpts{Mode: ModeKeyword})
	if err != nil || len(rs) != 1 || rs[0].SessionID != sidOld {
		t.Fatalf("Query over archive: %v err=%v", ids(rs), err)
	}
	if tr, err := svc.Session(context.Background(), "eeeeeeee"); err != nil || len(tr.Exchanges) != 1 {
		t.Errorf("Session over archive: %+v err=%v", tr.Detail, err)
	}
}

func TestDefaultService_IndexesArchiveDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	svc := DefaultService(nil)
	want := filepath.Join(home, ".aimux", "archive")
	if len(svc.ArchiveDirs) != 1 || svc.ArchiveDirs[0] != want || DefaultArchiveDir() != want {
		t.Errorf("ArchiveDirs = %v, DefaultArchiveDir = %s, want [%s]", svc.ArchiveDirs, DefaultArchiveDir(), want)
	}
}

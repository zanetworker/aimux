package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/history"
)

const sidWingman = "eeeeeeee-0000-0000-0000-000000000005"

func wingmanProject(t *testing.T) string {
	t.Helper()
	return strings.NewReplacer("/", "-", ".", "-").Replace(history.WingmanSessionsDir())
}

// Page-lane transcripts hold untrusted web text: the index never reads them,
// from the projects dir or an archive.
func TestUpdateSkipsWingmanSessions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, inArchive := range []bool{false, true} {
		ix := openIndex(t)
		root := newProjects(t)
		archive := t.TempDir()
		dst := root
		if inArchive {
			dst = archive
		}
		put(t, dst, wingmanProject(t), sidWingman,
			human(history.WingmanSessionsDir(), "zebrafish injected page text"),
			reply("zebrafish answer"))
		if _, err := ix.Update(root, DefaultExtractOpts(), archive); err != nil {
			t.Fatal(err)
		}
		if _, err := ix.Resolve(sidWingman); err == nil {
			t.Fatalf("archive=%v: wingman session was indexed", inArchive)
		}
		rs, err := ix.Search("zebrafish", SearchOpts{IncludeAutomated: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(rs) != 0 {
			t.Fatalf("archive=%v: search found wingman text: %v", inArchive, ids(rs))
		}
		if _, err := ix.Resolve(sidAccounts); err != nil {
			t.Fatalf("archive=%v: owner session missing: %v", inArchive, err)
		}
	}
}

// An index built before the skip drops the wingman sessions it already has.
func TestUpdateDropsAlreadyIndexedWingmanSessions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ix := openIndex(t)
	root := newProjects(t)
	p := put(t, root, "-Users-me-research", sidWingman, human("/Users/me/research", "zebrafish"))
	if _, err := ix.Update(root, DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Resolve(sidWingman); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Move it under the wingman project, as an older index would have read it.
	wdir := filepath.Join(root, wingmanProject(t))
	if err := os.MkdirAll(wdir, 0o750); err != nil {
		t.Fatal(err)
	}
	np := filepath.Join(wdir, sidWingman+".jsonl")
	if err := os.Rename(p, np); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.db.Exec(`UPDATE sessions SET path = ? WHERE id = ?`, np, sidWingman); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Update(root, DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Resolve(sidWingman); err == nil {
		t.Fatal("already indexed wingman session kept")
	}
}

package search

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// indexAt builds an index over the fixture projects, then lets set rewrite
// what a newer aimux would have stored, and reopens it.
func indexAt(t *testing.T, set string) (*Index, string, string) {
	t.Helper()
	root := newProjects(t)
	path := filepath.Join(t.TempDir(), "search.db")
	ix, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Update(root, DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.db.Exec(set); err != nil {
		t.Fatal(err)
	}
	_ = ix.Close()
	ix, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = ix.Close() })
	return ix, root, path
}

// An index written by a newer aimux (several builds share ~/.aimux/search.db)
// is neither dropped nor rewritten: an older build serves it as it is.
func TestOpen_NewerSchemaIsKeptAndNotRefreshed(t *testing.T) {
	ix, root, _ := indexAt(t, fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion+1))
	if rs := mustSearch(t, ix, "hypotheses"); len(rs) != 1 {
		t.Fatalf("newer index dropped: %v", ids(rs))
	}
	if _, err := ix.Update(root, DefaultExtractOpts()); !errors.Is(err, ErrNewerIndex) {
		t.Fatalf("Update on a newer index: %v, want ErrNewerIndex", err)
	}
	var v int
	if err := ix.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != schemaVersion+1 {
		t.Fatalf("user_version %d (%v), want it left at %d", v, err, schemaVersion+1)
	}
}

func TestOpen_NewerExtractorIsKeptAndNotRefreshed(t *testing.T) {
	newer := strconv.Itoa(extractVersion + 1)
	ix, root, _ := indexAt(t, `UPDATE meta SET value = '`+newer+`' WHERE key = 'extract_version'`)
	var marked int
	if err := ix.db.QueryRow(`SELECT count(*) FROM sessions WHERE size = -1`).Scan(&marked); err != nil || marked != 0 {
		t.Fatalf("%d sessions marked for re-read (%v), want none", marked, err)
	}
	if _, err := ix.Update(root, DefaultExtractOpts()); !errors.Is(err, ErrNewerIndex) {
		t.Fatalf("Update: %v, want ErrNewerIndex", err)
	}
	var v string
	if err := ix.db.QueryRow(`SELECT value FROM meta WHERE key = 'extract_version'`).Scan(&v); err != nil || v != newer {
		t.Fatalf("extract_version %q (%v), want %s", v, err, newer)
	}
}

// An older or unreadable extract_version still means re-read (boundary: the
// current version is not "older").
func TestOpen_OlderExtractorStillRereads(t *testing.T) {
	for _, stored := range []string{"0", "garbage"} {
		ix, root, _ := indexAt(t, `UPDATE meta SET value = '`+stored+`' WHERE key = 'extract_version'`)
		if st, err := ix.Update(root, DefaultExtractOpts()); err != nil || st.Indexed != 4 {
			t.Errorf("stored %q: %+v %v, want every session re-read", stored, st, err)
		}
	}
	ix, root, _ := indexAt(t, `SELECT 1`)
	if st, err := ix.Update(root, DefaultExtractOpts()); err != nil || st.Indexed != 0 {
		t.Errorf("current version: %+v %v, want nothing re-read", st, err)
	}
}

// The service answers from a newer index without refreshing it, and says
// so once.
func TestService_NewerIndexServedWithoutRefresh(t *testing.T) {
	_, root, path := indexAt(t, fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion+1))
	svc, notes := newService(t, nil)
	svc.DBPath, svc.ProjectsDir = path, root
	for i := 0; i < 2; i++ {
		rs, err := svc.Recent(context.Background(), SearchOpts{Limit: 10})
		if err != nil || len(rs) == 0 {
			t.Fatalf("Recent on a newer index: %v %v", ids(rs), err)
		}
	}
	if n := strings.Count(notes.String(), "newer aimux"); n != 1 {
		t.Fatalf("notes %q: want one note about the newer index", notes.String())
	}
}

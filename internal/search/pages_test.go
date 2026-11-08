package search

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// pagedIndex indexes n sessions, newest first by id order: session i is i
// minutes old; sessions in tie share one mtime.
func pagedIndex(t *testing.T, n int, tie map[int]bool) (*Index, string, []string) {
	t.Helper()
	root := t.TempDir()
	base := time.Now().Add(-time.Hour)
	var ids []string
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("p%07d-0000-0000-0000-000000000000", i)
		p := put(t, root, "-w", id, human("/w", fmt.Sprintf("message %d", i)), reply("ok"))
		at := base.Add(-time.Duration(i) * time.Minute)
		if tie[i] {
			at = base.Add(-time.Duration(n) * time.Minute) // all ties older than the rest
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	ix := openIndex(t)
	if _, err := ix.Update(root, DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}
	return ix, root, ids
}

func idsOf(rs []Result) string {
	var out []string
	for _, r := range rs {
		out = append(out, r.SessionID[:8])
	}
	return strings.Join(out, " ")
}

// Rows keep rejects do not use up the limit: paging goes on until limit
// kept rows or the index runs out.
func TestRecentWhere_PagesPastRejectedRows(t *testing.T) {
	ix, _, ids := pagedIndex(t, 10, nil)
	rejected := map[string]bool{}
	for _, id := range ids[:6] { // the 6 newest
		rejected[id] = true
	}
	keep := func(r Result) bool { return !rejected[r.SessionID] }
	rs, capped, err := ix.RecentWhere(SearchOpts{Limit: 3}, 2, 20, keep)
	if err != nil || capped {
		t.Fatalf("err %v capped %v", err, capped)
	}
	if got := idsOf(rs); got != "p0000006 p0000007 p0000008" {
		t.Fatalf("rows %q", got)
	}
	// Exhausted before limit: every kept row, not capped.
	rs, capped, _ = ix.RecentWhere(SearchOpts{Limit: 10}, 2, 20, keep)
	if got := idsOf(rs); got != "p0000006 p0000007 p0000008 p0000009" || capped {
		t.Fatalf("exhausted: %q capped %v", got, capped)
	}
}

// The page cap stops a long scan and says so.
func TestRecentWhere_PageCap(t *testing.T) {
	ix, _, _ := pagedIndex(t, 10, nil)
	none := func(Result) bool { return false }
	rs, capped, err := ix.RecentWhere(SearchOpts{Limit: 3}, 2, 3, none)
	if err != nil || len(rs) != 0 || !capped {
		t.Fatalf("rows %q err %v capped %v, want none and capped", idsOf(rs), err, capped)
	}
	// Running out on the last allowed page is not a cap.
	if _, capped, _ := ix.RecentWhere(SearchOpts{Limit: 3}, 5, 2, none); capped {
		t.Fatal("index exhausted exactly at the cap reported as capped")
	}
}

// Rows sharing an mtime are neither repeated nor skipped across pages.
func TestRecentWhere_TiesAcrossPages(t *testing.T) {
	ix, _, _ := pagedIndex(t, 7, map[int]bool{2: true, 3: true, 4: true, 5: true})
	all := func(Result) bool { return true }
	rs, _, err := ix.RecentWhere(SearchOpts{Limit: 100}, 2, 20, all)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, r := range rs {
		seen[r.SessionID]++
	}
	if len(rs) != 7 || len(seen) != 7 {
		t.Fatalf("rows %q: want all 7 once", idsOf(rs))
	}
}

// Since is applied in the query, so old rows never fill a page.
func TestRecentWhere_Since(t *testing.T) {
	ix, _, _ := pagedIndex(t, 10, nil)
	since := time.Now().Add(-time.Hour - 150*time.Second) // sessions 0, 1 and 2
	rs, capped, err := ix.RecentWhere(SearchOpts{Limit: 10, Since: since}, 2, 20, func(Result) bool { return true })
	if err != nil || capped || idsOf(rs) != "p0000000 p0000001 p0000002" {
		t.Fatalf("rows %q err %v capped %v", idsOf(rs), err, capped)
	}
}

// The service refreshes once and pages the same way.
func TestService_RecentWhere(t *testing.T) {
	_, root, ids := pagedIndex(t, 6, nil)
	svc, _ := newService(t, nil)
	svc.ProjectsDir = root
	keep := func(r Result) bool { return r.SessionID != ids[0] }
	rs, capped, err := svc.RecentWhere(context.Background(), SearchOpts{Limit: 2}, 1, 20, keep)
	if err != nil || capped || idsOf(rs) != "p0000001 p0000002" {
		t.Fatalf("rows %q err %v capped %v", idsOf(rs), err, capped)
	}
}

package remoteapi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/search"
)

func TestCreatedStore_PersistsPrivately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote-created.json")
	s := &CreatedStore{Path: path}
	if err := s.Add("new-1", "/w/app", "start the cache work"); err != nil {
		t.Fatal(err)
	}
	again := &CreatedStore{Path: path} // as after a restart
	c, ok := again.Get("new-1")
	if !ok || c.Cwd != "/w/app" || c.Prompt != "start the cache work" {
		t.Fatalf("after reload: %+v %v", c, ok)
	}
	if _, ok := again.Get("other"); ok {
		t.Fatal("unknown id reported as created")
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("store file mode %v (%v), want 0600: prompts are private", fi.Mode().Perm(), err)
	}
}

func TestCreatedStore_MissingFileIsEmpty(t *testing.T) {
	s := &CreatedStore{Path: filepath.Join(t.TempDir(), "none.json")}
	if _, ok := s.Get("x"); ok {
		t.Fatal("empty store has an entry")
	}
}

// Sessions started from the phone run through the agent SDK, which marks
// them automated; aimux knows it created them and lists them anyway.
func TestLocalIndex_ListsSessionsItCreated(t *testing.T) {
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	x.Created = &CreatedStore{Path: filepath.Join(t.TempDir(), "c.json")}
	if err := x.Created.Add("cron", "/w", "x"); err != nil { // the fixture's automated session
		t.Fatal(err)
	}
	got, err := x.Sessions(context.Background(), 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if !hasSession(got, "cron") {
		t.Fatalf("created session missing from %v", ids(got))
	}
}

// Right after creation the agent may not have written the file yet; the
// session still opens, empty, titled by its first prompt.
func TestLocalIndex_CreatedSessionBeforeItsFile(t *testing.T) {
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	x.Created = &CreatedStore{Path: filepath.Join(t.TempDir(), "c.json")}
	if err := x.Created.Add("phone-1", "/w/app", "plan the release"); err != nil {
		t.Fatal(err)
	}
	d, err := x.Session(context.Background(), "phone-1", 20)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != "phone-1" || d.Title != "plan the release" || d.Project != "/w/app" || len(d.Turns) != 0 || d.State != StateIdle {
		t.Fatalf("detail %+v", d)
	}
}

func TestLocalIndex_Projects(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	for _, d := range []string{a, b} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	x := &LocalIndex{Discover: func(history.DiscoverOpts, string) ([]history.Session, error) {
		return []history.Session{
			{ID: "1", Provider: "claude", Project: a, LastActive: now},
			{ID: "2", Provider: "claude", Project: b, LastActive: now.Add(-time.Hour)},
			{ID: "3", Provider: "claude", Project: a, LastActive: now.Add(-2 * time.Hour)},
			{ID: "4", Provider: "claude", Project: filepath.Join(root, "gone"), LastActive: now},
			{ID: "5", Provider: "claude", Project: "/tmp/x", Automated: true, LastActive: now},
			{ID: "6", Provider: "codex", Project: b, LastActive: now},
		}, nil
	}}
	got, err := x.Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Path != a || got[1].Path != b || got[0].Name != "a" {
		t.Fatalf("projects %+v, want a then b, once each, existing dirs only", got)
	}
}

func TestLocalIndex_Search(t *testing.T) {
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	x.Created = &CreatedStore{Path: filepath.Join(t.TempDir(), "c.json")}
	if err := x.Created.Add("made", "/w", "x"); err != nil {
		t.Fatal(err)
	}
	var gotQuery string
	x.SearchIndex = func(ctx context.Context, q string, limit int) ([]search.Result, error) {
		gotQuery = q
		return []search.Result{
			{SessionID: "idle", Title: "Fix the cache", CWD: "/w", Snippet: "the [cache] TTL"},
			{SessionID: "auto", Title: "nightly", Automated: true, Snippet: "cache"},
			{SessionID: "made", Title: "", Automated: true, CWD: "/w", Snippet: "cache from the phone"},
			{SessionID: "archived-only", Title: "old", Snippet: "cache"},
		}, nil
	}
	hits, err := x.Search(context.Background(), "cache", 20)
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != "cache" {
		t.Fatalf("query %q", gotQuery)
	}
	if len(hits) != 2 || hits[0].ID != "idle" || hits[0].Snippet != "the [cache] TTL" || hits[0].State != StateIdle || hits[1].ID != "made" {
		t.Fatalf("hits %+v; want idle and the created session, not automated or archived-only ones", hits)
	}
}

func hasSession(list []Session, id string) bool {
	for _, s := range list {
		if s.ID == id {
			return true
		}
	}
	return false
}

func ids(list []Session) []string {
	var out []string
	for _, s := range list {
		out = append(out, s.ID)
	}
	return out
}

// Eligible matches below a page of automated ones are still found: the
// limit applies after filtering.
func TestLocalIndex_SearchLooksPastIneligibleMatches(t *testing.T) {
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	all := make([]search.Result, 0, 41)
	for i := 0; i < 40; i++ {
		all = append(all, search.Result{SessionID: fmt.Sprintf("auto-%d", i), Automated: true, Snippet: "cache"})
	}
	all = append(all, search.Result{SessionID: "idle", Title: "Fix the cache", Snippet: "the [cache]"})
	x.SearchIndex = func(ctx context.Context, q string, limit int) ([]search.Result, error) {
		return all[:min(limit, len(all))], nil
	}
	hits, err := x.Search(context.Background(), "cache", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "idle" {
		t.Fatalf("hits %+v, want the interactive session ranked below 40 automated ones", hits)
	}
}

// A session created here is listed before Claude Code has written its file
// (still starting, or failed early), so the user can always find it.
func TestLocalIndex_ListsCreatedSessionsWithoutAFile(t *testing.T) {
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	x.Created = &CreatedStore{Path: filepath.Join(t.TempDir(), "c.json")}
	if err := x.Created.Add("born-now", "/w/app", "plan the release"); err != nil {
		t.Fatal(err)
	}
	got, err := x.Sessions(context.Background(), 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "born-now" || got[0].Title != "plan the release" {
		t.Fatalf("got %v; want the just-created session first (newest), within the limit", ids(got))
	}
}

// Semantic ranking always returns its nearest sessions, even for nonsense;
// a hit with no matching text says nothing, so it is left out.
func TestLocalIndex_SearchDropsHitsWithoutAMatch(t *testing.T) {
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	x.SearchIndex = func(ctx context.Context, q string, limit int) ([]search.Result, error) {
		return []search.Result{{SessionID: "idle", Title: "Fix the cache", Snippet: ""}}, nil
	}
	hits, err := x.Search(context.Background(), "zqxjvwk", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("hits %+v, want none for a query nothing contains", hits)
	}
}

func TestLocalIndex_ProjectsUseTheRecordedCwd(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "my-app")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "my", "app"), 0o700); err != nil { // the look-alike
		t.Fatal(err)
	}
	x := &LocalIndex{Discover: func(history.DiscoverOpts, string) ([]history.Session, error) {
		return []history.Session{{ID: "1", Provider: "claude", Project: filepath.Join(root, "my", "app"), Cwd: real, LastActive: time.Now()}}, nil
	}}
	got, err := x.Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != real {
		t.Fatalf("projects %+v, want the recorded %s", got, real)
	}
}

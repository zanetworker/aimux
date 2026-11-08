package remoteapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/harness"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/search"
	"github.com/zanetworker/aimux/internal/trace"
)

// secondProvider registers a fake harness next to the fixture's Claude
// store: its own transcripts, sessions, tail and live holders. Only the
// registration differs; no index code knows it.
func secondProvider(t *testing.T, x *LocalIndex) (dir string) {
	t.Helper()
	projects := t.TempDir()
	dir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(projects, "-x"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"fx-1", "fx-2"} {
		if err := os.WriteFile(filepath.Join(projects, "-x", id+".jsonl"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().Add(time.Hour) // newer than the fixture's sessions
	fake := &stubStore{name: "fake", ProjectsDir: projects,
		Discover: func(history.DiscoverOpts, string) ([]history.Session, error) {
			return []history.Session{
				{ID: "fx-1", Title: "Fake one", Cwd: dir, LastActive: now},
				{ID: "fx-2", Provider: "fake", Title: "Fake two", LastActive: now.Add(-time.Minute)},
				{ID: "fx-foreign", Provider: "claude", LastActive: now}, // not this store's
			}, nil
		},
		Tail: func(path string, n int) ([]trace.Turn, error) {
			if !strings.HasSuffix(path, "fx-1.jsonl") {
				t.Errorf("tail of %s, want fx-1's file", path)
			}
			return []trace.Turn{{Number: 1, UserLines: []string{"fake question"}}}, nil
		},
	}
	live := &countingLive{hs: []harness.Holder{{PID: 4242, SessionID: "fx-1", Status: "busy"}}}
	x.Providers = append(x.Providers, Provider{Store: fake, Live: live})
	return dir
}

func TestLocalIndex_SecondProviderListsAndResolvesItsSessions(t *testing.T) {
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	dir := secondProvider(t, x)
	ctx := context.Background()

	list, err := x.Sessions(ctx, 20, false)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Session{}
	for _, s := range list {
		byID[s.ID] = s
	}
	if s := byID["fx-1"]; s.Provider != "fake" || s.State != StateOpenInTerminal || !s.Working {
		t.Fatalf("fx-1 = %+v, want a fake session open in a terminal, working (its own Live)", s)
	}
	if s := byID["fx-2"]; s.Provider != "fake" || s.State != StateIdle {
		t.Fatalf("fx-2 = %+v, want an idle fake session", s)
	}
	if _, ok := byID["fx-foreign"]; ok {
		t.Fatal("a record the fake store gives for another provider was listed")
	}
	if s := byID["idle"]; s.Provider != "claude" {
		t.Fatalf("Claude's idle = %+v, want provider claude", s)
	}
	if list[0].ID != "fx-1" {
		t.Fatalf("first %q, want the newest session of either provider", list[0].ID)
	}

	d, err := x.Session(ctx, "fx-1", 5)
	if err != nil {
		t.Fatal(err)
	}
	if d.Provider != "fake" || d.Title != "Fake one" || len(d.Turns) != 1 || d.Turns[0].User != "fake question" {
		t.Fatalf("detail %+v, want fx-1 from the fake store", d)
	}
	if err := x.SessionExists(ctx, "fx-2"); err != nil {
		t.Fatalf("SessionExists(fx-2) = %v", err)
	}
	if pid, ok := x.TerminalPID("fx-1"); !ok || pid != 4242 {
		t.Fatalf("TerminalPID(fx-1) = %d, %v", pid, ok)
	}

	projects, err := x.Projects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) == 0 || projects[0].Path != dir {
		t.Fatalf("projects %+v, want the fake session's folder first", projects)
	}

	x.SearchIndex = func(context.Context, string, int, []string) ([]search.Result, error) {
		return []search.Result{{SessionID: "fx-2", Snippet: "two"}, {SessionID: "idle", Snippet: "cache"}}, nil
	}
	hits, err := x.Search(ctx, "q", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].Provider != "fake" || hits[1].Provider != "claude" {
		t.Fatalf("hits %+v, want fx-2 as fake and idle as claude", hits)
	}
}

// A provider whose sessions cannot be listed fails the list, naming it.
func TestLocalIndex_ProviderListErrorNamesIt(t *testing.T) {
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	x.Providers = append(x.Providers, Provider{Store: &stubStore{name: "fake",
		Discover: func(history.DiscoverOpts, string) ([]history.Session, error) { return nil, errors.New("disk gone") }}})
	if _, err := x.Sessions(context.Background(), 5, false); err == nil || !strings.Contains(err.Error(), "fake: disk gone") {
		t.Fatalf("err = %v, want the fake provider's failure", err)
	}
}

// With no provider registered the index lists nothing and knows no session.
func TestLocalIndex_NoProviders(t *testing.T) {
	x := &LocalIndex{Broker: NewBroker(BrokerDeps{}, BrokerOptions{})}
	if list, err := x.Sessions(context.Background(), 5, false); err != nil || len(list) != 0 {
		t.Fatalf("Sessions = %v, %v; want none", list, err)
	}
	if err := x.SessionExists(context.Background(), "any"); err == nil {
		t.Fatal("SessionExists without providers: no error")
	}
}

// Without NewProvider, sessions created here take the first provider's
// name, so a Created row never carries provider "".
func TestLocalIndex_NewProviderDefaultsToTheFirst(t *testing.T) {
	x := &LocalIndex{Broker: NewBroker(BrokerDeps{}, BrokerOptions{}),
		Providers: []Provider{{Store: &stubStore{name: "first"}}, {Store: &stubStore{name: "second"}}},
		Created:   &CreatedStore{Path: filepath.Join(t.TempDir(), "c.json")},
		Running:   func(string) bool { return true }}
	if err := x.Created.Add("made", "/w", "hello"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	list, err := x.Sessions(ctx, 5, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Provider != "first" {
		t.Fatalf("list %+v, want the created session as the first provider's", list)
	}
	d, err := x.Session(ctx, "made", 5)
	if err != nil || d.Provider != "first" {
		t.Fatalf("detail %+v, %v; want provider first", d, err)
	}
	x.SearchIndex = func(context.Context, string, int, []string) ([]search.Result, error) {
		return []search.Result{{SessionID: "made", Snippet: "hello"}}, nil
	}
	if hits, err := x.Search(ctx, "hello", 5); err != nil || len(hits) != 1 || hits[0].Provider != "first" {
		t.Fatalf("hits %+v, %v; want provider first", hits, err)
	}
	x.NewProvider = "second" // an explicit setting wins
	if list, _ := x.Sessions(ctx, 5, false); len(list) != 1 || list[0].Provider != "second" {
		t.Fatalf("list %+v, want the explicit NewProvider", list)
	}
	if got := (&LocalIndex{}).newProvider(); got != "" {
		t.Fatalf("newProvider without providers = %q, want empty", got)
	}
}

package remoteapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/search"
)

// A search hit shows the same title as the list for the same session, even
// when the index holds an older or differently chosen title.
func TestLocalIndex_SearchHitTitleMatchesList(t *testing.T) {
	root := t.TempDir()
	projects, registry := filepath.Join(root, "projects"), filepath.Join(root, "sessions")
	for _, d := range []string{filepath.Join(projects, "-w"), registry} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ids := []string{"meta", "ai", "first"}
	for _, id := range ids {
		if err := os.WriteFile(filepath.Join(projects, "-w", id+".jsonl"), []byte(`{"cwd":"/w"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	sessions := []history.Session{
		{ID: "meta", Provider: "claude", Title: "Meta title", AITitle: "Meta ai name", FirstPrompt: "meta first", LastActive: now},
		{ID: "ai", Provider: "claude", AITitle: "AI only name", FirstPrompt: "ai first", LastActive: now},
		{ID: "first", Provider: "claude", FirstPrompt: "[Image #1] plain first message", LastActive: now},
	}
	x := &LocalIndex{Broker: NewBroker(BrokerDeps{}, BrokerOptions{}), NewProvider: "claude", Providers: stubProviders(&stubStore{
		ProjectsDir: projects,
		Discover:    func(history.DiscoverOpts, string) ([]history.Session, error) { return sessions, nil },
	}, discovery.NewLiveCache(registry, 0))}
	x.SearchIndex = func(ctx context.Context, q string, limit int, allowed []string) ([]search.Result, error) {
		var out []search.Result
		for _, id := range ids {
			out = append(out, search.Result{SessionID: id, Title: "stale index title " + id, Snippet: "the [match]"})
		}
		return out, nil
	}

	list, err := x.Sessions(context.Background(), 50, false)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]string{}
	for _, s := range list {
		listed[s.ID] = s.Title
	}
	hits, err := x.Search(context.Background(), "match", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 {
		t.Fatalf("%d hits, want 3", len(hits))
	}
	want := map[string]string{"meta": "Meta title", "ai": "AI only name", "first": "plain first message"}
	for _, h := range hits {
		if h.Title != want[h.ID] || h.Title != listed[h.ID] {
			t.Errorf("%s: hit title %q, list title %q, want %q", h.ID, h.Title, listed[h.ID], want[h.ID])
		}
	}
}

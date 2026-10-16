package search

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newService(t *testing.T, e Embedder) (*Service, *bytes.Buffer) {
	t.Helper()
	var notes bytes.Buffer
	return &Service{
		DBPath:      filepath.Join(t.TempDir(), "search.db"),
		ProjectsDir: newProjects(t),
		Embedder:    e,
		Notes:       &notes,
	}, &notes
}

func TestService_RefreshesAndRanks(t *testing.T) {
	svc, _ := newService(t, nil)
	rs, semantic, err := svc.Query(context.Background(), "Ying deck", QueryOpts{Limit: 5})
	if err != nil || semantic || len(rs) != 1 || rs[0].SessionID != sidDeck {
		t.Fatalf("got %v semantic=%v err=%v", ids(rs), semantic, err)
	}
	// automated sessions only on request
	if rs, _, _ := svc.Query(context.Background(), "service accounts", QueryOpts{Limit: 10}); len(rs) != 2 {
		t.Errorf("default: %v, want the 2 human sessions", ids(rs))
	}
	if rs, _, _ := svc.Query(context.Background(), "service accounts", QueryOpts{Limit: 10, IncludeAutomated: true}); len(rs) != 3 {
		t.Errorf("IncludeAutomated: %v, want 3", ids(rs))
	}
}

func TestService_ModesAndFallbacks(t *testing.T) {
	ctx := context.Background()

	// no embedder: hybrid quietly becomes keyword and says why
	svc, notes := newService(t, nil)
	if _, semantic, err := svc.Query(ctx, "Ying deck", QueryOpts{Mode: ModeHybrid}); err != nil || semantic {
		t.Errorf("hybrid without embedder: semantic=%v err=%v", semantic, err)
	}
	if !strings.Contains(notes.String(), "OPENAI_API_KEY") {
		t.Errorf("fallback should explain itself, notes=%q", notes.String())
	}
	// semantic explicitly requested without an embedder is an error
	if _, _, err := svc.Query(ctx, "Ying", QueryOpts{Mode: ModeSemantic}); err == nil {
		t.Error("semantic without embedder: want error")
	}

	// with an embedder: hybrid uses it, embedding small backlogs on the spot
	svc, _ = newService(t, &conceptEmbedder{})
	rs, semantic, err := svc.Query(ctx, "presentation", QueryOpts{Mode: ModeHybrid, Limit: 5})
	if err != nil || !semantic || len(rs) == 0 || rs[0].SessionID != sidDeck {
		t.Errorf("hybrid: %v semantic=%v err=%v", ids(rs), semantic, err)
	}
	// quotes mean exact, even in hybrid mode
	if rs, semantic, _ := svc.Query(ctx, `"presentation"`, QueryOpts{Mode: ModeHybrid}); semantic || len(rs) != 0 {
		t.Errorf("quoted: %v semantic=%v, want exact keyword (none)", ids(rs), semantic)
	}
	// an unknown mode is rejected with the valid values
	if _, _, err := svc.Query(ctx, "x", QueryOpts{Mode: "fuzzy"}); err == nil || !strings.Contains(err.Error(), "hybrid") {
		t.Errorf("bad mode: %v", err)
	}
}

func TestService_EmbedderFailureFallsBackToKeyword(t *testing.T) {
	svc, _ := newService(t, &conceptEmbedder{fail: errors.New("offline")})
	rs, semantic, err := svc.Query(context.Background(), "Ying deck", QueryOpts{Mode: ModeHybrid})
	if err != nil || semantic || len(rs) != 1 {
		t.Errorf("offline embedder: %v semantic=%v err=%v, want keyword results", ids(rs), semantic, err)
	}
}

func TestService_BlankQueryReturnsNothing(t *testing.T) {
	svc, _ := newService(t, nil)
	if rs, _, err := svc.Query(context.Background(), "   ", QueryOpts{}); err != nil || len(rs) != 0 {
		t.Errorf("blank: %v err=%v", ids(rs), err)
	}
}

func TestService_RefreshIndexesAndEmbedsChanges(t *testing.T) {
	e := &conceptEmbedder{}
	svc, _ := newService(t, e)
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	ix, err := Open(svc.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ix.Close() }()
	if n, _ := ix.PendingEmbeddings(e.Model()); n != 0 {
		t.Errorf("after Refresh %d texts lack vectors; the picker needs them for hybrid ranking", n)
	}
	// without an embedder Refresh still indexes
	svc2, _ := newService(t, nil)
	if err := svc2.Refresh(context.Background()); err != nil {
		t.Errorf("Refresh without embedder: %v", err)
	}
}

func TestService_SessionByPrefix(t *testing.T) {
	svc, _ := newService(t, nil)
	tr, err := svc.Session(context.Background(), "aaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Detail.SessionID != sidAccounts || tr.Detail.CWD != "/Users/me/OpenShell" {
		t.Errorf("detail = %+v", tr.Detail)
	}
	if len(tr.Exchanges) != 1 {
		t.Errorf("exchanges = %d, want 1", len(tr.Exchanges))
	}
}

func TestService_SessionSeesNewFile(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t, nil)
	if _, _, err := svc.Query(ctx, "Ying", QueryOpts{}); err != nil {
		t.Fatal(err)
	}
	sid := "eeeeeeee-0000-0000-0000-000000000005"
	put(t, svc.ProjectsDir, "-Users-me-research", sid, human("/Users/me/research", "a brand new session"), reply("ok"))
	tr, err := svc.Session(ctx, "eeeeeeee")
	if err != nil || tr.Detail.SessionID != sid {
		t.Fatalf("Session(new file) = %q err=%v", tr.Detail.SessionID, err)
	}
}

func TestService_SessionAmbiguous(t *testing.T) {
	svc, _ := newService(t, nil)
	put(t, svc.ProjectsDir, "-Users-me-research", "bbbbbbbb-9999-0000-0000-000000000009",
		human("/Users/me/research", "a second b session"), reply("ok"))
	_, err := svc.Session(context.Background(), "bbbbbbbb")
	var amb *AmbiguousError
	if !errors.As(err, &amb) {
		t.Fatalf("err = %v, want *AmbiguousError", err)
	}
}

func TestService_RecentHidesAutomated(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t, nil)
	rs, err := svc.Recent(ctx, SearchOpts{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if r.SessionID == sidAuto {
			t.Errorf("default Recent includes automated session: %v", ids(rs))
		}
	}
	if len(rs) == 0 {
		t.Error("default Recent returned nothing")
	}
	rs, _ = svc.Recent(ctx, SearchOpts{Limit: 10, IncludeAutomated: true})
	found := false
	for _, r := range rs {
		found = found || r.SessionID == sidAuto
	}
	if !found {
		t.Errorf("IncludeAutomated Recent = %v, want %s", ids(rs), sidAuto)
	}
}

// MCP clients issue tool calls concurrently; each refreshes the index.
func TestService_ConcurrentCallsDoNotLock(t *testing.T) {
	svc, _ := newService(t, nil)
	for i := 0; i < 300; i++ {
		sid := fmt.Sprintf("ffffffff-0000-0000-0000-%012d", i)
		put(t, svc.ProjectsDir, "-Users-me-bulk", sid, human("/Users/me/bulk", fmt.Sprintf("bulk session %d %s", i, strings.Repeat("words ", 2000))), reply("ok"))
	}
	ctx := context.Background()
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		go func(i int) {
			if i%2 == 0 {
				_, err := svc.Session(ctx, "aaaaaaaa")
				errs <- err
				return
			}
			_, err := svc.Recent(ctx, SearchOpts{Limit: 5})
			errs <- err
		}(i)
	}
	for i := 0; i < 16; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent call: %v", err)
		}
	}
}

// gatedEmbedder blocks every Embed call until release is closed; safe for
// concurrent use.
type gatedEmbedder struct {
	mu      sync.Mutex
	inner   conceptEmbedder
	release chan struct{}
	started chan struct{} // closed on the first call
	once    sync.Once
}

func newGatedEmbedder() *gatedEmbedder {
	return &gatedEmbedder{release: make(chan struct{}), started: make(chan struct{})}
}

func (g *gatedEmbedder) Model() string { return g.inner.Model() }

func (g *gatedEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	g.once.Do(func() { close(g.started) })
	<-g.release
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inner.Embed(ctx, texts)
}

// Long-lived frontends (TUI, web, MCP) must not wait on the embedding API
// for whatever live sessions wrote since the last search.
func TestService_BackgroundEmbedDoesNotBlockQueries(t *testing.T) {
	g := newGatedEmbedder()
	svc, _ := newService(t, g)
	svc.EmbedInBackground = true
	ctx := context.Background()

	done := make(chan error, 1)
	go func() {
		rs, _, err := svc.Query(ctx, "Ying deck", QueryOpts{Mode: ModeKeyword, Limit: 5})
		if err == nil && (len(rs) != 1 || rs[0].SessionID != sidDeck) {
			err = fmt.Errorf("got %v", ids(rs))
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		close(g.release)
		t.Fatal("query waited for embedding; it should run in the background")
	}

	// the background job did start, and finishes once the API answers
	select {
	case <-g.started:
	case <-time.After(3 * time.Second):
		t.Fatal("changed sessions were never queued for embedding")
	}
	close(g.release)
	svc.Wait()
	ix, err := Open(svc.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ix.Close() }()
	if n, _ := ix.PendingEmbeddings(g.Model()); n != 0 {
		t.Errorf("%d texts still lack vectors after the background job", n)
	}
}

// One-shot CLI runs exit right after the query, so they keep embedding
// inline; background mode is opt-in.
func TestService_InlineEmbedIsTheDefault(t *testing.T) {
	e := &conceptEmbedder{}
	svc, _ := newService(t, e)
	if _, _, err := svc.Query(context.Background(), "presentation", QueryOpts{Mode: ModeKeyword}); err != nil {
		t.Fatal(err)
	}
	svc.Wait() // no-op without background work
	if e.calls == 0 {
		t.Error("default Query should embed changed sessions before returning")
	}
}

// The picker refreshes once in the background, so per-keystroke queries skip
// the file scan.
func TestService_NoRefreshSkipsTheScan(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t, nil)
	if _, _, err := svc.Query(ctx, "Ying", QueryOpts{}); err != nil {
		t.Fatal(err)
	}
	sid := "eeeeeeee-0000-0000-0000-000000000005"
	put(t, svc.ProjectsDir, "-Users-me-research", sid, human("/Users/me/research", "zanzibar walkthrough"), reply("ok"))
	if rs, _, _ := svc.Query(ctx, "zanzibar", QueryOpts{NoRefresh: true}); len(rs) != 0 {
		t.Errorf("NoRefresh still scanned files: %v", ids(rs))
	}
	if rs, _, _ := svc.Query(ctx, "zanzibar", QueryOpts{}); len(rs) != 1 {
		t.Errorf("default Query should pick up the new file: %v", ids(rs))
	}
}

package search

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
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

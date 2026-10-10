package search

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRecent_NewestFirstAndHidesAutomated(t *testing.T) {
	root := newProjects(t)
	ix := openIndex(t)
	if _, err := ix.Update(root, DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}
	// make sidDeck the most recently active session
	p := root + "/-Users-me-research/" + sidDeck + ".jsonl"
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(p, future, future)
	if _, err := ix.Update(root, DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}

	rs, err := ix.Recent(SearchOpts{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 3 || rs[0].SessionID != sidDeck {
		t.Errorf("Recent = %v, want 3 human sessions with %s first", ids(rs), sidDeck)
	}
	for _, r := range rs {
		if r.Automated || r.Title == "" {
			t.Errorf("bad row %+v", r)
		}
	}
	if rs, _ := ix.Recent(SearchOpts{Limit: 10, IncludeAutomated: true}); len(rs) != 4 {
		t.Errorf("IncludeAutomated: %d rows, want 4", len(rs))
	}
	if rs, _ := ix.Recent(SearchOpts{Limit: 2}); len(rs) != 2 {
		t.Errorf("limit 2: %d rows", len(rs))
	}
}

func TestDetail_FirstAndRecentPrompts(t *testing.T) {
	root := t.TempDir()
	put(t, root, "-Users-me-OpenShell", sidAccounts,
		human("/Users/me/OpenShell", "tell me what is on my cluster"),
		reply("Here is the list."),
		human("/Users/me/OpenShell", "why service accounts"),
		human("/Users/me/OpenShell", "retitle the badge to agent-ops"))
	ix := openIndex(t)
	if _, err := ix.Update(root, DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}

	d, err := ix.Detail(sidAccounts, 2)
	if err != nil {
		t.Fatal(err)
	}
	if d.CWD != "/Users/me/OpenShell" || d.FirstPrompt != "tell me what is on my cluster" || d.Exchanges != 3 {
		t.Errorf("detail = %+v", d)
	}
	want := []string{"why service accounts", "retitle the badge to agent-ops"}
	if strings.Join(d.RecentPrompts, "|") != strings.Join(want, "|") {
		t.Errorf("RecentPrompts = %q, want %q (oldest first, last 2)", d.RecentPrompts, want)
	}

	if _, err := ix.Detail("no-such-session", 2); err == nil {
		t.Error("unknown session: want error")
	}
}

func TestSearchOpts_IDsRestrictEveryQuery(t *testing.T) {
	ix, _ := indexed(t)
	e := &conceptEmbedder{}
	if _, err := ix.EmbedMissing(context.Background(), e, 50); err != nil {
		t.Fatal(err)
	}
	deckOnly := SearchOpts{Limit: 10, IDs: []string{sidDeck}}

	if rs, _ := ix.Recent(deckOnly); len(rs) != 1 || rs[0].SessionID != sidDeck {
		t.Errorf("Recent restricted to deck: %v", ids(rs))
	}
	if rs, _ := ix.Search("service accounts", deckOnly); len(rs) != 0 {
		t.Errorf("keyword search leaked sessions outside IDs: %v", ids(rs))
	}
	if rs, _ := ix.Search("Ying deck", deckOnly); len(rs) != 1 {
		t.Errorf("keyword search inside IDs: %v", ids(rs))
	}
	accountsOnly := SearchOpts{Limit: 10, IDs: []string{sidAccounts}}
	if rs, _ := ix.Semantic(context.Background(), "presentation", accountsOnly, e); len(rs) != 1 || rs[0].SessionID != sidAccounts {
		t.Errorf("semantic search leaked sessions outside IDs: %v", ids(rs))
	}
	// restricted to nothing (live-only with no live sessions) is empty, not unrestricted
	none := SearchOpts{Limit: 10, IDs: []string{}}
	if rs, _ := ix.Recent(none); len(rs) != 0 {
		t.Errorf("empty IDs must match nothing, got %v", ids(rs))
	}
	if rs, _ := ix.Search("deck", none); len(rs) != 0 {
		t.Errorf("empty IDs must match nothing, got %v", ids(rs))
	}
}

func indexedAt(t *testing.T, root string) *Index {
	t.Helper()
	ix := openIndex(t)
	if _, err := ix.Update(root, DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}
	return ix
}

func TestResolve_ExactAndPrefix(t *testing.T) {
	ix := indexedAt(t, newProjects(t))
	for _, in := range []string{sidDeck, "bbbbbbbb"} {
		if got, err := ix.Resolve(in); err != nil || got != sidDeck {
			t.Errorf("Resolve(%q) = %q, %v; want %s", in, got, err, sidDeck)
		}
	}
}

func TestResolve_Ambiguous(t *testing.T) {
	root := newProjects(t)
	other := "bbbbbbbb-9999-0000-0000-000000000009"
	put(t, root, "-Users-me-research", other, human("/Users/me/research", "a second b session"), reply("ok"))
	ix := indexedAt(t, root)
	_, err := ix.Resolve("bbbbbbbb")
	var amb *AmbiguousError
	if !errors.As(err, &amb) {
		t.Fatalf("Resolve(prefix of two) err = %v, want *AmbiguousError", err)
	}
	if len(amb.Candidates) != 2 {
		t.Errorf("candidates = %d, want 2", len(amb.Candidates))
	}
	if msg := err.Error(); !strings.Contains(msg, sidDeck) || !strings.Contains(msg, other) {
		t.Errorf("error %q should list both IDs", msg)
	}
}

func TestResolve_NoMatch(t *testing.T) {
	ix := indexedAt(t, newProjects(t))
	for _, in := range []string{"zzzz", ""} {
		if _, err := ix.Resolve(in); err == nil || !strings.Contains(err.Error(), "no session matches") {
			t.Errorf("Resolve(%q) err = %v, want 'no session matches'", in, err)
		}
	}
}

func TestResolve_AutomatedByID(t *testing.T) {
	ix := indexedAt(t, newProjects(t))
	if got, err := ix.Resolve(sidAuto); err != nil || got != sidAuto {
		t.Errorf("Resolve(automated) = %q, %v", got, err)
	}
}

func TestExchanges_Ordered(t *testing.T) {
	ix := indexedAt(t, newProjects(t))
	ex, err := ix.Exchanges(sidDeck)
	if err != nil || len(ex) != 1 {
		t.Fatalf("Exchanges = %v, %v; want 1 exchange", ex, err)
	}
	if ex[0].Seq != 0 || !strings.Contains(ex[0].Prompt, "hypotheses proposals") || !strings.Contains(ex[0].Prose, "summary slide") {
		t.Errorf("exchange = %+v", ex[0])
	}
}

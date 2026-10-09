package search

import (
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

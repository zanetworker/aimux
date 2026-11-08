package search

import (
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/history"
)

// The index stores the shown title: the .meta.json title (the owner's or
// aimux's namer's) first, then what the transcript says.
func TestExtractFile_TitleRule(t *testing.T) {
	meta := writeSession(t, humanFirst, aiTitle)
	if err := history.UpdateMeta(meta, func(m *history.Meta) { m.Title = "Owner title"; m.TitleBy = "owner" }); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, path, want string
	}{
		{"meta title", meta, "Owner title"},
		{"ai-title only", writeSession(t, humanFirst, aiTitle), "Openshell prerequisites"},
		{"first message only", writeSession(t, humanFirst, assistantTx), "why does openshell need service accounts"},
	}
	for _, c := range cases {
		doc, err := ExtractFile(c.path, DefaultExtractOpts())
		if err != nil {
			t.Fatal(err)
		}
		if doc.Title != c.want {
			t.Errorf("%s: Title = %q, want %q", c.name, doc.Title, c.want)
		}
	}
}

func recentTitle(t *testing.T, ix *Index, id string) string {
	t.Helper()
	d, err := ix.Detail(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	return d.Title
}

// A title written to (or removed from) .meta.json shows on the next Update
// although the transcript did not change.
func TestUpdate_FollowsMetaTitle(t *testing.T) {
	root := t.TempDir()
	path := put(t, root, "-Users-me-repo", sidAccounts, human("/Users/me/repo", "why does openshell need service accounts"), aiTitle)
	ix := openIndex(t)
	if _, err := ix.Update(root, DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}
	if got := recentTitle(t, ix, sidAccounts); got != "Openshell prerequisites" {
		t.Fatalf("before meta: %q", got)
	}

	if err := history.UpdateMeta(path, func(m *history.Meta) { m.Title = "Named by the owner" }); err != nil {
		t.Fatal(err)
	}
	st, err := ix.Update(root, DefaultExtractOpts())
	if err != nil {
		t.Fatal(err)
	}
	if got := recentTitle(t, ix, sidAccounts); got != "Named by the owner" {
		t.Errorf("after meta: %q", got)
	}
	if len(st.Changed) != 1 {
		t.Errorf("Changed = %v, want the one session", st.Changed)
	}
	rs, err := ix.Search("owner", SearchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 || rs[0].Title != "Named by the owner" {
		t.Errorf("search by the meta title: %+v", rs)
	}

	// nothing changed: nothing re-read
	if st, _ := ix.Update(root, DefaultExtractOpts()); st.Indexed != 0 {
		t.Errorf("unchanged Update re-read %d sessions", st.Indexed)
	}

	if err := history.RemoveMeta(path); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Update(root, DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}
	if got := recentTitle(t, ix, sidAccounts); got != "Openshell prerequisites" {
		t.Errorf("after meta removed: %q", got)
	}
}

// The index title is history.ShownTitle for every kind of session, so search
// hits and lists agree: renamed (with and without a namer or owner title),
// ai-titled, untitled with a long first message.
func TestUpdate_TitleIsHistoryShownTitle(t *testing.T) {
	root := t.TempDir()
	long := strings.Repeat("explain the cache eviction policy ", 6) // longer than 120
	user := func(text string) string {
		return `{"type":"user","entrypoint":"cli","cwd":"/Users/me/repo","message":{"role":"user","content":"` + text + `"}}`
	}
	rename := `{"type":"custom-title","customTitle":"cache-work"}`
	put(t, root, "-Users-me-repo", "s-renamed", user("rename me"), aiTitle, rename)
	namer := put(t, root, "-Users-me-repo", "s-renamed-namer", user("rename me too"), rename)
	owner := put(t, root, "-Users-me-repo", "s-renamed-owner", user("owner named"), rename)
	put(t, root, "-Users-me-repo", "s-ai", user("ai titled"), aiTitle)
	put(t, root, "-Users-me-repo", "s-long", user(long))
	if err := history.UpdateMeta(namer, func(m *history.Meta) { m.Title, m.TitleBy = "Namer title", "model" }); err != nil {
		t.Fatal(err)
	}
	if err := history.UpdateMeta(owner, func(m *history.Meta) { m.Title, m.TitleBy = "Owner title", "owner" }); err != nil {
		t.Fatal(err)
	}

	ix := openIndex(t)
	if _, err := ix.Update(root, DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}
	sessions, err := history.Discover(history.DiscoverOpts{}, root)
	if err != nil || len(sessions) != 5 {
		t.Fatalf("Discover: %v, %d sessions", err, len(sessions))
	}
	want := map[string]string{"s-renamed": "cache-work", "s-renamed-namer": "cache-work", "s-renamed-owner": "Owner title",
		"s-ai": "Openshell prerequisites"}
	for _, s := range sessions {
		got := recentTitle(t, ix, s.ID)
		if s.ID == "s-long" { // history's first-message form, not the index's own cut
			want[s.ID] = history.ShownTitle(s)
			if got == oneLine(long, 120) {
				t.Errorf("s-long: index kept its own first-message form %q", got)
			}
		}
		if got != history.ShownTitle(s) || got != want[s.ID] {
			t.Errorf("%s: index title %q, history.ShownTitle %q, want %q", s.ID, got, history.ShownTitle(s), want[s.ID])
		}
	}
}

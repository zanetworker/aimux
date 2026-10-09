package search

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	sidAccounts = "aaaaaaaa-0000-0000-0000-000000000001"
	sidDeck     = "bbbbbbbb-0000-0000-0000-000000000002"
	sidAuto     = "cccccccc-0000-0000-0000-000000000003"
	sidTitle    = "dddddddd-0000-0000-0000-000000000004"
)

func human(cwd, text string) string {
	return `{"type":"user","entrypoint":"cli","cwd":"` + cwd + `","message":{"content":"` + text + `"}}`
}

func reply(text string) string {
	return `{"type":"assistant","message":{"content":[{"type":"text","text":"` + text + `"}]}}`
}

// newProjects lays out a ~/.claude/projects-style tree and returns its root.
func newProjects(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	put(t, root, "-Users-me-OpenShell", sidAccounts,
		human("/Users/me/OpenShell", "why does openshell need service accounts"),
		reply("Service accounts give sandboxes short-lived tokens without tying identity to Kubernetes."))
	put(t, root, "-Users-me-research", sidDeck,
		human("/Users/me/research", "can I add the hypotheses proposals to Ying's deck"),
		reply("Yes, add a summary slide after the jobs section."))
	put(t, root, "-private-var-folders-T-tmp", sidAuto,
		`{"type":"user","entrypoint":"sdk-cli","cwd":"/private/var/folders/T/tmp","message":{"content":"YOU ARE A SESSION ANALYZER. service accounts mentioned here"}}`)
	put(t, root, "-Users-me-research", sidTitle,
		human("/Users/me/research", "unrelated opening question"),
		`{"type":"custom-title","customTitle":"service-accounts-design"}`,
		reply("nothing about identity in this body"))
	return root
}

func put(t *testing.T, root, project, sid string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(root, project)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func openIndex(t *testing.T) *Index {
	t.Helper()
	ix, err := Open(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = ix.Close() })
	return ix
}

func ids(rs []Result) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.SessionID)
	}
	return out
}

func TestIndex_SearchRanksAndFiltersAutomated(t *testing.T) {
	ix := openIndex(t)
	if _, err := ix.Update(newProjects(t), DefaultExtractOpts()); err != nil {
		t.Fatalf("Update: %v", err)
	}

	rs, err := ix.Search("service accounts", SearchOpts{Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	got := ids(rs)
	if len(got) != 2 {
		t.Fatalf("got %v, want the two human sessions mentioning service accounts", got)
	}
	// a title hit outranks a body-only hit
	if got[0] != sidTitle || got[1] != sidAccounts {
		t.Errorf("order = %v, want [%s %s]", got, sidTitle, sidAccounts)
	}
	for _, r := range rs {
		if r.SessionID == sidAuto {
			t.Error("automated session returned without IncludeAutomated")
		}
	}

	rs, _ = ix.Search("service accounts", SearchOpts{Limit: 10, IncludeAutomated: true})
	if len(rs) != 3 {
		t.Errorf("IncludeAutomated: got %v, want 3 sessions", ids(rs))
	}
}

func TestIndex_ResultCarriesMetadataAndSnippet(t *testing.T) {
	ix := openIndex(t)
	if _, err := ix.Update(newProjects(t), DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}
	rs, err := ix.Search("Ying deck", SearchOpts{Limit: 5})
	if err != nil || len(rs) != 1 {
		t.Fatalf("got %v err=%v, want one hit", ids(rs), err)
	}
	r := rs[0]
	if r.SessionID != sidDeck || r.CWD != "/Users/me/research" || r.Title == "" || r.ModTime.IsZero() {
		t.Errorf("metadata incomplete: %+v", r)
	}
	if !strings.Contains(strings.ToLower(r.Snippet), "deck") {
		t.Errorf("snippet %q does not show the match", r.Snippet)
	}
}

func TestIndex_PrefixAndFallbackToAnyTerm(t *testing.T) {
	ix := openIndex(t)
	if _, err := ix.Update(newProjects(t), DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}
	if rs, _ := ix.Search("hypoth", SearchOpts{Limit: 5}); len(rs) != 1 || rs[0].SessionID != sidDeck {
		t.Errorf("prefix search: got %v", ids(rs))
	}
	// no session has both words: fall back to any-term matching instead of nothing
	if rs, _ := ix.Search("kubernetes slide", SearchOpts{Limit: 5}); len(rs) != 2 {
		t.Errorf("any-term fallback: got %v, want 2", ids(rs))
	}
}

func TestIndex_IncrementalUpdate(t *testing.T) {
	root := newProjects(t)
	ix := openIndex(t)

	st, err := ix.Update(root, DefaultExtractOpts())
	if err != nil || st.Indexed != 4 {
		t.Fatalf("first update: %+v err=%v, want 4 indexed", st, err)
	}
	st, _ = ix.Update(root, DefaultExtractOpts())
	if st.Indexed != 0 || st.Removed != 0 {
		t.Errorf("unchanged tree re-indexed: %+v", st)
	}

	// change one session: it alone is re-read, and new text becomes searchable
	p := put(t, root, "-Users-me-research", sidDeck,
		human("/Users/me/research", "now about praxis grid coupling"))
	future := time.Now().Add(time.Minute)
	_ = os.Chtimes(p, future, future)
	st, _ = ix.Update(root, DefaultExtractOpts())
	if st.Indexed != 1 {
		t.Errorf("after one change: %+v, want 1 indexed", st)
	}
	if rs, _ := ix.Search("praxis", SearchOpts{Limit: 5}); len(rs) != 1 {
		t.Errorf("changed text not searchable: %v", ids(rs))
	}
	if rs, _ := ix.Search("Ying", SearchOpts{Limit: 5}); len(rs) != 0 {
		t.Errorf("stale text still searchable: %v", ids(rs))
	}

	// delete one session: it disappears from results
	if err := os.Remove(filepath.Join(root, "-Users-me-OpenShell", sidAccounts+".jsonl")); err != nil {
		t.Fatal(err)
	}
	st, _ = ix.Update(root, DefaultExtractOpts())
	if st.Removed != 1 {
		t.Errorf("after delete: %+v, want 1 removed", st)
	}
	for _, id := range ids(mustSearch(t, ix, "kubernetes")) {
		if id == sidAccounts {
			t.Error("deleted session still returned")
		}
	}
}

func TestIndex_PersistsAcrossReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "search.db")
	root := newProjects(t)
	ix, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Update(root, DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}
	_ = ix.Close()

	ix2, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ix2.Close() }()
	if rs := mustSearch(t, ix2, "hypotheses"); len(rs) != 1 {
		t.Errorf("after reopen: %v", ids(rs))
	}
	if st, _ := ix2.Update(root, DefaultExtractOpts()); st.Indexed != 0 {
		t.Errorf("reopened index re-read unchanged files: %+v", st)
	}
}

func TestIndex_EdgeQueries(t *testing.T) {
	ix := openIndex(t)
	if _, err := ix.Update(newProjects(t), DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}
	if rs, err := ix.Search("   ", SearchOpts{Limit: 5}); err != nil || len(rs) != 0 {
		t.Errorf("blank query: %v err=%v", ids(rs), err)
	}
	// FTS5 syntax characters in user input must not error
	for _, q := range []string{`deck"`, `(Ying`, `service-accounts`, `NOT`, `a*b:c^`} {
		if _, err := ix.Search(q, SearchOpts{Limit: 5}); err != nil {
			t.Errorf("query %q: %v", q, err)
		}
	}
	if rs := mustSearch(t, ix, "service"); len(rs) > 0 {
		if rs2, _ := ix.Search("service", SearchOpts{Limit: 1}); len(rs2) != 1 {
			t.Errorf("Limit 1 returned %d", len(rs2))
		}
	}
}

func TestIndex_MissingProjectsDir(t *testing.T) {
	ix := openIndex(t)
	st, err := ix.Update(filepath.Join(t.TempDir(), "nope"), DefaultExtractOpts())
	if err != nil || st.Indexed != 0 {
		t.Errorf("missing dir: %+v err=%v, want empty and no error", st, err)
	}
}

func mustSearch(t *testing.T, ix *Index, q string) []Result {
	t.Helper()
	rs, err := ix.Search(q, SearchOpts{Limit: 20})
	if err != nil {
		t.Fatalf("Search(%q): %v", q, err)
	}
	return rs
}

func TestQueryTerms_DropsNoiseTokens(t *testing.T) {
	got := strings.Join(queryTerms("where we compared IBM's agent builder with our platform"), " ")
	want := `"compared"* "IBM"* "agent"* "builder"* "platform"*`
	if got != want {
		t.Errorf("queryTerms = %s\nwant          %s", got, want)
	}
	// a query made only of stopwords keeps them, rather than searching for nothing
	if got := queryTerms("how to"); len(got) != 2 {
		t.Errorf("all-stopword query: %v, want both words kept", got)
	}
	// single letters and digits never become prefix terms; two-letter acronyms do
	if got := strings.Join(queryTerms("a b 7 AI s"), " "); got != `"AI"*` {
		t.Errorf("short tokens: %s", got)
	}
}

func TestIndex_AllTermHitsOnlyNoBackfill(t *testing.T) {
	ix := openIndex(t)
	if _, err := ix.Update(newProjects(t), DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}
	// only sidAccounts has both words; sessions with just one of them must not pad the list
	rs, err := ix.Search("service kubernetes", SearchOpts{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(rs); len(got) != 1 || got[0] != sidAccounts {
		t.Errorf("got %v, want only %s", got, sidAccounts)
	}
}

func TestIndex_AnyTermFallbackIsCapped(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 30; i++ {
		put(t, root, "-p", fmt.Sprintf("%08d-0000-0000-0000-000000000000", i), human("/p", fmt.Sprintf("alpha note %d", i)))
	}
	put(t, root, "-p", "99999999-0000-0000-0000-000000000000", human("/p", "beta only"))
	ix := openIndex(t)
	if _, err := ix.Update(root, DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}
	// nothing has both words, so any-term matches are shown, but at most AnyTermCap
	rs, _ := ix.Search("alpha beta", SearchOpts{Limit: 100})
	if len(rs) == 0 || len(rs) > AnyTermCap {
		t.Errorf("fallback returned %d results, want 1..%d", len(rs), AnyTermCap)
	}
}

func TestOpen_ResetsOutdatedSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "search.db")
	ix, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// simulate an index written by an older layout
	if _, err := ix.db.Exec(`DROP TABLE chunks; CREATE TABLE chunks (x); PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	_ = ix.Close()

	ix, err = Open(path)
	if err != nil {
		t.Fatalf("reopen outdated index: %v", err)
	}
	defer func() { _ = ix.Close() }()
	if _, err := ix.Update(newProjects(t), DefaultExtractOpts()); err != nil {
		t.Fatalf("update after reset: %v", err)
	}
	if rs := mustSearch(t, ix, "hypotheses"); len(rs) != 1 {
		t.Errorf("after reset: %v", ids(rs))
	}
}

func TestSearch_PrefixDoesNotOvermatchViaStemming(t *testing.T) {
	root := t.TempDir()
	put(t, root, "-p", "eeeeeeee-0000-0000-0000-000000000005", human("/p", "open the openclaw diagram"))
	put(t, root, "-p", "ffffffff-0000-0000-0000-000000000006", human("/p", "agent ops dashboard for AgentOps"))
	ix := openIndex(t)
	if _, err := ix.Update(root, DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}
	rs := mustSearch(t, ix, "ops")
	if len(rs) != 1 || rs[0].SessionID != "ffffffff-0000-0000-0000-000000000006" {
		t.Errorf(`"ops" matched %v; it must not match "open"`, ids(rs))
	}
}

func TestSearch_ExactTitleThenAllTermTitlesFirst(t *testing.T) {
	root := t.TempDir()
	heavy := strings.Repeat("agentic api jira ", 40)
	// body-heavy session whose title also has every word: BM25 favours it
	put(t, root, "-p", "11111111-0000-0000-0000-00000000000a",
		human("/p", heavy), `{"type":"custom-title","customTitle":"Agentic API Jira notes"}`)
	// the session the user named; its body never repeats the words
	put(t, root, "-p", "22222222-0000-0000-0000-00000000000b",
		human("/p", "clean up what we have for the release"), `{"type":"custom-title","customTitle":"agentic-api-3.7-jira"}`)
	// matches every word in the body only
	put(t, root, "-p", "33333333-0000-0000-0000-00000000000c",
		human("/p", heavy+heavy), `{"type":"custom-title","customTitle":"unrelated title"}`)
	ix := openIndex(t)
	if _, err := ix.Update(root, DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}
	got := ids(mustSearch(t, ix, "agentic-api-3.7-jira"))
	want := []string{"22222222-0000-0000-0000-00000000000b", "11111111-0000-0000-0000-00000000000a", "33333333-0000-0000-0000-00000000000c"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v\nwant    %v (exact title, then all-term titles, then body)", got, want)
	}
	// case and punctuation don't matter for the exact-title rule
	if got := ids(mustSearch(t, ix, "Agentic API 3.7 Jira")); len(got) == 0 || got[0] != want[0] {
		t.Errorf("normalized exact title not first: %v", got)
	}
}

func TestSearch_LongQueriesNeedMostTermsNotAll(t *testing.T) {
	root := t.TempDir()
	put(t, root, "-p", "aaaaaaaa-1111-0000-0000-000000000001",
		human("/p", "is maas ga with the praxis backend in this release")) // 4 of 6 terms, no "uses"/"but"
	put(t, root, "-p", "bbbbbbbb-1111-0000-0000-000000000002",
		human("/p", "praxis praxis praxis praxis praxis notes")) // 1 of 6, repeated
	put(t, root, "-p", "cccccccc-1111-0000-0000-000000000003",
		human("/p", "praxis backend praxis backend praxis backend ga ga")) // 3 of 6, repeated
	ix := openIndex(t)
	if _, err := ix.Update(root, DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}
	got := ids(mustSearch(t, ix, "MaaS is GA but uses the praxis backend"))
	// terms: maas ga but uses praxis backend -> 6, at least 4 must match
	if len(got) != 1 || got[0] != "aaaaaaaa-1111-0000-0000-000000000001" {
		t.Errorf("got %v, want only the session covering 4 of 6 terms", got)
	}
}

func TestSearch_CoverageBeatsRepetition(t *testing.T) {
	root := t.TempDir()
	put(t, root, "-p", "aaaaaaaa-2222-0000-0000-000000000001",
		human("/p", "keycloak registration race in token exchange"))
	put(t, root, "-p", "bbbbbbbb-2222-0000-0000-000000000002",
		human("/p", strings.Repeat("keycloak token exchange ", 30)))
	ix := openIndex(t)
	if _, err := ix.Update(root, DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}
	got := ids(mustSearch(t, ix, "keycloak registration race token exchange"))
	if len(got) == 0 || got[0] != "aaaaaaaa-2222-0000-0000-000000000001" {
		t.Errorf("got %v: the chunk covering all 5 terms must beat one repeating 3", got)
	}
}

func TestSearch_QuotedPhraseIsExact(t *testing.T) {
	root := t.TempDir()
	put(t, root, "-p", "aaaaaaaa-3333-0000-0000-000000000001", human("/p", "the token exchange fails behind keycloak"))
	put(t, root, "-p", "bbbbbbbb-3333-0000-0000-000000000002", human("/p", "exchange the old token for a new one"))
	put(t, root, "-p", "cccccccc-3333-0000-0000-000000000003", human("/p", "token exchange with service accounts on kubernetes"))
	ix := openIndex(t)
	if _, err := ix.Update(root, DefaultExtractOpts()); err != nil {
		t.Fatal(err)
	}

	got := ids(mustSearch(t, ix, `"token exchange"`))
	if len(got) != 2 || strings.Contains(strings.Join(got, ","), "bbbbbbbb") {
		t.Errorf(`"token exchange" = %v, want the two sessions with the exact phrase`, got)
	}
	// phrase plus a free word: phrase required, word must match too
	if got := ids(mustSearch(t, ix, `"token exchange" kubernetes`)); len(got) != 1 || got[0] != "cccccccc-3333-0000-0000-000000000003" {
		t.Errorf("phrase + word = %v", got)
	}
	// unclosed quote while typing still means a phrase
	if got := ids(mustSearch(t, ix, `"token exch`)); len(got) != 2 {
		t.Errorf("unclosed quote = %v, want 2", got)
	}
	// a phrase nobody wrote finds nothing, rather than falling back to loose matches
	if got := ids(mustSearch(t, ix, `"exchange token"`)); len(got) != 0 {
		t.Errorf("reversed phrase = %v, want none", got)
	}
}

func TestHybrid_QuotedQueryStaysExact(t *testing.T) {
	ix, _ := indexed(t)
	e := &conceptEmbedder{}
	if _, err := ix.EmbedMissing(context.Background(), e, 10); err != nil {
		t.Fatal(err)
	}
	// "presentation" would pull the deck session in semantically; quotes forbid that
	rs, used, err := ix.Hybrid(context.Background(), `"presentation"`, SearchOpts{Limit: 5}, e)
	if err != nil || used || len(rs) != 0 {
		t.Errorf("quoted query: used=%v got=%v err=%v, want exact keyword only (none)", used, ids(rs), err)
	}
}

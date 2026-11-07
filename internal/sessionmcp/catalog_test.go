package sessionmcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/catalog"
	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/harness"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/trace"
)

const sidAnalyzer = "dddddddd-0000-0000-0000-000000000004"

// catalogStore is a catalog.Store over the fixture's transcripts with fixed
// history records: the deck session carries an owner's .meta.json title.
type catalogStore struct {
	dir      string
	sessions []history.Session
	scans    int
}

func (s *catalogStore) Name() string                                { return "claude" }
func (s *catalogStore) SessionFile(id string) string                { return discovery.FindSessionFile(id, s.dir) }
func (s *catalogStore) SessionCwd(string) (string, error)           { return "", nil }
func (s *catalogStore) Fingerprint(string) string                   { return "" }
func (s *catalogStore) ParseTail(string, int) ([]trace.Turn, error) { return nil, nil }
// Reread reads a record with a file again (history.Reread); a stub record
// without one comes back as it is.
func (s *catalogStore) Reread(r history.Session) (history.Session, error) {
	if r.FilePath == "" {
		return r, nil
	}
	return history.Reread(r)
}

func (s *catalogStore) Sessions(history.DiscoverOpts) ([]history.Session, error) {
	s.scans++
	return s.sessions, nil
}

type holders []harness.Holder

func (h holders) Holders(harness.Mode) ([]harness.Holder, error) { return h, nil }

// withCatalog is the fixture's server with a catalog: the shell session is
// open in a terminal, and an analyzer session exists only in the history.
func withCatalog(t *testing.T) (*Server, *catalogStore) {
	t.Helper()
	s, dir := newFixture(t)
	now := time.Now()
	st := &catalogStore{dir: dir, sessions: []history.Session{
		{ID: sidDeck, Title: "Owner deck title", TitleBy: "owner", FirstPrompt: "add the hypotheses slide", Cwd: "/Users/me/research", LastActive: now.Add(-time.Minute)},
		{ID: sidShell, FirstPrompt: "why does the sandbox need service accounts", Cwd: "/Users/me/OpenShell", LastActive: now.Add(-2 * time.Minute)},
		{ID: sidAuto, Automated: true, FirstPrompt: "nightly job", Cwd: "/private/var/folders/T/tmp", LastActive: now.Add(-3 * time.Minute)},
		{ID: sidAnalyzer, FirstPrompt: "YOU ARE A SESSION ANALYZER", Cwd: "/Users/me/research", LastActive: now.Add(-4 * time.Minute)},
	}}
	cat := &catalog.Catalog{Providers: []catalog.Provider{{Store: st, Live: holders{{PID: 7, SessionID: sidShell}}}}}
	return s.WithCatalog(cat), st
}

func lineOf(out, id string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, id) {
			return l
		}
	}
	return ""
}

func TestList_CatalogTitlesProviderAndState(t *testing.T) {
	s, _ := withCatalog(t)
	out, isErr := call(t, s.handleList, map[string]any{})
	if isErr {
		t.Fatal(out)
	}
	deck := lineOf(out, sidDeck)
	if !strings.HasPrefix(deck, "- `"+sidDeck+"` ") || !strings.Contains(deck, "· **Owner deck title** · /Users/me/research · claude · idle") {
		t.Errorf("deck line %q", deck)
	}
	if shell := lineOf(out, sidShell); !strings.HasSuffix(shell, "· claude · open_in_terminal") {
		t.Errorf("shell line %q", shell)
	}
	if strings.Contains(out, sidAuto) || strings.Contains(out, sidAnalyzer) {
		t.Errorf("automated or analyzer shown by default:\n%s", out)
	}
	if strings.Index(out, sidDeck) > strings.Index(out, sidShell) {
		t.Errorf("not newest first:\n%s", out)
	}
}

func TestList_CatalogIncludeAutomated(t *testing.T) {
	s, _ := withCatalog(t)
	out, _ := call(t, s.handleList, map[string]any{"include_automated": true})
	if !strings.Contains(out, sidAuto) || !strings.Contains(out, sidAnalyzer) {
		t.Errorf("include_automated should show automated and analyzer sessions:\n%s", out)
	}
}

func TestList_CatalogFiltersAndLimit(t *testing.T) {
	s, st := withCatalog(t)
	st.sessions[1].LastActive = time.Now().AddDate(0, 0, -40)
	if out, _ := call(t, s.handleList, map[string]any{}); strings.Contains(out, sidShell) {
		t.Errorf("default 30 days kept a 40-day-old session:\n%s", out)
	}
	if out, _ := call(t, s.handleList, map[string]any{"days": 60}); !strings.Contains(out, sidShell) {
		t.Errorf("days=60:\n%s", out)
	}
	out, _ := call(t, s.handleList, map[string]any{"project_filter": "/Users/me/OpenShell/", "days": 60})
	if strings.Contains(out, sidDeck) || !strings.Contains(out, sidShell) {
		t.Errorf("project_filter:\n%s", out)
	}
	if out, _ := call(t, s.handleList, map[string]any{"project_filter": "/Users/me/Open", "days": 60}); out != "No sessions found." {
		t.Errorf("a name prefix is not a directory:\n%s", out)
	}
	if out, _ := call(t, s.handleList, map[string]any{"limit": 1}); strings.Count(out, "\n") != 0 || !strings.Contains(out, sidDeck) {
		t.Errorf("limit 1:\n%s", out)
	}
}

// Without a catalog the list is the index's, in the old format.
func TestList_NilCatalogKeepsTheIndexFormat(t *testing.T) {
	s, _ := newFixture(t)
	out, _ := call(t, s.handleList, map[string]any{})
	if deck := lineOf(out, sidDeck); deck == "" || strings.Contains(deck, "· claude") || !strings.HasSuffix(deck, "· /Users/me/research") {
		t.Errorf("deck line %q", deck)
	}
}

func TestGet_CatalogTitleProviderState(t *testing.T) {
	s, _ := withCatalog(t)
	out, isErr := call(t, s.handleGet, map[string]any{"session_id": "aaaaaaaa-0000"})
	if isErr {
		t.Fatal(out)
	}
	for _, want := range []string{"# Session " + sidDeck, "- **Title:** Owner deck title\n", "- **Provider:** claude\n", "- **State:** idle\n", "- **Exchanges:** 1\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if out, _ := call(t, s.handleGet, map[string]any{"session_id": sidShell}); !strings.Contains(out, "- **State:** open_in_terminal\n") {
		t.Errorf("shell state:\n%s", out)
	}
	// get_session still finds automated sessions; continue_session is unchanged.
	if out, isErr := call(t, s.handleGet, map[string]any{"session_id": sidAuto}); isErr || !strings.Contains(out, "automated") {
		t.Errorf("automated by id: %v\n%s", isErr, out)
	}
	if out, _ := call(t, s.handleContinue, map[string]any{"session_id": sidDeck}); strings.Contains(out, "**Provider:**") || strings.Contains(out, "Owner deck title") {
		t.Errorf("continue_session changed:\n%s", out)
	}
}

func TestGet_NilCatalogHasNoProviderOrState(t *testing.T) {
	s, _ := newFixture(t)
	out, _ := call(t, s.handleGet, map[string]any{"session_id": sidDeck})
	if strings.Contains(out, "**Provider:**") || strings.Contains(out, "**State:**") {
		t.Errorf("nil catalog added fields:\n%s", out)
	}
}

// A session the catalog does not know keeps the index's title and gets no
// provider or state line.
func TestGet_CatalogUnknownSessionKeepsIndexTitle(t *testing.T) {
	s, st := withCatalog(t)
	st.sessions = nil
	out, _ := call(t, s.handleGet, map[string]any{"session_id": sidDeck})
	if strings.Contains(out, "**Provider:**") || strings.Contains(out, "Owner deck title") {
		t.Errorf("unknown to the catalog:\n%s", out)
	}
}

// History times are often UTC; the list prints local time, as the index's
// list and get_session do.
func TestFormatEntries_LocalTime(t *testing.T) {
	at := time.Date(2026, 10, 10, 9, 5, 0, 0, time.UTC)
	out := formatEntries([]catalog.Entry{{ID: "x", Title: "t", Cwd: "/w", LastActive: at, Provider: "claude", State: "idle"}})
	if want := at.Local().Format("2006-01-02 15:04"); !strings.Contains(out, " "+want+" ") {
		t.Errorf("got %q, want local time %s", out, want)
	}
	if formatEntries(nil) != "No sessions found." {
		t.Error("empty list")
	}
}

// A session only in aimux's archive (Claude Code cleaned its transcript up)
// is unknown to the catalog; the list keeps it from the index, marked
// archived, under the same filters. A session both know is listed once.
func TestList_CatalogKeepsArchivedSessions(t *testing.T) {
	s, _ := withCatalog(t)
	archive := t.TempDir()
	const sidArchived = "eeeeeeee-0000-0000-0000-000000000005"
	writeSession(t, archive, "-Users-me-old", sidArchived, userLine("/Users/me/old", "archived work"), replyLine("ok"))
	s.svc.ArchiveDirs = []string{archive}
	out, isErr := call(t, s.handleList, map[string]any{})
	if isErr {
		t.Fatal(out)
	}
	if l := lineOf(out, sidArchived); !strings.HasSuffix(l, "· /Users/me/old · unknown · archived") {
		t.Errorf("archived line %q in\n%s", l, out)
	}
	if strings.Count(out, sidDeck) != 1 {
		t.Errorf("deck listed %d times", strings.Count(out, sidDeck))
	}
	if out, _ := call(t, s.handleList, map[string]any{"include_automated": true}); strings.Count(out, sidAuto) != 1 || strings.Contains(lineOf(out, sidAuto), "archived") {
		t.Errorf("automated session known to the catalog must not repeat as archived:\n%s", out)
	}
	if out, _ := call(t, s.handleList, map[string]any{"project_filter": "/Users/me/research"}); strings.Contains(out, sidArchived) {
		t.Errorf("project_filter kept the archived session:\n%s", out)
	}
	old := time.Now().AddDate(0, 0, -40)
	if err := os.Chtimes(filepath.Join(archive, "-Users-me-old", sidArchived+".jsonl"), old, old); err != nil {
		t.Fatal(err)
	}
	if out, _ := call(t, s.handleList, map[string]any{}); strings.Contains(out, sidArchived) {
		t.Errorf("days kept a 40-day-old archived session:\n%s", out)
	}
	if out, _ := call(t, s.handleList, map[string]any{"days": 60, "limit": 2}); strings.Count(out, "\n")+1 != 2 {
		t.Errorf("limit 2 over catalog and archive:\n%s", out)
	}
}

// When the index cannot be read, the catalog rows still list and the
// output says the archive is missing.
func TestList_CatalogIndexUnavailable(t *testing.T) {
	s, _ := withCatalog(t)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s.svc.DBPath = filepath.Join(blocker, "search.db") // a file in the way of its folder
	out, isErr := call(t, s.handleList, map[string]any{})
	if isErr || !strings.Contains(out, sidDeck) || !strings.Contains(out, "archived sessions not listed") {
		t.Errorf("isErr %v\n%s", isErr, out)
	}
}

// An undated session takes its transcript's modified time; one without a
// file either is not listed (it would print year 1).
func TestList_CatalogUndatedSessions(t *testing.T) {
	s, st := withCatalog(t)
	st.sessions[0].LastActive = time.Time{} // deck: has a file
	st.sessions = append(st.sessions, history.Session{ID: "ffffffff-0000-0000-0000-000000000006", Cwd: "/Users/me/x"})
	out, _ := call(t, s.handleList, map[string]any{"days": 0})
	if strings.Contains(out, "0001-01-01") || strings.Contains(out, "ffffffff") {
		t.Errorf("undated rows:\n%s", out)
	}
	if !strings.Contains(out, sidDeck) {
		t.Errorf("deck dropped instead of dated by its file:\n%s", out)
	}
}

// limit 0 or below is the default cap, 200, never uncapped.
func TestList_CatalogLimitDefault(t *testing.T) {
	s, st := withCatalog(t)
	now := time.Now()
	for i := 0; i < 205; i++ {
		st.sessions = append(st.sessions, history.Session{ID: fmt.Sprintf("99999999-0000-0000-0000-%012d", i), Cwd: "/Users/me/many", LastActive: now.Add(-time.Duration(i+10) * time.Minute)})
	}
	for _, limit := range []int{0, -1} {
		out, _ := call(t, s.handleList, map[string]any{"limit": limit})
		if n := strings.Count(out, "\n") + 1; n != 200 {
			t.Errorf("limit %d: %d rows, want 200", limit, n)
		}
	}
}

// get_session reads the catalog's last scan; only a session it has not
// seen costs a scan.
func TestGet_CatalogUsesTheLastScan(t *testing.T) {
	s, st := withCatalog(t)
	for i := 0; i < 2; i++ {
		if out, _ := call(t, s.handleGet, map[string]any{"session_id": sidDeck}); !strings.Contains(out, "- **Provider:** claude\n") {
			t.Fatalf("call %d:\n%s", i, out)
		}
	}
	if st.scans != 1 {
		t.Fatalf("%d scans for two get_session calls, want 1", st.scans)
	}
	if _, _ = call(t, s.handleList, map[string]any{}); st.scans != 2 {
		t.Fatalf("list scans: %d", st.scans)
	}
	if out, _ := call(t, s.handleGet, map[string]any{"session_id": sidShell}); st.scans != 2 || !strings.Contains(out, "- **State:** open_in_terminal\n") {
		t.Fatalf("get after a list: scans %d\n%s", st.scans, out)
	}
}

// The header title is the whole shown title, not the list's 80-byte form.
func TestGet_CatalogTitleIsNotCut(t *testing.T) {
	s, st := withCatalog(t)
	long := strings.Repeat("a long first message ", 10)
	st.sessions[1].FirstPrompt = long // shell: no written title
	out, _ := call(t, s.handleGet, map[string]any{"session_id": sidShell})
	if !strings.Contains(out, "- **Title:** "+long+"\n") {
		t.Errorf("title cut:\n%s", out)
	}
}

func TestListToolDescribesProviderAndState(t *testing.T) {
	d := listTool().Description
	for _, want := range []string{"provider", "state", "open_in_terminal", "remote adapter", "archived"} {
		if !strings.Contains(d, want) {
			t.Errorf("list_sessions description lacks %q: %s", want, d)
		}
	}
}

// Index rows the catalog knows (here analyzer sessions, which the catalog
// hides and the index does not, newest in the index) must not use up the index's limit before the older
// archive-only rows are reached.
func TestList_CatalogArchiveRowsBehindKnownOnes(t *testing.T) {
	s, st := withCatalog(t)
	dir := s.svc.ProjectsDir
	soon := time.Now().Add(time.Minute)
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("dddddddd-1111-0000-0000-%012d", i)
		p := writeSession(t, dir, "-Users-me-hooks", id, userLine("/Users/me/hooks", "please act as a session analyzer"), replyLine("ok"))
		if err := os.Chtimes(p, soon, soon); err != nil {
			t.Fatal(err)
		}
		st.sessions = append(st.sessions, history.Session{ID: id, FirstPrompt: "please act as a session analyzer", Cwd: "/Users/me/hooks", LastActive: soon})
	}
	archive := t.TempDir()
	s.svc.ArchiveDirs = []string{archive}
	hourAgo := time.Now().Add(-time.Hour)
	var archived []string
	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("eeeeeeee-1111-0000-0000-%012d", i)
		p := writeSession(t, archive, "-Users-me-old", id, userLine("/Users/me/old", "archived work"), replyLine("ok"))
		if err := os.Chtimes(p, hourAgo, hourAgo); err != nil {
			t.Fatal(err)
		}
		archived = append(archived, id)
	}
	out, _ := call(t, s.handleList, map[string]any{"limit": 4})
	for _, id := range archived {
		if !strings.Contains(out, id) {
			t.Errorf("archive-only %s missing:\n%s", id, out)
		}
	}
	if strings.Contains(out, "dddddddd-1111") || strings.Count(out, "\n")+1 != 4 {
		t.Errorf("want deck, shell and the two archived rows:\n%s", out)
	}
}

// Archive-only rows follow the same visibility rule (history.MCPOpts): an
// analyzer session is hidden by default and shown with include_automated.
func TestList_CatalogArchivedAnalyzerFollowsMCPOpts(t *testing.T) {
	s, _ := withCatalog(t)
	archive := t.TempDir()
	const sidOldAnalyzer = "eeeeeeee-2222-0000-0000-000000000001"
	writeSession(t, archive, "-Users-me-old", sidOldAnalyzer, userLine("/Users/me/old", "please act as a session analyzer"), replyLine("ok"))
	s.svc.ArchiveDirs = []string{archive}
	if out, _ := call(t, s.handleList, map[string]any{}); strings.Contains(out, sidOldAnalyzer) {
		t.Errorf("archived analyzer listed by default:\n%s", out)
	}
	if out, _ := call(t, s.handleList, map[string]any{"include_automated": true}); !strings.Contains(lineOf(out, sidOldAnalyzer), "· unknown · archived") {
		t.Errorf("include_automated should list the archived analyzer:\n%s", out)
	}
}

// archiveRows writes n archive-only sessions with prompt text, newest first,
// starting at age.
func archiveRows(t *testing.T, s *Server, prefix, text string, n int, age time.Duration) []string {
	t.Helper()
	if len(s.svc.ArchiveDirs) == 0 {
		s.svc.ArchiveDirs = []string{t.TempDir()}
	}
	var ids []string
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%s-3333-0000-0000-%012d", prefix, i)
		p := writeSession(t, s.svc.ArchiveDirs[0], "-Users-me-old", id, userLine("/Users/me/old", text), replyLine("ok"))
		at := time.Now().Add(-age - time.Duration(i)*time.Second)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// Hidden archive-only rows (analyzers) newer than the visible archived
// ones must not use up the index reads: the visible ones still list.
func TestList_CatalogHiddenArchiveRowsAhead(t *testing.T) {
	s, _ := withCatalog(t)
	s.archivePageSize = 2
	archiveRows(t, s, "abababab", "please act as a session analyzer", 5, time.Minute)
	visible := archiveRows(t, s, "cdcdcdcd", "archived work", 2, time.Hour)
	out, _ := call(t, s.handleList, map[string]any{"limit": 4})
	for _, id := range visible {
		if !strings.Contains(out, id) {
			t.Errorf("visible archived %s missing:\n%s", id, out)
		}
	}
	if strings.Contains(out, "abababab") || strings.Contains(out, "stopped after") {
		t.Errorf("hidden rows or a cap note:\n%s", out)
	}
}

// The page cap stops the archive scan and the output says so.
func TestList_CatalogArchivePageCap(t *testing.T) {
	s, _ := withCatalog(t)
	s.archivePageSize, s.archiveMaxPages = 1, 2
	archiveRows(t, s, "abababab", "please act as a session analyzer", 5, time.Minute)
	archiveRows(t, s, "cdcdcdcd", "archived work", 1, time.Hour)
	out, _ := call(t, s.handleList, map[string]any{})
	if !strings.Contains(out, sidDeck) || !strings.Contains(out, "stopped after 2 pages") || strings.Contains(out, "cdcdcdcd") {
		t.Errorf("want the catalog rows and a cap note:\n%s", out)
	}
}

// get_session shows a title changed after the last list (a /rename in the
// transcript, then the owner's .meta.json title) without a full scan.
func TestGet_CatalogSeesRenamesWithoutAScan(t *testing.T) {
	s, st := withCatalog(t)
	deckFile := discovery.FindSessionFile(sidDeck, st.dir)
	st.sessions[0].FilePath = deckFile
	st.sessions[0].Title, st.sessions[0].TitleBy = "", ""
	if _, isErr := call(t, s.handleList, map[string]any{}); isErr {
		t.Fatal("list failed")
	}
	f, err := os.OpenFile(deckFile, os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- the test's temp file
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"custom-title","customTitle":"slash-renamed"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if out, _ := call(t, s.handleGet, map[string]any{"session_id": sidDeck}); !strings.Contains(out, "- **Title:** slash-renamed\n") {
		t.Errorf("after /rename:\n%s", out)
	}
	if err := history.UpdateMeta(deckFile, func(m *history.Meta) { m.Title, m.TitleBy = "Meta renamed", "owner" }); err != nil {
		t.Fatal(err)
	}
	if out, _ := call(t, s.handleGet, map[string]any{"session_id": sidDeck}); !strings.Contains(out, "- **Title:** Meta renamed\n") {
		t.Errorf("after the meta title:\n%s", out)
	}
	if st.scans != 1 {
		t.Errorf("%d full scans, want only the list's", st.scans)
	}
}

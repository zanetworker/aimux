package catalog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/harness"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/provider"
	"github.com/zanetworker/aimux/internal/trace"
)

// Claude's store is what remote serve and mcp serve register.
var _ Store = (*provider.Claude)(nil)

// stubStore is a Store over a fixed session list; SessionFile finds files
// under dir.
type stubStore struct {
	name     string
	dir      string
	sessions []history.Session
	err      error
	scans    int
}

func (s *stubStore) Name() string                                { return s.name }
func (s *stubStore) SessionFile(id string) string                { return discovery.FindSessionFile(id, s.dir) }
func (s *stubStore) SessionCwd(string) (string, error)           { return "", nil }
func (s *stubStore) Fingerprint(string) string                   { return "" }
func (s *stubStore) ParseTail(string, int) ([]trace.Turn, error) { return nil, nil }
func (s *stubStore) Sessions(history.DiscoverOpts) ([]history.Session, error) {
	s.scans++
	return s.sessions, s.err
}

type fixedLive []harness.Holder

func (l fixedLive) Holders(harness.Mode) ([]harness.Holder, error) { return l, nil }

type busySet map[string]bool

func (b busySet) Busy(id string) bool { return b[id] }

// writeFiles creates an empty transcript per id under dir/-w.
func writeFiles(t *testing.T, dir string, ids ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "-w"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if err := os.WriteFile(filepath.Join(dir, "-w", id+".jsonl"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// fixture: idle (owner-titled), open (in a terminal, registry busy), busy
// (aimux runs a turn), a subagent, an automated and an analyzer session, a
// record of another provider, and one held by aimux's own adapter.
func fixture(t *testing.T) (*Catalog, *stubStore) {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, "idle", "open", "busy", "mine")
	now := time.Now()
	st := &stubStore{name: "claude", dir: dir, sessions: []history.Session{
		{ID: "idle", Title: "Owner title", TitleBy: "owner", AITitle: "ai", Project: "/w", Cwd: "/w/app", LastActive: now.Add(-1 * time.Minute)},
		{ID: "open", FirstPrompt: "fix the [Image #1] cache", LastActive: now.Add(-2 * time.Minute)},
		{ID: "busy", Provider: "claude", AITitle: "Busy one", LastActive: now.Add(-3 * time.Minute)},
		{ID: "sub", IsSubagent: true, LastActive: now},
		{ID: "cron", Automated: true, LastActive: now},
		{ID: "hook", FirstPrompt: "YOU ARE A SESSION ANALYZER", LastActive: now},
		{ID: "cx", Provider: "codex", LastActive: now},
		{ID: "mine", LastActive: now.Add(-4 * time.Minute)},
	}}
	live := fixedLive{{PID: 10, SessionID: "open", Status: "busy"}, {PID: 11, SessionID: "mine", Status: "idle"}}
	return &Catalog{
		Providers: []Provider{{Store: st, Live: live}},
		Busy:      busySet{"busy": true},
		Owned:     func(pid int) bool { return pid == 11 },
	}, st
}

func ids(es []Entry) string {
	var out []string
	for _, e := range es {
		out = append(out, e.ID)
	}
	return strings.Join(out, " ")
}

func TestList_RemoteRuleNewestFirstWithStateAndTitle(t *testing.T) {
	c, _ := fixture(t)
	got, err := c.List(history.RemoteOpts(false, false))
	if err != nil {
		t.Fatal(err)
	}
	// hook is not hidden by the remote rule; cx is another provider's.
	if g := ids(got); g != "hook idle open busy mine" {
		t.Fatalf("ids %q", g)
	}
	by := map[string]Entry{}
	for _, e := range got {
		by[e.ID] = e
	}
	if e := by["idle"]; e.Title != "Owner title" || e.Provider != "claude" || e.State != StateIdle || e.Cwd != "/w/app" || e.Project != "/w" || e.Record == nil {
		t.Errorf("idle %+v", e)
	}
	if e := by["open"]; e.State != StateOpenInTerminal || !e.Working || e.Title != history.DisplayTitle(*e.Record) {
		t.Errorf("open %+v", e)
	}
	if e := by["busy"]; e.State != StateBusy || e.Working || e.Title != "Busy one" {
		t.Errorf("busy %+v", e)
	}
	if e := by["mine"]; e.State != StateIdle {
		t.Errorf("a session aimux's own adapter holds is not open in a terminal: %+v", e)
	}
}

func TestList_MCPRuleAndIncludeAutomated(t *testing.T) {
	c, _ := fixture(t)
	got, err := c.List(history.MCPOpts(false))
	if err != nil {
		t.Fatal(err)
	}
	if g := ids(got); g != "idle open busy mine" {
		t.Fatalf("default ids %q: automated and analyzer hidden", g)
	}
	got, _ = c.List(history.MCPOpts(true))
	if g := ids(got); g != "cron hook idle open busy mine" {
		t.Fatalf("include_automated ids %q", g)
	}
}

func TestList_CreatedSessions(t *testing.T) {
	c, _ := fixture(t)
	c.Created = &CreatedStore{Path: filepath.Join(t.TempDir(), "c.json")}
	for _, id := range []string{"cron", "starting", "failed"} {
		if err := c.Created.Add(id, "/w/new", "first message "+id); err != nil {
			t.Fatal(err)
		}
	}
	c.Running = func(id string) bool { return id == "starting" }
	c.NewProvider = "claude"
	got, err := c.List(history.RemoteOpts(false, false))
	if err != nil {
		t.Fatal(err)
	}
	g := ids(got)
	if !strings.Contains(g, "cron") || !strings.Contains(g, "starting") || strings.Contains(g, "failed") {
		t.Fatalf("ids %q: want the created automated one and the running one, not the failed one", g)
	}
	for _, e := range got {
		if e.ID == "starting" && (e.Record != nil || e.Title != "first message starting" || e.Project != "/w/new" || e.Cwd != "/w/new" || e.Provider != "claude" || e.State != StateIdle) {
			t.Errorf("starting %+v", e)
		}
	}
}

func TestList_ScanErrorNamesTheProvider(t *testing.T) {
	c, st := fixture(t)
	st.err = errors.New("boom")
	if _, err := c.List(history.RemoteOpts(false, false)); err == nil || !strings.Contains(err.Error(), "claude: boom") {
		t.Fatalf("err %v", err)
	}
}

func TestGet(t *testing.T) {
	c, _ := fixture(t)
	e, ok := c.Get("open")
	if !ok || e.State != StateOpenInTerminal || e.Record == nil || e.Provider != "claude" {
		t.Fatalf("open: %+v %v", e, ok)
	}
	// Get finds hidden sessions too: visibility is the list's business.
	if e, ok := c.Get("cron"); !ok || e.Record == nil {
		t.Fatalf("cron: %+v %v", e, ok)
	}
	if _, ok := c.Get("nope"); ok {
		t.Fatal("unknown session found")
	}
	c.Created = &CreatedStore{Path: filepath.Join(t.TempDir(), "c.json")}
	if err := c.Created.Add("fresh", "/w", "hello"); err != nil {
		t.Fatal(err)
	}
	if e, ok := c.Get("fresh"); !ok || e.State != StateUnavailable || e.Title != "hello" {
		t.Fatalf("created, not running: %+v %v", e, ok)
	}
	c.Running = func(string) bool { return true }
	if e, _ := c.Get("fresh"); e.State != StateIdle {
		t.Fatalf("created and running: %+v", e)
	}
}

func TestLookupReadsTheLastScan(t *testing.T) {
	c, st := fixture(t)
	if _, ok := c.Lookup("idle"); ok {
		t.Fatal("found before any scan")
	}
	if _, err := c.List(history.RemoteOpts(false, false)); err != nil {
		t.Fatal(err)
	}
	n := st.scans
	if s, ok := c.Lookup("idle"); !ok || s.Title != "Owner title" || st.scans != n {
		t.Fatalf("lookup %+v %v, scans %d -> %d", s, ok, n, st.scans)
	}
	// A shared Cache carries the scan across Catalog values.
	cache := &Cache{}
	if _, err := (&Catalog{Providers: c.Providers, Cache: cache}).Scan(); err != nil {
		t.Fatal(err)
	}
	if _, ok := (&Catalog{Cache: cache}).Lookup("idle"); !ok {
		t.Fatal("shared cache lost the scan")
	}
}

func TestExists(t *testing.T) {
	c, _ := fixture(t)
	if !c.Exists("idle") || c.Exists("nope") {
		t.Fatal("Exists by file")
	}
	c.Created = &CreatedStore{Path: filepath.Join(t.TempDir(), "c.json")}
	if err := c.Created.Add("fresh", "/w", "hi"); err != nil {
		t.Fatal(err)
	}
	if !c.Exists("fresh") {
		t.Fatal("a created session exists before its file")
	}
}

// liveStore is a Store that is also a LiveSource.
type liveStore struct {
	stubStore
	ttl time.Duration
}

func (l *liveStore) LiveSessions(ttl time.Duration) harness.LiveSessions {
	l.ttl = ttl
	return fixedLive{}
}

func TestProvidersOfKeepsStoresWithLive(t *testing.T) {
	a := &liveStore{stubStore: stubStore{name: "a"}}
	b := &stubStore{name: "store-only"}
	c := &liveStore{stubStore: stubStore{name: "c"}}
	got := ProvidersOf([]any{a, b, "not a provider", c}, 3*time.Second)
	if len(got) != 2 || got[0].Store.Name() != "a" || got[1].Store.Name() != "c" || got[0].Live == nil {
		t.Fatalf("providers %+v", got)
	}
	if a.ttl != 3*time.Second {
		t.Fatalf("ttl %v", a.ttl)
	}
	if ProvidersOf([]any(nil), 0) != nil {
		t.Fatal("none: nil")
	}
}

func TestFileFirstStoreWins(t *testing.T) {
	c, st := fixture(t)
	c.Providers = append(c.Providers, Provider{Store: &stubStore{name: "fake", dir: st.dir}})
	if f, s := c.File("idle"); f == "" || s.Name() != "claude" {
		t.Fatalf("file %q store %v", f, s)
	}
	if f, s := c.File("nope"); f != "" || s != nil {
		t.Fatalf("nope: %q %v", f, s)
	}
}

func TestStateAndTerminal(t *testing.T) {
	c, _ := fixture(t)
	if c.State("busy") != StateBusy || c.State("open") != StateOpenInTerminal || c.State("idle") != StateIdle {
		t.Fatal("states")
	}
	c.Busy = nil
	if c.State("busy") != StateIdle {
		t.Fatal("no busy reader: idle")
	}
	if pid, ok := c.TerminalPID("open"); !ok || pid != 10 {
		t.Fatalf("pid %d %v", pid, ok)
	}
	if _, ok := c.TerminalPID("mine"); ok {
		t.Fatal("aimux's own holder")
	}
	if c.RegistryStatus("open") != "busy" || c.RegistryStatus("idle") != "" {
		t.Fatal("registry status")
	}
	if p := c.TerminalPIDs(); len(p) != 1 || p["open"] != 10 {
		t.Fatalf("pids %v", p)
	}
	if (&Catalog{}).TerminalPIDs() == nil {
		t.Fatal("no providers: want an empty map")
	}
}

func TestDefaultProvider(t *testing.T) {
	c, _ := fixture(t)
	if c.DefaultProvider() != "claude" {
		t.Fatal("first provider's name")
	}
	c.NewProvider = "x"
	if c.DefaultProvider() != "x" {
		t.Fatal("NewProvider wins")
	}
	if (&Catalog{}).DefaultProvider() != "" {
		t.Fatal("no providers")
	}
}

func TestProjects(t *testing.T) {
	dir := t.TempDir()
	app, gone := filepath.Join(dir, "app"), filepath.Join(dir, "gone")
	if err := os.Mkdir(app, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	st := &stubStore{name: "claude", sessions: []history.Session{
		{ID: "a", Cwd: app, Project: "/elsewhere", LastActive: now.Add(-time.Hour)},
		{ID: "b", Project: app, LastActive: now},
		{ID: "g", Cwd: gone, LastActive: now},
		{ID: "auto", Cwd: dir, Automated: true, LastActive: now},
	}}
	c := &Catalog{Providers: []Provider{{Store: st}}}
	got, err := c.Projects(history.RemoteOpts(false, false), 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != app || got[0].Name != "app" || !got[0].LastActive.Equal(now) {
		t.Fatalf("projects %+v", got)
	}
	if got, _ := c.Projects(history.RemoteOpts(true, false), 12); len(got) != 2 {
		t.Fatalf("with automated: %+v", got)
	}
	if got, _ := c.Projects(history.RemoteOpts(true, false), 1); len(got) != 1 {
		t.Fatalf("max 1: %+v", got)
	}
}

func TestListableIDsAndSearchable(t *testing.T) {
	c, _ := fixture(t)
	c.Created = &CreatedStore{Path: filepath.Join(t.TempDir(), "c.json")}
	for _, id := range []string{"starting", "failed"} {
		if err := c.Created.Add(id, "/w", "x"); err != nil {
			t.Fatal(err)
		}
	}
	c.Running = func(id string) bool { return id == "starting" }
	got := map[string]bool{}
	for _, id := range c.ListableIDs(history.RemoteOpts(false, false)) {
		got[id] = true
	}
	if !got["idle"] || !got["starting"] || got["failed"] || got["cron"] || got["sub"] {
		t.Fatalf("listable %v", got)
	}
	if p, ok := c.Searchable("idle", false); !ok || p != "claude" {
		t.Fatalf("idle: %q %v", p, ok)
	}
	if _, ok := c.Searchable("idle", true); ok {
		t.Fatal("automated and not created: not searchable")
	}
	if p, ok := c.Searchable("starting", true); !ok || p != "claude" {
		t.Fatalf("created and running: %q %v", p, ok)
	}
	if _, ok := c.Searchable("failed", false); ok {
		t.Fatal("created, no file, no run: not searchable")
	}
	if _, ok := c.Searchable("nope", false); ok {
		t.Fatal("unknown: not searchable")
	}
}

// Claude's store rereads one session for Refresh.
var _ Rereader = (*provider.Claude)(nil)

// rereadStore is a stubStore that rereads one record from reread.
type rereadStore struct {
	stubStore
	reread  func(history.Session) (history.Session, error)
	rereads int
}

func (r *rereadStore) Reread(s history.Session) (history.Session, error) {
	r.rereads++
	return r.reread(s)
}

func TestRefresh_RereadsOneSession(t *testing.T) {
	c, st := fixture(t)
	rs := &rereadStore{stubStore: *st, reread: func(s history.Session) (history.Session, error) {
		s.CustomTitle = "renamed"
		return s, nil
	}}
	c.Providers[0].Store = rs
	if _, err := c.List(history.RemoteOpts(false, false)); err != nil {
		t.Fatal(err)
	}
	scans := rs.scans
	e, ok := c.Refresh("open")
	if !ok || e.Record == nil || e.Record.CustomTitle != "renamed" || e.State != StateOpenInTerminal || e.Provider != "claude" {
		t.Fatalf("refresh: %+v %v", e, ok)
	}
	if rs.scans != scans || rs.rereads != 1 {
		t.Fatalf("scans %d -> %d, rereads %d: want one reread, no scan", scans, rs.scans, rs.rereads)
	}
	if s, _ := c.Lookup("open"); s.CustomTitle != "renamed" {
		t.Fatalf("cache not updated: %+v", s)
	}
}

// Not in the last scan, a store without Reread, or a failed reread: one
// fresh Get instead.
func TestRefresh_FallsBackToGet(t *testing.T) {
	c, st := fixture(t)
	if e, ok := c.Refresh("idle"); !ok || e.ID != "idle" || st.scans != 1 {
		t.Fatalf("not scanned yet: %+v %v, scans %d", e, ok, st.scans)
	}
	if _, ok := c.Refresh("idle"); !ok || st.scans != 2 {
		t.Fatalf("store without Reread: scans %d, want 2", st.scans)
	}
	rs := &rereadStore{stubStore: *st, reread: func(history.Session) (history.Session, error) {
		return history.Session{}, errors.New("gone")
	}}
	c.Providers[0].Store = rs
	if _, ok := c.Refresh("idle"); !ok || rs.rereads != 1 || rs.scans != st.scans+1 {
		t.Fatalf("failed reread: rereads %d, scans %d", rs.rereads, rs.scans)
	}
	if _, ok := c.Refresh("nope"); ok {
		t.Fatal("unknown session refreshed")
	}
}

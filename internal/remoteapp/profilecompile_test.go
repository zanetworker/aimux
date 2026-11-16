package remoteapp

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

func seedHome(t *testing.T, files map[string]string) string {
	t.Helper()
	home := t.TempDir()
	for p, body := range files {
		full := filepath.Join(home, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func ownerPins(t *testing.T, home string) {
	t.Helper()
	pins := toolgateway.Pins{
		"wiki": {"wiki_read": "a", "wiki_search": "b"},
		"jira": {"search_issues_jql": "c"},
	}
	if err := (toolgateway.PinStore{Path: toolgateway.DefaultPinsPath(home)}).Save(hashOnly(pins)); err != nil {
		t.Fatal(err)
	}
}

// An upgraded install (readonly.json, pins, no registry) compiles
// work-readonly exactly as before: the wiki keeps its server name and
// pins, nothing is re-pinned, and the compile writes nothing (remote serve
// migrates at start).
func TestCompileProfile_MigratesAndKeepsWiki(t *testing.T) {
	home := seedHome(t, map[string]string{".aimux/wingman/readonly.json": `{"HTTP": {"jira": {"URL": "http://127.0.0.1:8001/mcp"}}, "WikiRoot": "/w", "Allow": {"jira": ["search_issues_jql"], "wiki": ["wiki_search", "wiki_read"]}}`})
	ownerPins(t, home)
	c, err := compileProfile(toolgateway.NewStore(home).Current(), t.TempDir(), "work-readonly")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"wiki": {"wiki_read", "wiki_search"}, "jira": {"search_issues_jql"}}
	if !reflect.DeepEqual(c.Grant.Scope(), want) {
		t.Errorf("grant = %v, want %v", c.Grant.Scope(), want)
	}
	if _, err := os.Lstat(toolreg.DefaultPath(home)); err == nil {
		t.Error("the compile wrote the registry")
	}
}

// A registry that does not load fails the compile with its reason (every
// set is unavailable), rather than silently granting nothing.
func TestCompileProfile_BrokenRegistryNamesTheProblem(t *testing.T) {
	home := seedHome(t, map[string]string{".config/aimux/tool-sources.yaml": "version: 1\nsorces: {}\n"})
	ownerPins(t, home)
	_, err := compileProfile(toolgateway.NewStore(home).Current(), t.TempDir(), "work-readonly")
	if !errors.Is(err, profile.ErrUnavailable) || !strings.Contains(err.Error(), "sorces") {
		t.Fatalf("err = %v, want ErrUnavailable naming the bad key", err)
	}
}

// hashOnly is p as v1-style records (hash only), for seeding a pin store.
func hashOnly(p toolgateway.Pins) toolgateway.PinFile {
	f := toolgateway.PinFile{}
	for s, tools := range p {
		f[s] = map[string]toolgateway.PinRecord{}
		for t, h := range tools {
			f[s][t] = toolgateway.PinRecord{Hash: h}
		}
	}
	return f
}

// testSnapshots is remote serve's reload over home with a settable clock;
// loads counts the pins reads (one per Reload).
func testSnapshots(home string, logw *bytes.Buffer) (*toolSnapshots, func(time.Duration)) {
	s, advance, _ := countingSnapshots(home, logw)
	return s, advance
}

func countingSnapshots(home string, logw *bytes.Buffer) (*toolSnapshots, func(time.Duration), *atomic.Int32) {
	now := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	store := toolgateway.NewStore(home)
	loads := &atomic.Int32{}
	loadPins := store.LoadPins
	store.LoadPins = func() (toolreg.PinTable, error) { loads.Add(1); return loadPins() }
	s := &toolSnapshots{store: store, log: log.New(logw, "", 0).Printf, files: snapshotFiles(home),
		now: func() time.Time { mu.Lock(); defer mu.Unlock(); return now }}
	return s, func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }, loads
}

// A turn reloads when tool-sources.yaml or pins.json changed since the last
// reload (so an approve is seen by the very next turn), and otherwise only
// once a minute as a backstop; a reload that changed them is logged with
// the new version.
func TestToolSnapshots_ReloadOnFileChangeElseEveryMinute(t *testing.T) {
	home := seedHome(t, nil)
	ownerPins(t, home)
	var logw bytes.Buffer
	snaps, advance, loads := countingSnapshots(home, &logw)
	first := snaps.turn()
	if !strings.Contains(logw.String(), "tool snapshot "+first.Short()) {
		t.Fatalf("log %q should name the first snapshot", logw.String())
	}
	advance(time.Second)
	if snaps.turn(); loads.Load() != 1 {
		t.Fatalf("%d pins reads, want no reload while nothing changed", loads.Load())
	}
	// aimux tools approve rewrites pins.json; the next turn, a second later, sees it.
	if err := (toolgateway.PinStore{Path: toolgateway.DefaultPinsPath(home)}).Save(hashOnly(toolgateway.Pins{"jira": {"search_issues_jql": "z"}})); err != nil {
		t.Fatal(err)
	}
	advance(time.Second)
	second := snaps.turn()
	if second.Version() == first.Version() || !strings.Contains(logw.String(), "tool snapshot "+second.Short()) {
		t.Fatalf("approve not seen by the next turn, or not logged: %q", logw.String())
	}
	// A registry edit is seen at once too.
	writeHomeFile(t, toolreg.DefaultPath(home), "version: 1\nsources:\n  jira: {type: mcp-http, url: \"http://127.0.0.1:9001/mcp\"}\nsets:\n  jira:readonly: {source: jira, tools: [search_issues_jql]}\n")
	if third := snaps.turn(); third.Version() == second.Version() {
		t.Fatal("registry edit not seen by the next turn")
	}
	n := loads.Load()
	advance(30 * time.Second)
	if snaps.turn(); loads.Load() != n {
		t.Fatal("reloaded within a minute with nothing changed")
	}
	advance(31 * time.Second)
	if snaps.turn(); loads.Load() != n+1 {
		t.Fatal("no backstop reload after a minute")
	}
}

// A pins file that does not read fails closed for the turns: the compile
// grants nothing and names the file; once it is fixed, the next turn
// compiles again.
func TestCompileProfile_BrokenPinsFailsClosedAndRecovers(t *testing.T) {
	home := seedHome(t, map[string]string{".aimux/wingman/readonly.json": `{"HTTP": {"jira": {"URL": "http://127.0.0.1:8001/mcp"}}, "WikiRoot": "/w", "Allow": {"jira": ["search_issues_jql"], "wiki": ["wiki_search", "wiki_read"]}}`})
	ownerPins(t, home)
	var logw bytes.Buffer
	snaps, advance := testSnapshots(home, &logw)
	dir := t.TempDir()
	if _, err := compileProfile(snaps.turn(), dir, "work-readonly"); err != nil {
		t.Fatal(err)
	}
	good, _ := os.ReadFile(toolgateway.DefaultPinsPath(home))
	writeHomeFile(t, toolgateway.DefaultPinsPath(home), "{bad")
	advance(time.Second)
	_, err := compileProfile(snaps.turn(), dir, "work-readonly")
	if !errors.Is(err, profile.ErrUnpinned) || !strings.Contains(err.Error(), "pins.json") {
		t.Fatalf("broken pins: err = %v, want nothing granted and the file named", err)
	}
	if !strings.Contains(logw.String(), "pins.json") {
		t.Errorf("log %q should name the pins file", logw.String())
	}
	writeHomeFile(t, toolgateway.DefaultPinsPath(home), string(good))
	advance(time.Second)
	if c, err := compileProfile(snaps.turn(), dir, "work-readonly"); err != nil || c.Grant.Empty() {
		t.Fatalf("after the fix: %v", err)
	}
}

func writeHomeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil { //nolint:gosec // G703: test temp paths
		t.Fatal(err)
	}
}

// A reload while a turn runs does not change the version that turn compiled
// with; the next turn compiles at the new one.
func TestCompileProfile_ReloadDuringTurnKeepsItsVersion(t *testing.T) {
	home := seedHome(t, map[string]string{".aimux/wingman/readonly.json": `{"HTTP": {"jira": {"URL": "http://127.0.0.1:8001/mcp"}}, "WikiRoot": "/w", "Allow": {"jira": ["search_issues_jql"], "wiki": ["wiki_search", "wiki_read"]}}`})
	ownerPins(t, home)
	snaps, advance := testSnapshots(home, &bytes.Buffer{})
	c, err := compileProfile(snaps.turn(), t.TempDir(), "work-readonly")
	if err != nil {
		t.Fatal(err)
	}
	compiled := c.Snapshot.Version()
	if err := (toolgateway.PinStore{Path: toolgateway.DefaultPinsPath(home)}).Save(hashOnly(toolgateway.Pins{"wiki": {"wiki_read": "a2", "wiki_search": "b"}, "jira": {"search_issues_jql": "c"}})); err != nil {
		t.Fatal(err)
	}
	advance(time.Minute)
	next := snaps.turn() // another turn reloads
	if c.Snapshot.Version() != compiled || next.Version() == compiled {
		t.Fatalf("compiled %s, still %s; next %s", compiled, c.Snapshot.Version(), next.Version())
	}
	c2, err := compileProfile(next, t.TempDir(), "work-readonly")
	if err != nil || c2.Snapshot.Version() != next.Version() {
		t.Fatalf("next turn compiled at %s (%v), want %s", c2.Snapshot.Version(), err, next.Version())
	}
}

// Turns compiling while others reload see whole snapshots (run with -race).
func TestCompileProfile_ConcurrentReloadAndCompile(t *testing.T) {
	home := seedHome(t, map[string]string{".aimux/wingman/readonly.json": `{"HTTP": {"jira": {"URL": "http://127.0.0.1:8001/mcp"}}, "WikiRoot": "/w", "Allow": {"jira": ["search_issues_jql"], "wiki": ["wiki_search", "wiki_read"]}}`})
	ownerPins(t, home)
	snaps, advance := testSnapshots(home, &bytes.Buffer{})
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 10 {
				if i == 0 {
					h := "c"
					if j%2 == 1 {
						h = "c2"
					}
					_ = (toolgateway.PinStore{Path: toolgateway.DefaultPinsPath(home)}).Save(hashOnly(toolgateway.Pins{"wiki": {"wiki_read": "a", "wiki_search": "b"}, "jira": {"search_issues_jql": h}}))
				}
				advance(time.Minute)
				snap := snaps.turn()
				c, err := compileProfile(snap, dir, "work-readonly")
				if err != nil {
					t.Error(err)
					return
				}
				if c.Snapshot.Version() != snap.Version() {
					t.Errorf("compiled at %s, turn had %s", c.Snapshot.Version(), snap.Version())
				}
			}
		}()
	}
	wg.Wait()
}

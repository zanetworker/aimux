package remoteapp

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/clientscope"
	"github.com/zanetworker/aimux/internal/profilerun"
	"github.com/zanetworker/aimux/internal/remoteapi"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// fakeEngine records what the serve wiring asks of it.
type fakeEngine struct {
	news    []profilerun.Meta
	cancels []string
	id      string
}

func (f *fakeEngine) New(_ context.Context, m profilerun.Meta, _ string) (string, error) {
	f.news = append(f.news, m)
	return f.id, nil
}
func (f *fakeEngine) Send(context.Context, string, string, func(acprun.Event)) (string, error) {
	return "end_turn", nil
}
func (f *fakeEngine) Cancel(s string) error { f.cancels = append(f.cancels, s); return nil }

func noEnv(string) string { return "" }

// A new profile session goes to the engine for its profile's Runs; its
// cancel goes there too. A host profile has no engine here.
func TestProfileWiring_NewAndCancelGoToTheProfilesEngine(t *testing.T) {
	home := t.TempDir()
	page, sandbox := &fakeEngine{id: "page-1"}, &fakeEngine{id: "sb-1"}
	p, closeAll, err := buildProfileWiring(home, noEnv, &bytes.Buffer{}, map[string]profilerun.Engine{"acp": page, "sandbox": sandbox})
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	if id, err := p.newSession(context.Background(), profilerun.Meta{Profile: "page"}, "PAGE"); err != nil || id != "page-1" {
		t.Fatalf("page session %q %v", id, err)
	}
	if id, err := p.newSession(context.Background(), profilerun.Meta{Profile: "work-readonly"}, ""); err != nil || id != "sb-1" {
		t.Fatalf("sandbox session %q %v", id, err)
	}
	if len(page.news) != 1 || len(sandbox.news) != 1 {
		t.Fatalf("page %v sandbox %v", page.news, sandbox.news)
	}
	if _, err := p.newSession(context.Background(), profilerun.Meta{Profile: "coding"}, ""); err == nil {
		t.Fatal("a host profile started an engine session")
	}
	if err := p.cancelSession("sb-1"); err != nil || len(sandbox.cancels) != 1 {
		t.Fatalf("cancel %v %v", err, sandbox.cancels)
	}
}

// After a restart, sandbox sessions in the store are known again (Check
// passes and a turn would resume); a sandbox session recorded by someone
// else after startup fails Check with a clear error. Built with the real
// engines: nothing runs until a turn, so no openshell here.
func TestProfileWiring_RestartKnowsRecordedSandboxSessions(t *testing.T) {
	home := t.TempDir()
	store := &profilerun.Store{Path: profilerun.DefaultStorePath(home)}
	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	if err := store.Put("11111111-2222-4333-8444-555555555555", profilerun.Meta{Profile: "work-readonly", Client: "wingman", Created: at, Started: true}); err != nil {
		t.Fatal(err)
	}
	var logw bytes.Buffer
	p, closeAll, err := buildProfileWiring(home, noEnv, &logw, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	p.router.FallbackCheck = func(string) error { return errors.New("not a profile session") }
	if err := p.router.Check("11111111-2222-4333-8444-555555555555"); err != nil {
		t.Fatalf("recorded sandbox session after restart: %v", err)
	}
	if err := store.Put("lost", profilerun.Meta{Profile: "work-readonly", Client: "wingman", Created: at}); err != nil {
		t.Fatal(err)
	}
	if err := p.router.Check("lost"); err == nil || !strings.Contains(err.Error(), "not known to the sandbox engine") {
		t.Fatalf("unknown sandbox session: %v", err)
	}
	if p.running("11111111-2222-4333-8444-555555555555") || p.owns(os.Getpid()) {
		t.Fatal("nothing runs yet, and this process is not an engine's adapter")
	}
	if _, err := os.Stat(filepath.Join(home, ".aimux", "wingman", "sessions")); err != nil {
		t.Fatalf("page sessions dir: %v", err)
	}
}

// A sandbox session recorded before its first turn started cannot resume
// (claude never made its id) and its page was not kept: after a restart it
// fails with the restart error and is hidden, so the same page starts a new
// session instead of reaching it again. The engine reports a first turn's
// start to the store.
func TestProfileWiring_RestartDropsUnstartedSandboxSession(t *testing.T) {
	home := t.TempDir()
	store := &profilerun.Store{Path: profilerun.DefaultStorePath(home)}
	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	const id = "22222222-2222-4333-8444-555555555555"
	if err := store.Put(id, profilerun.Meta{Profile: "work-readonly", Client: "wingman", URL: "https://a.example/x", Created: at}); err != nil {
		t.Fatal(err)
	}
	p, closeAll, err := buildProfileWiring(home, noEnv, &bytes.Buffer{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	if err := p.router.Check(id); err == nil || !strings.Contains(err.Error(), "never started before aimux restarted") {
		t.Fatalf("Check = %v, want the restart error", err)
	}
	if got, ok := store.FindPage("wingman", "", "https://a.example/x", "work-readonly", false, false); ok {
		t.Fatalf("FindPage still continues the dropped session %s", got)
	}
	if p.sandbox.OnStarted == nil {
		t.Fatal("the sandbox engine does not report started turns")
	}
	_ = store.Put("s3", profilerun.Meta{Profile: "work-readonly", Client: "wingman", Created: at})
	if err := p.sandbox.OnStarted("s3"); err != nil {
		t.Fatal(err)
	}
	if m, _ := store.Get("s3"); !m.Started {
		t.Fatal("OnStarted did not record Started in the store")
	}
}

// Install serves a catalog in which a profile with no engine here (host,
// run) is unavailable, so GET /v1/profiles and POST /v1/sessions agree.
func TestProfileWiring_InstallMarksEngineLessProfilesUnavailable(t *testing.T) {
	p, closeAll, err := buildProfileWiring(t.TempDir(), noEnv, &bytes.Buffer{},
		map[string]profilerun.Engine{"acp": &fakeEngine{}, "sandbox": &fakeEngine{}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	raw, err := p.catalog()
	if err != nil {
		t.Fatal(err)
	}
	api := &remoteapi.API{}
	p.Install(api, clientscope.Defaults(), false)
	all, err := api.Profiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"coding", "base", "research-with-work"} {
		if all[n].Available {
			t.Errorf("%s (runs %s) available with no engine for it", n, all[n].Runs)
		}
	}
	for _, n := range []string{"page", "work-readonly", "research"} {
		if all[n].Available != raw[n].Available {
			t.Errorf("%s availability changed: %v, catalog %v", n, all[n].Available, raw[n].Available)
		}
	}
}

// A finished sandbox session's transcript is archived under
// ~/.aimux/archive (sandboxlaunch.Archive's layout); archived names it so
// the session detail reads it. Archived transcripts of other sessions, and
// of sessions on another engine, stay out of the detail.
func TestProfileWiring_ArchivedNamesOnlySandboxSessionTranscripts(t *testing.T) {
	home := t.TempDir()
	store := &profilerun.Store{Path: profilerun.DefaultStorePath(home)}
	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	const sb, page, other = "33333333-2222-4333-8444-555555555555", "44444444-2222-4333-8444-555555555555", "55555555-2222-4333-8444-555555555555"
	if err := store.Put(sb, profilerun.Meta{Profile: "work-readonly", Client: "wingman", Created: at, Started: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(page, profilerun.Meta{Profile: "page", Client: "wingman", Created: at}); err != nil {
		t.Fatal(err)
	}
	p, closeAll, err := buildProfileWiring(home, noEnv, &bytes.Buffer{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	dir := filepath.Join(home, ".aimux", "archive", "openshell-aimux-work-readonly")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{sb, page, other} {
		if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := p.archived(sb), filepath.Join(dir, sb+".jsonl"); got != want {
		t.Fatalf("archived(sandbox session) = %q, want %q", got, want)
	}
	if got := p.archived(page); got != "" {
		t.Fatalf("archived(page session) = %q, want none", got)
	}
	if got := p.archived(other); got != "" {
		t.Fatalf("archived(session without a profile) = %q, want none", got)
	}
}

// A user profile extending builtin:page with model.name opus makes opus the
// page engine's default for its sessions; a builtin page session keeps the
// engine's default (sonnet).
func TestProfileWiring_PageEngineDefaultsToTheProfileModel(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "aimux", "profiles")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "my-page.yaml"), []byte("name: my-page\nextends: builtin:page\nmodel:\n  name: opus\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &profilerun.Store{Path: profilerun.DefaultStorePath(home)}
	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	_ = store.Put("mine", profilerun.Meta{Profile: "my-page", Client: "wingman", Created: at})
	_ = store.Put("builtin", profilerun.Meta{Profile: "page", Client: "wingman", Created: at})
	p, closeAll, err := buildProfileWiring(home, noEnv, &bytes.Buffer{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	page, _ := p.engines["acp"].(*profilerun.PageEngine)
	if page == nil || page.SessionModel == nil {
		t.Fatal("the page engine has no SessionModel")
	}
	if got := page.SessionModel("mine"); got != profilerun.OpusPageModel {
		t.Fatalf("SessionModel(my-page session) = %q, want opus", got)
	}
	if got := page.SessionModel("builtin"); got != "" {
		t.Fatalf("SessionModel(builtin page session) = %q, want none (engine default)", got)
	}
	if got := page.SessionModel("unknown"); got != "" {
		t.Fatalf("SessionModel(unknown) = %q, want none", got)
	}
}

// The real wiring serves GET /v1/tools from the turns' snapshot store
// (reloaded when a file changed, as a turn would) and the gateway's own
// view, which is empty before the gateway starts: no backend is contacted.
// Injected engines have no tool gateway, so no Tools.
func TestProfileWiring_InstallServesToolsFromTheStore(t *testing.T) {
	home := t.TempDir()
	reg := toolreg.DefaultPath(home)
	if err := os.MkdirAll(filepath.Dir(reg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reg, []byte("version: 1\nsources:\n  jira: {type: mcp-http, url: \"http://127.0.0.1:1/mcp\"}\nsets:\n  jira:readonly: {source: jira, tools: [search]}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, closeAll, err := buildProfileWiring(home, noEnv, &bytes.Buffer{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	api := &remoteapi.API{}
	p.Install(api, clientscope.Defaults(), false)
	if api.Tools == nil {
		t.Fatal("real wiring: no Tools")
	}
	v := api.Tools()
	if _, ok := v.Snapshot.Registry().Sets["jira:readonly"]; !ok || len(v.Live) != 0 {
		t.Fatalf("view: sets %v live %v, want the registry and nothing listed yet", v.Snapshot.Registry().Sets, v.Live)
	}
	first := v.Snapshot.Version()
	ps := toolgateway.PinStore{Path: toolgateway.DefaultPinsPath(home)}
	if err := ps.Save(toolgateway.PinFile{"jira": {"search": {Hash: "sha256:1"}}}); err != nil {
		t.Fatal(err)
	}
	if v := api.Tools(); v.Snapshot.Version() == first {
		t.Fatal("a new pin did not reach the view: it reports a stale snapshot")
	}

	injected, closeInjected, err := buildProfileWiring(t.TempDir(), noEnv, &bytes.Buffer{}, map[string]profilerun.Engine{"acp": &fakeEngine{}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeInjected()
	api2 := &remoteapi.API{}
	injected.Install(api2, clientscope.Defaults(), false)
	if api2.Tools != nil {
		t.Fatal("injected engines: Tools set")
	}
}

const legacyReadonly = `{"HTTP": {"jira": {"URL": "http://127.0.0.1:1/mcp"}}, "Allow": {"jira": ["search_issues_jql"]}}`

// remote serve migrates readonly.json once, when it starts; it is never
// migrated by a reload afterwards.
func TestProfileWiring_MigratesOnceAtStart(t *testing.T) {
	home := t.TempDir()
	writeFile(t, toolreg.LegacyPath(home), legacyReadonly)
	var logw bytes.Buffer
	_, closeAll, err := buildProfileWiring(home, noEnv, &logw, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	if _, err := os.Stat(toolreg.DefaultPath(home)); err != nil {
		t.Fatalf("serve start did not migrate: %v", err)
	}
	if _, err := os.Stat(toolreg.LegacyPath(home) + ".migrated"); err != nil || !strings.Contains(logw.String(), "igrated") {
		t.Fatalf("legacy file kept: %v; log %q", err, logw.String())
	}
}

// GET /v1/tools and a turn's compile only read: on an install whose
// readonly.json was never migrated (it appeared after serve started), the
// registry is read from it in memory and both files are left exactly as
// they were.
func TestProfileWiring_ToolsAndTurnsWriteNothing(t *testing.T) {
	home := t.TempDir()
	p, closeAll, err := buildProfileWiring(home, noEnv, &bytes.Buffer{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	writeFile(t, toolreg.LegacyPath(home), legacyReadonly)
	before, err := os.Stat(toolreg.LegacyPath(home))
	if err != nil {
		t.Fatal(err)
	}
	api := &remoteapi.API{}
	p.Install(api, clientscope.Defaults(), false)
	v := api.Tools()
	if _, ok := v.Snapshot.Registry().Sets["jira:readonly"]; !ok || v.Snapshot.Err() != nil {
		t.Fatalf("view %v (%v): want readonly.json's set", v.Snapshot.Registry().Sets, v.Snapshot.Err())
	}
	_, _ = p.sandbox.Compile("work-readonly") // a turn's compile; no pins, so it may fail: only its side effects matter
	api.Tools()
	if _, err := os.Lstat(toolreg.DefaultPath(home)); err == nil {
		t.Fatal("a reload wrote tool-sources.yaml")
	}
	after, err := os.Stat(toolreg.LegacyPath(home))
	if err != nil || !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatalf("readonly.json changed: %v", err)
	}
	if _, err := os.Stat(toolreg.LegacyPath(home) + ".migrated"); err == nil {
		t.Fatal("readonly.json was renamed")
	}
}

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The real wiring registers the run engine, so research-with-work (its
// roles resolve) is served available, with its planner and reader on the
// sandbox engine and its writer on the page engine.
func TestServedCatalog_ResearchWithWorkAvailable(t *testing.T) {
	p, closeAll, err := buildProfileWiring(t.TempDir(), noEnv, &bytes.Buffer{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	all, err := p.servedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if rww := all["research-with-work"]; !rww.Available || rww.Runs != "run" {
		t.Fatalf("research-with-work = runs %q available %v, want run and available", rww.Runs, rww.Available)
	}
	run, ok := p.engines["run"].(*profilerun.RunEngine)
	if !ok {
		t.Fatalf("engines[run] = %T, want *profilerun.RunEngine", p.engines["run"])
	}
	if run.Planner != p.sandbox || run.Reader != p.sandbox || run.Writer != p.engines["acp"] {
		t.Fatal("run engine stages are not on the sandbox (planner, reader) and page (writer) engines")
	}
	if run.Store != p.store || run.Compile == nil || run.Resolve == nil {
		t.Fatal("run engine has no store, compile or resolve")
	}
	if _, err := run.Resolve("research-with-work"); err != nil {
		t.Fatalf("Resolve(research-with-work) = %v", err)
	}
}

// Run sessions live only in the process that ran them. A restart writes
// nothing to the shared store (another remote serve may own those
// sessions); page reuse asks the serving engine instead: a run session it
// does not know is not live, a sandbox session it adopted is.
func TestProfileWiring_DeadRunSessionNotLive(t *testing.T) {
	home := t.TempDir()
	store := &profilerun.Store{Path: profilerun.DefaultStorePath(home)}
	at := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	const run, sb = "66666666-2222-4333-8444-555555555555", "77777777-2222-4333-8444-555555555555"
	if err := store.Put(run, profilerun.Meta{Profile: "research-with-work", Client: "wingman", URL: "https://x.example/", Created: at, Started: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(sb, profilerun.Meta{Profile: "work-readonly", Client: "wingman", URL: "https://x.example/", Created: at, Started: true}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	p, closeAll, err := buildProfileWiring(home, noEnv, &bytes.Buffer{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	if after, _ := os.ReadFile(store.Path); string(after) != string(before) {
		t.Fatal("startup rewrote the shared profile store")
	}
	if p.live(run) {
		t.Fatal("a run session this process does not hold is live")
	}
	if !p.live(sb) {
		t.Fatal("an adopted sandbox session is not live")
	}
	if !p.live("not-a-profile-session") {
		t.Fatal("a session without Meta must not be judged here")
	}
	api := &remoteapi.API{}
	p.Install(api, clientscope.Defaults(), false)
	if api.ProfileSessionLive == nil || api.ProfileSessionLive(run) {
		t.Fatal("Install did not set ProfileSessionLive")
	}
}

// A run session's turns are kept as its own transcript in aimux's archive
// (question and answer), which the session detail reads after a restart.
func TestProfileWiring_ArchivedNamesRunTranscript(t *testing.T) {
	home := t.TempDir()
	store := &profilerun.Store{Path: profilerun.DefaultStorePath(home)}
	const run = "88888888-2222-4333-8444-555555555555"
	if err := store.Put(run, profilerun.Meta{Profile: "research-with-work", Client: "wingman"}); err != nil {
		t.Fatal(err)
	}
	p, closeAll, err := buildProfileWiring(home, noEnv, &bytes.Buffer{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	re := p.engines["run"].(*profilerun.RunEngine)
	if re.ArchiveDir != p.sandbox.ArchiveDir {
		t.Fatalf("run ArchiveDir %q, want the sandbox archive %q", re.ArchiveDir, p.sandbox.ArchiveDir)
	}
	f := profilerun.RunTranscriptPath(re.ArchiveDir, run)
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := p.archived(run); got != f {
		t.Fatalf("archived(run) = %q, want %q", got, f)
	}
}

// A run session's transcript is read even when its profile no longer
// resolves (removed or renamed): history does not depend on the profile.
func TestProfileWiring_ArchivedRunWithoutItsProfile(t *testing.T) {
	home := t.TempDir()
	store := &profilerun.Store{Path: profilerun.DefaultStorePath(home)}
	const run = "99999999-2222-4333-8444-555555555555"
	if err := store.Put(run, profilerun.Meta{Profile: "my-gone-run", Client: "wingman"}); err != nil {
		t.Fatal(err)
	}
	p, closeAll, err := buildProfileWiring(home, noEnv, &bytes.Buffer{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	f := profilerun.RunTranscriptPath(p.sandbox.ArchiveDir, run)
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := p.archived(run); got != f {
		t.Fatalf("archived = %q, want %q", got, f)
	}
}

// turnLinks reads a run session's per-turn link sets from its transcript;
// other sessions have none.
func TestProfileWiring_TurnLinksFromRunTranscript(t *testing.T) {
	home := t.TempDir()
	store := &profilerun.Store{Path: profilerun.DefaultStorePath(home)}
	const run = "aaaaaaaa-2222-4333-8444-555555555555"
	if err := store.Put(run, profilerun.Meta{Profile: "research-with-work", Client: "wingman"}); err != nil {
		t.Fatal(err)
	}
	p, closeAll, err := buildProfileWiring(home, noEnv, &bytes.Buffer{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	f := profilerun.RunTranscriptPath(p.sandbox.ArchiveDir, run)
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		t.Fatal(err)
	}
	data := `{"type":"user","timestamp":"2026-11-19T10:00:00Z","message":{"role":"user","content":"q"}}
{"type":"assistant","timestamp":"2026-11-19T10:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"a"}]},"aimuxLinks":["https://github.com/x"]}
`
	if err := os.WriteFile(f, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC).UnixNano()
	if got := p.turnLinks(run)[at]; len(got) != 1 || got[0] != "https://github.com/x" {
		t.Fatalf("turnLinks = %v", p.turnLinks(run))
	}
	if p.turnLinks("bbbbbbbb-2222-4333-8444-555555555555") != nil {
		t.Fatal("links for a session not in the store")
	}
}

// Install gives the run engine the API's per-client set check, so a Both
// turn's held-tools notice offers only sets the client may approve.
func TestProfileWiring_InstallWiresRunSetsFor(t *testing.T) {
	p, closeAll, err := buildProfileWiring(t.TempDir(), noEnv, &bytes.Buffer{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	p.Install(&remoteapi.API{}, clientscope.Defaults(), false)
	if run := p.engines["run"].(*profilerun.RunEngine); run.SetsFor == nil {
		t.Fatal("run engine has no SetsFor after Install")
	}
}

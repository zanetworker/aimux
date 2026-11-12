package remoteapp

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/clientscope"
	"github.com/zanetworker/aimux/internal/profilerun"
	"github.com/zanetworker/aimux/internal/remoteapi"
	"github.com/zanetworker/aimux/internal/toolgateway"
)

// The gateway starts on first use only, once; a failed start is retried by
// the next use; closing stops what started.
func TestLazyGateway_StartsOnceOnFirstUseAndStopsOnClose(t *testing.T) {
	starts, stops := 0, 0
	fail := true
	g := &lazyGateway{tokens: &toolgateway.Tokens{}, start: func(ctx context.Context) (func(), error) {
		starts++
		if fail {
			return nil, errors.New("listen on 127.0.0.1:7390: address in use")
		}
		return func() { stops++ }, nil
	}}
	if starts != 0 {
		t.Fatal("started before use")
	}
	if _, _, err := g.get(); err == nil || !strings.Contains(err.Error(), "7390") {
		t.Fatalf("first get = %v, want the start error", err)
	}
	fail = false
	for i := 0; i < 3; i++ {
		base, tokens, err := g.get()
		if err != nil || base != toolgateway.ReadonlyURL || tokens != g.tokens {
			t.Fatalf("get = %q %p %v", base, tokens, err)
		}
	}
	if starts != 2 {
		t.Fatalf("%d starts, want 2 (one failed, one served three uses)", starts)
	}
	g.close()
	g.close()
	if stops != 1 {
		t.Fatalf("%d stops, want 1", stops)
	}
}

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
	if got, ok := store.FindPage("wingman", "https://a.example/x", "work-readonly"); ok {
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

// get refreshes the running gateway (down backends, re-pins) at most once
// per refreshEvery, never on the get that started it, and outside the start
// lock: a slow refresh does not hold up another turn's get.
func TestLazyGateway_RefreshesAtMostOncePerMinuteOutsideTheLock(t *testing.T) {
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	clock := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }
	advance := func(d time.Duration) { clockMu.Lock(); now = now.Add(d); clockMu.Unlock() }
	var refreshes atomic.Int32
	entered, release := make(chan struct{}, 4), make(chan struct{})
	g := &lazyGateway{tokens: &toolgateway.Tokens{},
		start: func(context.Context) (func(), error) { return func() {}, nil },
		refresh: func(ctx context.Context) {
			refreshes.Add(1)
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
		},
		now: clock}
	defer g.close()
	if _, _, err := g.get(); err != nil || refreshes.Load() != 0 {
		t.Fatalf("starting get: %v, %d refreshes (a fresh start read everything)", err, refreshes.Load())
	}
	advance(30 * time.Second)
	_, _, _ = g.get()
	if refreshes.Load() != 0 {
		t.Fatal("refreshed within a minute of the start")
	}
	advance(31 * time.Second)
	go func() { _, _, _ = g.get() }()
	select {
	case <-entered: // a slow refresh is running
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("no refresh a minute after the start")
	}
	done := make(chan struct{})
	go func() { _, _, _ = g.get(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a get waited on another get's refresh")
	}
	close(release)
	if n := refreshes.Load(); n != 1 {
		t.Fatalf("%d refreshes in one minute, want 1", n)
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

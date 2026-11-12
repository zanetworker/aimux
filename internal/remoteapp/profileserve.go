package remoteapp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/clientscope"
	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/profilerun"
	"github.com/zanetworker/aimux/internal/remoteapi"
	"github.com/zanetworker/aimux/internal/sandboxlaunch"
	"github.com/zanetworker/aimux/internal/toolgateway"
)

// lazyGateway is the read-only tool gateway of remote serve: started on the
// first sandbox turn that needs it (never at startup, so serve does not hold
// 127.0.0.1:7390 unless profile sessions use it), checking the session
// tokens the sandbox engine issues. A failed start is retried by the next
// use; close stops what started.
type lazyGateway struct {
	tokens *toolgateway.Tokens
	start  func(ctx context.Context) (stop func(), err error)
	// refresh, if set, updates the running gateway (backends that were
	// down, changed pins); now is the clock (nil: time.Now).
	refresh func(ctx context.Context)
	now     func() time.Time

	mu        sync.Mutex
	ctx       context.Context // ends at close; nil: background
	cancel    context.CancelFunc
	stop      func()
	stopped   bool
	refreshed time.Time // last refresh, or the start
}

// The running gateway is refreshed at most once per gatewayRefreshEvery,
// each refresh bounded by gatewayRefreshTimeout.
const (
	gatewayRefreshEvery   = 60 * time.Second
	gatewayRefreshTimeout = 15 * time.Second
)

func (g *lazyGateway) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

// get is the sandbox engine's Gateway: the base URL a sandbox reaches the
// gateway at and the token table it checks, starting it if needed. A
// gateway already running is refreshed (backends that were down, changed
// pins) at most once per gatewayRefreshEvery, outside the lock, so only the
// turn that refreshes waits, and for at most gatewayRefreshTimeout.
func (g *lazyGateway) get() (string, *toolgateway.Tokens, error) {
	g.mu.Lock()
	if g.stopped {
		g.mu.Unlock()
		return "", nil, fmt.Errorf("the tool gateway is shut down")
	}
	now, refresh := g.clock(), false
	if g.stop == nil {
		if g.ctx == nil {
			g.ctx, g.cancel = context.WithCancel(context.Background())
		}
		stop, err := g.start(g.ctx)
		if err != nil {
			g.mu.Unlock()
			return "", nil, err
		}
		g.stop, g.refreshed = stop, now // a fresh start read every backend and the pins
	} else if g.refresh != nil && now.Sub(g.refreshed) >= gatewayRefreshEvery {
		g.refreshed, refresh = now, true
	}
	ctx := g.ctx
	g.mu.Unlock()
	if refresh {
		rctx, cancel := context.WithTimeout(ctx, gatewayRefreshTimeout)
		g.refresh(rctx) // logs its own failures
		cancel()
	}
	return toolgateway.ReadonlyURL, g.tokens, nil
}

// close stops the gateway if it started; later gets fail.
func (g *lazyGateway) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped {
		return
	}
	g.stopped = true
	if g.stop != nil {
		g.stop()
	}
	if g.cancel != nil {
		g.cancel()
	}
}

// profileWiring is remote serve's profile sessions: the catalog the API
// serves, the store, the engines by Manifest.Runs and the router the
// broker sends through (its fallbacks are set by build).
type profileWiring struct {
	router  *profilerun.Router
	store   *profilerun.Store
	catalog func() (map[string]profile.Manifest, error)
	runs    func(name string) (string, error)
	engines map[string]profilerun.Engine
	pageMgr *acprun.Manager           // nil with injected engines
	sandbox *profilerun.SandboxEngine // nil with injected engines

	mu    sync.Mutex
	owner map[string]profilerun.Engine // session -> the engine that made it
}

// ProfileWiring is the profile sessions of remote serve, exported so another
// client of the same wiring (the second-client test) runs exactly what serve
// runs.
type ProfileWiring = profileWiring

// NewProfileWiring is buildProfileWiring for callers outside serve: with
// engines set, no real engine is built.
func NewProfileWiring(home string, getenv func(string) string, errOut io.Writer, engines map[string]profilerun.Engine) (*ProfileWiring, func(), error) {
	return buildProfileWiring(home, getenv, errOut, engines)
}

// Router is the router the broker sends through; set its Fallback,
// FallbackCheck and FallbackCancel for sessions without a profile.
func (p *profileWiring) Router() *profilerun.Router { return p.router }

// Install sets the API's profile fields: the catalog, the clients' scopes,
// the store, the engines' session start and cancel, the store's health and
// the step-up of sessions without a profile.
func (p *profileWiring) Install(api *remoteapi.API, scopes clientscope.Scopes, stepUpLegacy bool) {
	api.Profiles, api.Scopes, api.ProfileSessions = p.servedCatalog, scopes, p.store
	api.NewProfileSession, api.CancelProfileSession = p.newSession, p.cancelSession
	api.ProfileHealth, api.StepUpLegacy = p.router.Health, stepUpLegacy
}

// servedCatalog is the catalog the API serves: a profile whose Runs has no
// engine here (host, run) is unavailable, so listing it and starting it
// agree (409 profile_not_available) instead of a start failing late.
func (p *profileWiring) servedCatalog() (map[string]profile.Manifest, error) {
	all, err := p.catalog()
	if err != nil {
		return nil, err
	}
	for n, m := range all {
		if m.Available && p.engines[m.Runs] == nil {
			m.Available = false
			all[n] = m
		}
	}
	return all, nil
}

// buildProfileWiring wires the profile sessions over home. With engines nil
// it builds the real ones: the page engine ("acp") on the Wingman adapter
// (~/.aimux/wingman/adapter) and the sandbox engine ("sandbox") on the
// openshell CLI, compiling each profile afresh against the pinned tools,
// with the lazily started read-only gateway. Nothing starts a process until
// a session does. Sandbox sessions already in the store are adopted, so a
// turn after a restart resumes them. closeAll stops what was built.
func buildProfileWiring(home string, getenv func(string) string, errOut io.Writer, engines map[string]profilerun.Engine) (p *profileWiring, closeAll func(), err error) {
	dir := profileDir(home, getenv)
	p = &profileWiring{
		store:   &profilerun.Store{Path: profilerun.DefaultStorePath(home)},
		catalog: profileCatalog(dir, errOut),
		runs:    profileRuns(dir),
		engines: engines,
	}
	closeAll = func() {}
	if p.engines == nil {
		var closeReal func()
		if closeReal, err = p.buildEngines(home, dir, errOut); err != nil {
			return nil, nil, err
		}
		closeAll = closeReal
	}
	p.router = &profilerun.Router{Store: p.store, Engines: p.engines, Runs: p.runs}
	p.adopt(errOut)
	return p, closeAll, nil
}

// buildEngines builds the real page and sandbox engines into p.
func (p *profileWiring) buildEngines(home, dir string, errOut io.Writer) (func(), error) {
	sessions := filepath.Join(home, ".aimux", "wingman", "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", sessions, err)
	}
	adapter := filepath.Join(home, ".aimux", "wingman", "adapter", "node_modules", ".bin", "claude-agent-acp")
	p.pageMgr = profilerun.NewPageManager(adapter, profilerun.DefaultPageModel)
	page := profilerun.NewPageEngine(p.pageMgr, sessions, profilerun.DefaultPageModel)
	logger := slog.New(slog.NewTextHandler(errOut, nil))
	page.Log = logger
	page.SessionModel = func(session string) string { return sessionModel(p.store, dir, session) }

	admin, err := randomToken()
	if err != nil {
		p.pageMgr.Close()
		return nil, fmt.Errorf("make the tool gateway's admin token: %w", err)
	}
	tokens := &toolgateway.Tokens{}
	ro := &toolgateway.ReadonlyServer{Home: home, Token: admin, Tokens: tokens,
		Log:    log.New(errOut, "tool gateway: ", log.LstdFlags),
		Listen: net.Listen, Build: mcpproxy.BuildReadonly, Serve: mcpproxy.Serve, Rebuild: mcpproxy.BuildReadonlyBackends}
	gw := &lazyGateway{tokens: tokens, start: ro.Start, refresh: ro.Refresh}

	launcher := &sandboxlaunch.Launcher{Backend: sandboxlaunch.NewCLIBackend(sandboxlaunch.ExecRunner(), filepath.Join(home, ".aimux", "sandboxes"))}
	compile := func(name string) (profile.Compiled, error) {
		user, _ := profile.LoadDir(dir) // a bad file is skipped; the catalog logs it
		m, err := profile.Resolve(name, user)
		if err != nil {
			return profile.Compiled{}, err
		}
		pins, err := toolgateway.PinStore{Path: toolgateway.DefaultPinsPath(home)}.Load()
		if err != nil {
			return profile.Compiled{}, err
		}
		return profile.Compile(m, profile.PinnedSets(pins), profile.GatewayAddr{})
	}
	p.sandbox = profilerun.NewSandboxEngine(launcher, compile, gw.get)
	p.sandbox.ArchiveDir = filepath.Join(home, ".aimux", "archive")
	p.sandbox.OnStarted = p.store.MarkStarted
	p.sandbox.Log = logger
	p.engines = map[string]profilerun.Engine{"acp": page, "sandbox": p.sandbox}
	return func() {
		gw.close()
		p.pageMgr.Close()
	}, nil
}

// adopt makes the sandbox sessions in the store known to the sandbox
// engine again (a restart). A started one resumes. One whose first turn
// never started cannot (claude never made its id) and its page is not
// stored, so the engine drops it, and it is hidden here so its page starts
// a new session rather than reaching the dropped one. A store that cannot be
// read adopts nothing; the router reports it on every turn.
func (p *profileWiring) adopt(errOut io.Writer) {
	if p.sandbox == nil {
		return
	}
	all, err := p.store.All()
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "profile sessions: %v\n", err)
		return
	}
	for id, m := range all {
		if runs, err := p.runs(m.Profile); err != nil || runs != "sandbox" {
			continue
		}
		p.sandbox.Adopt(id, m, nil) // the store keeps no page
		if !m.Started && !m.Hidden {
			m.Hidden = true
			if err := p.store.Put(id, m); err != nil {
				_, _ = fmt.Fprintf(errOut, "profile session %s: hide the session that never started: %v\n", id, err)
			}
		}
	}
}

// engineFor is the engine for profile's Manifest.Runs.
func (p *profileWiring) engineFor(name string) (profilerun.Engine, error) {
	runs, err := p.runs(name)
	if err != nil {
		return nil, fmt.Errorf("profile %s: %w", name, err)
	}
	e := p.engines[runs]
	if e == nil {
		return nil, fmt.Errorf("profile %s: no engine runs %q sessions here", name, runs)
	}
	return e, nil
}

// newSession is the API's NewProfileSession: a new session on the engine
// for m's profile.
func (p *profileWiring) newSession(ctx context.Context, m profilerun.Meta, page string) (string, error) {
	e, err := p.engineFor(m.Profile)
	if err != nil {
		return "", err
	}
	id, err := e.New(ctx, m, page)
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	if p.owner == nil {
		p.owner = map[string]profilerun.Engine{}
	}
	p.owner[id] = e
	p.mu.Unlock()
	return id, nil
}

// cancelSession is the API's CancelProfileSession: it stops a session
// newSession made (its record failed, so the router does not know it).
func (p *profileWiring) cancelSession(id string) error {
	p.mu.Lock()
	e := p.owner[id]
	delete(p.owner, id)
	p.mu.Unlock()
	if e == nil {
		return fmt.Errorf("session %s was not started here", id)
	}
	return e.Cancel(id)
}

// owns reports whether pid is an adapter process of the page engine, which
// holds its session in Claude's registry without being a terminal.
func (p *profileWiring) owns(pid int) bool { return p.pageMgr != nil && p.pageMgr.Owns(pid) }

// running reports whether an engine has a run of session.
func (p *profileWiring) running(id string) bool {
	return (p.pageMgr != nil && p.pageMgr.Running(id)) || (p.sandbox != nil && p.sandbox.Running(id))
}

// sessionModel is the model.name of session's resolved profile (user
// profiles from dir), "" when it has none or cannot be resolved; the
// router resolved the same profile just before the turn, so a failure here
// only means the engine's default.
func sessionModel(store *profilerun.Store, dir, session string) string {
	m, ok := store.Get(session)
	if !ok {
		return ""
	}
	user, _ := profile.LoadDir(dir) // a bad file is skipped, as everywhere profiles resolve
	man, err := profile.Resolve(m.Profile, user)
	if err != nil {
		return ""
	}
	return man.Model.Name
}

// archived is the archived transcript of a sandbox profile session (the
// engine copies it to ArchiveDir after each turn), for the session detail
// (LocalIndex.Archived); "" for any other session or none archived yet.
func (p *profileWiring) archived(id string) string {
	if p.sandbox == nil {
		return ""
	}
	m, ok := p.store.Get(id)
	if !ok {
		return ""
	}
	if runs, err := p.runs(m.Profile); err != nil || runs != "sandbox" {
		return ""
	}
	return discovery.FindSessionFile(id, p.sandbox.ArchiveDir)
}

// reap stops the page engine's adapters idle past their timeout.
func (p *profileWiring) reap() {
	if p.pageMgr != nil {
		p.pageMgr.Reap()
	}
}

// randomToken is 32 random bytes in hex.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

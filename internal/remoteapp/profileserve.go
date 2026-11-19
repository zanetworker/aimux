package remoteapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/clientscope"
	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/macconfirm"
	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/profilerun"
	"github.com/zanetworker/aimux/internal/remoteapi"
	"github.com/zanetworker/aimux/internal/sandboxlaunch"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// profileWiring is remote serve's profile sessions: the catalog the API
// serves, the store, the engines by Manifest.Runs and the router the
// broker sends through (its fallbacks are set by build).
type profileWiring struct {
	router  *profilerun.Router
	store   *profilerun.Store
	catalog func() (map[string]profile.Manifest, error)
	runs    func(name string) (string, error)
	engines map[string]profilerun.Engine
	pageMgr *acprun.Manager                                                                 // nil with injected engines
	sandbox *profilerun.SandboxEngine                                                       // nil with injected engines
	tools   func() remoteapi.ToolsView                                                      // GET /v1/tools; nil with injected engines
	pins    *toolgateway.PinStore                                                           // POST /v1/tools/approve; nil with injected engines
	relist  func(ctx context.Context, source, version string) (remoteapi.RelistView, error) // the worker's fresh listing for an approval

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
// the store, the engines' session start and cancel, the store's health,
// the step-up of sessions without a profile and the tool sets' view.
func (p *profileWiring) Install(api *remoteapi.API, scopes clientscope.Scopes, stepUpLegacy bool) {
	api.Profiles, api.Scopes, api.ProfileSessions, api.Tools, api.Pins = p.servedCatalog, scopes, p.store, p.tools, p.pins
	if p.relist != nil {
		api.Relist = p.relist
	}
	// The mac-confirm step-up: a native dialog on this Mac, which a client
	// can ask for but never answer. None elsewhere.
	if runtime.GOOS == "darwin" {
		api.MacConfirm = macconfirm.System.Confirm
	}
	api.NewProfileSession, api.CancelProfileSession = p.newSession, p.cancelSession
	api.ProfileHealth, api.StepUpLegacy = p.router.Health, stepUpLegacy
	api.ProfileSessionLive = p.live
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
	p.router = &profilerun.Router{Store: p.store, Engines: p.engines, Runs: p.runs,
		Admit: func(name string) error { _, err := resolveChecked(dir, name); return err }}
	p.adopt(errOut)
	return p, closeAll, nil
}

// buildEngines builds the real page, sandbox and run engines into p.
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

	gwLog := log.New(errOut, "tool gateway: ", log.LstdFlags)
	migrateToolSources(home, gwLog.Printf)
	tg := newToolGateway(home, gwLog)
	snaps := tg.snaps
	p.tools = tg.tools
	p.pins = &toolgateway.PinStore{Path: toolgateway.DefaultPinsPath(home)}
	p.relist = func(ctx context.Context, source, version string) (remoteapi.RelistView, error) {
		res, err := tg.worker.Relist(ctx, version, []string{source})
		if err != nil {
			return remoteapi.RelistView{}, err
		}
		// As GET /v1/tools reads listings: one from another endpoint than
		// the registry names now, or whose relist failed, is not listed.
		live, listed := toolgateway.LiveFor(tg.snaps.turn().Registry(), res.Observed)[source]
		return remoteapi.RelistView{Version: res.Version, Live: live, Listed: listed}, nil
	}

	launcher := &sandboxlaunch.Launcher{Backend: sandboxlaunch.NewCLIBackend(sandboxlaunch.ExecRunner(), filepath.Join(home, ".aimux", "sandboxes"))}
	compile := func(name string) (profile.Compiled, error) {
		c, err := compileProfile(snaps.turn(), dir, name)
		if err == nil {
			gwLog.Printf("profile %s compiled at tool snapshot %s", name, c.Snapshot.Short())
		}
		return c, err
	}
	p.sandbox = profilerun.NewSandboxEngine(launcher, compile, tg.gateway)
	p.sandbox.ArchiveDir = filepath.Join(home, ".aimux", "archive")
	p.sandbox.OnStarted = p.store.MarkStarted
	p.sandbox.Review = tg.review(dir)
	p.sandbox.GatewayCheck = openshellGatewayCheck
	p.sandbox.Log = logger
	// research-with-work ("runs: run"): planner and reader in sandboxes,
	// the writer on the page engine; Resolve checks roles as the catalog does.
	run := &profilerun.RunEngine{Planner: p.sandbox, Reader: p.sandbox, Writer: page, Compile: compile,
		Resolve: func(name string) (profile.Manifest, error) { return resolveChecked(dir, name) },
		Store:   p.store, Log: logger, ArchiveDir: p.sandbox.ArchiveDir}
	p.engines = map[string]profilerun.Engine{"acp": page, "sandbox": p.sandbox, "run": run}
	return func() {
		p.pageMgr.Close()
	}, nil
}

// toolGatewayParts is the tool gateway as remote serve's profile wiring
// uses it: the turns' snapshot reloads, the gateway worker's lease table
// (over its control socket) and GET /v1/tools' view.
type toolGatewayParts struct {
	snaps   *toolSnapshots
	tools   func() remoteapi.ToolsView
	gateway func() (string, profilerun.Leases, error)
	worker  toolgateway.ControlClient
}

// observedTimeout bounds GET /v1/tools' read of the worker's listings.
const observedTimeout = 2 * time.Second

// newToolGateway is remote serve's side of the gateway: it compiles turns
// against its own reload of home's registry and pins, puts each turn's
// lease on the gateway worker (aimux mcp proxy gateway serve) and never
// serves tools itself, so it never opens the gateway's port.
func newToolGateway(home string, gwLog *log.Logger) toolGatewayParts {
	store := toolgateway.NewStore(home)
	snaps := &toolSnapshots{store: store, files: snapshotFiles(home), log: gwLog.Printf}
	worker := toolgateway.ControlClient{Path: toolgateway.ControlSocketPath(home)}
	// GET /v1/tools sees the snapshot the next turn compiles against and
	// what the worker last listed; with no worker every source is unknown.
	tools := func() remoteapi.ToolsView {
		snap := snaps.turn()
		ctx, cancel := context.WithTimeout(context.Background(), observedTimeout)
		defer cancel()
		obs, err := worker.Observed(ctx)
		if err != nil {
			obs = nil
		}
		reg := snap.Registry()
		return remoteapi.ToolsView{Snapshot: snap, Live: toolgateway.LiveFor(reg, obs), RelistFailed: toolgateway.RelistFailedFor(reg, obs), LiveErr: err}
	}
	gateway := func() (string, profilerun.Leases, error) { return toolgateway.ReadonlyURL, worker, nil }
	return toolGatewayParts{snaps: snaps, tools: tools, gateway: gateway, worker: worker}
}

// review is the sandbox engine's Review over the profiles in dir: the tools
// of the turn's profile's sets the worker lists but that need review, in
// one line, and the granted servers on which all of them are held back
// (left out of the turn). Nothing when the worker cannot be asked: its
// last listings are read, never a fresh listing, so a slow backend never
// holds up the turn.
func (tg toolGatewayParts) review(dir string) func(c profile.Compiled) profilerun.TurnReview {
	return func(c profile.Compiled) profilerun.TurnReview {
		user, _ := profile.LoadDir(dir) // a bad file is skipped, as everywhere profiles resolve
		m, err := profile.Resolve(c.Profile, user)
		if err != nil || len(m.Tools) == 0 {
			return profilerun.TurnReview{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), observedTimeout)
		defer cancel()
		obs, err := tg.worker.Observed(ctx)
		if err != nil {
			return profilerun.TurnReview{}
		}
		reg := c.Snapshot.Registry()
		held := toolgateway.HeldBack(reg, toolgateway.PinsOf(c.Snapshot), toolgateway.LiveFor(reg, obs), m.Tools)
		left := toolgateway.LeftOut(held, c.Grant.Scope())
		return profilerun.TurnReview{Notice: toolgateway.HeldBackNotice(held, left), LeftOut: left,
			Sets: toolgateway.HeldSets(reg, held, m.Tools)}
	}
}

// migrateToolSources carries readonly.json into tool-sources.yaml once, as
// remote serve starts: snapshot reloads (turns, GET /v1/tools) only read.
// A failure is logged; the reloads then read readonly.json in memory and
// report its problem.
func migrateToolSources(home string, logf func(format string, args ...any)) {
	old, reg := toolreg.LegacyPath(home), toolreg.DefaultPath(home)
	m, err := toolreg.MigrateReport(old, reg)
	switch {
	case err != nil:
		logf("migrate %s: %v", old, err)
	case m.Migrated:
		logf("migrated %s to %s (the old file is kept as %s.migrated)", old, reg, old)
		for _, d := range m.Dropped {
			logf("migrated tool config to %s; %s", reg, d)
		}
	}
}

// toolSnapshots is the one place remote serve reloads the tool-source
// registry and pins: the turns ask it for their snapshot, and it reloads
// the store when one of files changed since the last reload (time, size or
// inode; a missing file is a state too), and at most once per
// snapshotReloadEvery otherwise, as a backstop. The gateway then applies
// what changed on the turn's get. A reload that changed them, or failed, is
// logged.
type toolSnapshots struct {
	store *toolreg.Store
	files []string // snapshotFiles: what a reload reads
	log   func(format string, args ...any)
	now   func() time.Time // nil: time.Now

	mu       sync.Mutex
	reloaded time.Time // zero: never
	stamp    string    // files' stamp at the last reload
}

// snapshotReloadEvery is the backstop reload interval when no file looks
// changed (the gateway worker reloads at the same rate).
const snapshotReloadEvery = 60 * time.Second

// snapshotFiles is what the store reads under home: the registry, the
// readonly.json it migrates from, and the pins.
func snapshotFiles(home string) []string {
	return []string{toolreg.DefaultPath(home), toolreg.LegacyPath(home), toolgateway.DefaultPinsPath(home)}
}

// stampOf is each file's modification time, size and inode, or "missing".
func stampOf(files []string) string {
	var b strings.Builder
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil {
			fmt.Fprintf(&b, "%s:missing;", f)
			continue
		}
		fmt.Fprintf(&b, "%s:%d:%d:%d;", f, fi.ModTime().UnixNano(), fi.Size(), inode(fi))
	}
	return b.String()
}

// turn is the snapshot a turn compiles against, reloading it first when a
// file changed or snapshotReloadEvery has passed since the last reload.
func (s *toolSnapshots) turn() toolreg.Snapshot {
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	stamp := stampOf(s.files)
	s.mu.Lock()
	t := now()
	due := s.reloaded.IsZero() || stamp != s.stamp || t.Sub(s.reloaded) >= snapshotReloadEvery
	if due {
		s.reloaded, s.stamp = t, stamp
	}
	s.mu.Unlock()
	if due {
		snap, changed, err := s.store.Reload()
		switch {
		case err != nil:
			s.log("tool snapshot %s: %v; no tool is granted or served until it is fixed", snap.Short(), err)
		case changed:
			s.log("tool snapshot %s (registry and pins reloaded)", snap.Short())
		}
	}
	return s.store.Current()
}

// resolveChecked resolves the profile name (user profiles in dir, then the
// built-ins) and checks its roles, as the catalog admits it: no new turn
// starts under a profile whose roles do not check (Router.Admit).
func resolveChecked(dir, name string) (profile.Manifest, error) {
	user, _ := profile.LoadDir(dir) // a bad file is skipped; the catalog logs it
	m, err := profile.Resolve(name, user)
	if err != nil {
		return profile.Manifest{}, err
	}
	return m, profile.CheckRoles(m, user)
}

// compileProfile compiles the profile name (user profiles in dir, then the
// built-ins) against snap. A registry that did not load leaves every set
// unavailable and its error is added to the compile error.
func compileProfile(snap toolreg.Snapshot, dir, name string) (profile.Compiled, error) {
	user, _ := profile.LoadDir(dir) // a bad file is skipped; the catalog logs it
	m, err := profile.Resolve(name, user)
	if err != nil {
		return profile.Compiled{}, err
	}
	c, err := profile.CompileAt(m, snap, profile.GatewayAddr{})
	if err != nil && snap.Err() != nil {
		return profile.Compiled{}, fmt.Errorf("%w (%v)", err, snap.Err())
	}
	return c, err
}

// adopt makes the sandbox sessions in the store known to the sandbox
// engine again (a restart). Run sessions are not adopted: the run engine
// holds them in memory only, and live says so to page reuse. A started one resumes. One whose first turn
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

// live reports whether a recorded profile session can still run here, for
// page reuse: false only for a run session the run engine does not hold
// (lost when remote serve restarted, or another remote serve's). It reads
// the store and writes nothing, so two serve processes sharing the store
// never mark each other's sessions.
func (p *profileWiring) live(id string) bool {
	m, ok := p.store.Get(id)
	if !ok {
		return true
	}
	if runs, err := p.runs(m.Profile); err != nil || runs != "run" {
		return true
	}
	e, ok := p.engines["run"].(profilerun.Checker)
	return ok && e.Check(id) == nil
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
	// A run's own transcript is read whatever its profile is now (removed
	// or renamed): history does not depend on the profile resolving.
	if f := profilerun.RunTranscriptPath(p.sandbox.ArchiveDir, id); discovery.ValidSessionID(id) && fileExists(f) {
		return f
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

// openshellGatewayCheck is the sandbox engine's OpenShell gateway check:
// openshell -g local status, which never starts the gateway. Only a status
// that ran and failed says the gateway is down; an openshell that cannot be
// started (not on PATH) says nothing about it, so the failure keeps its own
// reason. A status the probe's own deadline killed also reads as down;
// accepted: the next failure's check corrects it.
func openshellGatewayCheck(ctx context.Context) error {
	err := exec.CommandContext(ctx, "openshell", "-g", "local", "status").Run() //nolint:gosec // G204: fixed command, no shell
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return err
	}
	return nil
}

// turnLinks is a run session's link set per turn, read from its own
// transcript (LocalIndex.TurnLinks); nil for any other session.
func (p *profileWiring) turnLinks(id string) map[int64][]string {
	if p.sandbox == nil || !discovery.ValidSessionID(id) {
		return nil
	}
	if _, ok := p.store.Get(id); !ok {
		return nil
	}
	f := profilerun.RunTranscriptPath(p.sandbox.ArchiveDir, id)
	if !fileExists(f) {
		return nil
	}
	return profilerun.RunTranscriptLinks(f)
}

// fileExists reports a regular file at path.
func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

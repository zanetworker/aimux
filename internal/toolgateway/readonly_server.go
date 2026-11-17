package toolgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"net"
	"reflect"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// ReadonlyAddr is where the read-only tool gateway listens on the Mac;
// ReadonlyURL is the same listener as a sandbox reaches it.
const (
	ReadonlyAddr = "127.0.0.1:7390"
	ReadonlyURL  = "http://host.containers.internal:7390"
)

// ReadonlyServer serves the allowlisted read-only MCP servers on
// ReadonlyAddr behind bearer tokens: Token, an admin credential (aimux
// browser verify, the browser host's work lane), and Tokens, the session
// tokens the sandbox engine issues, each limited to its own tools. Status
// and Reload stay off so a token grants nothing else. It serves Store's
// snapshot of the registry and pins; it never reads those files itself,
// so what it serves and what the compiler grants come from one snapshot.
// It remembers the backends that were down so Refresh can add them while
// it runs. A backend that stays up but changes its tools is noticed by a
// call's own live listing or by Refresh's relisting, both in the
// background (one at a time, never holding up a turn), so the changed tool
// stops being listed and Observed reports it.
type ReadonlyServer struct {
	Store  *toolreg.Store
	Token  string
	Tokens mcpproxy.TokenAuthorizer
	Log    *log.Logger
	Listen func(network, addr string) (net.Listener, error)
	Build  func(ctx context.Context, cfg mcpproxy.ReadonlyConfig, pins mcpproxy.ToolPins, reject func(server, tool string, err error)) (map[string]*server.MCPServer, *mcpproxy.Pool, map[string]error, error)
	Serve  func(ctx context.Context, ln net.Listener, servers map[string]*server.MCPServer, pool *mcpproxy.Pool, opt mcpproxy.DaemonOptions) error
	// Rebuild retries backends that were down (mcpproxy.BuildReadonlyBackends);
	// nil turns Refresh off.
	Rebuild func(ctx context.Context, cfg mcpproxy.ReadonlyConfig, names []string, pins mcpproxy.ToolPins, pool *mcpproxy.Pool, reject func(server, tool string, err error)) (map[string]*server.MCPServer, map[string]error)
	// Starter is how a changed registry's backends start on the running
	// pool (nil: mcpproxy.ReadonlyStarter, what BuildReadonly uses).
	Starter func(cfg mcpproxy.ReadonlyConfig) mcpproxy.Starter
	// Runner binds the runner sandbox compiled from a snapshot to each
	// source (mcpproxy.ReadonlyConfig.Tools); nil starts no cli sources.
	Runner func(snap toolreg.Snapshot) func(source string) mcpproxy.ToolRunner

	applied atomic.Pointer[toolreg.Snapshot] // the snapshot the servers were built from

	mu     sync.Mutex
	cfg    mcpproxy.ReadonlyConfig
	pool   *mcpproxy.Pool
	pins   SnapshotPins
	add    func(name string, srv *server.MCPServer) // set once Serve runs
	remove func(name string) bool                   // set once Serve runs
	down   []string
	all    []string        // every backend built or found down
	ctx    context.Context // Start's serving context; ends at stop

	obsMu     sync.Mutex
	observed  map[string]Observed          // what the builds and calls listed, by backend
	built     map[string]map[string]string // each served backend's tools (name -> pin) as built
	pending   map[string]bool              // backends with a recheck queued or running
	coalesced map[string]int               // notifications folded into a queued recheck, logged when it ends
	dirty     map[string]bool              // pending backends changed again since their recheck began listing
	stopped   bool                         // stop began: no background work is started any more

	relistMu  sync.Mutex     // held by a background relist or recheck: one at a time
	relisting atomic.Bool    // a minute relist is queued or running
	bg        sync.WaitGroup // background relists and rechecks
}

// recheckTimeout bounds the background rebuild of one backend whose
// listing changed.
const recheckTimeout = 15 * time.Second

// Observed is one source as the gateway last listed it while building its
// servers: its allowed live tools, or why it could not be listed, and the
// registry endpoint it was listed from.
type Observed struct {
	LiveSource
	Endpoint string
	// RelistFailed: the last relist of this served backend failed (or
	// timed out); LiveSource is from the listing before, and the backend is
	// still served under its call guard.
	RelistFailed bool
}

// Observed is what the gateway last listed of each backend (a build, a
// rebuild, Refresh's relisting, or a call's own check), copied; a backend it never listed (it has not started,
// or the build did not list) is absent: unknown. It opens no connection.
func (r *ReadonlyServer) Observed() map[string]Observed {
	r.obsMu.Lock()
	defer r.obsMu.Unlock()
	out := make(map[string]Observed, len(r.observed))
	for n, o := range r.observed {
		if o.Tools != nil {
			o.Tools = maps.Clone(o.Tools)
		}
		out[n] = o
	}
	return out
}

// LiveFor is obs as Review's live listings under reg: what was listed from
// another endpoint than reg now names, of a source reg no longer has, or
// whose last relist failed, is left out (unknown).
func LiveFor(reg toolreg.Registry, obs map[string]Observed) map[string]LiveSource {
	out := map[string]LiveSource{}
	for n, o := range obs {
		if src, ok := reg.Sources[n]; ok && src.Endpoint() == o.Endpoint && !o.RelistFailed {
			out[n] = o.LiveSource
		}
	}
	return out
}

// RelistFailedFor is the sources of reg whose last relist failed while the
// gateway still serves them: unknown now, not down.
func RelistFailedFor(reg toolreg.Registry, obs map[string]Observed) map[string]bool {
	out := map[string]bool{}
	for n, o := range obs {
		if src, ok := reg.Sources[n]; ok && src.Endpoint() == o.Endpoint && o.RelistFailed {
			out[n] = true
		}
	}
	return out
}

// observing is pins with hooks: one keeps the catalog its Filter is given,
// and record files it with errs under snap's endpoints: each of names is
// down (errs), or listed when a catalog was seen (a build that never
// filtered lists nothing, so its backends stay unknown). Backends cfg no
// longer configures are dropped. The other hears each listing a call's
// check makes (see listed), and a backend's tools/list_changed queues a
// recheck of it (see changed).
func (r *ReadonlyServer) observing(snap toolreg.Snapshot, cfg mcpproxy.ReadonlyConfig, pins SnapshotPins) (SnapshotPins, func(names []string, errs map[string]error)) {
	var mu sync.Mutex
	var seen *mcpproxy.Catalog
	pins.seen = func(cat *mcpproxy.Catalog) {
		mu.Lock()
		seen = cat
		mu.Unlock()
	}
	reg := snap.Registry()
	served := pins.Pins
	pins.listed = func(server string, tools []json.RawMessage) {
		r.listed(snap.Version(), reg.Sources[server].Endpoint(), cfg.Allow, served, server, tools)
	}
	pins.onChanged = r.changed
	return pins, func(names []string, errs map[string]error) {
		mu.Lock()
		cat := seen
		mu.Unlock()
		r.obsMu.Lock()
		defer r.obsMu.Unlock()
		if r.observed == nil {
			r.observed = map[string]Observed{}
		}
		keep := backendNames(cfg)
		for n := range r.observed {
			if !slices.Contains(keep, n) {
				delete(r.observed, n)
			}
		}
		for _, n := range names {
			o := Observed{Endpoint: reg.Sources[n].Endpoint()}
			switch err, down := errs[n]; {
			case down:
				o.Err = err
			case cat != nil:
				o.Tools = toolsOf(cat.Servers[n])
			default:
				continue
			}
			r.observed[n] = o
		}
	}
}

// listed files a call's live listing of server as Observed and, when the
// tools it would serve differ from what its server was built with, queues
// a rebuild of that backend. A listing made under another snapshot than
// the served one (a server being replaced) is ignored.
func (r *ReadonlyServer) listed(version, endpoint string, allow mcpproxy.Allowlist, pins Pins, server string, raws []json.RawMessage) {
	if r.Applied().Version() != version {
		return
	}
	cat := &mcpproxy.Catalog{Servers: map[string]mcpproxy.CatalogEntry{server: {Tools: raws}}}
	tools := toolsOf(cat.Allowed(allow).Servers[server])
	r.obsMu.Lock()
	if r.observed == nil {
		r.observed = map[string]Observed{}
	}
	r.observed[server] = Observed{LiveSource: LiveSource{Tools: tools}, Endpoint: endpoint}
	changed := !maps.Equal(servedTools(tools, pins[server]), r.built[server])
	r.obsMu.Unlock()
	if changed {
		r.queueRecheck(server, false)
	}
}

// changed hears a backend's tools/list_changed (SnapshotPins.Changed has
// already invalidated its listing) and queues a recheck. It logs the first
// notification of a batch; the rest are counted and logged once when the
// recheck finishes. It does not block.
func (r *ReadonlyServer) changed(server string) {
	r.queueRecheck(server, true)
}

// queueRecheck runs recheck(server) in the background unless one is
// already queued or running, so a burst of changes relists it once; one
// running is marked dirty so it lists once more. After stop began it does
// nothing (the Add happens under obsMu, so never after stop's Wait).
func (r *ReadonlyServer) queueRecheck(server string, notified bool) {
	r.obsMu.Lock()
	defer r.obsMu.Unlock()
	if r.stopped {
		return
	}
	if notified {
		if r.pending[server] {
			if r.coalesced == nil {
				r.coalesced = map[string]int{}
			}
			r.coalesced[server]++
		} else {
			r.Log.Printf("read-only: tools changed: %s (notification)", server)
		}
	}
	if r.pending[server] {
		if r.dirty == nil {
			r.dirty = map[string]bool{}
		}
		r.dirty[server] = true
		return
	}
	if r.pending == nil {
		r.pending = map[string]bool{}
	}
	r.pending[server] = true
	r.bg.Add(1)
	go func() {
		defer r.bg.Done()
		r.recheck(server)
	}()
}

// addBackground counts one background job on r.bg unless stop began, and
// reports whether it did.
func (r *ReadonlyServer) addBackground() bool {
	r.obsMu.Lock()
	defer r.obsMu.Unlock()
	if r.stopped {
		return false
	}
	r.bg.Add(1)
	return true
}

// recheck rebuilds server after a call's listing changed what it would
// serve, or the backend said its tools changed; it does nothing once the
// gateway stopped or server is not served. A change queued while it lists
// (dirty) makes it list once more, since that listing may predate it.
func (r *ReadonlyServer) recheck(server string) {
	for {
		func() {
			r.relistMu.Lock()
			defer r.relistMu.Unlock()
			r.obsMu.Lock()
			delete(r.dirty, server) // this listing covers what came before it
			r.obsMu.Unlock()
			r.relistNow([]string{server})
		}()
		r.obsMu.Lock()
		again := r.dirty[server] && !r.stopped
		delete(r.dirty, server)
		var folded int
		if !again {
			delete(r.pending, server)
			folded = r.coalesced[server]
			delete(r.coalesced, server)
		}
		r.obsMu.Unlock()
		if folded > 0 {
			r.Log.Printf("read-only: tools changed: %s (%d more notifications coalesced)", server, folded)
		}
		if !again {
			return
		}
	}
}

// servedTools is the tools (name -> pin) a server built from tools under
// pins exposes: those whose live pin equals the served one.
func servedTools(tools map[string]json.RawMessage, pins map[string]string) map[string]string {
	out := map[string]string{}
	for name, raw := range tools {
		if h, err := PinTool(raw); err == nil && pins[name] == h {
			out[name] = h
		}
	}
	return out
}

// markBuilt records what each of names' servers was built with, from
// Observed and pins; a name not served is forgotten.
func (r *ReadonlyServer) markBuilt(names []string, servers map[string]*server.MCPServer, pins Pins) {
	r.obsMu.Lock()
	defer r.obsMu.Unlock()
	if r.built == nil {
		r.built = map[string]map[string]string{}
	}
	for _, n := range names {
		if _, ok := servers[n]; ok {
			r.built[n] = servedTools(r.observed[n].Tools, pins[n])
		} else {
			delete(r.built, n)
		}
	}
}

// toolsOf is a catalog entry's tools by name.
func toolsOf(e mcpproxy.CatalogEntry) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, raw := range e.Tools {
		var t struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &t) == nil && t.Name != "" {
			out[t.Name] = raw
		}
	}
	return out
}

// Applied is the snapshot the running servers were built from (the zero
// Snapshot before Start). Tokens.Current reads it.
func (r *ReadonlyServer) Applied() toolreg.Snapshot {
	if s := r.applied.Load(); s != nil {
		return *s
	}
	return toolreg.Snapshot{}
}

// Stale reports whether Store holds another snapshot than the one served,
// so a Refresh would apply it.
func (r *ReadonlyServer) Stale() bool {
	return r.Store.Current().Version() != r.Applied().Version()
}

// configOf is snap's registry as the servers' config; problems are logged
// and leave out what they affect.
func (r *ReadonlyServer) configOf(snap toolreg.Snapshot) mcpproxy.ReadonlyConfig {
	if err := snap.Err(); err != nil {
		r.Log.Printf("read-only: %v; failing closed: serving no tools from it until it is fixed", err)
	}
	reg := snap.Registry()
	for _, m := range []map[string]error{reg.SourceErrors, reg.SetErrors} {
		for _, n := range slices.Sorted(maps.Keys(m)) {
			r.Log.Printf("read-only: skipped: %v", m[n])
		}
	}
	cfg := mcpproxy.ReadonlyConfigFrom(reg)
	if r.Runner != nil {
		cfg.Tools = r.Runner(snap)
	}
	return cfg
}

func (r *ReadonlyServer) reject(server, tool string, _ error) {
	r.Log.Printf("rejected: %s/%s", server, tool)
}

// Start builds the servers from Store's current snapshot (loading it if
// none is loaded), listens on ReadonlyAddr and serves until the returned
// stop is called or ctx ends. Backends that are down, sets the registry
// dropped, and a registry that does not load at all are logged, not fatal:
// the gateway then serves what is left, which may be nothing.
func (r *ReadonlyServer) Start(ctx context.Context) (stop func(), err error) {
	snap := r.Store.Current()
	cfg, pins := r.configOf(snap), GatewayPins(snap)
	if len(pins.Pins) == 0 {
		r.Log.Printf("read-only: no pinned tools: run aimux tools status, then aimux tools approve")
	}
	pins, observe := r.observing(snap, cfg, pins)
	servers, pool, errs, err := r.Build(ctx, cfg, pins, r.reject)
	if err != nil {
		r.Log.Printf("read-only: build servers: %v", err)
		return nil, err
	}
	observe(backendNames(cfg), errs)
	r.markBuilt(backendNames(cfg), servers, pins.Pins)
	down := logDown(r.Log, errs)
	ln, err := r.Listen("tcp", ReadonlyAddr)
	if err != nil {
		if pool != nil {
			pool.Close()
		}
		r.Log.Printf("read-only: listen on %s: %v", ReadonlyAddr, err)
		return nil, fmt.Errorf("listen on %s: %w", ReadonlyAddr, err)
	}
	sctx, cancel := context.WithCancel(ctx)
	r.obsMu.Lock()
	r.stopped = false
	r.obsMu.Unlock()
	r.mu.Lock()
	r.cfg, r.pool, r.pins, r.down, r.add, r.remove, r.all, r.ctx = cfg, pool, pins, down, nil, nil, names(servers, down), sctx
	r.applied.Store(&snap)
	r.mu.Unlock()
	done := make(chan struct{})
	opt := mcpproxy.DaemonOptions{Token: r.Token, Tokens: r.Tokens, AddServers: func(add func(string, *server.MCPServer), remove func(string) bool) {
		r.mu.Lock()
		r.add, r.remove = add, remove
		r.mu.Unlock()
	}}
	go func() {
		defer close(done)
		if err := r.Serve(sctx, ln, servers, pool, opt); err != nil {
			r.Log.Printf("read-only: serve: %v", err)
		}
	}()
	r.Log.Printf("read-only: serving %d servers on %s, snapshot %s", len(servers), ReadonlyAddr, snap.Short())
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			r.Log.Printf("read-only: server did not stop within 5s")
		}
		r.obsMu.Lock()
		r.stopped = true // no background work starts from here on
		r.obsMu.Unlock()
		r.bg.Wait() // their rebuilds end with the cancelled context
	}, nil
}

// names is the sorted union of the servers' names and down.
func names(servers map[string]*server.MCPServer, down []string) []string {
	all := append([]string(nil), down...)
	for n := range servers {
		all = append(all, n)
	}
	sort.Strings(all)
	return slices.Compact(all)
}

// Refresh brings the running servers to Store's current snapshot. With the
// snapshot unchanged it only starts a background relist (relistNow: the
// backends that are up, rebuilding each whose tools changed, and the ones
// that were down) and returns at once, so a turn never waits on a backend;
// at most one relist is queued or running. It reads no file: the snapshot is
// reloaded in one place (remote serve's turns). The new snapshot is
// published (Applied, which Tokens.Current reads) before any server is
// swapped, so during the rebuild a token from an older snapshot has already
// lost the tools whose pins changed, and a backend not yet rebuilt still
// checks the older pins: the window fails closed. A changed registry keeps
// the one pool Serve owns: new backends start from the new config, and only
// the moved or dropped backends are retired (a call in flight finishes
// first). Every backend is then rebuilt with the new pins; one that comes
// back with no pinned tool, or fails to rebuild, has its endpoint removed
// (fail closed). Failures are logged. Callers rate limit it.
func (r *ReadonlyServer) Refresh(ctx context.Context) {
	if r.Rebuild == nil {
		return
	}
	if r.Store.Current().Version() == r.Applied().Version() {
		r.relistInBackground()
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.add == nil {
		return
	}
	snap := r.Store.Current()
	if snap.Version() == r.Applied().Version() {
		return // applied by a turn that held the lock first
	}
	r.applied.Store(&snap)
	cfg := r.configOf(snap)
	r.pins = GatewayPins(snap)
	// Bound runner sources restart on every snapshot (moved), so only a
	// config without them can take the pins-only path.
	if sameConfig(cfg, r.cfg) && cfg.Tools == nil {
		r.Log.Printf("read-only: snapshot %s (pins changed); rebuilding %d backends", snap.Short(), len(r.all))
		r.apply(ctx, r.all)
		return
	}
	r.Log.Printf("read-only: snapshot %s (tool sources changed); rebuilding every backend", snap.Short())
	keep := backendNames(cfg)
	if r.pool != nil {
		start := mcpproxy.ReadonlyStarter
		if r.Starter != nil {
			start = r.Starter
		}
		r.pool.SetStarter(start(cfg))
		for _, n := range r.all {
			if moved(r.cfg, cfg, n) {
				r.pool.RetireServer(n)() // one with a call in flight closes after it
			}
		}
	}
	for _, n := range r.all {
		switch {
		case !slices.Contains(keep, n):
			if r.remove(n) {
				r.Log.Printf("read-only: backend %s removed (no longer in tool-sources.yaml)", n)
			}
		case moved(r.cfg, cfg, n):
			// Not found until the rebuild adds it back, so no call reaches
			// the new endpoint through a server built for the old one.
			r.remove(n)
		}
	}
	r.cfg, r.all = cfg, keep
	r.apply(ctx, keep)
}

// backendNames is every backend cfg configures, sorted.
func backendNames(cfg mcpproxy.ReadonlyConfig) []string {
	all := slices.Collect(maps.Keys(cfg.HTTP))
	for n := range cfg.Notes {
		all = append(all, n)
	}
	for n := range cfg.Runner {
		if cfg.StartsRunner(n) {
			all = append(all, n)
		}
	}
	sort.Strings(all)
	return slices.Compact(all)
}

// sameConfig compares a and b without their runner binding: a func is
// never DeepEqual, and the binding follows the snapshot, which callers
// compare by version.
func sameConfig(a, b mcpproxy.ReadonlyConfig) bool {
	a.Tools, b.Tools = nil, nil
	return reflect.DeepEqual(a, b)
}

// moved reports whether backend n is reached differently (or not at all)
// under next than under prev. A runner source always moves: next binds the
// runner sandbox compiled from a new snapshot, whose policy may differ.
func moved(prev, next mcpproxy.ReadonlyConfig, n string) bool {
	if _, ok := next.Runner[n]; ok && next.Tools != nil {
		return true
	}
	ph, pok := prev.HTTP[n]
	nh, nok := next.HTTP[n]
	pr, prok := prev.Notes[n]
	nr, nrok := next.Notes[n]
	return pok != nok || prok != nrok || !reflect.DeepEqual(ph, nh) || pr != nr
}

// apply rebuilds the named backends with r.pins on the running pool,
// replacing or removing each endpoint, and records which are down. The
// snapshot is accepted already, so its pins become the ones the pool's
// backends report tools/list_changed to; a background relist never
// publishes, so one superseded by this snapshot cannot take that back.
func (r *ReadonlyServer) apply(ctx context.Context, names []string) {
	if len(names) == 0 {
		return
	}
	pins, observe := r.observing(r.Applied(), r.cfg, r.pins)
	if r.pool != nil {
		r.pool.PublishToolPins(pins)
	}
	servers, errs := r.Rebuild(ctx, r.cfg, names, pins, r.pool, r.reject)
	observe(names, errs)
	r.markBuilt(names, servers, r.pins.Pins)
	for _, n := range names {
		if srv, ok := servers[n]; ok {
			r.add(n, srv)
			r.Log.Printf("read-only: backend %s now available", n)
			continue
		}
		// Fail closed: a backend left with no pinned tool, or whose rebuild
		// failed, must not keep serving under its old pins and guard.
		if r.remove(n) {
			r.Log.Printf("read-only: backend %s removed (no pinned tools or rebuild failed)", n)
		}
	}
	r.down = make([]string, 0, len(errs))
	for n := range errs {
		r.down = append(r.down, n)
	}
	sort.Strings(r.down)
}

// relistInBackground starts relistNow over every backend unless a relist
// is already queued or running.
func (r *ReadonlyServer) relistInBackground() {
	if !r.relisting.CompareAndSwap(false, true) {
		return
	}
	if !r.addBackground() {
		r.relisting.Store(false)
		return
	}
	go func() {
		defer r.bg.Done()
		defer r.relisting.Store(false)
		r.relistMu.Lock()
		defer r.relistMu.Unlock()
		r.relistNow(nil)
	}()
}

// WaitBackground waits for the background relists and rebuilds started so
// far (tests; stop waits for them too).
func (r *ReadonlyServer) WaitBackground() { r.bg.Wait() }

// relistNow lists again the served backends among only (nil: all of them,
// and the ones that were down too), without holding r.mu while it lists,
// so turns and a newer snapshot never wait for it. Its results are dropped
// when the snapshot or registry moved meanwhile. A served backend whose
// tools changed since it was built is rebuilt, or removed when none still
// matches its pin (fail closed); one whose relist failed keeps serving
// under its call guard, and Observed marks it RelistFailed (unknown, not
// down), leaving the others' views as listed. A backend that was down is
// added when it now builds, as Refresh used to on the turn. r.relistMu is
// held.
func (r *ReadonlyServer) relistNow(only []string) {
	r.mu.Lock()
	if r.add == nil || r.Rebuild == nil || r.ctx == nil || r.ctx.Err() != nil {
		r.mu.Unlock()
		return
	}
	snap, cfg, pins, pool, base := r.Applied(), r.cfg, r.pins, r.pool, r.ctx
	var up, down []string
	for _, n := range r.all {
		switch {
		case slices.Contains(r.down, n):
			if only == nil {
				down = append(down, n)
			}
		case only == nil || slices.Contains(only, n):
			up = append(up, n)
		}
	}
	r.mu.Unlock()
	names := append(append([]string(nil), up...), down...)
	if len(names) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(base, recheckTimeout)
	defer cancel()
	p, record := r.observing(snap, cfg, pins)
	servers, errs := r.Rebuild(ctx, cfg, names, p, pool, r.reject)

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.add == nil || r.Applied().Version() != snap.Version() || !sameConfig(cfg, r.cfg) {
		return // superseded by a newer snapshot, which rebuilt what it needed
	}
	var listed, failed []string
	for _, n := range up {
		if errs[n] != nil {
			failed = append(failed, n)
		} else {
			listed = append(listed, n)
		}
	}
	record(append(listed, down...), errs)
	r.markRelistFailed(failed, errs)
	for _, n := range listed {
		r.obsMu.Lock()
		same := maps.Equal(servedTools(r.observed[n].Tools, r.pins.Pins[n]), r.built[n])
		r.obsMu.Unlock()
		if same {
			continue
		}
		r.markBuilt([]string{n}, servers, r.pins.Pins)
		if srv, ok := servers[n]; ok {
			r.add(n, srv)
			r.Log.Printf("read-only: backend %s rebuilt: its live tools changed", n)
		} else if r.remove(n) {
			r.Log.Printf("read-only: backend %s removed: no tool still matches its pin", n)
		}
	}
	if only != nil {
		return
	}
	r.markBuilt(down, servers, r.pins.Pins)
	var still []string
	for _, n := range down {
		if srv, ok := servers[n]; ok {
			r.add(n, srv)
			r.Log.Printf("read-only: backend %s now available", n)
		} else {
			still = append(still, n)
		}
	}
	r.down = still
}

// markRelistFailed flags names' Observed views RelistFailed, keeping what
// was last listed of them, and logs why.
func (r *ReadonlyServer) markRelistFailed(names []string, errs map[string]error) {
	r.obsMu.Lock()
	defer r.obsMu.Unlock()
	for _, n := range names {
		o := r.observed[n]
		o.RelistFailed = true
		if r.observed == nil {
			r.observed = map[string]Observed{}
		}
		r.observed[n] = o
		r.Log.Printf("read-only: relist of %s failed (still served under its pin check): %s", n, clip(errs[n].Error(), 200))
	}
}

// logDown logs each unavailable backend and returns their names, sorted.
func logDown(l *log.Logger, errs map[string]error) []string {
	names := make([]string, 0, len(errs))
	for n := range errs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		msg := errs[n].Error()
		if r := []rune(msg); len(r) > 200 {
			msg = string(r[:200]) + "..."
		}
		l.Printf("read-only: backend %s unavailable: %s", n, msg)
	}
	return names
}

package mcpproxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/zanetworker/aimux/internal/netguard"
)

// DaemonOptions configures Serve. Zero values give the defaults.
type DaemonOptions struct {
	// IdleTimeout is how long a backend may sit unused before it is stopped.
	// Default 10 minutes.
	IdleTimeout time.Duration
	// ReapEvery is how often idle backends are checked. Default 1 minute.
	ReapEvery time.Duration
	// Status, when set, adds GET /status and POST /status/probe.
	Status *Monitor
	// Reload, when set, adds POST /reload.
	Reload *ReloadOptions
	// Log receives one line per catalog tool a reload could not serve.
	// nil discards them.
	Log io.Writer
	// Token, when set, replaces the loopback-only guard with a bearer-token
	// guard on every route (for a client that reaches the daemon through a
	// container gateway, with Host host.containers.internal). Empty keeps
	// netguard.LocalOnly.
	Token string
	// Tokens, when set, makes the bearer token an issued session token
	// (toolgateway.Tokens): it may reach only the /mcp/<server> endpoints in
	// its allow map (others get 403), sees only its tools in tools/list and
	// is refused any other tool call. Like Token, it replaces the
	// loopback-only guard. Token, when also set, stays valid as an
	// unrestricted admin credential.
	Tokens TokenAuthorizer
	// AddServers, when set, is called once Serve routes requests, with
	// functions that add (or replace) and remove a served endpoint while it
	// runs; remove ends the endpoint's client sessions and reports whether
	// it was served.
	AddServers func(add func(name string, srv *server.MCPServer), remove func(name string) bool)

	// lookupPeers and processIdentity find and check the process that owns a
	// client session (tests); default lsof and kill 0.
	lookupPeers     func(port int) map[int]int
	processIdentity func(pid int) (int64, bool)
}

// sessionTransportTTL is how long the HTTP transport keeps an idle
// session's state: only a leak backstop now. When a session's per-session
// backends stop is decided by its owner process (sessionOwners).
const sessionTransportTTL = 24 * time.Hour

// EndpointPath is where the daemon serves the named server.
func EndpointPath(server string) string { return "/mcp/" + url.PathEscape(server) }

// endpoints routes /mcp/<escaped server> to that server's Streamable HTTP
// handler. The map is replaced under mu, so endpoints can be added and
// removed while requests are served.
type endpoints struct {
	ttl time.Duration
	// customGuard: the bearer token vets requests, so the transport's own
	// loopback Host check (which rejects host.containers.internal) is off.
	customGuard bool
	owners      *sessionOwners // records each request's session and client
	conns       *connTracker   // which connection each request came on
	binds       *sessionBinds  // which token created each session; nil without Tokens
	mu          sync.RWMutex
	m           map[string]*endpoint
}

type endpoint struct {
	mcp  *server.MCPServer
	http *server.StreamableHTTPServer
}

func newEndpoints(ttl time.Duration) *endpoints {
	return &endpoints{ttl: ttl, m: map[string]*endpoint{}}
}

// add serves srv as name, replacing (and closing) an endpoint of that name.
func (e *endpoints) add(name string, srv *server.MCPServer) {
	// Clients such as Claude Code exit without ending their MCP session;
	// expiring idle sessions runs the unregister hook, which stops their
	// per-session backends. A client that comes back after expiry gets a new
	// session, as the MCP spec requires.
	ep := &endpoint{mcp: srv, http: server.NewStreamableHTTPServer(srv, server.WithSessionIdleTTL(e.ttl),
		server.WithDisableLocalhostProtection(e.customGuard))}
	e.mu.Lock()
	old := e.m[name]
	e.m[name] = ep
	e.mu.Unlock()
	if old != nil {
		old.close()
	}
}

// remove stops serving name and ends its client sessions.
func (e *endpoints) remove(name string) bool {
	e.mu.Lock()
	ep := e.m[name]
	delete(e.m, name)
	e.mu.Unlock()
	if ep != nil {
		ep.close()
	}
	return ep != nil
}

// setTools replaces the tools name serves; its sessions stay connected
// and are told the list changed.
func (e *endpoints) setTools(name string, tools []server.ServerTool) {
	if ep := e.get(name); ep != nil {
		ep.mcp.SetTools(tools...)
	}
}

func (e *endpoints) get(name string) *endpoint {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.m[name]
}

func (e *endpoints) names() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]string, 0, len(e.m))
	for n := range e.m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (e *endpoints) closeAll() {
	for _, n := range e.names() {
		e.remove(n)
	}
}

// close ends the endpoint's sessions (running their unregister hooks) and
// stops its idle-session sweeper.
// terminate ends one client session the way the client's DELETE would, so
// the transport drops its state for it now rather than at the backstop TTL.
// The transport is stateless about ids, so a later request with the same id
// is served as a fresh session, as after the TTL.
func (ep *endpoint) terminate(session string) {
	req := httptest.NewRequest(http.MethodDelete, "/", nil)
	req.Header.Set("Mcp-Session-Id", session)
	ep.http.ServeHTTP(httptest.NewRecorder(), req)
}

func (ep *endpoint) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = ep.http.Shutdown(ctx)
}

// lookup returns the server name a request path addresses: exactly one
// escaped segment after /mcp/.
func lookupName(r *http.Request) (string, bool) {
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), "/mcp/")
	if !ok || rest == "" || strings.Contains(rest, "/") {
		return "", false
	}
	name, err := url.PathUnescape(rest)
	return name, err == nil
}

// handler serves /mcp/<server> requests and passes the rest to next. It
// routes before any ServeMux, so escaped slashes and dot segments in a
// server name are never cleaned into another path.
func (e *endpoints) handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := r.URL.EscapedPath(); p != "/mcp" && !strings.HasPrefix(p, "/mcp/") {
			next.ServeHTTP(w, r)
			return
		}
		name, ok := lookupName(r)
		var ep *endpoint
		if ok {
			ep = e.get(name)
		}
		if ep == nil {
			http.NotFound(w, r)
			return
		}
		if e.owners != nil {
			e.owners.seen(r.Header.Get("Mcp-Session-Id"), name, r.RemoteAddr, e.conns.idOf(r))
		}
		// A reload may remove this endpoint between the lookup and here. The
		// request then finishes on the old server, whose sessions remove
		// already closed; a session it creates now is never swept (that
		// server's sweeper is stopped), but it holds no backend: the removed
		// server's slots are closed and its endpoint answers 404 from now on.
		ep.http.ServeHTTP(w, r)
	})
}

// Serve exposes each of servers over Streamable HTTP at EndpointPath(name)
// on ln, for local clients only, and stops idle backends in pool. With
// opt.Reload, POST /reload changes the served set in place, never
// restarting the process. It returns when ctx is cancelled, after closing
// every backend.
func Serve(ctx context.Context, ln net.Listener, servers map[string]*server.MCPServer, pool *Pool, opt DaemonOptions) error {
	if opt.IdleTimeout <= 0 {
		opt.IdleTimeout = 10 * time.Minute
	}
	if opt.ReapEvery <= 0 {
		opt.ReapEvery = time.Minute
	}
	if opt.lookupPeers == nil {
		opt.lookupPeers = lsofPeers
	}
	if opt.processIdentity == nil {
		opt.processIdentity = processIdentity
	}
	port := 0
	if a, ok := ln.Addr().(*net.TCPAddr); ok {
		port = a.Port
	}
	eps := newEndpoints(sessionTransportTTL)
	eps.customGuard = opt.Token != "" || opt.Tokens != nil
	eps.conns = newConnTracker()
	if opt.Tokens != nil {
		eps.binds = newSessionBinds()
	}
	eps.owners = &sessionOwners{
		open:     eps.conns.open,
		idle:     opt.IdleTimeout,
		now:      time.Now,
		lookup:   func() map[int]int { return opt.lookupPeers(port) },
		identity: opt.processIdentity,
		busy:     pool.SessionBusy,
	}
	for name, srv := range servers {
		eps.add(name, srv)
	}
	if opt.AddServers != nil {
		opt.AddServers(eps.add, eps.remove)
	}
	mux := http.NewServeMux()
	if opt.Status != nil {
		opt.Status.setEndpoints(func(name string) bool { return eps.get(name) != nil })
		opt.Status.handle(mux)
	}
	if opt.Reload != nil {
		r := &reloader{opt: *opt.Reload, cur: opt.Reload.Current, eps: eps, pool: pool, mon: opt.Status, log: opt.Log}
		r.handle(mux)
		if opt.Reload.Changes != nil {
			go r.follow(ctx, opt.Reload.Changes)
		}
	}
	guard := netguard.LocalOnly
	if opt.Tokens != nil {
		guard = func(next http.Handler) http.Handler { return tokenGuard(opt.Token, opt.Tokens, eps.binds, next) }
	} else if opt.Token != "" {
		guard = func(next http.Handler) http.Handler { return netguard.BearerOnly(opt.Token, next) }
	}
	httpSrv := &http.Server{Handler: guard(eps.handler(mux)), ReadHeaderTimeout: 10 * time.Second,
		ConnState: eps.conns.state, ConnContext: eps.conns.context}

	go func() {
		t := time.NewTicker(opt.ReapEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pool.Reap(opt.IdleTimeout)
				for _, s := range eps.owners.sweep() {
					// Its owner exited, or it idled with no owner found. Ending it in
					// the transport drops the transport's state for it and, through
					// the unregister hook (newServer), stops its per-session backends.
					if eps.binds != nil {
						eps.binds.forget(s.ID)
					}
					if ep := eps.get(s.Endpoint); ep != nil {
						ep.terminate(s.ID)
					} else {
						pool.CloseSession(s.ID) // its endpoint was removed by a reload
					}
				}
			}
		}
	}()

	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()
	select {
	case err := <-errc:
		eps.closeAll()
		pool.Close()
		return err
	case <-ctx.Done():
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	eps.closeAll() // ends long-lived streams, so Shutdown need not wait for them
	err := httpSrv.Shutdown(shutCtx)
	pool.Close()
	if errors.Is(err, context.DeadlineExceeded) {
		// Long-lived streams did not finish; the backends are closed anyway.
		_ = httpSrv.Close()
		return nil
	}
	return err
}

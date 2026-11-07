package mcpproxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
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
}

// EndpointPath is where the daemon serves the named server.
func EndpointPath(server string) string { return "/mcp/" + url.PathEscape(server) }

// endpoints routes /mcp/<escaped server> to that server's Streamable HTTP
// handler. The map is replaced under mu, so endpoints can be added and
// removed while requests are served.
type endpoints struct {
	ttl time.Duration
	mu  sync.RWMutex
	m   map[string]*endpoint
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
	ep := &endpoint{mcp: srv, http: server.NewStreamableHTTPServer(srv, server.WithSessionIdleTTL(e.ttl))}
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
	eps := newEndpoints(opt.IdleTimeout)
	for name, srv := range servers {
		eps.add(name, srv)
	}
	mux := http.NewServeMux()
	if opt.Status != nil {
		opt.Status.setEndpoints(func(name string) bool { return eps.get(name) != nil })
		opt.Status.handle(mux)
	}
	if opt.Reload != nil {
		r := &reloader{opt: *opt.Reload, cur: opt.Reload.Current, eps: eps, pool: pool, mon: opt.Status, log: opt.Log}
		r.handle(mux)
	}
	httpSrv := &http.Server{Handler: netguard.LocalOnly(eps.handler(mux)), ReadHeaderTimeout: 10 * time.Second}

	go func() {
		t := time.NewTicker(opt.ReapEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pool.Reap(opt.IdleTimeout)
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

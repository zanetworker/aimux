package toolgateway

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/zanetworker/aimux/internal/mcpproxy"
)

// ReadonlyAddr is where the read-only tool gateway listens on the Mac;
// ReadonlyURL is the same listener as a sandbox reaches it.
const (
	ReadonlyAddr = "127.0.0.1:7390"
	ReadonlyURL  = "http://host.containers.internal:7390"
)

// ReadonlyConfigPath is the read-only gateway's config,
// ~/.aimux/wingman/readonly.json (written by `aimux browser setup`).
func ReadonlyConfigPath(home string) string {
	return filepath.Join(home, ".aimux", "wingman", "readonly.json")
}

// ReadonlyServer serves the allowlisted read-only MCP servers on
// ReadonlyAddr behind bearer tokens: Token, an admin credential (aimux
// browser verify, the browser host's work lane), and Tokens, the session
// tokens the sandbox engine issues, each limited to its own tools. Status
// and Reload stay off so a token grants nothing else. It remembers the
// backends that were down so Refresh can add them while it runs.
type ReadonlyServer struct {
	Home   string
	Token  string
	Tokens mcpproxy.TokenAuthorizer
	Log    *log.Logger
	Listen func(network, addr string) (net.Listener, error)
	Build  func(ctx context.Context, cfg mcpproxy.ReadonlyConfig, pins mcpproxy.ToolPins, reject func(server, tool string, err error)) (map[string]*server.MCPServer, *mcpproxy.Pool, map[string]error, error)
	Serve  func(ctx context.Context, ln net.Listener, servers map[string]*server.MCPServer, pool *mcpproxy.Pool, opt mcpproxy.DaemonOptions) error
	// Rebuild retries backends that were down (mcpproxy.BuildReadonlyBackends);
	// nil turns Refresh off.
	Rebuild func(ctx context.Context, cfg mcpproxy.ReadonlyConfig, names []string, pins mcpproxy.ToolPins, pool *mcpproxy.Pool, reject func(server, tool string, err error)) (map[string]*server.MCPServer, map[string]error)

	mu     sync.Mutex
	cfg    mcpproxy.ReadonlyConfig
	pool   *mcpproxy.Pool
	pins   Pins
	add    func(name string, srv *server.MCPServer) // set once Serve runs
	remove func(name string) bool                   // set once Serve runs
	down   []string
	all    []string // every backend Start built or found down
}

func (r *ReadonlyServer) reject(server, tool string, _ error) {
	r.Log.Printf("rejected: %s/%s", server, tool)
}

// Start loads the config (defaults when missing) and the pinned tools,
// builds the servers, listens on ReadonlyAddr and serves until the returned
// stop is called or ctx ends. Backends that are down are logged, not fatal.
func (r *ReadonlyServer) Start(ctx context.Context) (stop func(), err error) {
	cfgPath := ReadonlyConfigPath(r.Home)
	cfg, err := mcpproxy.LoadReadonlyConfig(cfgPath)
	if errors.Is(err, os.ErrNotExist) {
		r.Log.Printf("read-only: %s missing, using defaults (run `aimux browser setup`)", cfgPath)
		cfg, err = mcpproxy.DefaultReadonlyConfig(r.Home), nil
	}
	if err != nil {
		r.Log.Printf("read-only: %v", err)
		return nil, err
	}
	pins, err := PinStore{Path: DefaultPinsPath(r.Home)}.Load()
	if err != nil {
		r.Log.Printf("read-only: %v", err)
		return nil, err
	}
	if len(pins) == 0 {
		r.Log.Printf("read-only: no pinned tools: run aimux browser setup")
	}
	servers, pool, errs, err := r.Build(ctx, cfg, pins, r.reject)
	if err != nil {
		r.Log.Printf("read-only: build servers: %v", err)
		return nil, err
	}
	down := logDown(r.Log, errs)
	ln, err := r.Listen("tcp", ReadonlyAddr)
	if err != nil {
		if pool != nil {
			pool.Close()
		}
		r.Log.Printf("read-only: listen on %s: %v", ReadonlyAddr, err)
		return nil, fmt.Errorf("listen on %s: %w", ReadonlyAddr, err)
	}
	r.mu.Lock()
	all := append([]string(nil), down...)
	for n := range servers {
		all = append(all, n)
	}
	sort.Strings(all)
	r.cfg, r.pool, r.pins, r.down, r.add, r.remove, r.all = cfg, pool, pins, down, nil, nil, all
	r.mu.Unlock()
	sctx, cancel := context.WithCancel(ctx)
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
	r.Log.Printf("read-only: serving %d servers on %s", len(servers), ReadonlyAddr)
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			r.Log.Printf("read-only: server did not stop within 5s")
		}
	}, nil
}

// Refresh reloads the pins and retries the backends that were down, adding
// the ones now up to the running server. When the pins changed, every
// backend is rebuilt with them, replacing its running server, so a re-pin
// takes effect without a restart; a rebuilt backend that comes back with
// no pinned tool, or fails to rebuild, has its endpoint removed. Failures
// are logged. Callers rate limit it.
func (r *ReadonlyServer) Refresh(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.add == nil || r.Rebuild == nil {
		return
	}
	names := r.down
	if pins, err := (PinStore{Path: DefaultPinsPath(r.Home)}).Load(); err != nil {
		r.Log.Printf("read-only: reload pins: %v", err)
	} else if !reflect.DeepEqual(pins, r.pins) {
		r.pins, names = pins, r.all
		r.Log.Printf("read-only: pins changed; rebuilding %d backends", len(names))
	}
	if len(names) == 0 {
		return
	}
	servers, errs := r.Rebuild(ctx, r.cfg, names, r.pins, r.pool, r.reject)
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

package mcpproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"sort"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/zanetworker/aimux/internal/clipack"
	"github.com/zanetworker/aimux/internal/toolreg"
	"github.com/zanetworker/aimux/internal/wikitools"
)

// ReadonlyConfig is the view of the tool-source registry the read-only
// servers are built from (ReadonlyConfigFrom).
type ReadonlyConfig struct {
	HTTP  map[string]HTTPServerConfig // backends reached over HTTP, by name
	Notes map[string]string           // local notes backends: name -> root directory
	// Runner holds the cli and http-openapi sources by name; they are
	// neither HTTP nor notes backends.
	Runner map[string]toolreg.Source
	Allow  Allowlist // tools each backend may expose
	// Tools binds the runner sandbox to a source. With it, cli sources
	// start (NewCLIBackend); without it, or for http-openapi sources until
	// they are supported, runner sources serve no tools.
	Tools func(source string) ToolRunner
}

// errRunnerNotStarted is why a runner source serves no tools.
var errRunnerNotStarted = errors.New("runner sources are not started yet")

// startsRunner reports whether cfg starts runner source n.
func (cfg ReadonlyConfig) startsRunner(n string) bool {
	src, ok := cfg.Runner[n]
	return ok && cfg.Tools != nil && src.Type == toolreg.TypeCLI
}

// ReadonlyConfigFrom is reg's sources and sets as a ReadonlyConfig: each
// mcp-http source an HTTP backend, each local-notes source a notes backend,
// and each backend allowed the sorted union of the tools its sets list.
// Entries Load dropped are not in reg, so they are not served.
func ReadonlyConfigFrom(reg toolreg.Registry) ReadonlyConfig {
	cfg := ReadonlyConfig{HTTP: map[string]HTTPServerConfig{}, Notes: map[string]string{}, Runner: map[string]toolreg.Source{}, Allow: Allowlist{}}
	for n, s := range reg.Sources {
		switch s.Type {
		case toolreg.TypeMCPHTTP:
			cfg.HTTP[n] = HTTPServerConfig{URL: s.URL}
		case toolreg.TypeLocalNotes:
			cfg.Notes[n] = s.Root
		case toolreg.TypeCLI, toolreg.TypeHTTPOpenAPI:
			cfg.Runner[n] = s
		}
	}
	tools := map[string]map[string]bool{}
	for _, set := range reg.Sets {
		if tools[set.Source] == nil {
			tools[set.Source] = map[string]bool{}
		}
		for _, t := range set.Tools {
			tools[set.Source][t] = true
		}
	}
	for src, ts := range tools {
		cfg.Allow[src] = slices.Sorted(maps.Keys(ts))
	}
	return cfg
}

// ToolPins pins the tools a read-only server exposes. toolgateway.Pins is
// the implementation; it lives there because toolgateway imports this
// package.
type ToolPins interface {
	// Filter keeps only the catalog tools whose live definition matches
	// its pin.
	Filter(cat *Catalog) *Catalog
	// Guard returns the check run before each call, which lists a server's
	// live tools with list to compare them with their pins.
	Guard(list func(ctx context.Context, server string) ([]json.RawMessage, error)) func(ctx context.Context, server, tool string) error
	// Changed hears that server sent tools/list_changed, so its next
	// call's check lists it again. It runs on the backend's notification
	// goroutine and must not block.
	Changed(server string)
}

// toolPinsRef holds a ToolPins for Pool.pins (an atomic.Pointer needs one
// concrete type).
type toolPinsRef struct{ pins ToolPins }

// notifyChanged is the pool's BeforeReady for read-only builds: a backend
// that reports tools/list_changed has it passed to the pins published when
// it arrives (PublishToolPins), since an accepted rebuild swaps them. It is
// subscribed before any caller can use the backend, so a change sent right
// after start is not missed.
func (p *Pool) notifyChanged(server string, b Backend) {
	n, ok := b.(ToolsChangedNotifier)
	if !ok {
		return
	}
	n.OnToolsChanged(func() {
		if ref := p.pins.Load(); ref != nil && ref.pins != nil {
			ref.pins.Changed(server)
		}
	})
}

// BuildReadonly builds one MCP server per live backend, each exposing only
// the tools cfg.Allow lists that are also pinned and still match their pin,
// and re-checking the pin before each call. A nil pins exposes nothing. The
// catalog comes from the live backends; a
// backend that fails (or a missing notes root) is returned in the error map
// and left out of the servers, and is not fatal. rejectLog, when set, hears
// every failed tools/call: a call to a tool outside the allowlist, or a
// backend error. The caller closes the returned Pool.
func BuildReadonly(ctx context.Context, cfg ReadonlyConfig, pins ToolPins, rejectLog func(server, tool string, err error)) (map[string]*server.MCPServer, *Pool, map[string]error, error) {
	names := readonlyNames(cfg)
	pool := readonlyPool(ReadonlyStarter(cfg))
	pool.PublishToolPins(pins)
	servers, errs := BuildReadonlyBackends(ctx, cfg, names, pins, pool, rejectLog)
	return servers, pool, errs, nil
}

// readonlyPool is the pool BuildReadonly serves from: its backends report
// tools/list_changed to the pins last published on it, also those from a
// starter set later (SetStarter). Idle backends are kept (KeepIdle): a
// stopped backend hears no notification, so a change announced while no
// call ran would only be seen at the next relist.
func readonlyPool(start Starter) *Pool {
	var pool *Pool
	pool = NewPool(start, PoolOptions{KeepIdle: true, BeforeReady: func(server string, b Backend) { pool.notifyChanged(server, b) }})
	return pool
}

// PublishToolPins makes pins the ones the pool's backends report
// tools/list_changed to. BuildReadonly publishes its pins; a caller of
// BuildReadonlyBackends publishes the rebuild's pins only once it accepts
// that rebuild, so one superseded meanwhile never takes over from newer
// pins.
func (p *Pool) PublishToolPins(pins ToolPins) {
	p.pins.Store(&toolPinsRef{pins: pins})
}

// ToolPins is the pins last published on the pool (nil: none).
func (p *Pool) ToolPins() ToolPins {
	if ref := p.pins.Load(); ref != nil {
		return ref.pins
	}
	return nil
}

// readonlyNames is every backend cfg starts, sorted.
func readonlyNames(cfg ReadonlyConfig) []string {
	names := slices.Collect(maps.Keys(cfg.HTTP))
	for n := range cfg.Notes {
		names = append(names, n)
	}
	for n := range cfg.Runner {
		if cfg.startsRunner(n) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// runnerNotStarted is the runner sources cfg does not start, each with
// errRunnerNotStarted.
func runnerNotStarted(cfg ReadonlyConfig, errs map[string]error) {
	for n := range cfg.Runner {
		if !cfg.startsRunner(n) {
			errs[n] = errRunnerNotStarted
		}
	}
}

// ReadonlyCatalog lists every configured backend's live tools, unfiltered,
// through the same starter BuildReadonly uses; the allowlist is the
// caller's to apply. A backend that is down is returned in the error map.
func ReadonlyCatalog(ctx context.Context, cfg ReadonlyConfig) (*Catalog, map[string]error) {
	cat := &Catalog{Servers: map[string]CatalogEntry{}}
	errs := RefreshCatalog(ctx, cat, ReadonlyStarter(cfg), readonlyNames(cfg))
	runnerNotStarted(cfg, errs)
	return cat, errs
}

// BuildReadonlyBackends builds servers for the named backends only, on pool
// (from BuildReadonly), for a caller that retries backends that were down. A
// name cfg does not configure is an error in the map. It does not change
// the pins pool's backends report tools/list_changed to: the caller
// publishes pins (PublishToolPins) when it accepts the rebuild.
func BuildReadonlyBackends(ctx context.Context, cfg ReadonlyConfig, names []string, pins ToolPins, pool *Pool, rejectLog func(server, tool string, err error)) (map[string]*server.MCPServer, map[string]error) {
	errs := map[string]error{}
	var live []string
	runnerNotStarted(cfg, errs) // say why they are down
	for _, n := range names {
		root, notes := cfg.Notes[n]
		switch _, ok := cfg.HTTP[n]; {
		case cfg.startsRunner(n):
			live = append(live, n)
		case notes:
			if st, err := os.Stat(root); err != nil {
				errs[n] = fmt.Errorf("notes root: %w", err)
			} else if !st.IsDir() {
				errs[n] = fmt.Errorf("notes root %s is not a directory", root)
			} else {
				live = append(live, n)
			}
		case ok:
			live = append(live, n)
		default:
			if _, ok := cfg.Runner[n]; ok {
				continue // runnerNotStarted said why
			}
			errs[n] = fmt.Errorf("no read-only backend %q is configured", n)
		}
	}
	// Listing through pool leaves each backend running there, subscribed
	// to its notifications before any tool call (a lease's Close only
	// releases it); without a pool the backends are started and stopped.
	start := ReadonlyStarter(cfg)
	if pool != nil {
		start = func(ctx context.Context, name string) (Backend, error) { return pool.Lease(ctx, name) }
	}
	cat := &Catalog{Servers: map[string]CatalogEntry{}}
	for n, err := range RefreshCatalog(ctx, cat, start, live) {
		errs[n] = err
	}
	var opts []ServerOption
	if rejectLog != nil {
		opts = append(opts, WithRejectLog(rejectLog))
	}
	if pins == nil { // fail closed: with no pins nothing is trusted
		return NewServers(&Catalog{}, pool, opts...), errs
	}
	opts = append(opts, WithCallGuard(pins.Guard(poolLister(pool))))
	return NewServers(pins.Filter(cat.Allowed(cfg.Allow)), pool, opts...), errs
}

// poolLister lists a server's live tools through pool, as the call guard
// needs; the listing is bounded so a stuck backend refuses a call instead
// of hanging it.
func poolLister(pool *Pool) func(ctx context.Context, server string) ([]json.RawMessage, error) {
	return func(ctx context.Context, server string) ([]json.RawMessage, error) {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		b, err := pool.Lease(ctx, server)
		if err != nil {
			return nil, err
		}
		defer func() { _ = b.Close() }()
		return b.ListToolsRaw(ctx)
	}
}

// ReadonlyStarter starts cfg's notes backends in process, its cli sources
// in the runner sandbox (cfg.Tools) and the rest over HTTP: the starter
// BuildReadonly gives its pool.
func ReadonlyStarter(cfg ReadonlyConfig) Starter {
	httpStart := HTTPStarter(cfg.HTTP)
	return func(ctx context.Context, name string) (Backend, error) {
		if root, ok := cfg.Notes[name]; ok {
			return wikitools.NewBackend(root, 0), nil
		}
		if src, ok := cfg.Runner[name]; ok {
			if !cfg.startsRunner(name) {
				return nil, errRunnerNotStarted
			}
			pack, err := clipack.Builtin(src.Pack)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			return NewCLIBackend(name, pack, cfg.Tools(name)), nil
		}
		return httpStart(ctx, name)
	}
}

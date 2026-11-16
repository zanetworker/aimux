package mcpproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"sort"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/zanetworker/aimux/internal/toolreg"
	"github.com/zanetworker/aimux/internal/wikitools"
)

// ReadonlyConfig is the view of the tool-source registry the read-only
// servers are built from (ReadonlyConfigFrom).
type ReadonlyConfig struct {
	HTTP  map[string]HTTPServerConfig // backends reached over HTTP, by name
	Notes map[string]string           // local notes backends: name -> root directory
	Allow Allowlist                   // tools each backend may expose
}

// ReadonlyConfigFrom is reg's sources and sets as a ReadonlyConfig: each
// mcp-http source an HTTP backend, each local-notes source a notes backend,
// and each backend allowed the sorted union of the tools its sets list.
// Entries Load dropped are not in reg, so they are not served.
func ReadonlyConfigFrom(reg toolreg.Registry) ReadonlyConfig {
	cfg := ReadonlyConfig{HTTP: map[string]HTTPServerConfig{}, Notes: map[string]string{}, Allow: Allowlist{}}
	for n, s := range reg.Sources {
		switch s.Type {
		case toolreg.TypeMCPHTTP:
			cfg.HTTP[n] = HTTPServerConfig{URL: s.URL}
		case toolreg.TypeLocalNotes:
			cfg.Notes[n] = s.Root
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
	pool := NewPool(ReadonlyStarter(cfg), PoolOptions{})
	servers, errs := BuildReadonlyBackends(ctx, cfg, names, pins, pool, rejectLog)
	return servers, pool, errs, nil
}

// readonlyNames is every backend cfg configures, sorted.
func readonlyNames(cfg ReadonlyConfig) []string {
	names := slices.Collect(maps.Keys(cfg.HTTP))
	for n := range cfg.Notes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ReadonlyCatalog lists every configured backend's live tools, unfiltered,
// through the same starter BuildReadonly uses; the allowlist is the
// caller's to apply. A backend that is down is returned in the error map.
func ReadonlyCatalog(ctx context.Context, cfg ReadonlyConfig) (*Catalog, map[string]error) {
	cat := &Catalog{Servers: map[string]CatalogEntry{}}
	errs := RefreshCatalog(ctx, cat, ReadonlyStarter(cfg), readonlyNames(cfg))
	return cat, errs
}

// BuildReadonlyBackends builds servers for the named backends only, on pool
// (from BuildReadonly), for a caller that retries backends that were down. A
// name cfg does not configure is an error in the map.
func BuildReadonlyBackends(ctx context.Context, cfg ReadonlyConfig, names []string, pins ToolPins, pool *Pool, rejectLog func(server, tool string, err error)) (map[string]*server.MCPServer, map[string]error) {
	errs := map[string]error{}
	var live []string
	for _, n := range names {
		root, notes := cfg.Notes[n]
		switch _, ok := cfg.HTTP[n]; {
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
			errs[n] = fmt.Errorf("no read-only backend %q is configured", n)
		}
	}
	cat := &Catalog{Servers: map[string]CatalogEntry{}}
	for n, err := range RefreshCatalog(ctx, cat, ReadonlyStarter(cfg), live) {
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

// ReadonlyStarter starts cfg's notes backends in process and the rest over
// HTTP: the starter BuildReadonly gives its pool.
func ReadonlyStarter(cfg ReadonlyConfig) Starter {
	httpStart := HTTPStarter(cfg.HTTP)
	return func(ctx context.Context, name string) (Backend, error) {
		if root, ok := cfg.Notes[name]; ok {
			return wikitools.NewBackend(root, 0), nil
		}
		return httpStart(ctx, name)
	}
}

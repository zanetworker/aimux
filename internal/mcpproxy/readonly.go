package mcpproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/zanetworker/aimux/internal/wikitools"
)

const wikiServer = "wiki"

// ReadonlyConfig describes the read-only servers Wingman's sandbox may reach.
type ReadonlyConfig struct {
	HTTP     map[string]HTTPServerConfig // backends reached over HTTP, by name
	WikiRoot string                      // directory the wiki backend serves
	Allow    Allowlist                   // tools each backend may expose
}

// DefaultReadonlyConfig is the standard layout under home.
func DefaultReadonlyConfig(home string) ReadonlyConfig {
	return ReadonlyConfig{
		HTTP: map[string]HTTPServerConfig{
			"slack":  {URL: "http://127.0.0.1:7381/mcp/slack"},
			"jira":   {URL: "http://127.0.0.1:8001/mcp"},
			"google": {URL: "http://127.0.0.1:8000/mcp"},
		},
		WikiRoot: filepath.Join(home, "go/src/github.com/zanetworker/research/wiki"),
		Allow: Allowlist{
			"wiki":   {"wiki_search", "wiki_read"},
			"jira":   {"search_issues_jql", "get_issue_details", "get_comments", "get_jira_issues"},
			"slack":  {"search_messages", "get_thread", "get_thread_messages", "get_channel_history", "get_user_profile"},
			"google": {"search_gmail_messages", "get_gmail_message_content", "get_gmail_thread_content", "search_drive_files", "get_doc_as_markdown"},
		},
	}
}

func marshalReadonlyConfig(cfg ReadonlyConfig) ([]byte, error) {
	return json.MarshalIndent(cfg, "", "  ")
}

// LoadReadonlyConfig reads a ReadonlyConfig from a JSON file.
func LoadReadonlyConfig(path string) (ReadonlyConfig, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is the user's own readonly config file
	if err != nil {
		return ReadonlyConfig{}, err
	}
	var cfg ReadonlyConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ReadonlyConfig{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
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
// backend that fails (or a missing wiki root) is returned in the error map
// and left out of the servers, and is not fatal. rejectLog, when set, hears
// every failed tools/call: a call to a tool outside the allowlist, or a
// backend error. The caller closes the returned Pool.
func BuildReadonly(ctx context.Context, cfg ReadonlyConfig, pins ToolPins, rejectLog func(server, tool string, err error)) (map[string]*server.MCPServer, *Pool, map[string]error, error) {
	names := readonlyNames(cfg)
	pool := NewPool(readonlyStarter(cfg), PoolOptions{})
	servers, errs := BuildReadonlyBackends(ctx, cfg, names, pins, pool, rejectLog)
	return servers, pool, errs, nil
}

// readonlyNames is every backend cfg configures, sorted.
func readonlyNames(cfg ReadonlyConfig) []string {
	names := []string{wikiServer}
	for n := range cfg.HTTP {
		if n != wikiServer {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// ReadonlyCatalog lists every configured backend's live tools, unfiltered,
// through the same starter BuildReadonly uses; the allowlist is the
// caller's to apply. A backend that is down is returned in the error map.
func ReadonlyCatalog(ctx context.Context, cfg ReadonlyConfig) (*Catalog, map[string]error) {
	cat := &Catalog{Servers: map[string]CatalogEntry{}}
	errs := RefreshCatalog(ctx, cat, readonlyStarter(cfg), readonlyNames(cfg))
	return cat, errs
}

// BuildReadonlyBackends builds servers for the named backends only, on pool
// (from BuildReadonly), for a caller that retries backends that were down. A
// name cfg does not configure is an error in the map.
func BuildReadonlyBackends(ctx context.Context, cfg ReadonlyConfig, names []string, pins ToolPins, pool *Pool, rejectLog func(server, tool string, err error)) (map[string]*server.MCPServer, map[string]error) {
	errs := map[string]error{}
	var live []string
	for _, n := range names {
		switch _, ok := cfg.HTTP[n]; {
		case n == wikiServer:
			if st, err := os.Stat(cfg.WikiRoot); err != nil {
				errs[n] = fmt.Errorf("wiki root: %w", err)
			} else if !st.IsDir() {
				errs[n] = fmt.Errorf("wiki root %s is not a directory", cfg.WikiRoot)
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
	for n, err := range RefreshCatalog(ctx, cat, readonlyStarter(cfg), live) {
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

// readonlyStarter starts the wiki backend in process and the rest over HTTP.
func readonlyStarter(cfg ReadonlyConfig) func(ctx context.Context, name string) (Backend, error) {
	httpStart := HTTPStarter(cfg.HTTP)
	return func(ctx context.Context, name string) (Backend, error) {
		if name == wikiServer {
			return wikitools.NewBackend(cfg.WikiRoot, 0), nil
		}
		return httpStart(ctx, name)
	}
}

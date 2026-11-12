package mcpproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mark3labs/mcp-go/server"
)

// ServerSet is what the daemon serves: each server's launch definition,
// the ones install already resolved, and the tool catalog. A server gets
// an endpoint when it has both a definition and a catalog entry.
type ServerSet struct {
	Configs  map[string]ServerConfig
	Resolved []string
	Catalog  *Catalog
}

// served is each served server's catalog tools.
func (s ServerSet) served() map[string][]json.RawMessage {
	out := map[string][]json.RawMessage{}
	if s.Catalog == nil {
		return out
	}
	for name, e := range s.Catalog.Servers {
		if _, ok := s.Configs[name]; ok {
			out[name] = e.Tools
		}
	}
	return out
}

// definitionChanged reports whether name starts differently in next.
func definitionChanged(cur, next ServerSet, name string) bool {
	return !reflect.DeepEqual(cur.Configs[name], next.Configs[name]) ||
		slices.Contains(cur.Resolved, name) != slices.Contains(next.Resolved, name)
}

// ReloadResult is the POST /reload answer: the servers whose endpoint was
// added or removed, and those whose definition changed.
type ReloadResult struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
	Changed []string `json:"changed"`
}

// ReloadOptions enables POST /reload.
type ReloadOptions struct {
	// Current is the set the servers given to Serve were built from.
	Current ServerSet
	// Load re-reads the servers file and catalog.
	Load func() (ServerSet, error)
	// Apply, when set, receives the new set before any backend is closed,
	// so the next start uses the new definitions.
	Apply func(ServerSet)
	// Changes, when set, triggers a reload on each value received
	// (CatalogUpdater.Changes): tools a backend start found are published.
	Changes <-chan struct{}
	// ServerOptions are the options the servers given to Serve were built
	// with (a call guard, a reject log). A reload builds its new and
	// changed servers with them too, so a guarded server stays guarded.
	ServerOptions []ServerOption
}

// reloadToolsPublished, when set (tests), runs right after a changed
// server's new tools are published.
var reloadToolsPublished atomic.Pointer[func(server string)]

// reloader applies a newly loaded ServerSet to the running daemon without
// restarting it.
type reloader struct {
	mu   sync.Mutex // one reload at a time
	opt  ReloadOptions
	cur  ServerSet
	eps  *endpoints
	pool *Pool
	mon  *Monitor
	log  io.Writer // skipped tools; nil discards
}

// reload loads the set and applies the difference: a new server gets an
// endpoint; a removed one loses it and its backends; a changed definition
// retires its backends (calls in flight finish first) so the next call
// starts the new one; changed tools are updated in place. Other servers'
// endpoints, sessions and backends are untouched. A load error changes
// nothing.
func (r *reloader) reload() (ReloadResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	next, err := r.opt.Load()
	if err != nil {
		return ReloadResult{}, err
	}
	old, now := r.cur.served(), next.served()
	res := ReloadResult{Added: []string{}, Removed: []string{}, Changed: []string{}}
	var retools []string
	for name, tools := range now {
		oldTools, ok := old[name]
		switch {
		case !ok:
			res.Added = append(res.Added, name)
		case definitionChanged(r.cur, next, name):
			res.Changed = append(res.Changed, name)
		case ToolsFingerprint(oldTools) != ToolsFingerprint(tools):
			retools = append(retools, name)
		}
	}
	for name := range old {
		if _, ok := now[name]; !ok {
			res.Removed = append(res.Removed, name)
		}
	}
	// Servers without an endpoint can still run (a probe starts every
	// configured server); stop them too when their definition went away
	// or changed, including one that gains its endpoint now, so the new
	// endpoint never forwards to a backend of the old definition.
	var stale []string
	for name := range r.cur.Configs {
		if _, wasServed := old[name]; !wasServed && definitionChanged(r.cur, next, name) {
			stale = append(stale, name)
		}
	}
	sort.Strings(res.Added)
	sort.Strings(res.Removed)
	sort.Strings(res.Changed)

	if r.opt.Apply != nil {
		r.opt.Apply(next)
	}
	for _, name := range res.Removed {
		r.eps.remove(name)
		r.pool.CloseServer(name)
	}
	for _, name := range stale {
		r.pool.CloseServer(name)
	}
	for _, name := range res.Added {
		srv := newServer(name, r.pool, r.opt.ServerOptions...)
		srv.SetTools(r.tools(name, now[name])...)
		r.eps.add(name, srv)
	}
	// A changed server's backends are retired before its new tools are
	// published, so a client acting on tools/list_changed always reaches a
	// backend of the new definition (a call to a tool it dropped gets a
	// clear unknown-tool error there). Calls in flight on a retired backend
	// finish; it closes after them.
	var closeRetired []func()
	for _, name := range res.Changed {
		closeRetired = append(closeRetired, r.pool.RetireServer(name))
	}
	for _, name := range append(res.Changed, retools...) {
		r.eps.setTools(name, r.tools(name, now[name]))
	}
	if hook := reloadToolsPublished.Load(); hook != nil {
		for _, name := range res.Changed {
			(*hook)(name)
		}
	}
	for _, closeIt := range closeRetired {
		closeIt()
	}
	r.cur = next
	if r.mon != nil {
		registered := map[string]string{}
		for name, tools := range now {
			registered[name] = ToolsFingerprint(tools)
		}
		r.mon.setServers(sortedServerNames(next.Configs), registered)
	}
	return res, nil
}

// tools decodes name's catalog tools and logs one line per tool it skips.
func (r *reloader) tools(name string, raw []json.RawMessage) []server.ServerTool {
	tools, skipped := serverTools(name, raw, r.pool, buildConfig(r.opt.ServerOptions))
	if r.log != nil {
		for _, msg := range skipped {
			_, _ = fmt.Fprintf(r.log, "skipped %s\n", msg)
		}
	}
	return tools
}

// handle adds POST /reload to mux. Like a probe, it needs ProbeHeader.
func (r *reloader) handle(mux *http.ServeMux) {
	mux.HandleFunc("POST /reload", func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get(ProbeHeader) == "" {
			http.Error(w, "a reload needs the "+ProbeHeader+" header", http.StatusForbidden)
			return
		}
		res, err := r.reload()
		if err != nil {
			http.Error(w, "reload: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, res)
	})
}

// reloadTimeout bounds ReloadDaemon: closing a changed server's backends
// can take a few seconds each.
const reloadTimeout = time.Minute

// ReloadDaemon asks the daemon on 127.0.0.1:port to re-read its servers
// file and catalog and apply the difference in place.
func ReloadDaemon(port int) (ReloadResult, error) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ctx, cancel := context.WithTimeout(context.Background(), reloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/reload", nil)
	if err != nil {
		return ReloadResult{}, err
	}
	req.Header.Set(ProbeHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ReloadResult{}, fmt.Errorf("reload the MCP proxy on %s: %w", addr, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ReloadResult{}, fmt.Errorf("read the MCP proxy's reload answer from %s: %w", addr, err)
	}
	if resp.StatusCode != http.StatusOK {
		return ReloadResult{}, fmt.Errorf("MCP proxy on %s answered %s: %s", addr, resp.Status, firstLine(strings.TrimSpace(string(body))))
	}
	var res ReloadResult
	if err := json.Unmarshal(body, &res); err != nil {
		return ReloadResult{}, fmt.Errorf("read the MCP proxy's reload answer from %s: %w", addr, err)
	}
	return res, nil
}

// LiveStarter starts stdio servers from definitions a reload can replace.
type LiveStarter struct {
	mu    sync.RWMutex
	start Starter
}

// NewLiveStarter starts servers from cfgs, as StdioStarter does.
func NewLiveStarter(cfgs map[string]ServerConfig, resolved []string) *LiveStarter {
	return &LiveStarter{start: StdioStarter(cfgs, resolved...)}
}

// Set replaces the definitions; starts already under way keep the old ones.
func (l *LiveStarter) Set(cfgs map[string]ServerConfig, resolved []string) {
	s := StdioStarter(cfgs, resolved...)
	l.mu.Lock()
	l.start = s
	l.mu.Unlock()
}

// Start is a Starter using the current definitions.
func (l *LiveStarter) Start(ctx context.Context, server string) (Backend, error) {
	l.mu.RLock()
	s := l.start
	l.mu.RUnlock()
	return s(ctx, server)
}

func sortedServerNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// follow reloads on each value from changes until ctx ends. A failed reload
// is logged; the next change or POST /reload tries again.
func (r *reloader) follow(ctx context.Context, changes <-chan struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-changes:
			if _, err := r.reload(); err != nil && r.log != nil {
				_, _ = fmt.Fprintf(r.log, "reload after a tools change: %v\n", err)
			}
		}
	}
}

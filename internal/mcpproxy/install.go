// Install (install.go) moves Claude Code's stdio MCP servers behind the
// proxy and back, one mcpServers entry per server under its own name, so
// tool names and every permission rule stay as they are.
//
// Install prepares the daemon before it touches Claude's config: it lists
// the moved servers' tools into the catalog, saves their resolved
// definitions (and adds them to the servers file's moved list), starts the
// launchd agent or has a running daemon reload in place, and probes exactly
// those servers. Only then does one atomic jsonedit edit of mcpServers swap
// each moved entry for its HTTP entry. The manifest, written once after
// that edit, records per server the original raw entry and the entry
// written. If install fails before the edit, the daemon's files are put
// back as they were (and a running daemon reloads them); nothing in
// Claude's config changed. Running install again moves only the stdio
// servers not moved yet. Uninstall puts back, in one edit, each entry that
// is still what install wrote, keeps and reports any the owner changed,
// then removes the agent and the manifest.
package mcpproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/zanetworker/aimux/internal/jsonedit"
)

// Install states reported by InstallStatus.
const (
	InstallNotInstalled = "not_installed"
	InstallInPlace      = "installed"
	InstallDrifted      = "drifted"
)

const (
	backupsKept = 10
	statusPoll  = 250 * time.Millisecond
	defaultWait = 10 * time.Second
	editHint    = "moved servers are configured with `aimux mcp proxy edit <server>`, not in ~/.claude.json"
	driftFix    = DriftFix
)

// manifestVersion is the manifest format this aimux writes and reads.
// manifestVersion is the format install writes; LoadManifest also reads
// minManifestVersion (2: before HTTP servers moved).
const (
	manifestVersion    = 3
	minManifestVersion = 2
)

// DriftFix is the advice for a moved server whose entry is no longer its
// proxy URL.
const DriftFix = "run `aimux mcp proxy uninstall`, then `aimux mcp proxy install`"

// errManifestCorrupt marks a manifest that does not parse.
var errManifestCorrupt = errors.New("the install manifest is unreadable")

// Manifest is what install changed, in ~/.aimux/mcp-proxy-install.json
// (owner-only: the original entries hold env values).
type Manifest struct {
	Version int                   `json:"version"`
	Port    int                   `json:"port"`
	Servers map[string]MovedEntry `json:"servers"`
	// Skipped are the stdio servers kept direct with --skip; a re-run keeps
	// them direct until --unskip.
	Skipped []string `json:"skipped,omitempty"`
	// IdleTimeout is what install did to Claude Code's http idle limit in
	// settings.json (idleAdded, idleAddedEnv, or empty: nothing).
	IdleTimeout string `json:"idleTimeout,omitempty"`
	// Hooks is "added" once install added the server hooks to settings.json.
	Hooks string `json:"hooks,omitempty"`
	// Direct are the HTTP servers the last install found need OAuth, so
	// status can name them without a network call.
	Direct []KeptDirect `json:"direct,omitempty"`
}

// MovedEntry is one moved server's mcpServers entry: Original is its raw
// bytes before install (put back verbatim), Written the entry install
// wrote. They are strings because encoding/json reformats a RawMessage.
type MovedEntry struct {
	Original string `json:"original"`
	Written  string `json:"written"`
	// Keychain is the item holding the entry's header values (HTTP servers
	// with headers); Original then has each value replaced by
	// headerPlaceholder.
	Keychain string `json:"keychain,omitempty"`
	// redacted is Original as recorded, set by unredacted when it rebuilt
	// Original from the item (never saved).
	redacted string
	// without: uninstall --without named it (never saved).
	without bool
}

// InstallPlan is what install would do, by name only.
type InstallPlan struct {
	Move    []string // stdio and HTTP servers that would move behind the proxy
	Skip    []string // left direct with --skip
	Moved   []string // already behind the proxy (the manifest)
	Remove  []string // moved, but their entry is gone from the Claude config: install takes them off
	Refresh []string // missing from the catalog (after DryRun: the ones it could not list)
	Listed  []string // DryRun started these once to list their tools into a temporary catalog
	// HTTP are the names in Move that are HTTP servers.
	HTTP []string
	// KeptDirect are HTTP servers this run leaves direct, with the reason.
	KeptDirect []KeptDirect
}

// InstallState is how the install looks now.
type InstallState struct {
	State string   `json:"state"`
	Moved []string `json:"moved,omitempty"`
	// Drifted are moved servers whose entry is no longer their proxy URL.
	Drifted []string `json:"drifted,omitempty"`
	// Removed are moved servers whose entry is gone from the Claude config
	// (the owner removed it); the proxy still serves them, unused.
	Removed []string `json:"removed,omitempty"`
	// NotProxied are stdio servers in mcpServers not behind the proxy.
	NotProxied []string `json:"notProxied,omitempty"`
	// Skipped are servers kept direct with --skip.
	Skipped []string `json:"skipped,omitempty"`
	// KeptDirect are HTTP servers install leaves direct, with the reason
	// (from the config, and the OAuth ones the last install recorded).
	KeptDirect []KeptDirect `json:"keptDirect,omitempty"`
	// UpgradePending: the running daemon's version differs from this aimux.
	UpgradePending bool   `json:"upgradePending,omitempty"`
	DaemonVersion  string `json:"daemonVersion,omitempty"`
	// Hooks: settings.json holds both server hooks.
	Hooks bool `json:"hooks,omitempty"`
}

// Installer moves Claude Code's stdio MCP servers behind the proxy and
// back. Every path, the launchctl runner and the daemon calls are injected.
type Installer struct {
	ClaudeConfig string // ~/.claude.json
	ManifestPath string // ~/.aimux/mcp-proxy-install.json
	BackupDir    string // ~/.aimux/backups
	ServersPath  string // the proxy's saved backend definitions
	CatalogPath  string // the tool catalog
	Settings     string // ~/.claude/settings.json, for the http idle limit and the hooks; empty leaves it alone
	HookBinary   string // the aimux the hooks run; empty adds no hooks
	Port         int    // the daemon's port on 127.0.0.1
	Version      string // this aimux's version, compared with the daemon's
	// Unskip are recorded --skip servers this run moves after all.
	Unskip []string
	// Without are moved servers uninstall gives up the original of (their
	// Keychain item is gone): the proxy entry is removed instead of
	// restored, and a note says to add the server again.
	Without []string

	Agent   Agent
	Status  func() (StatusReport, error)
	Probe   func(servers []string) (StatusReport, error)                // the daemon's POST /status/probe for these servers
	Reload  func() (ReloadResult, error)                                // the daemon's POST /reload
	Refresh func(catalogPath string, servers []string) map[string]error // lists tools and saves them in the catalog at catalogPath
	// RefreshHTTP lists the tools of HTTP servers with their headers held
	// in memory and saves them in the catalog at catalogPath (nil:
	// refreshHTTPCatalog).
	RefreshHTTP func(catalogPath string, entries map[string]HTTPEntry, names []string) map[string]error
	// CheckHTTP checks an HTTP server before it moves (nil: CheckHTTPServer).
	CheckHTTP func(ctx context.Context, url string, headers map[string]string) HTTPReach
	// Vault holds header values (the Keychain); nil moves no HTTP server
	// with headers.
	Vault    VaultStore
	Sessions func() ([]string, error) // ids of open Claude Code sessions; nil skips the note
	Out      io.Writer
	Wait     time.Duration // for the daemon to answer; 0 means 10 s
	Sleep    func(time.Duration)
	Now      func() time.Time // default time.Now; bounds the wait for the daemon

	edit          func(path, key string, fn func(json.RawMessage) (json.RawMessage, error)) error // test hook
	writeManifest func(path string, m *Manifest) error                                            // test hook
}

func (i Installer) out() io.Writer {
	if i.Out == nil {
		return io.Discard
	}
	return i.Out
}

func (i Installer) printf(format string, a ...any) { _, _ = fmt.Fprintf(i.out(), format, a...) }

func (i Installer) sleep(d time.Duration) {
	if i.Sleep != nil {
		i.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (i Installer) now() time.Time {
	if i.Now != nil {
		return i.Now()
	}
	return time.Now()
}

func (i Installer) editKey(path, key string, fn func(json.RawMessage) (json.RawMessage, error)) error {
	if i.edit != nil {
		return i.edit(path, key, fn)
	}
	return jsonedit.EditKeyVerbatim(path, key, fn)
}

func (i Installer) saveManifest(m *Manifest) error {
	if i.writeManifest != nil {
		return i.writeManifest(i.ManifestPath, m)
	}
	return saveManifest(i.ManifestPath, m)
}

// EntryURL is the proxy URL of server's endpoint on port.
func EntryURL(port int, server string) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", port, EndpointPath(server))
}

// entryFor is the mcpServers entry install writes for server.
func (i Installer) entryFor(server string) json.RawMessage {
	v, _ := json.Marshal(struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	}{"http", EntryURL(i.Port, server)})
	return v
}

// Plan reports what Install(skip) would change, touching nothing.
func (i Installer) Plan(skip []string) (InstallPlan, error) {
	p, _, err := i.plan(skip)
	return p, err
}

// DryRun is Plan with the tool listing the real install does first: the
// servers to move that are missing from the catalog are started once each
// to list their tools into a temporary copy of the catalog. Nothing outside
// a temporary directory is written; the copy is removed before it returns.
// Servers that fail to list stay in Refresh.
func (i Installer) DryRun(skip []string) (InstallPlan, error) {
	p, in, err := i.plan(skip)
	if err != nil || len(p.Refresh) == 0 {
		return p, err
	}
	dir, err := os.MkdirTemp("", "aimux-dry-run-*")
	if err != nil {
		return InstallPlan{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	cat, err := LoadCatalog(i.CatalogPath)
	if err != nil {
		return InstallPlan{}, err
	}
	tmp := filepath.Join(dir, filepath.Base(i.CatalogPath))
	if err := cat.Save(tmp); err != nil {
		return InstallPlan{}, err
	}
	failed := i.refreshAll(tmp, p.Refresh, in.http)
	list := p.Refresh
	p.Refresh = nil
	for _, s := range list {
		if failed[s] != nil {
			p.Refresh = append(p.Refresh, s)
		} else {
			p.Listed = append(p.Listed, s)
		}
	}
	return p, nil
}

// planInput is what plan read for Install: the stdio definitions, the
// movable HTTP entries, the manifest and the HTTP servers that need OAuth.
type planInput struct {
	stdio    map[string]ServerConfig
	http     map[string]HTTPEntry
	manifest *Manifest
	oauth    []KeptDirect
}

// plan reads the Claude config, the manifest and the catalog: the stdio and
// HTTP servers to move are those not skipped and not moved already (a moved
// server whose entry is direct again is drift, reported by status, not
// moved twice). HTTP servers not moved or skipped are checked first: one
// that needs OAuth or is down stays direct with its reason.
func (i Installer) plan(skip []string) (InstallPlan, planInput, error) {
	m, err := i.loadManifest()
	if err != nil {
		return InstallPlan{}, planInput{}, err
	}
	stdio, err := LoadClaudeServers(i.ClaudeConfig, nil)
	if err != nil {
		return InstallPlan{}, planInput{}, err
	}
	httpAll, staticKept, err := LoadClaudeHTTPServers(i.ClaudeConfig, i.Port, i.Vault != nil)
	if err != nil {
		return InstallPlan{}, planInput{}, err
	}
	for _, name := range sortedKeys(httpAll) {
		if reason := headersTooLong(i.Vault, name, httpAll[name]); reason != "" {
			staticKept = append(staticKept, KeptDirect{Server: name, Reason: reason})
			delete(httpAll, name)
		}
	}
	known := map[string]bool{}
	for n := range stdio {
		known[n] = true
	}
	for n := range httpAll {
		known[n] = true
	}
	for _, k := range staticKept {
		known[k.Server] = true
	}
	var p InstallPlan
	skipped := map[string]bool{}
	var recorded []string
	if m != nil {
		recorded = m.Skipped
		_, members, err := readMCPServers(i.ClaudeConfig)
		if err != nil {
			return InstallPlan{}, planInput{}, err
		}
		for _, name := range sortedKeys(m.Servers) {
			if _, ok := members[name]; ok {
				p.Moved = append(p.Moved, name)
			} else {
				p.Remove = append(p.Remove, name)
			}
		}
	}
	for _, s := range i.Unskip {
		if !contains(recorded, s) {
			return InstallPlan{}, planInput{}, fmt.Errorf("--unskip %s: not a server install kept direct (kept direct: %s)", s, listOrNone(recorded))
		}
	}
	for _, s := range recorded {
		if known[s] && !contains(i.Unskip, s) {
			skipped[s] = true // a recorded skip stays until --unskip
		}
	}
	for _, s := range skip {
		if !known[s] {
			return InstallPlan{}, planInput{}, fmt.Errorf("--skip %s: no MCP server by that name in %s", s, i.ClaudeConfig)
		}
		skipped[s] = true
	}
	p.Skip = sortedKeys(skipped)
	pending := func(s string) bool { _, moved := m.server(s); return !skipped[s] && !moved }
	for _, s := range sortedKeys(stdio) {
		if pending(s) {
			p.Move = append(p.Move, s)
		}
	}
	candidates := map[string]HTTPEntry{}
	for n, e := range httpAll {
		if pending(n) {
			candidates[n] = e
		}
	}
	check := i.CheckHTTP
	if check == nil {
		check = CheckHTTPServer
	}
	httpMove, checkKept, oauth := checkAll(context.Background(), candidates, check)
	p.HTTP = httpMove
	p.Move = append(p.Move, httpMove...)
	sort.Strings(p.Move)
	for _, k := range append(append([]KeptDirect(nil), staticKept...), checkKept...) {
		if pending(k.Server) {
			p.KeptDirect = append(p.KeptDirect, k)
		}
	}
	sort.Slice(p.KeptDirect, func(a, b int) bool { return p.KeptDirect[a].Server < p.KeptDirect[b].Server })
	for _, k := range staticKept {
		if k.Reason == ReasonNeedsOAuth && pending(k.Server) {
			oauth = append(oauth, k)
		}
	}
	sort.Slice(oauth, func(a, b int) bool { return oauth[a].Server < oauth[b].Server })
	cat, err := LoadCatalog(i.CatalogPath)
	if err != nil {
		return InstallPlan{}, planInput{}, err
	}
	for _, s := range p.Move {
		if _, ok := cat.Servers[s]; !ok {
			p.Refresh = append(p.Refresh, s)
		}
	}
	moveHTTP := map[string]HTTPEntry{}
	for _, n := range httpMove {
		moveHTTP[n] = httpAll[n]
	}
	return p, planInput{stdio: stdio, http: moveHTTP, manifest: m, oauth: oauth}, nil
}

// server is the manifest's record of name; a nil manifest has none.
func (m *Manifest) server(name string) (MovedEntry, bool) {
	if m == nil {
		return MovedEntry{}, false
	}
	e, ok := m.Servers[name]
	return e, ok
}

// Install moves the stdio servers not moved yet (minus skip) behind the
// proxy; see the package comment for the order. A run with nothing new to
// move changes nothing.
func (i Installer) Install(skip []string) (err error) {
	unlock, err := lockInstall(i.ManifestPath)
	if err != nil {
		return err
	}
	defer unlock()
	p, in, err := i.plan(skip)
	if err != nil {
		return err
	}
	m := in.manifest
	if m != nil && m.Port != i.Port {
		return fmt.Errorf("the proxy is installed on port %d but this run would use %d; pass --port %d, "+
			"or run `aimux mcp proxy uninstall` and install again to change the port; nothing changed", m.Port, i.Port, m.Port)
	}
	if err := i.switchBinary(); err != nil {
		return err
	}
	// Before anything rewrites the servers file (forgetRemoved does too).
	if err := i.needCurrentDaemon(in.http); err != nil {
		return err
	}
	gone, err := i.forgetRemoved(m)
	if err != nil {
		return err
	}
	p.Moved = slices.DeleteFunc(p.Moved, func(n string) bool { return slices.Contains(gone, n) })
	if len(p.Move) == 0 {
		if m != nil {
			if !slices.Equal(m.Skipped, p.Skip) || !reflect.DeepEqual(m.Direct, in.oauth) {
				m.Skipped, m.Direct = p.Skip, in.oauth
				if err := i.saveManifest(m); err != nil {
					return err
				}
			}
			i.printf("The MCP proxy is installed (%d servers behind it); no new servers to move.\n", len(m.Servers))
			i.printSkipped(p.Skip)
			i.printKeptDirect(p.KeptDirect)
			i.ensureIdleTimeout(m)
			i.ensureHooks(m)
			return nil
		}
		i.printKeptDirect(p.KeptDirect)
		return fmt.Errorf("no MCP servers to move in %s", i.ClaudeConfig)
	}
	i.reportSessions()

	// Until the edit below succeeds, a failure puts the daemon's files back.
	snap, err := i.snapshotDaemon()
	if err != nil {
		return err
	}
	keepDaemon := false
	defer func() {
		if err != nil && !keepDaemon {
			err = i.restoreDaemon(snap, err)
		}
	}()

	i.printf("Listing the tools of %d servers: %s\n", len(p.Move), strings.Join(p.Move, ", "))
	if failed := sortedKeys(i.refreshAll(i.CatalogPath, p.Move, in.http)); len(failed) > 0 {
		return fmt.Errorf("these servers failed to list their tools: %s (`aimux mcp proxy refresh` shows why); fix them or leave them direct with --skip <name>",
			strings.Join(failed, ", "))
	}
	written, err := i.storeHeaders(in.http)
	defer func() {
		if err != nil && !keepDaemon {
			i.deleteItems(written)
		}
	}()
	if err != nil {
		return err
	}
	defs, err := i.saveDefinitions(p.Move, in.stdio, in.http)
	if err != nil {
		return err
	}
	if err := i.ensureDaemon(p.Move); err != nil {
		return err
	}
	if err := i.probeMoved(p.Move); err != nil {
		return err
	}
	backup, err := i.backupRedacted(in.http)
	if err != nil {
		return err
	}

	// The originals are read once, outside the edit: jsonedit may run fn
	// again on a file that already holds our entries.
	_, current, err := readMCPServers(i.ClaudeConfig)
	if err != nil {
		return err
	}
	originals := map[string]json.RawMessage{}
	for _, name := range p.Move {
		if raw, ok := current[name]; ok {
			originals[name] = raw
		}
	}
	// The record is written before the edit, with the entries about to be
	// written, so a crash after the edit still leaves them uninstallable;
	// a crash before it is harmless, as uninstall only restores entries
	// still equal to what install wrote. If the edit does not land, the
	// record goes back to what it was.
	prevManifest, err := snapshotFile(i.ManifestPath)
	if err != nil {
		return err
	}
	planned := map[string]MovedEntry{}
	for name, raw := range originals {
		if planned[name], err = i.movedEntry(name, raw, in.http); err != nil {
			return err
		}
	}
	if err := i.saveManifest(i.extendedWith(m, planned, p.Skip, in.oauth)); err != nil {
		return err
	}
	unchanged := func(name string, raw json.RawMessage) bool {
		if e, isHTTP := in.http[name]; isHTTP {
			return jsonEqual(raw, e.Raw) // what the daemon definition and the item came from
		}
		return sameDefinition(entryDefinition(raw), defs[name])
	}
	record := func(name string, raw json.RawMessage) (MovedEntry, error) { return i.movedEntry(name, raw, in.http) }
	var moved map[string]MovedEntry
	err = i.editKey(i.ClaudeConfig, "mcpServers", func(old json.RawMessage) (json.RawMessage, error) {
		v, mv, err := switchServers(old, p.Move, unchanged, originals, i.entryFor, record)
		moved = mv
		return v, err
	})
	if err != nil {
		// The edit may have landed before it reported the error.
		moved = i.landed(originals, in.http)
		if len(moved) == 0 {
			return i.revertManifest(prevManifest, fmt.Errorf("edit mcpServers in %s: %w", i.ClaudeConfig, err))
		}
		i.printf("Note: the edit of %s reported an error (%v), but its entries are in place; recording them.\n", i.ClaudeConfig, err)
	}
	if len(moved) == 0 {
		return i.revertManifest(prevManifest, fmt.Errorf("the servers to move (%s) were removed from %s meanwhile; nothing to record", strings.Join(p.Move, ", "), i.ClaudeConfig))
	}
	keepDaemon = true // the config points at the daemon now
	if gone := missingFrom(p.Move, moved); len(gone) > 0 {
		i.printf("Note: removed from %s while installing, so not moved: %s.\n", i.ClaudeConfig, strings.Join(gone, ", "))
		i.stopServing(gone)
		var items []string
		for _, n := range gone {
			if e, ok := in.http[n]; ok && len(e.Headers) > 0 {
				items = append(items, HeaderService(n))
			}
		}
		i.deleteItems(items) // their entries are gone: nothing will read them
	}
	if !reflect.DeepEqual(moved, planned) {
		if err := i.saveManifest(i.extendedWith(m, moved, p.Skip, in.oauth)); err != nil {
			i.printf("Note: could not update the record in %s (%v); it lists servers that were not moved, which uninstall reports as removed.\n", i.ManifestPath, err)
		}
	}

	i.printf("Moved behind the MCP proxy, each under its own name: %s\n", strings.Join(sortedKeys(moved), ", "))
	if len(p.Moved) > 0 {
		i.printf("  Already behind it: %d servers (`aimux mcp proxy status` lists them)\n", len(p.Moved))
	}
	i.printSkipped(p.Skip)
	i.printKeptDirect(p.KeptDirect)
	i.printf("  Backup: %s\n", backup)
	i.noteOlderBackups(backup, in.http)
	i.printf("New Claude Code sessions use the proxy; running sessions keep their direct servers until they restart.\n")
	i.printf("From now on, %s.\n", editHint)
	i.printf("Undo with: aimux mcp proxy uninstall\n")
	if final, err := i.loadManifest(); err == nil {
		i.ensureIdleTimeout(final)
		i.ensureHooks(final)
	}
	return nil
}

// forgetRemoved takes moved servers whose entry the owner removed from the
// Claude config (claude mcp remove) off the proxy: their saved definitions
// and manifest entries go, and the daemon reloads in place. A removed entry
// is deliberate: a stale session that saves its settings writes the old
// entry back (drift), it does not delete it.
func (i Installer) forgetRemoved(m *Manifest) ([]string, error) {
	if m == nil || len(m.Servers) == 0 {
		return nil, nil
	}
	_, members, err := readMCPServers(i.ClaudeConfig)
	if err != nil {
		return nil, err
	}
	var gone []string
	for _, name := range sortedKeys(m.Servers) {
		if _, ok := members[name]; !ok {
			gone = append(gone, name)
		}
	}
	if len(gone) == 0 {
		return nil, nil
	}
	if err := i.dropMoved(gone, true, false); err != nil {
		return nil, fmt.Errorf("take %s off the proxy: %w", strings.Join(gone, ", "), err)
	}
	for _, name := range gone {
		if svc := m.Servers[name].Keychain; svc != "" && i.Vault != nil {
			if err := i.Vault.Delete(svc); err != nil {
				i.printf("Note: could not delete Keychain item %s (%v); remove it with: security delete-generic-password -a aimux -s %s\n", svc, err, svc)
			}
		}
		delete(m.Servers, name)
	}
	if err := i.saveManifest(m); err != nil {
		return nil, err
	}
	if i.Reload != nil {
		if _, err := i.Reload(); err != nil {
			i.printf("Note: the proxy did not reload (%v); it keeps serving %s, unused, until it restarts.\n", err, strings.Join(gone, ", "))
		}
	}
	i.printf("No longer in %s, so taken off the proxy: %s\n", i.ClaudeConfig, strings.Join(gone, ", "))
	return gone, nil
}

// saveDefinitions writes the moved servers' definitions, variables resolved
// here (the daemon runs under launchd without this shell's variables), into
// the servers file and adds them to its moved list, which the daemon
// serves. It returns those definitions.
func (i Installer) saveDefinitions(move []string, stdio map[string]ServerConfig, http map[string]HTTPEntry) (map[string]ServerConfig, error) {
	saved, resolved, err := LoadServerFile(i.ServersPath)
	if err != nil {
		return nil, err
	}
	defs := map[string]ServerConfig{}
	for _, s := range move {
		if e, ok := http[s]; ok {
			defs[s] = ServerConfig{Type: "http", URL: e.URL, Secrets: keychainHeaderRefs(s, sortedKeys(e.Headers))}
		} else {
			defs[s] = expandConfig(stdio[s])
		}
		saved[s] = defs[s]
	}
	return defs, SaveServerFile(i.ServersPath, saved, union(resolved, move))
}

// switchServers is the install edit of mcpServers old: each server in move
// becomes entry(name) in place, its raw bytes recorded. An entry that is
// already entry(name) (jsonedit running fn again on our own write) counts
// as moved, with its original from originals (read before the edit). An
// entry removed meanwhile is left out; one that no longer matches the
// definition given to the daemon fails the edit.
func switchServers(old json.RawMessage, move []string, unchanged func(name string, raw json.RawMessage) bool, originals map[string]json.RawMessage, entry func(string) json.RawMessage, record func(string, json.RawMessage) (MovedEntry, error)) (json.RawMessage, map[string]MovedEntry, error) {
	if old == nil {
		return nil, nil, errors.New("there is no mcpServers object")
	}
	members, err := objectMembers(old)
	if err != nil {
		return nil, nil, err
	}
	cur, moved := old, map[string]MovedEntry{}
	for _, name := range move {
		raw, ok := members[name]
		if !ok {
			continue // removed meanwhile
		}
		w := entry(name)
		if jsonEqual(raw, w) {
			if orig, ok := originals[name]; ok {
				if moved[name], err = record(name, orig); err != nil {
					return nil, nil, err
				}
			}
			continue
		}
		if !unchanged(name, raw) {
			return nil, nil, fmt.Errorf("%s changed while installing; nothing was changed, run install again", name)
		}
		if cur, err = jsonedit.SetMember(cur, name, w); err != nil {
			return nil, nil, err
		}
		if moved[name], err = record(name, raw); err != nil {
			return nil, nil, err
		}
	}
	return cur, moved, nil
}

// restoreServers is the uninstall edit of mcpServers old: each entry still
// equal (as JSON) to what install wrote gets its original bytes back; one
// the owner changed or removed is kept and noted.
func restoreServers(old json.RawMessage, servers map[string]MovedEntry) (json.RawMessage, []string, []string, error) {
	var notes, restored []string
	if old == nil {
		for _, name := range sortedKeys(servers) {
			notes = append(notes, fmt.Sprintf("%s: entry removed since install; not restored", name))
		}
		return nil, notes, nil, nil
	}
	members, err := objectMembers(old)
	if err != nil {
		return nil, nil, nil, err
	}
	cur := old
	for _, name := range sortedKeys(servers) {
		e := servers[name]
		have, ok := members[name]
		switch {
		case e.without && ok && jsonEqual(have, json.RawMessage(e.Written)):
			if cur, err = removeMember(cur, name); err != nil {
				return nil, nil, nil, err
			}
			notes = append(notes, fmt.Sprintf("%s: its proxy entry was removed; its original could not be restored (its Keychain item is gone); add it again with `claude mcp add`", name))
		case e.without:
			notes = append(notes, fmt.Sprintf("%s: given up (--without); its entry is not the proxy entry, so it was left as it is", name))
		case !ok:
			notes = append(notes, fmt.Sprintf("%s: entry removed since install; not restored", name))
		case jsonEqual(have, json.RawMessage(e.Original)) || (e.Keychain != "" && !anyPlaceholder(have) && sameRedacted(have, e.Original)):
			notes = append(notes, fmt.Sprintf("%s: already restored", name))
			restored = append(restored, name)
		case e.redacted != "" && jsonEqual(have, json.RawMessage(e.redacted)):
			// A backup install wrote was put back: its header values are the
			// placeholder; the item fills them in.
			if cur, err = jsonedit.ReplaceMemberVerbatim(cur, name, json.RawMessage(e.Original)); err != nil {
				return nil, nil, nil, err
			}
			restored = append(restored, name)
		case !jsonEqual(have, json.RawMessage(e.Written)):
			notes = append(notes, fmt.Sprintf("%s: entry changed since install; kept yours", name))
		case e.Keychain != "" && e.redacted == "":
			// The entry became the proxy entry after its item was found
			// missing: restoring now would write placeholders.
			return nil, nil, nil, fmt.Errorf("%s changed while uninstalling and its Keychain item %s is gone; nothing was changed", name, e.Keychain)
		default:
			if cur, err = jsonedit.ReplaceMemberVerbatim(cur, name, json.RawMessage(e.Original)); err != nil {
				return nil, nil, nil, err
			}
			restored = append(restored, name)
		}
	}
	return cur, notes, restored, nil
}

// extended is m (a new manifest when nil) with servers added and skipped
// recorded, as a new value: m itself is not changed.
func (i Installer) extended(m *Manifest, servers map[string]MovedEntry, skipped []string) *Manifest {
	out := &Manifest{Version: manifestVersion, Port: i.Port, Servers: map[string]MovedEntry{}, Skipped: skipped}
	if m != nil {
		out.IdleTimeout = m.IdleTimeout
		for name, e := range m.Servers {
			out.Servers[name] = e
		}
	}
	for name, e := range servers {
		out.Servers[name] = e
	}
	return out
}

// revertManifest puts the record back as it was before this run (removes
// it on a first install) after cause, an edit that did not land.
func (i Installer) revertManifest(prev fileSnapshot, cause error) error {
	if err := prev.restore(); err != nil {
		return fmt.Errorf("%w; putting the record %s back also failed (%v): it lists servers that were not moved, which uninstall reports as removed", cause, i.ManifestPath, err)
	}
	return cause
}

// landed reads mcpServers again and returns the servers in originals whose
// entry is the one install writes: after an edit error, the entries that
// are in place.
func (i Installer) landed(originals map[string]json.RawMessage, http map[string]HTTPEntry) map[string]MovedEntry {
	_, members, err := readMCPServers(i.ClaudeConfig)
	if err != nil {
		return nil
	}
	out := map[string]MovedEntry{}
	for name, orig := range originals {
		if w := i.entryFor(name); jsonEqual(members[name], w) {
			if e, err := i.movedEntry(name, orig, http); err == nil {
				out[name] = e
			}
		}
	}
	return out
}

// stopServing takes names off the servers file's moved list and reloads
// the daemon; failures are reported, not returned (the Claude config does
// not point at these servers).
func (i Installer) stopServing(names []string) {
	if len(names) == 0 {
		return
	}
	if err := i.dropMoved(names, false, false); err != nil {
		i.printf("Note: could not take %s off the proxy's list (%v); it keeps serving them until the next install or uninstall.\n", strings.Join(names, ", "), err)
		return
	}
	if _, err := i.Reload(); err != nil {
		i.printf("Note: the proxy did not reload (%v); it keeps serving %s until it restarts.\n", err, strings.Join(names, ", "))
	}
}

func missingFrom[V any](names []string, have map[string]V) []string {
	var out []string
	for _, n := range names {
		if _, ok := have[n]; !ok {
			out = append(out, n)
		}
	}
	return out
}

func toSet(names []string) map[string]bool {
	out := map[string]bool{}
	for _, n := range names {
		out[n] = true
	}
	return out
}

func contains(list []string, s string) bool { return slices.Contains(list, s) }

func listOrNone(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ", ")
}

func (i Installer) printSkipped(skipped []string) {
	if len(skipped) > 0 {
		i.printf("  Kept direct (skipped): %s (`aimux mcp proxy install --unskip %s` moves them)\n", strings.Join(skipped, ", "), strings.Join(skipped, ","))
	}
}

// entryDefinition is what the daemon would run for a Claude config entry:
// its stdio definition with variables resolved; empty for anything else.
func entryDefinition(raw json.RawMessage) ServerConfig {
	var e claudeServerEntry
	if json.Unmarshal(raw, &e) != nil || (e.Type != "" && e.Type != "stdio") {
		return ServerConfig{}
	}
	return expandConfig(e.ServerConfig)
}

// sameDefinition compares two definitions, an empty list or map equal to
// none.
func sameDefinition(a, b ServerConfig) bool {
	if a.Command != b.Command || len(a.Args) != len(b.Args) || len(a.Env) != len(b.Env) ||
		a.Type != b.Type || a.URL != b.URL || !reflect.DeepEqual(a.Secrets, b.Secrets) {
		return false
	}
	for k := range a.Args {
		if a.Args[k] != b.Args[k] {
			return false
		}
	}
	for k, v := range a.Env {
		if w, ok := b.Env[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// daemonSnapshot is the daemon's configuration before install changed it:
// the servers file, the agent's plist and whether the agent was loaded.
type daemonSnapshot struct {
	servers, plist fileSnapshot
	loaded         bool
}

func (i Installer) snapshotDaemon() (daemonSnapshot, error) {
	servers, err := snapshotFile(i.ServersPath)
	if err != nil {
		return daemonSnapshot{}, err
	}
	plist, err := snapshotFile(i.Agent.PlistPath)
	if err != nil {
		return daemonSnapshot{}, err
	}
	loaded, err := i.Agent.Loaded()
	if err != nil {
		return daemonSnapshot{}, fmt.Errorf("ask launchd about %s: %w", i.Agent.Label, err)
	}
	return daemonSnapshot{servers: servers, plist: plist, loaded: loaded}, nil
}

// restoreDaemon puts the daemon's configuration back after cause, a failure
// before the Claude config was edited: an agent this run loaded is removed,
// a running one reloads the restored servers file (restarts on a changed
// plist). Nothing happens when nothing changed.
func (i Installer) restoreDaemon(s daemonSnapshot, cause error) error {
	cause = fmt.Errorf("%w; the Claude config was not changed", cause)
	nowLoaded, _ := i.Agent.Loaded()
	if !s.servers.changed() && !s.plist.changed() && nowLoaded == s.loaded {
		return cause
	}
	errs := []error{s.servers.restore()}
	switch {
	case !s.loaded:
		errs = append(errs, i.Agent.Uninstall(), s.plist.restore())
	case s.plist.changed():
		errs = append(errs, s.plist.restore(), i.Agent.Restart())
	default:
		_, rerr := i.Reload()
		errs = append(errs, rerr)
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("%w; putting the proxy's previous configuration back also failed (%v): check `aimux mcp proxy status`", cause, err)
	}
	if !s.loaded {
		return fmt.Errorf("%w; the launchd agent this run started was removed again", cause)
	}
	return fmt.Errorf("%w; the proxy runs its previous configuration again", cause)
}

// fileSnapshot is a file's bytes, or its absence, to put back later.
type fileSnapshot struct {
	path    string
	data    []byte
	existed bool
}

func snapshotFile(path string) (fileSnapshot, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- aimux's own state file
	if errors.Is(err, os.ErrNotExist) {
		return fileSnapshot{path: path}, nil
	}
	if err != nil {
		return fileSnapshot{}, fmt.Errorf("read %s: %w", path, err)
	}
	return fileSnapshot{path: path, data: b, existed: true}, nil
}

// changed reports whether the file differs from the snapshot now.
func (s fileSnapshot) changed() bool {
	b, err := os.ReadFile(s.path) // #nosec G304 -- a file aimux snapshotted
	if errors.Is(err, os.ErrNotExist) {
		return s.existed
	}
	return err != nil || !s.existed || !bytes.Equal(b, s.data)
}

func (s fileSnapshot) restore() error {
	if !s.existed {
		if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return writePrivateFile(s.path, s.data)
}

// switchBinary points an installed launchd agent that runs another aimux
// binary at this one and waits until the restarted daemon answers, so a
// re-run from a new binary never leaves the job on the old one.
func (i Installer) switchBinary() error {
	a := i.Agent
	snap, err := snapshotFile(a.PlistPath)
	if err != nil {
		return err
	}
	was, switched, err := a.SwitchBinary()
	if err != nil {
		return fmt.Errorf("point the launchd agent %s at %s: %w", a.Label, a.Binary, err)
	}
	if !switched {
		return nil
	}
	if was == "" {
		was = "an unreadable program"
	}
	i.printf("The proxy's launchd agent ran %s; it now runs %s (restarted).\n", was, a.Binary)
	if i.Status == nil {
		return nil
	}
	wait := i.Wait
	if wait <= 0 {
		wait = defaultWait
	}
	deadline := i.now().Add(wait)
	for {
		if rep, err := i.Status(); err == nil && rep.Running {
			return nil
		}
		if !i.now().Before(deadline) {
			// Back to the binary that ran, so the proxy serves again and the
			// next install tries the switch again.
			cause := fmt.Errorf("the MCP proxy did not answer within %v after switching its launchd agent to %s (logs in %s)", wait, a.Binary, a.LogDir)
			if err := errors.Join(writePlist(snap.path, snap.data), a.Restart()); err != nil {
				return fmt.Errorf("%w; switching it back to %s also failed: %v", cause, was, err)
			}
			return fmt.Errorf("%w; it runs %s again", cause, was)
		}
		i.sleep(statusPoll)
	}
}

// ensureDaemon makes the daemon serve every server in move: a running one
// reloads in place (never restarted for configuration), otherwise the
// launchd agent is (re)loaded. It then waits until the daemon serves them.
func (i Installer) ensureDaemon(move []string) error {
	a := i.Agent
	loaded, err := a.Loaded()
	if err != nil {
		return fmt.Errorf("ask launchd about %s: %w", a.Label, err)
	}
	rep, _ := i.Status()
	if loaded && rep.Running {
		res, err := i.Reload()
		if err != nil {
			return fmt.Errorf("reload the running MCP proxy: %w", err)
		}
		i.printf("Reloaded the running MCP proxy in place (added %d servers); open sessions keep their connections.\n", len(res.Added))
		i.noteUpgrade(rep)
	} else {
		if err := os.MkdirAll(filepath.Dir(a.PlistPath), 0o755); err != nil { //nolint:gosec // ~/Library/LaunchAgents is world-readable by design
			return err
		}
		if err := os.MkdirAll(a.LogDir, 0o700); err != nil {
			return err
		}
		if err := a.Install(); err != nil {
			return fmt.Errorf("start the proxy under launchd: %w", err)
		}
		i.printf("Started the MCP proxy under launchd (%s).\n", a.Label)
	}
	wait := i.Wait
	if wait <= 0 {
		wait = defaultWait
	}
	deadline := i.now().Add(wait)
	for {
		running, missing := i.serving(move)
		if running && len(missing) == 0 {
			return nil
		}
		if !i.now().Before(deadline) {
			if !running {
				return fmt.Errorf("the MCP proxy is not answering its status endpoint after %v (launchd agent %s, logs in %s)", wait, a.Label, a.LogDir)
			}
			return fmt.Errorf("the running MCP proxy does not serve: %s (check `aimux mcp proxy status`)", strings.Join(missing, ", "))
		}
		i.sleep(statusPoll)
	}
}

// upgradePending reports whether a running daemon runs another aimux
// version than this one.
func (i Installer) upgradePending(rep StatusReport) bool {
	return rep.Running && i.Version != "" && rep.Version != i.Version
}

func (i Installer) noteUpgrade(rep StatusReport) {
	if i.upgradePending(rep) {
		i.printf("Note: %s.\n", i.UpgradeText(rep.Version))
	}
}

// UpgradeText says the running daemon is another version than this aimux and
// how to restart it.
func (i Installer) UpgradeText(daemon string) string {
	if daemon == "" {
		daemon = "an older version"
	}
	return fmt.Sprintf("upgrade restart pending: the running proxy is aimux %s, this binary is %s; restart it when convenient with `aimux mcp proxy restart`",
		daemon, i.Version)
}

// Restart restarts the daemon under launchd (after an upgrade), in place,
// and waits until a new daemon (another
// pid) answers, then returns its report. Open sessions reconnect on their
// next call.
func (i Installer) Restart() (StatusReport, error) {
	a := i.Agent
	if !fileExists(a.PlistPath) {
		return StatusReport{}, fmt.Errorf("the MCP proxy is not installed (no launchd agent at %s); `aimux mcp proxy install` sets it up", a.PlistPath)
	}
	before, _ := i.Status()
	// In place (kickstart -k): the job stays loaded, so no failed bootstrap
	// can leave the daemon down. An upgrade keeps the plist as it is.
	if err := a.Kickstart(); err != nil {
		return StatusReport{}, fmt.Errorf("restart the MCP proxy's launchd agent %s: %w", a.Label, err)
	}
	wait := i.Wait
	if wait <= 0 {
		wait = defaultWait
	}
	deadline := i.now().Add(wait)
	for {
		rep, err := i.Status()
		if err == nil && rep.Running && (!before.Running || rep.PID != before.PID) {
			return rep, nil
		}
		if !i.now().Before(deadline) {
			return StatusReport{}, fmt.Errorf("the MCP proxy did not answer within %v after the restart (launchd agent %s, logs in %s)", wait, a.Label, a.LogDir)
		}
		i.sleep(statusPoll)
	}
}

// probeMoved has the daemon start each server in move and list its tools,
// under launchd's environment, before any config changes.
func (i Installer) probeMoved(move []string) error {
	rep, err := i.Probe(move)
	if err != nil {
		return fmt.Errorf("check the servers under the proxy daemon: %w", err)
	}
	ok := map[string]bool{}
	for _, s := range rep.Servers {
		ok[s.Server] = s.ProbeError == ""
	}
	var failed []string
	for _, s := range move {
		if !ok[s] {
			failed = append(failed, s)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("these servers did not start or list their tools under the proxy daemon: %s (`aimux mcp proxy status --probe` shows why; logs in %s); fix them or leave them direct with --skip <name>",
			strings.Join(failed, ", "), i.Agent.LogDir)
	}
	return nil
}

// serving asks the daemon once whether it runs and which servers in move
// have no endpoint there (configured is not enough: a server without a
// catalog entry in the daemon gets none, and /mcp/<name> would answer 404).
func (i Installer) serving(move []string) (running bool, missing []string) {
	rep, err := i.Status()
	if err != nil || !rep.Running {
		return false, nil
	}
	served := map[string]bool{}
	for _, s := range rep.Servers {
		served[s.Server] = s.Endpoint
	}
	for _, s := range move {
		if !served[s] {
			missing = append(missing, s)
		}
	}
	return true, missing
}

func (i Installer) reportSessions() {
	if i.Sessions == nil {
		return
	}
	ids, err := i.Sessions()
	if err != nil {
		i.printf("Could not list open Claude Code sessions: %v\n", err)
		return
	}
	if len(ids) == 0 {
		return
	}
	i.printf("%d Claude Code sessions are open (%s). They keep their direct servers until they restart; "+
		"one that saves its settings later can bring a direct entry back (`aimux mcp proxy status` shows that).\n", len(ids), strings.Join(ids, ", "))
}

// Uninstall puts back, in one edit, every moved entry still as install
// wrote it, keeps and reports the others, then removes the manifest and
// the launchd agent. Without a manifest it only removes a leftover agent.
func (i Installer) Uninstall() error {
	unlock, err := lockInstall(i.ManifestPath)
	if err != nil {
		return err
	}
	defer unlock()
	m, err := i.loadManifest()
	if err != nil {
		return err
	}
	if m == nil {
		if fileExists(i.Agent.PlistPath) {
			if err := i.Agent.Uninstall(); err != nil {
				return fmt.Errorf("remove the leftover launchd agent %s: %w", i.Agent.PlistPath, err)
			}
			i.printf("Removed a leftover launchd agent (%s).\n", i.Agent.PlistPath)
		}
		i.printf("The MCP proxy is not installed (no manifest at %s); the Claude config was not changed.\n", i.ManifestPath)
		return nil
	}
	// Header values come back from the Keychain before anything changes:
	// an item that cannot be read stops uninstall here.
	var current map[string]json.RawMessage
	if fileExists(i.ClaudeConfig) {
		if _, current, err = readMCPServers(i.ClaudeConfig); err != nil {
			return err
		}
	}
	for _, w := range i.Without {
		if _, ok := m.Servers[w]; !ok {
			return fmt.Errorf("--without %s: not a server install moved (moved: %s); nothing was changed", w, listOrNone(sortedKeys(m.Servers)))
		}
	}
	servers, err := i.unredacted(context.Background(), m, current)
	if err != nil {
		return err
	}
	var notes, restored []string
	if !fileExists(i.ClaudeConfig) {
		_, notes, _, _ = restoreServers(nil, servers) // every entry is gone
	} else if err := i.editKey(i.ClaudeConfig, "mcpServers", func(old json.RawMessage) (json.RawMessage, error) {
		v, n, r, err := restoreServers(old, servers)
		notes, restored = n, r
		return v, err
	}); err != nil {
		return fmt.Errorf("restore mcpServers in %s: %w; nothing was changed, run uninstall again in a moment", i.ClaudeConfig, err)
	}
	for _, n := range notes {
		i.printf("  %s\n", n)
	}
	names := sortedKeys(m.Servers)
	if err := i.dropMoved(names, true, true); err != nil {
		return fmt.Errorf("%w; the Claude config is restored; run `aimux mcp proxy uninstall` again to finish", err)
	}
	i.dropItems(m, restored, current)
	i.dropIdleTimeout(m)
	i.dropHooks(m)
	// Without the manifest, uninstall again only removes the agent: the way
	// to finish if removing it fails below.
	if err := removeManifest(i.ManifestPath); err != nil {
		return err
	}
	if err := i.Agent.Uninstall(); err != nil {
		return fmt.Errorf("stop the proxy's launchd agent: %w; the Claude config is restored; run `aimux mcp proxy uninstall` again to finish", err)
	}
	i.printf("Restored Claude Code's direct MCP servers (%s) and stopped the proxy's launchd agent.\n"+
		"Running sessions keep using the proxy's tools until they restart.\n", strings.Join(names, ", "))
	return nil
}

// dropMoved takes names off the servers file's moved list (the daemon no
// longer serves them); with forget it also deletes their saved definitions,
// which hold resolved env values. Only uninstalling drops the list once it
// is empty: an installed proxy keeps an empty list (serve nothing), since a
// missing list means not installed (a manual serve serves everything).
func (i Installer) dropMoved(names []string, forget, uninstalling bool) error {
	servers, resolved, err := LoadServerFile(i.ServersPath)
	if err != nil {
		return err
	}
	drop := toSet(names)
	keep := []string{} // an empty list still says "installed: serve nothing"
	for _, r := range resolved {
		if !drop[r] {
			keep = append(keep, r)
		}
	}
	changed := len(keep) != len(resolved)
	if forget {
		for _, n := range names {
			if _, ok := servers[n]; ok {
				delete(servers, n)
				changed = true
			}
		}
	}
	if uninstalling && len(keep) == 0 {
		// uninstalled: no list, a manual serve serves everything; an empty
		// list left by taking every server off counts as a change too
		changed = changed || resolved != nil
		keep = nil
	}
	if !changed {
		return nil
	}
	return SaveServerFile(i.ServersPath, servers, keep)
}

// InstallStatus reports the moved servers whose entry drifted, the stdio
// servers not behind the proxy and whether an upgrade restart is pending.
func (i Installer) InstallStatus() (InstallState, error) {
	m, err := i.loadManifest()
	if err != nil {
		return InstallState{}, err
	}
	st := InstallState{State: InstallNotInstalled}
	if i.Status != nil {
		if rep, err := i.Status(); err == nil && rep.Running {
			st.DaemonVersion, st.UpgradePending = rep.Version, i.upgradePending(rep)
		}
	}
	if m == nil {
		return st, nil
	}
	st.State, st.Moved, st.Hooks = InstallInPlace, sortedKeys(m.Servers), i.hooksOn()
	_, members, err := readMCPServers(i.ClaudeConfig)
	if err != nil {
		return InstallState{}, err
	}
	for _, name := range st.Moved {
		raw, ok := members[name]
		switch {
		case !ok:
			st.Removed = append(st.Removed, name)
		case entryURL(raw) != EntryURL(m.Port, name):
			st.Drifted = append(st.Drifted, name)
		}
	}
	if len(st.Drifted) > 0 {
		st.State = InstallDrifted
	}
	stdio, err := LoadClaudeServers(i.ClaudeConfig, nil)
	if err != nil {
		return InstallState{}, err
	}
	// HTTP servers are classified from the config and the manifest only;
	// status never probes the network.
	httpAll, staticKept, err := LoadClaudeHTTPServers(i.ClaudeConfig, m.Port, i.Vault != nil)
	if err != nil {
		return InstallState{}, err
	}
	// A recorded OAuth server counts only while its entry is still an HTTP
	// entry (a stdio server added under the same name is not it).
	direct := map[string]bool{}
	var recordedDirect []KeptDirect
	for _, k := range m.Direct {
		if _, isStdio := stdio[k.Server]; !isStdio && entryURL(members[k.Server]) != "" {
			direct[k.Server] = true
			recordedDirect = append(recordedDirect, k)
		}
	}
	for _, name := range sortedKeys(httpAll) {
		if reason := headersTooLong(i.Vault, name, httpAll[name]); reason != "" {
			staticKept = append(staticKept, KeptDirect{Server: name, Reason: reason})
			delete(httpAll, name)
		}
	}
	names := map[string]bool{}
	for n := range stdio {
		names[n] = true
	}
	for n := range httpAll {
		names[n] = true
	}
	for _, name := range sortedKeys(names) {
		switch _, moved := m.Servers[name]; {
		case moved:
		case contains(m.Skipped, name):
			st.Skipped = append(st.Skipped, name)
		case direct[name]:
		default:
			st.NotProxied = append(st.NotProxied, name)
		}
	}
	for _, k := range append(staticKept, recordedDirect...) {
		if _, moved := m.Servers[k.Server]; moved || contains(m.Skipped, k.Server) {
			continue
		}
		if _, ok := members[k.Server]; ok {
			st.KeptDirect = append(st.KeptDirect, k)
		}
	}
	sort.Slice(st.KeptDirect, func(a, b int) bool { return st.KeptDirect[a].Server < st.KeptDirect[b].Server })
	return st, nil
}

// entryURL is an HTTP entry's url; "" for anything else.
func entryURL(raw json.RawMessage) string {
	var e struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	if raw == nil || json.Unmarshal(raw, &e) != nil || e.Type != "http" {
		return ""
	}
	return e.URL
}

// PrintInstallStatus prints st in a few plain lines.
func (i Installer) PrintInstallStatus(st InstallState) {
	if st.State == InstallNotInstalled {
		i.printf("Install: not installed (`aimux mcp proxy install` moves Claude Code's stdio and HTTP servers behind the proxy)\n")
	} else {
		i.printf("Install: %d servers behind the proxy (%s); %s\n", len(st.Moved), strings.Join(st.Moved, ", "), editHint)
	}
	if len(st.Drifted) > 0 {
		i.printf("  Drifted, the entry is no longer its proxy URL (a running session saved an old copy, or it was edited): %s. To fix it, %s.\n",
			strings.Join(st.Drifted, ", "), driftFix)
	}
	if len(st.Removed) > 0 {
		i.printf("  Removed from %s: %s (the proxy still serves them, unused; `aimux mcp proxy install` takes them off).\n", i.ClaudeConfig, strings.Join(st.Removed, ", "))
	}
	if len(st.NotProxied) > 0 {
		i.printf("  Not behind the proxy: %s (run `aimux mcp proxy install` to move them)\n", strings.Join(st.NotProxied, ", "))
	}
	i.printSkipped(st.Skipped)
	for _, line := range KeptDirectLines(st.KeptDirect) {
		i.printf("  %s\n", line)
	}
	if st.UpgradePending {
		i.printf("  Note: %s.\n", i.UpgradeText(st.DaemonVersion))
	}
}

// PrintPlan prints p by name only.
func (i Installer) PrintPlan(p InstallPlan) {
	if len(p.Moved) > 0 {
		i.printf("Already behind the proxy: %d servers (`aimux mcp proxy status` lists them)\n", len(p.Moved))
	}
	if len(p.Remove) > 0 {
		i.printf("Would take off the proxy (removed from %s): %s\n", i.ClaudeConfig, strings.Join(p.Remove, ", "))
	}
	for _, k := range p.KeptDirect {
		i.printf("Would keep direct: %s: %s\n", k.Server, k.Reason)
	}
	if len(p.Move) == 0 {
		if len(p.Remove) == 0 {
			i.printf("No servers to move; nothing would change.\n")
		} else {
			i.printf("No servers to move.\n")
		}
		return
	}
	i.printf("Would move %d servers behind the proxy, each keeping its name:\n", len(p.Move))
	for _, s := range p.Move {
		kind := ""
		if contains(p.HTTP, s) {
			kind = " (http)"
		}
		i.printf("  %s%s -> %s\n", s, kind, EntryURL(i.Port, s))
	}
	if len(p.Skip) > 0 {
		i.printf("Would leave direct (--skip): %s\n", strings.Join(p.Skip, ", "))
	}
	if len(p.Listed) > 0 {
		i.printf("Started each of these servers once to list its tools (into a temporary catalog; nothing saved): %s\n", strings.Join(p.Listed, ", "))
	}
	if len(p.Refresh) > 0 {
		i.printf("Could not list the tools of: %s; install lists them first and refuses if they still fail (`aimux mcp proxy refresh` shows why)\n", strings.Join(p.Refresh, ", "))
	}
	i.printf("Would back up %s to %s, start the launchd agent or reload the running proxy, and record the change in %s.\n",
		i.ClaudeConfig, i.BackupDir, i.ManifestPath)
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range append(append([]string(nil), a...), b...) {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

// lockInstall serializes install and uninstall runs.
func lockInstall(manifestPath string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(manifestPath+".lock", os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- beside aimux's own manifest
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { // #nosec G115 -- file descriptors are small
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.New("another `aimux mcp proxy install` or `uninstall` is running; try again when it finishes")
		}
		return nil, fmt.Errorf("lock %s: %w", manifestPath+".lock", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) // #nosec G115 -- file descriptors are small
		_ = f.Close()
	}, nil
}

// loadManifest is LoadManifest with, for an unreadable manifest, the
// backup to restore from and how.
func (i Installer) loadManifest() (*Manifest, error) {
	m, err := LoadManifest(i.ManifestPath)
	if !errors.Is(err, errManifestCorrupt) {
		return m, err
	}
	b := newestBackup(i.BackupDir, filepath.Base(i.ClaudeConfig))
	if b == "" {
		b = "(none found in " + i.BackupDir + ")"
	}
	return nil, fmt.Errorf("%w. To restore by hand: copy the moved servers' mcpServers entries from the backup %s into %s "+
		"(only those entries, not the whole file), then move the manifest away and run `aimux mcp proxy uninstall` for the launchd agent",
		err, b, i.ClaudeConfig)
}

// LoadManifest reads the manifest; nil when there is none.
func LoadManifest(path string) (*Manifest, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- aimux's own manifest
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the install manifest %s: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%w: %s (%v); nothing changed", errManifestCorrupt, path, err)
	}
	if m.Version < minManifestVersion {
		return nil, fmt.Errorf("this manifest (%s) is from an older aimux design; move %s aside and run `aimux mcp proxy uninstall`; nothing changed", path, path)
	}
	if m.Version > manifestVersion {
		return nil, fmt.Errorf("the install manifest %s was written by a newer aimux (format %d, this one reads %d); nothing changed, use that aimux", path, m.Version, manifestVersion)
	}
	if m.Servers == nil {
		m.Servers = map[string]MovedEntry{}
	}
	return &m, nil
}

func saveManifest(path string, m *Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := writePrivateFile(path, b); err != nil {
		return fmt.Errorf("write the install manifest %s: %w", path, err)
	}
	return nil
}

func removeManifest(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove the install manifest %s: %w", path, err)
	}
	return nil
}

// readMCPServers returns the raw mcpServers value of a Claude config (nil
// when absent) and its members.
func readMCPServers(path string) (json.RawMessage, map[string]json.RawMessage, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- the owner's Claude config
	if err != nil {
		return nil, nil, fmt.Errorf("read Claude config %s: %w", path, err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		return nil, nil, fmt.Errorf("parse Claude config %s: %w", path, err)
	}
	raw := top["mcpServers"]
	if raw == nil {
		return nil, map[string]json.RawMessage{}, nil
	}
	members, err := objectMembers(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("read mcpServers of %s: %w", path, err)
	}
	return raw, members, nil
}

func objectMembers(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, errors.New("mcpServers is not a JSON object")
	}
	return m, nil
}

// jsonEqual reports whether a and b hold the same JSON value, whatever
// their spacing or member order (Claude Code rewrites the file in its own
// style). Numbers compare by their text.
func jsonEqual(a, b json.RawMessage) bool {
	decode := func(r json.RawMessage) (any, bool) {
		d := json.NewDecoder(bytes.NewReader(r))
		d.UseNumber()
		var v any
		if d.Decode(&v) != nil {
			return nil, false
		}
		return v, true
	}
	va, ok := decode(a)
	if !ok {
		return false
	}
	vb, ok := decode(b)
	return ok && reflect.DeepEqual(va, vb)
}

func newestBackup(dir, base string) string {
	matches, _ := filepath.Glob(filepath.Join(dir, base+".*"))
	if len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	return matches[len(matches)-1]
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

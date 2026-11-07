package mcpproxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/jsonedit"
)

// Non-canonical on purpose: inline arrays, odd spacing, unicode, a project
// key holding "mcpServers" text and a nested mcpServers that must not move.
const fixtureClaude = `{
  "numStartups": 42,
  "projects": {"/Users/x/mcpServers-demo": {"mcpServers": {"slack": {"command": "nested"}}, "allowedTools": ["a","b"]}},
  "mcpServers": {
    "slack": {"command": "npx", "args": ["-y", "slack-mcp"], "env": {"SLACK_TOKEN": "envvalue-slack-0001"}},
    "aimux":   { "type": "stdio", "command": "/usr/local/bin/aimux", "args": [ "mcp", "serve" ] },
    "remote": {"type": "http", "url": "https://example.com/mcp", "headers": {"X-Key": "envvalue-header-0002"}},
    "github":{"command":"gh-mcp","env":{"GH_TOKEN":"envvalue-github-0003"}}
  },
  "tips": "h\u00e9llo ✓ é",
  "z":[1,2,  3]
}
`

// The owner's settings; install must never read or write them.
const fixtureSettings = `{
  "permissions": {"allow": ["mcp__slack__post_message"], "deny": [ "mcp__github__delete" ]}
}
`

var envValues = []string{"envvalue-slack-0001", "envvalue-header-0002", "envvalue-github-0003"}

var originalNames = []string{"aimux", "github", "remote", "slack"}

const testPort = 7381

// installLaunchctl is a fake launchctl: print, bootout and bootstrap with
// the exit answers the real one gives for a service that is not loaded.
type installLaunchctl struct {
	mu     sync.Mutex
	loaded bool
	calls  []string
}

func (f *installLaunchctl) run(name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(args, " "))
	switch args[0] {
	case "print":
		if f.loaded {
			return []byte("state = running"), nil
		}
		return []byte("Could not find service"), errors.New("exit status 113")
	case "bootout":
		if !f.loaded {
			return []byte("No such process"), errors.New("exit status 3")
		}
		f.loaded = false
		return nil, nil
	case "bootstrap":
		f.loaded = true
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected launchctl %v", args)
}

func (f *installLaunchctl) isLoaded() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loaded
}

// ran reports whether launchctl ran verb since call index from.
func (f *installLaunchctl) ran(verb string, from int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls[from:] {
		if strings.HasPrefix(c, verb+" ") {
			return true
		}
	}
	return false
}

func (f *installLaunchctl) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type installFX struct {
	t             *testing.T
	dir           string
	claude        string
	settings      string
	manifest      string
	backups       string
	servers       string
	catalog       string
	lc            *installLaunchctl
	daemonDown    bool
	daemonVersion string
	clock         time.Time
	reloads       int
	probed        []string
	probeFailing  map[string]bool
	noEndpoint    map[string]bool // configured but given no endpoint (no catalog entry in the daemon)
	failing       map[string]bool
	refreshed     []string
	refreshedInto []string
	out           bytes.Buffer
	in            Installer
}

var fixtureTools = map[string][]string{
	"slack":  {"post_message", "read"},
	"github": {"issues"},
	"aimux":  {"list_sessions"},
}

func catalogTools(server string) []json.RawMessage {
	var out []json.RawMessage
	for _, n := range fixtureTools[server] {
		out = append(out, json.RawMessage(fmt.Sprintf(`{"name":%q,"inputSchema":{"type":"object"}}`, n)))
	}
	return out
}

// newInstallFX lays out a fake home in a temp dir; settings "" means no
// settings.json. Nothing outside the temp dir is touched.
func newInstallFX(t *testing.T, claude, settings string) *installFX {
	t.Helper()
	dir := t.TempDir()
	fx := &installFX{
		t:            t,
		dir:          dir,
		claude:       filepath.Join(dir, ".claude.json"),
		settings:     filepath.Join(dir, ".claude", "settings.json"),
		manifest:     filepath.Join(dir, ".aimux", "mcp-proxy-install.json"),
		backups:      filepath.Join(dir, ".aimux", "backups"),
		servers:      filepath.Join(dir, ".aimux", "mcp-servers.json"),
		catalog:      filepath.Join(dir, ".aimux", "mcp-catalog.json"),
		lc:           &installLaunchctl{},
		failing:      map[string]bool{},
		probeFailing: map[string]bool{},
		noEndpoint:   map[string]bool{},
	}
	must(t, os.MkdirAll(filepath.Dir(fx.settings), 0o700))
	must(t, os.WriteFile(fx.claude, []byte(claude), 0o600)) // #nosec G703 -- the test's temp dir
	if settings != "" {
		must(t, os.WriteFile(fx.settings, []byte(settings), 0o600))
	}
	cat := &Catalog{}
	for s := range fixtureTools {
		cat.Set(s, catalogTools(s), time.Now())
	}
	must(t, cat.Save(fx.catalog))
	fx.in = Installer{
		ClaudeConfig: fx.claude, ManifestPath: fx.manifest, BackupDir: fx.backups,
		ServersPath: fx.servers, CatalogPath: fx.catalog, Port: testPort,
		Agent: Agent{
			Label: "com.aimux.mcp-proxy", PlistPath: filepath.Join(dir, "LaunchAgents", "com.aimux.mcp-proxy.plist"),
			Binary: "/usr/local/bin/aimux", LogDir: filepath.Join(dir, ".aimux", "logs"),
			Args: []string{"mcp", "proxy", "serve", "--port", "7381"}, Run: fx.lc.run, UID: 501,
			Env: map[string]string{"PATH": "/opt/homebrew/bin:/usr/bin", "HOME": dir},
		},
		Status:   fx.status,
		Probe:    fx.probe,
		Reload:   fx.reload,
		Refresh:  fx.refresh,
		Sessions: func() ([]string, error) { return []string{"sess-1", "sess-2"}, nil },
		Out:      &fx.out,
		Wait:     time.Second,
	}
	fx.clock = time.Unix(1_000_000_000, 0)
	fx.in.Now = func() time.Time { return fx.clock }
	fx.in.Sleep = func(d time.Duration) { fx.clock = fx.clock.Add(d) }
	return fx
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (fx *installFX) running() bool { return fx.lc.isLoaded() && !fx.daemonDown }

// status answers like the daemon: running once the agent is loaded,
// serving what the servers file says (proxyLoadSet's rule).
func (fx *installFX) status() (StatusReport, error) {
	if !fx.running() {
		return StatusReport{}, nil
	}
	rep := StatusReport{Running: true, Version: fx.daemonVersion}
	for _, n := range fx.served() {
		rep.Servers = append(rep.Servers, ServerReport{ServerStatus: ServerStatus{Server: n}, Endpoint: !fx.noEndpoint[n]})
	}
	return rep, nil
}

// served is the servers file's moved list when it has one, else every
// saved server.
func (fx *installFX) served() []string {
	saved, resolved, err := LoadServerFile(fx.servers)
	must(fx.t, err)
	if len(resolved) > 0 {
		var out []string
		for _, r := range resolved {
			if _, ok := saved[r]; ok {
				out = append(out, r)
			}
		}
		return out
	}
	return sortedKeys(saved)
}

func (fx *installFX) reload() (ReloadResult, error) {
	if !fx.running() {
		return ReloadResult{}, errors.New("connection refused")
	}
	fx.reloads++
	return ReloadResult{Added: []string{}, Removed: []string{}, Changed: []string{}}, nil
}

// probe answers like the daemon's POST /status/probe for the named servers.
func (fx *installFX) probe(servers []string) (StatusReport, error) {
	fx.probed = append([]string(nil), servers...)
	rep := StatusReport{Running: true}
	for _, s := range servers {
		r := ServerReport{ServerStatus: ServerStatus{Server: s}}
		if !contains(fx.served(), s) {
			r.ProbeError = "not configured in the proxy"
		} else if fx.probeFailing[s] {
			r.ProbeError = "exec: tool: executable file not found in $PATH"
		}
		rep.Servers = append(rep.Servers, r)
	}
	return rep, nil
}

func (fx *installFX) refresh(catalogPath string, servers []string) map[string]error {
	fx.refreshed = append(fx.refreshed, servers...)
	fx.refreshedInto = append(fx.refreshedInto, catalogPath)
	cat, err := LoadCatalog(catalogPath)
	must(fx.t, err)
	errs := map[string]error{}
	for _, s := range servers {
		if fx.failing[s] {
			errs[s] = errors.New("exec: boom")
			continue
		}
		cat.Set(s, catalogTools(s), time.Now())
	}
	must(fx.t, cat.Save(catalogPath))
	return errs
}

func (fx *installFX) read(path string) string {
	fx.t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- a file in the test's temp dir
	must(fx.t, err)
	return string(b)
}

func (fx *installFX) write(path, s string) {
	fx.t.Helper()
	must(fx.t, os.WriteFile(path, []byte(s), 0o600))
}

func (fx *installFX) entries() map[string]json.RawMessage {
	fx.t.Helper()
	var top struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	must(fx.t, json.Unmarshal([]byte(fx.read(fx.claude)), &top))
	return top.MCPServers
}

func (fx *installFX) setEntry(name string, v json.RawMessage) {
	fx.t.Helper()
	must(fx.t, jsonedit.EditKeyVerbatim(fx.claude, "mcpServers", func(old json.RawMessage) (json.RawMessage, error) {
		return jsonedit.SetMember(old, name, v)
	}))
}

func (fx *installFX) assertNoEnv(texts ...string) {
	fx.t.Helper()
	for _, s := range texts {
		for _, v := range envValues {
			if strings.Contains(s, v) {
				fx.t.Fatalf("env value %q printed:\n%s", v, s)
			}
		}
	}
}

func (fx *installFX) assertNoManifest() {
	fx.t.Helper()
	if _, err := os.Stat(fx.manifest); !os.IsNotExist(err) {
		fx.t.Fatalf("manifest present (%v)", err)
	}
}

// snapshot is every file under the fake home, path to bytes.
func (fx *installFX) snapshot() map[string]string {
	fx.t.Helper()
	out := map[string]string{}
	must(fx.t, filepath.WalkDir(fx.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(p, ".lock") {
			return err
		}
		out[p] = fx.read(p)
		return nil
	}))
	return out
}

func sortedNames(m map[string]json.RawMessage) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func httpEntry(server string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"type":"http","url":%q}`, EntryURL(testPort, server)))
}

func TestInstall_ThenUninstall_ByteIdentical(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, fx.in.Install(nil))

	got := fx.entries()
	if !reflect.DeepEqual(sortedNames(got), originalNames) {
		t.Fatalf("mcpServers after install = %v, want the same names %v", sortedNames(got), originalNames)
	}
	for _, s := range []string{"aimux", "github", "slack"} {
		if !jsonEqual(got[s], httpEntry(s)) {
			t.Fatalf("%s = %s, want its proxy entry", s, got[s])
		}
	}
	if !strings.Contains(fx.read(fx.claude), `"remote": {"type": "http", "url": "https://example.com/mcp", "headers": {"X-Key": "envvalue-header-0002"}}`) {
		t.Fatalf("remote entry changed:\n%s", fx.read(fx.claude))
	}

	must(t, fx.in.Uninstall())
	if got := fx.read(fx.claude); got != fixtureClaude {
		t.Fatalf("claude.json not byte-identical after uninstall:\n%s", got)
	}
	fx.assertNoManifest()
	if fx.lc.isLoaded() {
		t.Fatal("agent still loaded after uninstall")
	}
	if _, err := os.Stat(fx.in.Agent.PlistPath); !os.IsNotExist(err) {
		t.Fatal("plist still there after uninstall")
	}
	if _, resolved, _ := LoadServerFile(fx.servers); resolved != nil {
		t.Fatalf("moved list after uninstall = %v, want empty", resolved)
	}
}

func TestInstall_OtherKeysAndEntriesUntouched(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, fx.in.Install(nil))
	after := fx.read(fx.claude)
	for _, s := range []string{`"numStartups": 42,`, `{"mcpServers": {"slack": {"command": "nested"}}, "allowedTools": ["a","b"]}`,
		`"tips": "h\u00e9llo ✓ é",`, `"z":[1,2,  3]`} {
		if !strings.Contains(after, s) {
			t.Fatalf("%s changed:\n%s", s, after)
		}
	}
}

// The settings files are never read or written: install and uninstall
// succeed with an unreadable settings file, which stays exactly as it was.
func TestInstall_SettingsNeverReadOrWritten(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	before, err := os.Stat(fx.settings)
	must(t, err)
	must(t, os.Chmod(fx.settings, 0))
	must(t, fx.in.Install(nil))
	must(t, fx.in.Uninstall())
	must(t, os.Chmod(fx.settings, 0o600))
	after, err := os.Stat(fx.settings)
	must(t, err)
	if fx.read(fx.settings) != fixtureSettings || !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("settings.json changed")
	}
}

func TestInstall_EscapedNames(t *testing.T) {
	cfg := `{"mcpServers": {
  "my server": {"command": "a-mcp"},
  "team/weather":{"command":"w-mcp"},
  "café":  {"command": "c-mcp", "args": ["x"]}
}}
`
	fx := newInstallFX(t, cfg, "")
	must(t, fx.in.Install(nil))
	got := fx.entries()
	want := map[string]string{
		"my server":    "http://127.0.0.1:7381/mcp/my%20server",
		"team/weather": "http://127.0.0.1:7381/mcp/team%2Fweather",
		"café":         "http://127.0.0.1:7381/mcp/caf%C3%A9",
	}
	for name, url := range want {
		if entryURL(got[name]) != url {
			t.Fatalf("%s url = %q, want %q", name, entryURL(got[name]), url)
		}
		if url != "http://127.0.0.1:7381"+EndpointPath(name) {
			t.Fatalf("%s: written URL and endpoint path disagree", name)
		}
	}
	if len(got) != 3 {
		t.Fatalf("entries %v, want the original names only", sortedNames(got))
	}
	must(t, fx.in.Uninstall())
	if fx.read(fx.claude) != cfg {
		t.Fatalf("not byte-identical after uninstall:\n%s", fx.read(fx.claude))
	}
}

func TestInstall_RerunMovesOnlyANewServer(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, fx.in.Install(nil))
	first, err := LoadManifest(fx.manifest)
	must(t, err)
	fx.setEntry("pypi", json.RawMessage(`{"command": "pypi-mcp",   "args": ["--x"]}`))
	before := fx.read(fx.claude)
	proxiedBefore := fx.entries()
	beforeEntry := string(proxiedBefore["pypi"])
	calls, reloads := fx.lc.count(), fx.reloads
	fx.refreshed, fx.out = nil, bytes.Buffer{}

	must(t, fx.in.Install(nil))
	after := fx.read(fx.claude)
	want := strings.Replace(before, beforeEntry, string(fx.entries()["pypi"]), 1)
	if after != want || after == before {
		t.Fatalf("more than the pypi entry changed:\nbefore %s\nafter  %s", before, after)
	}
	if !jsonEqual(fx.entries()["pypi"], httpEntry("pypi")) {
		t.Fatalf("pypi = %s", fx.entries()["pypi"])
	}
	if !reflect.DeepEqual(fx.refreshed, []string{"pypi"}) || !reflect.DeepEqual(fx.probed, []string{"pypi"}) {
		t.Fatalf("refreshed %v probed %v, want only pypi", fx.refreshed, fx.probed)
	}
	if fx.lc.ran("bootout", calls) || fx.lc.ran("bootstrap", calls) || fx.reloads != reloads+1 {
		t.Fatalf("the daemon must reload once, not restart (launchctl %v, reloads %d)", fx.lc.calls[calls:], fx.reloads-reloads)
	}
	m, err := LoadManifest(fx.manifest)
	must(t, err)
	if m.Servers["pypi"].Original != beforeEntry {
		t.Fatalf("pypi original = %q, want %q", m.Servers["pypi"].Original, beforeEntry)
	}
	for name, e := range first.Servers {
		if m.Servers[name] != e {
			t.Fatalf("%s record changed: %+v -> %+v", name, e, m.Servers[name])
		}
	}
	if !contains(fx.served(), "pypi") {
		t.Fatal("pypi not in the servers file's moved list")
	}

	// Nothing new: nothing changes, nothing reloads.
	reloads, snap := fx.reloads, fx.snapshot()
	must(t, fx.in.Install(nil))
	if !reflect.DeepEqual(fx.snapshot(), snap) || fx.reloads != reloads {
		t.Fatal("a run with nothing to move changed something")
	}
	if !strings.Contains(fx.out.String(), "no new stdio servers") {
		t.Fatalf("output: %s", fx.out.String())
	}

	// Uninstall gives back the original bytes of all four, pypi included.
	want = before
	for name, e := range first.Servers {
		want = strings.Replace(want, string(proxiedBefore[name]), e.Original, 1)
	}
	must(t, fx.in.Uninstall())
	if got := fx.read(fx.claude); got != want {
		t.Fatalf("uninstall after two runs not byte-identical:\n%s\nwant\n%s", got, want)
	}
}

func TestInstall_ProbeFailure_ConfigUntouchedDaemonFilesRestored(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, fx.in.Install(nil))
	fx.setEntry("pypi", json.RawMessage(`{"command": "pypi-mcp"}`))
	claudeBefore, serversBefore, manifestBefore := fx.read(fx.claude), fx.read(fx.servers), fx.read(fx.manifest)
	calls, reloads := fx.lc.count(), fx.reloads
	fx.probeFailing["pypi"] = true

	err := fx.in.Install(nil)
	if err == nil || !strings.Contains(err.Error(), "pypi") || !strings.Contains(err.Error(), "--skip") ||
		!strings.Contains(err.Error(), "not changed") {
		t.Fatalf("err = %v, want pypi refused, a --skip hint and the config unchanged", err)
	}
	if fx.read(fx.claude) != claudeBefore || fx.read(fx.manifest) != manifestBefore {
		t.Fatal("the Claude config or manifest changed although the probe failed")
	}
	if fx.read(fx.servers) != serversBefore {
		t.Fatal("the servers file was not restored")
	}
	if fx.reloads != reloads+2 {
		t.Fatalf("reloads = %d, want 2 (forward, then back to the restored file)", fx.reloads-reloads)
	}
	if fx.lc.ran("bootout", calls) || !fx.lc.isLoaded() {
		t.Fatal("a running daemon was restarted or stopped")
	}
	fx.assertNoEnv(err.Error(), fx.out.String())
}

func TestInstall_FirstRunProbeFailure_RemovesTheAgentItStarted(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	fx.probeFailing["github"] = true
	err := fx.in.Install(nil)
	if err == nil || !strings.Contains(err.Error(), "github") || !strings.Contains(err.Error(), "removed again") {
		t.Fatalf("err = %v", err)
	}
	if fx.read(fx.claude) != fixtureClaude {
		t.Fatal("claude.json changed")
	}
	fx.assertNoManifest()
	if fx.lc.isLoaded() || fileExists(fx.in.Agent.PlistPath) || fileExists(fx.servers) {
		t.Fatal("agent, plist or servers file left behind")
	}
	if entries, _ := os.ReadDir(fx.backups); len(entries) != 0 {
		t.Fatal("backup made although nothing was edited")
	}

	fx.out.Reset()
	must(t, fx.in.Install([]string{"github"}))
	if got := fx.entries(); !jsonEqual(got["slack"], httpEntry("slack")) || jsonEqual(got["github"], httpEntry("github")) {
		t.Fatalf("with --skip github: %s / %s", got["slack"], got["github"])
	}
	if strings.Contains(fx.read(fx.in.Agent.PlistPath), "--only") {
		t.Fatal("the plist must not carry --only: the daemon serves the servers file's moved list")
	}
}

func TestInstall_ListFailureRefusedUnlessSkipped(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	fx.failing["github"] = true
	err := fx.in.Install(nil)
	if err == nil || !strings.Contains(err.Error(), "github") || !strings.Contains(err.Error(), "--skip") {
		t.Fatalf("err = %v", err)
	}
	if fx.read(fx.claude) != fixtureClaude || fx.lc.ran("bootstrap", 0) || fileExists(fx.servers) {
		t.Fatalf("changed something before refusing (launchctl %v)", fx.lc.calls)
	}
	must(t, fx.in.Install([]string{"github"}))
}

func TestInstall_RunningDaemonReloadedNotRestarted(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	fx.lc.loaded = true // a daemon already running, e.g. a manual serve
	must(t, SaveServerFile(fx.servers, map[string]ServerConfig{}, nil))
	must(t, fx.in.Install(nil))
	if fx.lc.ran("bootout", 0) || fx.lc.ran("bootstrap", 0) || fx.reloads != 1 {
		t.Fatalf("launchctl %v, reloads %d: want one reload and no restart", fx.lc.calls, fx.reloads)
	}
}

func TestInstall_DaemonNotAnswering_ConfigUntouched(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	fx.daemonDown = true
	err := fx.in.Install(nil)
	if err == nil || !strings.Contains(err.Error(), "not answering") {
		t.Fatalf("err = %v", err)
	}
	if fx.read(fx.claude) != fixtureClaude || fx.lc.isLoaded() {
		t.Fatal("config changed or agent left loaded")
	}
	fx.assertNoManifest()
}

func TestInstall_DefinitionChangedBeforeTheEdit_ConfigUntouched(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	changed := strings.Replace(fixtureClaude, `"slack-mcp"]`, `"slack-mcp", "--v2"]`, 1)
	fx.in.edit = func(path, key string, fn func(json.RawMessage) (json.RawMessage, error)) error {
		fx.write(fx.claude, changed) // another program edits slack after the probe
		return jsonedit.EditKeyVerbatim(path, key, fn)
	}
	err := fx.in.Install(nil)
	if err == nil || !strings.Contains(err.Error(), "slack changed while installing") {
		t.Fatalf("err = %v", err)
	}
	if fx.read(fx.claude) != changed {
		t.Fatal("the edit wrote although a definition changed")
	}
	fx.assertNoManifest()
	if fx.lc.isLoaded() || fileExists(fx.servers) {
		t.Fatal("daemon side not restored")
	}
}

func TestInstall_ManifestRecordsOriginalAndWritten(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, fx.in.Install(nil))
	fi, err := os.Stat(fx.manifest)
	must(t, err)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("manifest mode %v, want 0600", fi.Mode().Perm())
	}
	m, err := LoadManifest(fx.manifest)
	must(t, err)
	if m.Version != manifestVersion || m.Port != testPort || !reflect.DeepEqual(sortedKeys(m.Servers), []string{"aimux", "github", "slack"}) {
		t.Fatalf("manifest = %+v", m)
	}
	if m.Servers["aimux"].Original != `{ "type": "stdio", "command": "/usr/local/bin/aimux", "args": [ "mcp", "serve" ] }` {
		t.Fatalf("aimux original = %q, want its raw bytes", m.Servers["aimux"].Original)
	}
	if !jsonEqual(json.RawMessage(m.Servers["slack"].Written), httpEntry("slack")) {
		t.Fatalf("slack written = %s", m.Servers["slack"].Written)
	}
	var raw map[string]json.RawMessage
	must(t, json.Unmarshal([]byte(fx.read(fx.manifest)), &raw))
	if want := []string{"port", "servers", "version"}; !reflect.DeepEqual(sortedNames(raw), want) {
		t.Fatalf("manifest keys %v, want %v", sortedNames(raw), want)
	}
}

func TestUninstall_OwnerEditedEntryKeptOthersRestored(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, fx.in.Install(nil))
	mine := json.RawMessage(`{"command": "my-slack"}`)
	fx.setEntry("slack", mine)
	fx.setEntry("github", nil)
	fx.out.Reset()
	must(t, fx.in.Uninstall())
	got := fx.entries()
	if !jsonEqual(got["slack"], mine) {
		t.Fatalf("slack = %s, want the owner's entry kept", got["slack"])
	}
	if _, ok := got["github"]; ok {
		t.Fatal("an entry the owner removed came back")
	}
	if !strings.Contains(fx.read(fx.claude), `"aimux":   { "type": "stdio", "command": "/usr/local/bin/aimux", "args": [ "mcp", "serve" ] }`) {
		t.Fatalf("aimux not restored to its exact bytes:\n%s", fx.read(fx.claude))
	}
	out := fx.out.String()
	if !strings.Contains(out, "slack: entry changed since install; kept yours") || !strings.Contains(out, "github: entry removed since install") {
		t.Fatalf("output does not report the kept entries:\n%s", out)
	}
	fx.assertNoManifest()
}

// A session that saved the entries in its own style still counts as what
// install wrote.
func TestUninstall_ReformattedEntryStillRestored(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, fx.in.Install(nil))
	fx.setEntry("slack", json.RawMessage(fmt.Sprintf(`{ "url" : %q, "type":"http" }`, EntryURL(testPort, "slack"))))
	must(t, fx.in.Uninstall())
	if !strings.Contains(fx.read(fx.claude), `"slack": {"command": "npx", "args": ["-y", "slack-mcp"], "env": {"SLACK_TOKEN": "envvalue-slack-0001"}}`) {
		t.Fatalf("slack not restored:\n%s", fx.read(fx.claude))
	}
}

func TestUninstall_NoManifest_RemovesLeftoverAgentOnly(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, os.MkdirAll(filepath.Dir(fx.in.Agent.PlistPath), 0o700))
	must(t, os.WriteFile(fx.in.Agent.PlistPath, []byte("x"), 0o600))
	fx.lc.loaded = true
	must(t, fx.in.Uninstall())
	if fx.lc.isLoaded() || fileExists(fx.in.Agent.PlistPath) || fx.read(fx.claude) != fixtureClaude {
		t.Fatal("leftover agent not removed, or the config changed")
	}
	calls := fx.lc.count()
	must(t, fx.in.Uninstall())
	if fx.lc.count() != calls || !strings.Contains(fx.out.String(), "not installed") {
		t.Fatal("a second uninstall without anything to remove called launchctl")
	}
}

func TestInstallStatus_DriftAndNotProxied(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	st, err := fx.in.InstallStatus()
	must(t, err)
	if st.State != InstallNotInstalled {
		t.Fatalf("state %s", st.State)
	}
	must(t, fx.in.Install([]string{"github"}))
	fx.setEntry("pypi", json.RawMessage(`{"command": "pypi-mcp"}`)) // added after install
	st, err = fx.in.InstallStatus()
	must(t, err)
	if st.State != InstallInPlace || !reflect.DeepEqual(st.Moved, []string{"aimux", "slack"}) ||
		!reflect.DeepEqual(st.NotProxied, []string{"pypi"}) || !reflect.DeepEqual(st.Skipped, []string{"github"}) || len(st.Drifted) != 0 {
		t.Fatalf("status = %+v", st)
	}
	// A running session saves its stale copy of slack.
	fx.setEntry("slack", json.RawMessage(`{"command": "npx", "args": ["-y", "slack-mcp"]}`))
	st, err = fx.in.InstallStatus()
	must(t, err)
	if st.State != InstallDrifted || !reflect.DeepEqual(st.Drifted, []string{"slack"}) || !reflect.DeepEqual(st.NotProxied, []string{"pypi"}) {
		t.Fatalf("status = %+v", st)
	}
	fx.out.Reset()
	fx.in.PrintInstallStatus(st)
	out := fx.out.String()
	for _, w := range []string{"Drifted", "slack", driftFix, "Not behind the proxy: pypi", "aimux mcp proxy install", "aimux mcp proxy edit",
		"Kept direct (skipped): github", "--unskip github"} {
		if !strings.Contains(out, w) {
			t.Fatalf("status output lacks %q:\n%s", w, out)
		}
	}
	// Drift is not moved again by a re-run: uninstall, then install fixes it.
	p, err := fx.in.Plan(nil)
	must(t, err)
	if !reflect.DeepEqual(p.Move, []string{"pypi"}) {
		t.Fatalf("plan moves %v, want only pypi (slack drifted, github skipped)", p.Move)
	}
}

func TestInstallStatus_UpgradeRestartPending(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, fx.in.Install(nil))
	fx.in.Version, fx.daemonVersion = "1.2.0", "1.1.0"
	st, err := fx.in.InstallStatus()
	must(t, err)
	if !st.UpgradePending || st.DaemonVersion != "1.1.0" {
		t.Fatalf("status = %+v, want an upgrade restart pending", st)
	}
	fx.out.Reset()
	fx.in.PrintInstallStatus(st)
	if !strings.Contains(fx.out.String(), "upgrade restart pending") || !strings.Contains(fx.out.String(), "kickstart -k gui/501/com.aimux.mcp-proxy") {
		t.Fatalf("output:\n%s", fx.out.String())
	}
	fx.daemonVersion = "1.2.0"
	if st, _ = fx.in.InstallStatus(); st.UpgradePending {
		t.Fatal("same version reported as pending")
	}
	fx.daemonDown = true
	if st, _ = fx.in.InstallStatus(); st.UpgradePending {
		t.Fatal("a daemon that is not running reported as pending")
	}
}

func TestInstall_ReloadNotesPendingUpgrade(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, fx.in.Install([]string{"github"}))
	fx.in.Version, fx.daemonVersion = "2.0", ""
	fx.out.Reset()
	fx.in.Unskip = []string{"github"}
	must(t, fx.in.Install(nil))
	if !strings.Contains(fx.out.String(), "upgrade restart pending: the running proxy is aimux an older version") {
		t.Fatalf("output:\n%s", fx.out.String())
	}
}

func TestDryRun_WritesNothing(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	cat, err := LoadCatalog(fx.catalog)
	must(t, err)
	delete(cat.Servers, "aimux")
	must(t, cat.Save(fx.catalog))
	snap := fx.snapshot()

	p, err := fx.in.DryRun([]string{"github"})
	must(t, err)
	if !reflect.DeepEqual(p.Move, []string{"aimux", "slack"}) || !reflect.DeepEqual(p.Skip, []string{"github"}) ||
		!reflect.DeepEqual(p.Listed, []string{"aimux"}) || len(p.Refresh) != 0 {
		t.Fatalf("plan = %+v", p)
	}
	if len(fx.refreshedInto) != 1 || fx.refreshedInto[0] == fx.catalog || fileExists(fx.refreshedInto[0]) {
		t.Fatalf("dry run listed into %v, want a temporary catalog that is gone", fx.refreshedInto)
	}
	fx.in.PrintPlan(p)
	if !reflect.DeepEqual(fx.snapshot(), snap) || fx.lc.count() != 0 {
		t.Fatal("dry run wrote a file or called launchctl")
	}
	out := fx.out.String()
	for _, w := range []string{"aimux -> http://127.0.0.1:7381/mcp/aimux", "slack -> http://127.0.0.1:7381/mcp/slack", "--skip): github"} {
		if !strings.Contains(out, w) {
			t.Fatalf("dry run output lacks %q:\n%s", w, out)
		}
	}
	fx.assertNoEnv(out)

	fx.failing["aimux"] = true
	if p, err = fx.in.DryRun(nil); err != nil || !reflect.DeepEqual(p.Refresh, []string{"aimux"}) {
		t.Fatalf("plan = %+v, err %v; want aimux in Refresh", p, err)
	}
}

func TestInstall_NeverPrintsEnv(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	p, err := fx.in.Plan(nil)
	must(t, err)
	fx.in.PrintPlan(p)
	must(t, fx.in.Install(nil))
	st, err := fx.in.InstallStatus()
	must(t, err)
	fx.in.PrintInstallStatus(st)
	must(t, fx.in.Uninstall())
	fx.assertNoEnv(fx.out.String())
	if !strings.Contains(fx.out.String(), "aimux mcp proxy edit") {
		t.Fatalf("install summary lacks the edit hint:\n%s", fx.out.String())
	}
}

func TestInstall_SavesResolvedDefinitionsAndKeepsSecretsOutOfThePlist(t *testing.T) {
	t.Setenv("AIMUX_TEST_BIN", "/opt/custom/bin/slack-mcp")
	t.Setenv("AIMUX_TEST_TOKEN", "envvalue-resolved-0004")
	cfg := `{"mcpServers": {
  "slack": {"command": "${AIMUX_TEST_BIN}", "args": ["--team", "${AIMUX_TEST_UNSET:-dflt}"], "env": {"TOKEN": "${AIMUX_TEST_TOKEN}"}}
}}
`
	fx := newInstallFX(t, cfg, "")
	must(t, fx.in.Install(nil))
	saved, resolved, err := LoadServerFile(fx.servers)
	must(t, err)
	got := saved["slack"]
	if got.Command != "/opt/custom/bin/slack-mcp" || !reflect.DeepEqual(got.Args, []string{"--team", "dflt"}) || got.Env["TOKEN"] != "envvalue-resolved-0004" {
		t.Fatalf("saved = %+v, want the definitions resolved in the installing environment", got)
	}
	if !reflect.DeepEqual(resolved, []string{"slack"}) {
		t.Fatalf("moved list = %v", resolved)
	}
	fi, err := os.Stat(fx.servers)
	must(t, err)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("servers file mode %v", fi.Mode().Perm())
	}
	plist := fx.read(fx.in.Agent.PlistPath)
	if strings.Contains(plist, "envvalue-resolved-0004") || !strings.Contains(plist, "/opt/homebrew/bin:/usr/bin") {
		t.Fatalf("plist must carry PATH and no secrets:\n%s", plist)
	}
	m, err := LoadManifest(fx.manifest)
	must(t, err)
	if !strings.Contains(m.Servers["slack"].Original, "${AIMUX_TEST_TOKEN}") {
		t.Fatal("the manifest must keep the entry as written, variables included")
	}
	if strings.Contains(fx.out.String(), "envvalue-resolved-0004") {
		t.Fatalf("resolved value printed:\n%s", fx.out.String())
	}
}

func TestInstall_RefusalsChangeNothing(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	if err := fx.in.Install([]string{"nosuch"}); err == nil || !strings.Contains(err.Error(), "nosuch") {
		t.Fatalf("unknown --skip: err = %v", err)
	}
	if err := fx.in.Install([]string{"remote"}); err == nil || !strings.Contains(err.Error(), "remote") {
		t.Fatalf("--skip of a remote server: err = %v", err)
	}
	const cfg = `{"a": 1,
 "b": [1, 2]}`
	empty := newInstallFX(t, cfg, "")
	if err := empty.in.Install(nil); err == nil || !strings.Contains(err.Error(), "no stdio MCP servers") {
		t.Fatalf("nothing to move: err = %v", err)
	}
	if fx.read(fx.claude) != fixtureClaude || empty.read(empty.claude) != cfg || fx.lc.count() != 0 {
		t.Fatal("a refused run changed something")
	}
}

func TestInstall_OtherPortRefused(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, fx.in.Install([]string{"github"}))
	fx.in.Port = 7400
	snap := fx.snapshot()
	if err := fx.in.Install(nil); err == nil || !strings.Contains(err.Error(), "--port 7381") {
		t.Fatalf("err = %v, want the installed port named", err)
	}
	if !reflect.DeepEqual(fx.snapshot(), snap) {
		t.Fatal("a refused run changed a file")
	}
}

func TestInstall_ConcurrentRunRefused(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	unlock, err := lockInstall(fx.manifest)
	must(t, err)
	defer unlock()
	if err := fx.in.Install(nil); err == nil || !strings.Contains(err.Error(), "another") {
		t.Fatalf("err = %v, want another run in progress", err)
	}
	if err := fx.in.Uninstall(); err == nil || !strings.Contains(err.Error(), "another") {
		t.Fatalf("uninstall err = %v, want another run in progress", err)
	}
}

// Another program rewriting ~/.claude.json while install edits it: jsonedit
// retries, so install either lands whole or fails cleanly, never half.
func TestInstall_ConcurrentWriter_NoHalfState(t *testing.T) {
	for round := 0; round < 6; round++ {
		pause := time.Duration(round*round) * time.Millisecond
		fx := newInstallFX(t, fixtureClaude, fixtureSettings)
		var stop atomic.Bool
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; !stop.Load(); i++ {
				b, err := os.ReadFile(fx.claude)
				if err != nil {
					continue
				}
				var m map[string]json.RawMessage
				if json.Unmarshal(b, &m) != nil {
					continue
				}
				m["numStartups"] = json.RawMessage(fmt.Sprint(i))
				nb, _ := json.MarshalIndent(m, "", "  ")
				tmp := fx.claude + ".w"
				_ = os.WriteFile(tmp, nb, 0o600)
				_ = os.Rename(tmp, fx.claude)
				time.Sleep(pause)
			}
		}()
		err := fx.in.Install(nil)
		stop.Store(true)
		wg.Wait()
		t.Logf("round %d: install err = %v", round, err)

		got := fx.entries()
		proxied := 0
		for _, s := range []string{"aimux", "github", "slack"} {
			if jsonEqual(got[s], httpEntry(s)) {
				proxied++
			}
		}
		if proxied != 0 && proxied != 3 {
			t.Fatalf("round %d: half state %v (err %v)", round, got, err)
		}
		m, merr := LoadManifest(fx.manifest)
		must(t, merr)
		if err != nil {
			if m != nil || proxied != 0 {
				t.Fatalf("round %d: install failed (%v) but left a manifest or proxied entries", round, err)
			}
			continue
		}
		if m == nil {
			t.Fatalf("round %d: install succeeded without a manifest", round)
		}
		if proxied == 0 {
			// The writer ignores locks and saves from a stale read, so it can
			// put the old entries back after install verified its own (as a
			// stale Claude Code could). No lock prevents that; status must
			// then report every moved server as drifted, for the owner to
			// re-run install.
			st, serr := fx.in.InstallStatus()
			must(t, serr)
			if !reflect.DeepEqual(st.Drifted, []string{"aimux", "github", "slack"}) {
				t.Fatalf("round %d: entries reverted after install but status reports drift %v", round, st.Drifted)
			}
		}
		must(t, fx.in.Uninstall())
		for _, s := range []string{"aimux", "github", "slack"} {
			if jsonEqual(fx.entries()[s], httpEntry(s)) {
				t.Fatalf("round %d: %s still proxied after uninstall", round, s)
			}
		}
	}
}

func TestManifest_OtherFormatRefused(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	fx.write(fx.manifest, `{"version": 1, "state": "done", "moved": {}}`)
	for name, run := range map[string]func() error{"install": func() error { return fx.in.Install(nil) }, "uninstall": fx.in.Uninstall} {
		if err := run(); err == nil || !strings.Contains(err.Error(), "older aimux design") ||
			!strings.Contains(err.Error(), "move "+fx.manifest+" aside and run `aimux mcp proxy uninstall`") {
			t.Fatalf("%s err = %v, want a format refusal", name, err)
		}
	}
	if fx.read(fx.claude) != fixtureClaude {
		t.Fatal("a refused run changed the config")
	}
}

func TestManifest_CorruptNamesBackupAndSteps(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, fx.in.Install(nil))
	backup := newestBackup(fx.backups, ".claude.json")
	if backup == "" {
		t.Fatal("no backup")
	}
	fx.write(fx.manifest, "{not json")
	for name, run := range map[string]func() error{"install": func() error { return fx.in.Install(nil) }, "uninstall": fx.in.Uninstall} {
		err := run()
		if err == nil || !strings.Contains(err.Error(), backup) || !strings.Contains(err.Error(), "mcpServers") {
			t.Fatalf("%s err = %v, want the backup %s and restore steps", name, err, backup)
		}
	}
}

func TestJSONEqual(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{`{"a":1,"b":[1,2]}`, `{ "b": [1, 2], "a": 1 }`, true},
		{`{"a":1}`, `{"a":1.0}`, false},
		{`{"a":"x"}`, `{"a":"y"}`, false},
		{`not json`, `not json`, false},
	}
	for _, c := range cases {
		if got := jsonEqual(json.RawMessage(c.a), json.RawMessage(c.b)); got != c.want {
			t.Errorf("jsonEqual(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

func TestSameDefinition(t *testing.T) {
	a := ServerConfig{Command: "npx", Args: []string{}, Env: map[string]string{}}
	if !sameDefinition(a, ServerConfig{Command: "npx"}) {
		t.Fatal("empty and nil args/env differ")
	}
	if sameDefinition(a, ServerConfig{Command: "npx", Args: []string{"x"}}) || sameDefinition(a, ServerConfig{Command: "uvx"}) ||
		sameDefinition(ServerConfig{Env: map[string]string{"A": "1"}}, ServerConfig{Env: map[string]string{"A": "2"}}) {
		t.Fatal("different definitions compare equal")
	}
}

// jsonedit may run fn again after its own rename (a torn read-back, or a
// writer that built on our file): that run sees the entries install wrote.
// It must count them as moved, with the originals read before the edit.
func TestInstall_EditRunsAgainOnItsOwnWrite_Recorded(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	fx.in.edit = func(path, key string, fn func(json.RawMessage) (json.RawMessage, error)) error {
		if err := jsonedit.EditKeyVerbatim(path, key, fn); err != nil {
			return err
		}
		return jsonedit.EditKeyVerbatim(path, key, fn) // the retry, on our own file
	}
	must(t, fx.in.Install(nil))
	m, err := LoadManifest(fx.manifest)
	must(t, err)
	if m.Servers["aimux"].Original != `{ "type": "stdio", "command": "/usr/local/bin/aimux", "args": [ "mcp", "serve" ] }` {
		t.Fatalf("original = %q, want the bytes read before the edit", m.Servers["aimux"].Original)
	}
	if got := fx.served(); !reflect.DeepEqual(got, []string{"aimux", "github", "slack"}) {
		t.Fatalf("served %v: the daemon must keep serving entries that point at it", got)
	}
	fx.in.edit = nil
	must(t, fx.in.Uninstall())
	if fx.read(fx.claude) != fixtureClaude {
		t.Fatal("uninstall not byte-identical")
	}
}

// An edit that reports an error although its value landed (a conflict
// detected after our rename): install finds its entries and records them
// instead of unserving servers whose entries point at the proxy.
func TestInstall_EditErrorAfterItsValueLanded_Recorded(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	fx.in.edit = func(path, key string, fn func(json.RawMessage) (json.RawMessage, error)) error {
		if err := jsonedit.EditKeyVerbatim(path, key, fn); err != nil {
			return err
		}
		return jsonedit.ErrConcurrentWrite
	}
	must(t, fx.in.Install(nil))
	m, err := LoadManifest(fx.manifest)
	must(t, err)
	if !reflect.DeepEqual(sortedKeys(m.Servers), []string{"aimux", "github", "slack"}) {
		t.Fatalf("manifest servers %v", sortedKeys(m.Servers))
	}
	if got := fx.served(); len(got) != 3 || !fx.lc.isLoaded() {
		t.Fatalf("served %v loaded %v", got, fx.lc.isLoaded())
	}
}

func TestInstall_EntryRemovedBeforeTheEdit_NotMovedNotServed(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	fx.in.edit = func(path, key string, fn func(json.RawMessage) (json.RawMessage, error)) error {
		fx.in.edit = nil
		fx.setEntry("github", nil) // the owner removes github after the probe
		return jsonedit.EditKeyVerbatim(path, key, fn)
	}
	reloads := fx.reloads
	must(t, fx.in.Install(nil))
	m, err := LoadManifest(fx.manifest)
	must(t, err)
	if !reflect.DeepEqual(sortedKeys(m.Servers), []string{"aimux", "slack"}) {
		t.Fatalf("manifest %v", sortedKeys(m.Servers))
	}
	if got := fx.served(); !reflect.DeepEqual(got, []string{"aimux", "slack"}) || fx.reloads != reloads+1 {
		t.Fatalf("served %v reloads %d: github must leave the moved list", got, fx.reloads-reloads)
	}
}

func TestInstall_EveryEntryRemovedBeforeTheEdit_NoManifest(t *testing.T) {
	cfg := `{"mcpServers": {"slack": {"command": "npx"}}}`
	fx := newInstallFX(t, cfg, "")
	fx.in.edit = func(path, key string, fn func(json.RawMessage) (json.RawMessage, error)) error {
		fx.setEntry("slack", nil)
		return jsonedit.EditKeyVerbatim(path, key, fn)
	}
	if err := fx.in.Install(nil); err == nil || !strings.Contains(err.Error(), "removed") {
		t.Fatalf("err = %v", err)
	}
	fx.assertNoManifest()
	if fx.lc.isLoaded() || fileExists(fx.servers) {
		t.Fatal("daemon side not restored")
	}
}

func TestUninstall_MissingClaudeConfig_StillFinishes(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, fx.in.Install(nil))
	must(t, os.Remove(fx.claude))
	fx.out.Reset()
	must(t, fx.in.Uninstall())
	fx.assertNoManifest()
	if fx.lc.isLoaded() || !strings.Contains(fx.out.String(), "slack: entry removed since install") {
		t.Fatalf("agent loaded %v, output:\n%s", fx.lc.isLoaded(), fx.out.String())
	}
}

func TestUninstall_RerunAfterFailureSaysAlreadyRestored(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, fx.in.Install(nil))
	good := fx.read(fx.servers)
	fx.write(fx.servers, "{broken")
	if err := fx.in.Uninstall(); err == nil || !strings.Contains(err.Error(), "uninstall` again") {
		t.Fatalf("err = %v", err)
	}
	fx.write(fx.servers, good)
	fx.out.Reset()
	must(t, fx.in.Uninstall())
	out := fx.out.String()
	if strings.Contains(out, "changed since install") || !strings.Contains(out, "slack: already restored") {
		t.Fatalf("output:\n%s", out)
	}
	if fx.read(fx.claude) != fixtureClaude {
		t.Fatal("config not restored")
	}
}

func TestUninstall_RemovesTheMovedDefinitions(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, SaveServerFile(fx.servers, map[string]ServerConfig{"other": {Command: "other-mcp"}}, nil))
	must(t, fx.in.Install(nil))
	must(t, fx.in.Uninstall())
	saved, resolved, err := LoadServerFile(fx.servers)
	must(t, err)
	if !reflect.DeepEqual(sortedKeys(saved), []string{"other"}) || len(resolved) != 0 {
		t.Fatalf("servers file %v %v, want only the definition install did not add", sortedKeys(saved), resolved)
	}
	fx.assertNoEnv(fx.read(fx.servers))
}

func TestInstall_SkipRecordedAndKeptUntilUnskip(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, fx.in.Install([]string{"github"}))
	m, err := LoadManifest(fx.manifest)
	must(t, err)
	if !reflect.DeepEqual(m.Skipped, []string{"github"}) {
		t.Fatalf("skipped = %v", m.Skipped)
	}
	snap := fx.snapshot()
	fx.out.Reset()
	must(t, fx.in.Install(nil))
	if !reflect.DeepEqual(fx.snapshot(), snap) || !strings.Contains(fx.out.String(), "--unskip github") {
		t.Fatalf("a re-run moved a skipped server or changed a file; output:\n%s", fx.out.String())
	}
	fx.in.Unskip = []string{"nosuch"}
	if err := fx.in.Install(nil); err == nil || !strings.Contains(err.Error(), "--unskip nosuch") {
		t.Fatalf("err = %v", err)
	}
	fx.in.Unskip = []string{"github"}
	must(t, fx.in.Install(nil))
	if !jsonEqual(fx.entries()["github"], httpEntry("github")) {
		t.Fatal("--unskip did not move github")
	}
	if m, _ = LoadManifest(fx.manifest); len(m.Skipped) != 0 {
		t.Fatalf("skipped = %v after --unskip", m.Skipped)
	}
}

// A server the daemon has configured but serves no endpoint for (its
// catalog entry is missing there) must not get a Claude entry: /mcp/<name>
// would answer 404.
func TestInstall_ConfiguredWithoutEndpoint_ConfigUntouched(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	fx.noEndpoint["slack"] = true
	err := fx.in.Install(nil)
	if err == nil || !strings.Contains(err.Error(), "does not serve: slack") {
		t.Fatalf("err = %v", err)
	}
	if fx.read(fx.claude) != fixtureClaude {
		t.Fatal("an entry was written for a server without an endpoint")
	}
	fx.assertNoManifest()
}

// The manifest is written before the edit, so a failure to write it
// changes nothing in Claude's config.
func TestInstall_ManifestWriteFails_ConfigUntouched(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	fx.in.writeManifest = func(string, *Manifest) error { return errors.New("disk full") }
	err := fx.in.Install(nil)
	if err == nil || !strings.Contains(err.Error(), "disk full") || !strings.Contains(err.Error(), "not changed") {
		t.Fatalf("err = %v", err)
	}
	if fx.read(fx.claude) != fixtureClaude {
		t.Fatal("the config changed although the record could not be written")
	}
	fx.assertNoManifest()
	if fx.lc.isLoaded() || fileExists(fx.servers) {
		t.Fatal("daemon side not restored")
	}
}

// crashAt makes install die (a panic stands in for the process being
// killed) at the edit: before it, or after it landed.
func crashAt(fx *installFX, afterEdit bool) {
	fx.in.edit = func(path, key string, fn func(json.RawMessage) (json.RawMessage, error)) error {
		if afterEdit {
			if err := jsonedit.EditKeyVerbatim(path, key, fn); err != nil {
				return err
			}
		}
		panic("killed")
	}
	func() {
		defer func() { _ = recover() }()
		_ = fx.in.Install(nil)
	}()
	fx.in.edit = nil
}

func TestInstall_CrashBeforeTheEdit_UninstallCleansUp(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	crashAt(fx, false)
	if m, err := LoadManifest(fx.manifest); err != nil || m == nil || len(m.Servers) != 3 {
		t.Fatalf("manifest %+v, err %v; want the planned servers recorded before the edit", m, err)
	}
	fx.out.Reset()
	must(t, fx.in.Uninstall())
	if fx.read(fx.claude) != fixtureClaude || strings.Contains(fx.out.String(), "changed since install") {
		t.Fatalf("uninstall after a crash before the edit changed the config or blamed the owner:\n%s", fx.out.String())
	}
	fx.assertNoManifest()
	if fx.lc.isLoaded() {
		t.Fatal("agent left loaded")
	}
}

func TestInstall_CrashAfterTheEdit_UninstallRestoresTheOriginals(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	crashAt(fx, true)
	if !jsonEqual(fx.entries()["slack"], httpEntry("slack")) {
		t.Fatal("the edit did not land before the crash")
	}
	must(t, fx.in.Uninstall())
	if fx.read(fx.claude) != fixtureClaude {
		t.Fatalf("not restored byte for byte:\n%s", fx.read(fx.claude))
	}
	fx.assertNoManifest()
}

func TestInstall_EditFails_ManifestBackToItsPreviousBytes(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	fail := func(string, string, func(json.RawMessage) (json.RawMessage, error)) error {
		return errors.New("locked")
	}

	fx.in.edit = fail // first install: no manifest before, none after
	if err := fx.in.Install(nil); err == nil {
		t.Fatal("install succeeded although the edit failed")
	}
	fx.assertNoManifest()

	fx.in.edit = nil
	must(t, fx.in.Install([]string{"github"}))
	before := fx.read(fx.manifest)
	fx.in.edit = fail
	fx.in.Unskip = []string{"github"}
	if err := fx.in.Install(nil); err == nil {
		t.Fatal("re-run succeeded although the edit failed")
	}
	if fx.read(fx.manifest) != before {
		t.Fatalf("manifest not back to its previous bytes:\n%s", fx.read(fx.manifest))
	}
}

// A moved server the owner removed from ~/.claude.json (claude mcp remove)
// is reported as removed, not as drift: nothing points at a stale copy, and
// the drift advice (uninstall, install) would churn every server for nothing.
func TestInstallStatus_RemovedEntryIsNotDrift(t *testing.T) {
	fx := newInstallFX(t, fixtureClaude, fixtureSettings)
	must(t, fx.in.Install(nil))
	must(t, jsonedit.EditKeyVerbatim(fx.claude, "mcpServers", func(old json.RawMessage) (json.RawMessage, error) {
		return jsonedit.SetMember(old, "slack", nil)
	}))
	st, err := fx.in.InstallStatus()
	must(t, err)
	if !reflect.DeepEqual(st.Removed, []string{"slack"}) || len(st.Drifted) != 0 || st.State != InstallInPlace {
		t.Fatalf("status = %+v, want slack removed, no drift, still in place", st)
	}
	fx.setEntry("github", json.RawMessage(`{"command": "gh-mcp"}`)) // and a real drift
	st, err = fx.in.InstallStatus()
	must(t, err)
	if !reflect.DeepEqual(st.Removed, []string{"slack"}) || !reflect.DeepEqual(st.Drifted, []string{"github"}) || st.State != InstallDrifted {
		t.Fatalf("status = %+v, want slack removed and github drifted", st)
	}
	fx.out.Reset()
	fx.in.PrintInstallStatus(st)
	if out := fx.out.String(); !strings.Contains(out, "Removed from") || !strings.Contains(out, "slack") {
		t.Fatalf("status output does not report the removed entry:\n%s", out)
	}
}

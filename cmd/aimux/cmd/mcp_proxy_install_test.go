package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/harness"
	"github.com/zanetworker/aimux/internal/mcpproxy"
)

func TestProxyInstaller_Paths(t *testing.T) {
	home := t.TempDir()
	var out bytes.Buffer
	in := proxyInstaller(home, 7390, &out)
	checks := map[string][2]string{
		"ClaudeConfig": {in.ClaudeConfig, filepath.Join(home, ".claude.json")},
		"ManifestPath": {in.ManifestPath, filepath.Join(home, ".aimux", "mcp-proxy-install.json")},
		"BackupDir":    {in.BackupDir, filepath.Join(home, ".aimux", "backups")},
		"ServersPath":  {in.ServersPath, filepath.Join(home, ".aimux", "mcp-servers.json")},
		"CatalogPath":  {in.CatalogPath, filepath.Join(home, ".aimux", "mcp-catalog.json")},
		"Version":      {in.Version, rootCmd.Version},
		"Label":        {in.Agent.Label, "com.aimux.mcp-proxy"},
		"PlistPath":    {in.Agent.PlistPath, filepath.Join(home, "Library", "LaunchAgents", "com.aimux.mcp-proxy.plist")},
		"LogDir":       {in.Agent.LogDir, filepath.Join(home, ".aimux", "logs", "mcp-proxy")},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s = %q, want %q", name, c[0], c[1])
		}
	}
	if want := []string{"mcp", "proxy", "serve", "--port", "7390"}; !reflect.DeepEqual(in.Agent.Args, want) {
		t.Errorf("Args = %v, want %v", in.Agent.Args, want)
	}
	if in.Port != 7390 || in.Out != &out || in.Agent.Run == nil || in.Status == nil || in.Probe == nil || in.Reload == nil || in.Refresh == nil || in.Sessions == nil {
		t.Errorf("installer not fully wired: %+v", in)
	}
}

func TestGetProxyStatus(t *testing.T) {
	addr, _ := fakeDaemon(t, fixtureReport())
	rep, err := getProxyStatus(addr)
	if err != nil || !rep.Running || len(rep.Servers) != 2 {
		t.Fatalf("got %+v, %v", rep, err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	_ = ln.Close()
	rep, err = getProxyStatus(closed)
	if err != nil || rep.Running {
		t.Fatalf("closed port: got %+v, %v; want not running, no error", rep, err)
	}
}

type fakeLive []harness.Holder

func (f fakeLive) Holders(harness.Mode) ([]harness.Holder, error) { return f, nil }

func TestOpenSessionIDs(t *testing.T) {
	got, err := openSessionIDs([]harness.LiveSessions{
		fakeLive{{PID: 1, SessionID: "b"}, {PID: 2, SessionID: "a"}, {PID: 3, SessionID: "b"}, {PID: 4}},
		fakeLive{{PID: 5, SessionID: "c"}},
	})
	if err != nil || !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err := openSessionIDs(nil); err != nil || len(got) != 0 {
		t.Fatalf("no sources: %v, %v", got, err)
	}
}

func TestMCPProxyCmd_InstallCommands(t *testing.T) {
	cmd := newMCPProxyCmd()
	install, _, err := cmd.Find([]string{"install"})
	if err != nil || install.Name() != "install" {
		t.Fatalf("install command missing: %v", err)
	}
	for _, f := range []string{"dry-run", "skip", "unskip", "port"} {
		if install.Flags().Lookup(f) == nil {
			t.Errorf("install lacks --%s", f)
		}
	}
	for _, f := range []string{"reapply", "local-rules"} {
		if install.Flags().Lookup(f) != nil {
			t.Errorf("install still has the removed --%s", f)
		}
	}
	if un, _, err := cmd.Find([]string{"uninstall"}); err != nil || un.Name() != "uninstall" {
		t.Fatalf("uninstall command missing: %v", err)
	}
}

func TestMCPProxyInstall_DryRunTouchesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Commands that cannot start: the dry run tries to list slack's tools.
	cfg := `{"mcpServers": {"slack": {"command": "/nonexistent/aimux-test/slack-mcp", "env": {"TOKEN": "envvalue-do-not-print"}}, "gh": {"command": "/nonexistent/aimux-test/gh-mcp"}}}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newMCPProxyCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"install", "--dry-run", "--skip", "gh"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "slack") || !strings.Contains(got, "gh") || strings.Contains(got, "envvalue-do-not-print") ||
		!strings.Contains(got, "Could not list the tools of: slack") {
		t.Fatalf("dry run output:\n%s", got)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".claude.json")); string(b) != cfg { // #nosec G304 -- test temp home
		t.Fatal("dry run changed the Claude config")
	}
	if _, err := os.Stat(filepath.Join(home, ".aimux")); !os.IsNotExist(err) {
		t.Fatal("dry run created ~/.aimux")
	}
	if _, err := os.Stat(filepath.Join(home, "Library")); !os.IsNotExist(err) {
		t.Fatal("dry run touched LaunchAgents")
	}
}

func TestMCPProxyInstall_DryRunListsToolsOfRealServers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`{"mcpServers": {"wiki": {"command": %q, "env": {%q: "1"}}}}`, exe, fakeMCPEnv)
	settings := `{"permissions": {"deny": ["mcp__wiki__delete_page"], "allow": ["mcp__wiki__read_page"]}}`
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(home, ".claude.json"), []byte(cfg), 0o600))
	must(os.MkdirAll(filepath.Join(home, ".claude"), 0o700))
	must(os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(settings), 0o600))
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	cmd := newMCPProxyCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"install", "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	got := out.String()
	for _, w := range []string{"Started each of these servers once to list its tools", "wiki -> http://127.0.0.1:7381/mcp/wiki"} {
		if !strings.Contains(got, w) {
			t.Fatalf("dry run output lacks %q:\n%s", w, got)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".aimux")); !os.IsNotExist(err) {
		t.Fatal("dry run created ~/.aimux")
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json")); string(b) != settings { // #nosec G304 -- test temp home
		t.Fatal("dry run changed the settings")
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Fatalf("dry run left %d entries in the temp dir", len(left))
	}
}

func TestMCPProxyUninstall_NotInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cmd := newMCPProxyCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"uninstall"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "not installed") {
		t.Fatalf("output:\n%s", out.String())
	}
}

func TestPrintInstallSection_NotInstalled(t *testing.T) {
	var out bytes.Buffer
	printInstallSection(proxyInstaller(t.TempDir(), 7381, &out), &out, false)
	if !strings.Contains(out.String(), "not installed") {
		t.Fatalf("output:\n%s", out.String())
	}
}

func TestProxyStatus_JSONIncludesInstall(t *testing.T) {
	inst := &mcpproxy.InstallState{State: mcpproxy.InstallDrifted, Moved: []string{"github", "slack"}, Drifted: []string{"slack"}, NotProxied: []string{"pypi"}}
	addr, _ := fakeDaemon(t, fixtureReport())
	var out bytes.Buffer
	if err := proxyStatus(context.Background(), proxyStatusOpts{addr: addr, json: true, install: inst}, &out, statusNow); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Running bool `json:"running"`
		Servers []any
		Install struct {
			State      string   `json:"state"`
			Drifted    []string `json:"drifted"`
			NotProxied []string `json:"notProxied"`
		} `json:"install"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !got.Running || len(got.Servers) != 2 || got.Install.State != "drifted" || len(got.Install.Drifted) != 1 || len(got.Install.NotProxied) != 1 {
		t.Fatalf("json = %s", out.String())
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	_ = ln.Close()
	out.Reset()
	if err := proxyStatus(context.Background(), proxyStatusOpts{addr: closed, json: true, install: inst}, &out, statusNow); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"running":false`) || !strings.Contains(out.String(), `"state":"drifted"`) {
		t.Fatalf("not running json = %s", out.String())
	}
}

func TestProxyInstaller_AgentEnvironmentIsPathAndHomeOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", "/opt/custom/bin:/usr/bin") // where npx/uv live only for this shell
	t.Setenv("SOME_TOKEN", "envvalue-not-for-the-plist")
	in := proxyInstaller(home, 7381, &bytes.Buffer{})
	if want := map[string]string{"PATH": "/opt/custom/bin:/usr/bin", "HOME": home}; !reflect.DeepEqual(in.Agent.Env, want) {
		t.Fatalf("Env = %v, want %v", in.Agent.Env, want)
	}
	plist := string(in.Agent.Plist())
	if !strings.Contains(plist, "/opt/custom/bin:/usr/bin") || strings.Contains(plist, "envvalue-not-for-the-plist") {
		t.Fatalf("plist:\n%s", plist)
	}
	if in.Probe == nil {
		t.Fatal("Probe not wired")
	}
}

func TestProbeProxy_NamedServersWithHeader(t *testing.T) {
	addr, reqs := fakeDaemon(t, fixtureReport())
	rep, err := probeProxy(addr, []string{"slack", "git hub"})
	if err != nil || len(rep.Servers) != 2 {
		t.Fatalf("got %+v, %v", rep, err)
	}
	if len(*reqs) != 1 || !strings.HasPrefix((*reqs)[0], "POST /status/probe?") ||
		!strings.Contains((*reqs)[0], "server=slack") || !strings.Contains((*reqs)[0], "server=git+hub") ||
		!strings.Contains((*reqs)[0], "X-Aimux-Probe=1") {
		t.Fatalf("requests %v", *reqs)
	}
}

func TestProxyBackends_InstallResolvedDefinitionsWin(t *testing.T) {
	dir := t.TempDir()
	claude := filepath.Join(dir, ".claude.json")
	servers := filepath.Join(dir, "servers.json")
	cfg := `{"mcpServers": {"slack": {"command": "npx", "env": {"TOKEN": "${AIMUX_TEST_UNSET_TOKEN}"}}, "gh": {"command": "gh-new"}}}`
	if err := os.WriteFile(claude, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mcpproxy.SaveServerFile(servers, map[string]mcpproxy.ServerConfig{
		"slack": {Command: "npx", Env: map[string]string{"TOKEN": "resolved-at-install"}},
		"gh":    {Command: "gh-old"},
	}, []string{"slack"}); err != nil {
		t.Fatal(err)
	}
	for _, save := range []bool{false, true} {
		cfgs, resolved, err := proxyBackends(claude, servers, nil, save)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(resolved, []string{"slack"}) {
			t.Fatalf("save=%v: resolved %v, want slack (its saved definition must not be expanded again)", save, resolved)
		}
		if cfgs["slack"].Env["TOKEN"] != "resolved-at-install" || cfgs["gh"].Command != "gh-new" {
			t.Fatalf("save=%v: backends %+v", save, cfgs)
		}
	}
	if _, resolved, _ := mcpproxy.LoadServerFile(servers); len(resolved) != 1 {
		t.Fatalf("refresh dropped the resolved set: %v", resolved)
	}
}

func TestProxyOnly(t *testing.T) {
	cfgs := map[string]mcpproxy.ServerConfig{"a": {Command: "a"}, "b": {Command: "b"}, "c": {Command: "c"}}
	if got, missing := proxyOnly(cfgs, nil); len(got) != 3 || len(missing) != 0 {
		t.Fatalf("no --only (manual serve) changed the set: %v %v", got, missing)
	}
	got, missing := proxyOnly(cfgs, []string{"a", "c", "z"})
	if len(got) != 2 || got["a"].Command != "a" || got["c"].Command != "c" || !reflect.DeepEqual(missing, []string{"z"}) {
		t.Fatalf("got %v missing %v", got, missing)
	}
	if newMCPProxyServeCmd().Flags().Lookup("only") == nil {
		t.Fatal("serve lacks --only")
	}
}

func TestMCPProxyRestartCmd_IsRegisteredAndNeedsAnInstall(t *testing.T) {
	var found bool
	for _, c := range newMCPProxyCmd().Commands() {
		if c.Name() == "restart" {
			found = true
		}
	}
	if !found {
		t.Fatal("aimux mcp proxy restart is not registered")
	}
	var out bytes.Buffer
	err := runProxyRestart(proxyInstaller(t.TempDir(), 7381, &out), &out)
	if err == nil || !strings.Contains(err.Error(), "aimux mcp proxy install") {
		t.Fatalf("err = %v, want the not-installed message pointing at install", err)
	}
}

// restart waits on the port the proxy was installed on, unless --port says
// otherwise; with no install it keeps the flag's value.
func TestRestartPort_UsesTheInstalledPort(t *testing.T) {
	home := t.TempDir()
	if got := restartPort(home, 7381, false); got != 7381 {
		t.Fatalf("no install: port %d, want the flag's 7381", got)
	}
	dir := filepath.Join(home, ".aimux")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mcp-proxy-install.json"), []byte(`{"version":2,"port":7399,"servers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := restartPort(home, 7381, false); got != 7399 {
		t.Fatalf("installed on 7399: port %d, want 7399", got)
	}
	if got := restartPort(home, 7500, true); got != 7500 {
		t.Fatalf("explicit --port 7500: port %d, want 7500", got)
	}
}

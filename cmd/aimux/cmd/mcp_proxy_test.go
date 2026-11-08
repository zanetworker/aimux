package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/zanetworker/aimux/internal/mcpproxy"
)

type stubBackend struct{ tools []json.RawMessage }

func (s stubBackend) ListToolsRaw(context.Context) ([]json.RawMessage, error) { return s.tools, nil }
func (s stubBackend) CallTool(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultText("ok"), nil
}
func (s stubBackend) Close() error { return nil }

func TestProxyRefresh_WritesCatalogAndReports(t *testing.T) {
	catalogPath := filepath.Join(t.TempDir(), "catalog.json")
	start := func(ctx context.Context, server string) (mcpproxy.Backend, error) {
		if server == "broken" {
			return nil, errors.New("exec: not found")
		}
		return stubBackend{tools: []json.RawMessage{json.RawMessage(`{"name":"a"}`), json.RawMessage(`{"name":"b"}`)}}, nil
	}
	var out bytes.Buffer
	err := proxyRefresh(context.Background(), []string{"slack", "broken"}, start, catalogPath, &out)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := mcpproxy.LoadCatalog(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Servers["slack"].Tools) != 2 {
		t.Fatalf("catalog = %+v, want slack with 2 tools", cat.Servers)
	}
	report := out.String()
	if !strings.Contains(report, "slack") || !strings.Contains(report, "2 tools") ||
		!strings.Contains(report, "broken") || !strings.Contains(report, "not found") {
		t.Fatalf("report missing a server or its outcome:\n%s", report)
	}
}

func TestProxyRefresh_NoServers(t *testing.T) {
	var out bytes.Buffer
	err := proxyRefresh(context.Background(), nil, nil, filepath.Join(t.TempDir(), "c.json"), &out)
	if err == nil || !strings.Contains(err.Error(), "no stdio MCP servers") {
		t.Fatalf("got %v, want an error saying there is nothing to proxy", err)
	}
}

func TestProxyClaudeConfig(t *testing.T) {
	got := proxyClaudeConfig([]string{"slack", "github", "team/weather"}, 7381, "/h/.aimux/mcp-servers.json")
	var parsed struct {
		MCPServers map[string]struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"mcpServers"`
	}
	start := strings.Index(got, "{")
	if start < 0 || json.Unmarshal([]byte(got[start:strings.LastIndex(got, "}")+1]), &parsed) != nil {
		t.Fatalf("output has no valid JSON entry:\n%s", got)
	}
	want := map[string]string{
		"slack":        "http://127.0.0.1:7381/mcp/slack",
		"github":       "http://127.0.0.1:7381/mcp/github",
		"team/weather": "http://127.0.0.1:7381/mcp/team%2Fweather",
	}
	if len(parsed.MCPServers) != len(want) {
		t.Fatalf("entries = %+v, want one per server under its own name", parsed.MCPServers)
	}
	for name, url := range want {
		if e := parsed.MCPServers[name]; e.Type != "http" || e.URL != url {
			t.Fatalf("%s entry = %+v, want http at %s", name, e, url)
		}
	}
	if _, ok := parsed.MCPServers["mux"]; ok {
		t.Fatal("output still has the combined mux entry")
	}
	if !strings.Contains(got, "/h/.aimux/mcp-servers.json") {
		t.Fatalf("output should say where the definitions are kept:\n%s", got)
	}
	if !strings.Contains(got, "github, slack, team/weather") {
		t.Fatalf("output should list the entries it replaces, sorted:\n%s", got)
	}
}

func TestProxyRefresh_AllFailedIsAnError(t *testing.T) {
	start := func(ctx context.Context, server string) (mcpproxy.Backend, error) {
		return nil, errors.New("exec: not found")
	}
	var out bytes.Buffer
	err := proxyRefresh(context.Background(), []string{"a", "b"}, start, filepath.Join(t.TempDir(), "c.json"), &out)
	if err == nil || !strings.Contains(err.Error(), "no server") {
		t.Fatalf("got %v, want an error when every server failed", err)
	}
	if !strings.Contains(out.String(), "not found") {
		t.Fatalf("per-server failures not reported:\n%s", out.String())
	}
}

func TestProxyCatalogProblem(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent.json")
	if msg := proxyCatalogProblem(missing, 0, false); !strings.Contains(msg, "no catalog at") || !strings.Contains(msg, "refresh") {
		t.Fatalf("missing catalog message = %q", msg)
	}
	present := filepath.Join(dir, "c.json")
	if err := (&mcpproxy.Catalog{}).Save(present); err != nil {
		t.Fatal(err)
	}
	if msg := proxyCatalogProblem(present, 0, false); !strings.Contains(msg, "failures") {
		t.Fatalf("empty catalog message = %q, want a pointer to the refresh failures", msg)
	}
	if msg := proxyCatalogProblem(present, 3, false); msg != "" {
		t.Fatalf("usable catalog reported a problem: %q", msg)
	}
	// An installed daemon whose moved list is empty serves nothing for now;
	// failing would make launchd restart it in a loop.
	if msg := proxyCatalogProblem(missing, 0, true); msg != "" {
		t.Fatalf("installed daemon with nothing to serve reported %q", msg)
	}
}

func TestProxyBackends_KeepsServersRemovedFromClaudeConfig(t *testing.T) {
	dir := t.TempDir()
	claudePath, serversPath := filepath.Join(dir, "claude.json"), filepath.Join(dir, "mcp-servers.json")
	if err := os.WriteFile(claudePath, []byte(`{"mcpServers":{"slack":{"command":"slack-mcp"},"pypi":{"command":"pypi-mcp"},"mux":{"type":"http","url":"http://127.0.0.1:7381/mcp"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mcpproxy.SaveServers(serversPath, map[string]mcpproxy.ServerConfig{"github": {Command: "gh-mcp"}}); err != nil {
		t.Fatal(err)
	}
	got, _, err := proxyBackends(claudePath, serversPath, []string{"pypi"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["slack"].Command != "slack-mcp" || got["github"].Command != "gh-mcp" {
		t.Fatalf("backends %v: want slack (Claude config) and github (saved), pypi excluded", got)
	}
	saved, _ := mcpproxy.LoadServers(serversPath)
	if len(saved) != 2 {
		t.Fatalf("saved %v, want the merged set written back", saved)
	}
	// serve reads the saved set even after the Claude entries are gone.
	if err := os.WriteFile(claudePath, []byte(`{"mcpServers":{"mux":{"type":"http","url":"x"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	again, _, err := proxyBackends(claudePath, serversPath, nil, false)
	if err != nil || len(again) != 2 {
		t.Fatalf("after removing the stdio entries: %v %v, want both saved servers", again, err)
	}
}

// Ctrl-C during `aimux mcp proxy refresh` stops the servers it started:
// they run in sessions of their own, so the terminal's interrupt never
// reaches them.
func TestProxyRefreshCmd_InterruptClosesStartedServers(t *testing.T) {
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(dir, "server.pid")
	cfg := fmt.Sprintf(`{"mcpServers": {"hang": {"command": %q, "env": {%q: "1", %q: "1", %q: %q}}}}`,
		exe, fakeMCPEnv, fakeMCPHangEnv, fakeMCPPIDEnv, pidFile)
	claude := filepath.Join(dir, "claude.json")
	if err := os.WriteFile(claude, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "refresh", "--claude-config", claude, "--servers", filepath.Join(dir, "servers.json"), "--catalog", filepath.Join(dir, "catalog.json")) // #nosec G204 -- the test binary
	cmd.Env = append(os.Environ(), proxyCLIEnv+"=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var pid int
	deadline := time.Now().Add(10 * time.Second)
	for pid == 0 {
		if b, err := os.ReadFile(pidFile); err == nil { // #nosec G304 -- test temp dir
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatal("the server never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // refresh is waiting on tools/list now
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("refresh did not exit after the interrupt")
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("server pid %d still running after refresh was interrupted", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Ctrl-C while `install --dry-run` lists tools stops the servers it started
// and exits without printing a plan or writing anything. The real install
// lists through the same Refresh closure (it refuses a test binary before
// that, and must never reach launchctl here).
func TestProxyInstallDryRun_InterruptClosesStartedServers(t *testing.T) {
	home, tmp := t.TempDir(), t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "server.pid")
	cfg := fmt.Sprintf(`{"mcpServers": {"hang": {"command": %q, "env": {%q: "1", %q: "1", %q: %q}}}}`,
		exe, fakeMCPEnv, fakeMCPHangEnv, fakeMCPPIDEnv, pidFile)
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := exec.Command(exe, "install", "--dry-run") // #nosec G204 -- the test binary
	cmd.Env = append(os.Environ(), proxyCLIEnv+"=1", "HOME="+home, "TMPDIR="+tmp)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := waitPIDFile(t, cmd, pidFile)
	time.Sleep(200 * time.Millisecond) // the dry run is waiting on tools/list now
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		if err == nil {
			t.Fatalf("an interrupted dry run exited 0:\n%s", out.String())
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the dry run did not exit after the interrupt")
	}
	waitPIDGone(t, pid)
	if strings.Contains(out.String(), "Would move") || !strings.Contains(out.String(), "interrupted") {
		t.Fatalf("output:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".aimux")); !os.IsNotExist(err) {
		t.Fatal("the interrupted dry run created ~/.aimux")
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Fatalf("the interrupted dry run left %d entries in the temp dir", len(left))
	}
}

func waitPIDFile(t *testing.T, cmd *exec.Cmd, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if b, err := os.ReadFile(pidFile); err == nil { // #nosec G304 -- test temp dir
			if pid, _ := strconv.Atoi(strings.TrimSpace(string(b))); pid != 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatal("the server never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitPIDGone(t *testing.T, pid int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("server pid %d still running after the interrupt", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// proxyLoadSet is what serve starts with and what every /reload re-reads:
// the servers file (plus the Claude config's stdio entries) under --only and
// --exclude, and the full catalog.
func TestProxyLoadSet_ReadsServersAndCatalogUnderOnly(t *testing.T) {
	dir := t.TempDir()
	f := proxyFlags{
		claudeConfig: filepath.Join(dir, "claude.json"),
		servers:      filepath.Join(dir, "mcp-servers.json"),
		catalog:      filepath.Join(dir, "catalog.json"),
		only:         []string{"slack", "github", "absent"},
	}
	if err := os.WriteFile(f.claudeConfig, []byte(`{"mcpServers":{"pypi":{"command":"pypi-mcp"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mcpproxy.SaveServerFile(f.servers, map[string]mcpproxy.ServerConfig{
		"slack": {Command: "slack-mcp"}, "github": {Command: "gh-mcp"},
	}, []string{"github"}); err != nil {
		t.Fatal(err)
	}
	cat := &mcpproxy.Catalog{}
	cat.Set("slack", []json.RawMessage{json.RawMessage(`{"name":"post"}`)}, time.Unix(0, 0))
	cat.Set("pypi", []json.RawMessage{json.RawMessage(`{"name":"search"}`)}, time.Unix(0, 0))
	if err := cat.Save(f.catalog); err != nil {
		t.Fatal(err)
	}
	set, missing, err := proxyLoadSet(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Configs) != 2 || set.Configs["slack"].Command != "slack-mcp" || set.Configs["github"].Command != "gh-mcp" {
		t.Fatalf("configs %v, want slack and github only (--only)", set.Configs)
	}
	if strings.Join(set.Resolved, ",") != "github" {
		t.Fatalf("resolved %v, want [github]", set.Resolved)
	}
	if len(set.Catalog.Servers) != 2 {
		t.Fatalf("catalog %v, want the whole file (the updater saves it back)", set.Catalog.Servers)
	}
	if strings.Join(missing, ",") != "absent" {
		t.Fatalf("missing %v, want [absent]", missing)
	}

	// A broken servers file is an error, so a reload changes nothing.
	if err := os.WriteFile(f.servers, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := proxyLoadSet(f); err == nil {
		t.Fatal("a broken servers file loaded")
	}
}

// Without --only, a servers file with a moved list (install's
// resolvedByInstall) is served as exactly that list, so install adds a
// server by saving it and calling /reload; without one, every configured
// server is served, as a manual serve always did.
func TestProxyLoadSet_ServesTheMovedServersWithoutOnly(t *testing.T) {
	dir := t.TempDir()
	f := proxyFlags{
		claudeConfig: filepath.Join(dir, "claude.json"),
		servers:      filepath.Join(dir, "mcp-servers.json"),
		catalog:      filepath.Join(dir, "catalog.json"),
	}
	if err := os.WriteFile(f.claudeConfig, []byte(`{"mcpServers":{"pypi":{"command":"pypi-mcp"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	defs := map[string]mcpproxy.ServerConfig{"slack": {Command: "slack-mcp"}, "github": {Command: "gh-mcp"}}
	if err := mcpproxy.SaveServerFile(f.servers, defs, []string{"github", "slack"}); err != nil {
		t.Fatal(err)
	}
	set, _, err := proxyLoadSet(f)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sortedKeys(set.Configs), ","); got != "github,slack" {
		t.Fatalf("served %s, want the moved list github,slack", got)
	}

	if err := mcpproxy.SaveServerFile(f.servers, defs, nil); err != nil {
		t.Fatal(err)
	}
	if set, _, err = proxyLoadSet(f); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sortedKeys(set.Configs), ","); got != "github,pypi,slack" {
		t.Fatalf("served %s, want every configured server without a moved list", got)
	}
}

func TestProxyLoadSet_EmptyMovedListServesNothing(t *testing.T) {
	dir := t.TempDir()
	f := proxyFlags{
		claudeConfig: filepath.Join(dir, "claude.json"),
		servers:      filepath.Join(dir, "mcp-servers.json"),
		catalog:      filepath.Join(dir, "catalog.json"),
	}
	if err := os.WriteFile(f.claudeConfig, []byte(`{"mcpServers":{"pypi":{"command":"pypi-mcp"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mcpproxy.SaveServerFile(f.servers, map[string]mcpproxy.ServerConfig{"slack": {Command: "slack-mcp"}}, []string{}); err != nil {
		t.Fatal(err)
	}
	set, _, err := proxyLoadSet(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Configs) != 0 {
		t.Fatalf("served %v, want nothing while the moved list is empty", sortedKeys(set.Configs))
	}
}

// The catalog updater compares a started backend's definition with what a
// reload would load now: the servers file (moved servers win) over the
// Claude config.
func TestProxyCurrentDefinition(t *testing.T) {
	dir := t.TempDir()
	f := proxyFlags{claudeConfig: filepath.Join(dir, "claude.json"), servers: filepath.Join(dir, "mcp-servers.json")}
	if err := os.WriteFile(f.claudeConfig, []byte(`{"mcpServers":{"pypi":{"command":"pypi-mcp"},"slack":{"command":"old-slack"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mcpproxy.SaveServerFile(f.servers, map[string]mcpproxy.ServerConfig{"slack": {Command: "slack-mcp", Args: []string{"--v2"}}}, []string{"slack"}); err != nil {
		t.Fatal(err)
	}
	current := proxyCurrentDefinition(f)
	if c, ok := current("slack"); !ok || c.Command != "slack-mcp" || len(c.Args) != 1 {
		t.Fatalf("slack = %+v, %v; want the moved definition", c, ok)
	}
	if c, ok := current("pypi"); !ok || c.Command != "pypi-mcp" {
		t.Fatalf("pypi = %+v, %v", c, ok)
	}
	if _, ok := current("nosuch"); ok {
		t.Fatal("an unknown server reported a definition")
	}
}

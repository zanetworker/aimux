package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/mcpproxy"
)

var statusNow = time.Unix(1_700_000_000, 0)

func fixtureReport() mcpproxy.StatusReport {
	return mcpproxy.StatusReport{
		Running: true, PID: 99, StartedAt: statusNow.Add(-3 * time.Hour), Port: 7381,
		Servers: []mcpproxy.ServerReport{
			{ServerStatus: mcpproxy.ServerStatus{Server: "slack", State: mcpproxy.StateRunning, Instances: 1,
				LastCall: statusNow.Add(-2 * time.Minute), Calls: 4}, Tools: 12},
			{ServerStatus: mcpproxy.ServerStatus{Server: "github", State: mcpproxy.StateFailed,
				LastError: "exec: gh-mcp: not found \x1b[31mred", Reason: mcpproxy.ReasonStartFailed,
				StderrTail: []string{"line one", "line two"}}},
		},
	}
}

// fakeDaemon serves a fixed report and records the requests it got.
func fakeDaemon(t *testing.T, rep mcpproxy.StatusReport) (addr string, got *[]string) {
	t.Helper()
	var reqs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		line := r.Method + " " + r.URL.RequestURI()
		if v := r.Header.Get("X-Aimux-Probe"); v != "" {
			line += " X-Aimux-Probe=" + v
		}
		reqs = append(reqs, line)
		_ = json.NewEncoder(w).Encode(rep)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), &reqs
}

func TestProxyStatus_Table(t *testing.T) {
	addr, reqs := fakeDaemon(t, fixtureReport())
	var out bytes.Buffer
	if err := proxyStatus(context.Background(), proxyStatusOpts{addr: addr}, &out, statusNow); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"SERVER", "STATE", "TOOLS", "LAST CALL", "LAST ERROR",
		"slack", "running", "12", "2m ago", "github", "failed", "exec: gh-mcp: not found", "never",
		"pid 99", "line one", "line two"} {
		if !strings.Contains(got, want) {
			t.Fatalf("table lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b") {
		t.Fatalf("a server's escape sequence reached the terminal:\n%q", got)
	}
	if len(*reqs) != 1 || (*reqs)[0] != "GET /status" {
		t.Fatalf("requests %v, want one GET /status", *reqs)
	}
}

func TestProxyStatus_JSON(t *testing.T) {
	addr, _ := fakeDaemon(t, fixtureReport())
	var out bytes.Buffer
	if err := proxyStatus(context.Background(), proxyStatusOpts{addr: addr, json: true}, &out, statusNow); err != nil {
		t.Fatal(err)
	}
	var rep mcpproxy.StatusReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("--json output is not JSON: %v\n%s", err, out.String())
	}
	if !rep.Running || len(rep.Servers) != 2 || rep.Servers[0].Tools != 12 {
		t.Fatalf("--json report %+v", rep)
	}
}

func TestProxyStatus_ProbeCallsTheProbeEndpoint(t *testing.T) {
	addr, reqs := fakeDaemon(t, fixtureReport())
	var out bytes.Buffer
	o := proxyStatusOpts{addr: addr, probe: true, timeout: 45 * time.Second}
	if err := proxyStatus(context.Background(), o, &out, statusNow); err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 || (*reqs)[0] != "POST /status/probe?timeout=45s X-Aimux-Probe=1" {
		t.Fatalf("requests %v, want one POST /status/probe?timeout=45s carrying X-Aimux-Probe: 1", *reqs)
	}
	if !strings.Contains(out.String(), "slack") {
		t.Fatalf("probe printed no table:\n%s", out.String())
	}
}

func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func TestProxyStatus_NotRunning(t *testing.T) {
	addr := closedAddr(t)
	var out bytes.Buffer
	if err := proxyStatus(context.Background(), proxyStatusOpts{addr: addr}, &out, statusNow); err != nil {
		t.Fatalf("not running should not be an error: %v", err)
	}
	if want := "MCP proxy is not running on " + addr + "\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
	out.Reset()
	if err := proxyStatus(context.Background(), proxyStatusOpts{addr: addr, json: true}, &out, statusNow); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != `{"running":false}` {
		t.Fatalf("--json when not running: %q", out.String())
	}
}

func TestProxyStatus_DaemonErrorIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()
	var out bytes.Buffer
	err := proxyStatus(context.Background(), proxyStatusOpts{addr: strings.TrimPrefix(srv.URL, "http://")}, &out, statusNow)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("got %v, want an error naming the status", err)
	}
}

// End to end against a real daemon whose server prints its env value: the
// value never reaches any status output.
func TestProxyStatus_NeverPrintsEnvValues(t *testing.T) {
	const secret = "abc123"
	cfgs := map[string]mcpproxy.ServerConfig{"leaky": {
		Command: "sh", Args: []string{"-c", `echo "token is $SECRET_TOKEN" >&2; exit 1`},
		Env: map[string]string{"SECRET_TOKEN": secret},
	}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pool := mcpproxy.NewPool(mcpproxy.StdioStarter(cfgs), mcpproxy.PoolOptions{})
	mon := &mcpproxy.Monitor{Pool: pool, Servers: []string{"leaky"}, Port: ln.Addr().(*net.TCPAddr).Port}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- mcpproxy.Serve(ctx, ln, mcpproxy.NewServers(&mcpproxy.Catalog{}, pool), pool, mcpproxy.DaemonOptions{Status: mon})
	}()
	defer func() { cancel(); <-done }()

	addr := ln.Addr().String()
	var all bytes.Buffer
	for _, o := range []proxyStatusOpts{
		{addr: addr, probe: true, timeout: 15 * time.Second},
		{addr: addr},
		{addr: addr, json: true},
	} {
		if err := proxyStatus(context.Background(), o, &all, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(all.String(), "token is [redacted]") {
		t.Fatalf("output lacks the scrubbed stderr line, so the test proves nothing:\n%s", all.String())
	}
	if strings.Contains(all.String(), secret) {
		t.Fatalf("status output contains the env value:\n%s", all.String())
	}
}

func styledReport() mcpproxy.StatusReport {
	rep := fixtureReport()
	rep.Servers = append(rep.Servers,
		mcpproxy.ServerReport{ServerStatus: mcpproxy.ServerStatus{Server: "nanobanana-image-creation", State: mcpproxy.StateIdleStopped,
			LastCall: statusNow.Add(-30 * time.Minute)}, Tools: 4},
		mcpproxy.ServerReport{ServerStatus: mcpproxy.ServerStatus{Server: "brief", State: mcpproxy.StateNotStarted}, Tools: 13})
	return rep
}

var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

func TestPrintProxyStatus_ColorStatesAndSummary(t *testing.T) {
	var out bytes.Buffer
	printProxyStatus(&out, "127.0.0.1:7381", styledReport(), statusNow, true)
	got := out.String()
	for _, want := range []string{
		"\x1b[32m● running", "\x1b[31m● failed", "\x1b[2m○ idle", "\x1b[2m○ not started",
		"4 servers", "1 running", "1 idle", "1 not started", "1 failed",
		"pid 99", "line one", // the failed server's stderr tail
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("styled status lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b[31mred") || !strings.Contains(got, "?[31mred") {
		t.Fatalf("a server's own escape sequence reached the terminal:\n%q", got)
	}
}

func TestPrintProxyStatus_ColumnsStayAlignedWithColor(t *testing.T) {
	var out bytes.Buffer
	printProxyStatus(&out, "127.0.0.1:7381", styledReport(), statusNow, true)
	row := regexp.MustCompile(`^(\s*\S+\s+)([●○◐] [a-z ]+?)\s{2,}(\d+)\s`)
	var stateCols, toolCols []int
	for _, line := range strings.Split(ansiRE.ReplaceAllString(out.String(), ""), "\n") {
		m := row.FindStringSubmatchIndex(line)
		if m == nil {
			continue
		}
		stateCols = append(stateCols, len([]rune(line[:m[4]])))
		toolCols = append(toolCols, len([]rune(line[:m[6]])))
	}
	if len(stateCols) != 4 {
		t.Fatalf("found %d table rows, want 4:\n%s", len(stateCols), out.String())
	}
	for i := range stateCols {
		if stateCols[i] != stateCols[0] || toolCols[i] != toolCols[0] {
			t.Fatalf("columns misaligned with colour on: state %v, tools %v\n%s", stateCols, toolCols, out.String())
		}
	}
}

func TestPrintProxyStatus_PlainHasNoEscapes(t *testing.T) {
	var out bytes.Buffer
	printProxyStatus(&out, "127.0.0.1:7381", styledReport(), statusNow, false)
	if got := out.String(); strings.Contains(got, "\x1b") || !strings.Contains(got, "● running") || !strings.Contains(got, "4 servers") {
		t.Fatalf("plain status:\n%q", got)
	}
}

func TestPrintInstallState_CountsRemovedDriftAndUpgrade(t *testing.T) {
	st := mcpproxy.InstallState{State: mcpproxy.InstallDrifted, Moved: []string{"aimux", "github", "minutes", "slack"},
		Drifted: []string{"slack"}, Removed: []string{"minutes"}, UpgradePending: true, DaemonVersion: "v1"}
	var out bytes.Buffer
	printInstallState(&out, st, "/home/x/.claude.json", "restart with `launchctl kickstart -k gui/501/com.aimux.mcp-proxy`", false)
	got := out.String()
	for _, want := range []string{"4 servers behind the proxy", "aimux mcp proxy edit", "minutes", "removed from /home/x/.claude.json",
		"slack", "drifted", "launchctl kickstart"} {
		if !strings.Contains(got, want) {
			t.Fatalf("install section lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "aimux, github") {
		t.Fatalf("install section lists every server again (the table does):\n%s", got)
	}
	if strings.Contains(got, "minutes") && strings.Contains(got, "drifted: minutes") {
		t.Fatalf("a removed entry is reported as drift:\n%s", got)
	}
}

func tableNames(t *testing.T, out string) []string {
	t.Helper()
	row := regexp.MustCompile(`^\s+(\S+)\s+[●○◐] `)
	var names []string
	for _, line := range strings.Split(ansiRE.ReplaceAllString(out, ""), "\n") {
		if m := row.FindStringSubmatch(line); m != nil {
			names = append(names, m[1])
		}
	}
	return names
}

// What needs attention comes first: failed, then active, then at rest.
func TestPrintProxyStatus_SortsByStateThenName(t *testing.T) {
	rep := styledReport() // slack running, github failed, nanobanana idle, brief not started
	rep.Servers = append(rep.Servers, mcpproxy.ServerReport{ServerStatus: mcpproxy.ServerStatus{Server: "aimux", State: mcpproxy.StateRunning}})
	var out bytes.Buffer
	printProxyStatus(&out, "127.0.0.1:7381", rep, statusNow, false)
	want := []string{"github", "aimux", "slack", "nanobanana-image-creation", "brief"}
	if got := tableNames(t, out.String()); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("rows %v, want %v", got, want)
	}
}

// A server removed from ~/.claude.json shows as removed, last, and counts as
// removed rather than in its live state.
func TestPrintProxyStatus_RemovedServerIsMarkedAndLast(t *testing.T) {
	var out bytes.Buffer
	printProxyStatus(&out, "127.0.0.1:7381", styledReport(), statusNow, false, "nanobanana-image-creation")
	got := out.String()
	names := tableNames(t, got)
	if names[len(names)-1] != "nanobanana-image-creation" {
		t.Fatalf("removed server not last: %v", names)
	}
	if !strings.Contains(got, "○ removed") || !strings.Contains(got, "1 removed") || strings.Contains(got, "1 idle") {
		t.Fatalf("removed server not marked or counted apart:\n%s", got)
	}
}

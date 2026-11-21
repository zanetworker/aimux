package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
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
	if err := proxyStatus(context.Background(), proxyStatusOpts{addr: addr}, &out, fixedClock(statusNow)); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"slack", "running", "12 tools", "called 2m ago", "github", "failed", "exec: gh-mcp: not found", "never called",
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
	if err := proxyStatus(context.Background(), proxyStatusOpts{addr: addr, json: true}, &out, fixedClock(statusNow)); err != nil {
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
	if err := proxyStatus(context.Background(), o, &out, fixedClock(statusNow)); err != nil {
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
	if err := proxyStatus(context.Background(), proxyStatusOpts{addr: addr}, &out, fixedClock(statusNow)); err != nil {
		t.Fatalf("not running should not be an error: %v", err)
	}
	if want := "MCP proxy ○ not running on " + addr + "\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
	out.Reset()
	if err := proxyStatus(context.Background(), proxyStatusOpts{addr: addr, json: true}, &out, fixedClock(statusNow)); err != nil {
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
	o := proxyStatusOpts{addr: strings.TrimPrefix(srv.URL, "http://"),
		install: &mcpproxy.InstallState{State: mcpproxy.InstallDrifted, Moved: []string{"slack"}, Drifted: []string{"slack"}, Hooks: true}}
	asked := false
	o.gateway = func() string { asked = true; return "tool gateway" }
	err := proxyStatus(context.Background(), o, &out, fixedClock(statusNow))
	if asked {
		t.Fatal("a proxy that gave no status still waited on the tool gateway")
	}
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("got %v, want an error naming the status", err)
	}
	// What the command already knows still helps with the broken daemon.
	if got := out.String(); !strings.Contains(got, "MCP proxy ? no status") || !strings.Contains(got, "! drifted: slack") {
		t.Fatalf("install diagnostics lost on a daemon error:\n%s", got)
	}
}

// The gateway line is read once the proxy answered, so a long probe does
// not leave it stale.
func TestProxyStatus_GatewayReadAfterTheAnswer(t *testing.T) {
	addr, got := fakeDaemon(t, fixtureReport())
	o := proxyStatusOpts{addr: addr, probe: true, timeout: time.Second, gateway: func() string {
		if len(*got) == 0 {
			return "tool gateway read before the proxy answered"
		}
		return "tool gateway read after"
	}}
	var out bytes.Buffer
	if err := proxyStatus(context.Background(), o, &out, fixedClock(statusNow)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "tool gateway read after") {
		t.Fatalf("output:\n%s", out.String())
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
	mon := &mcpproxy.Monitor{Pool: pool, Servers: []string{"leaky"}, Port: ln.Addr().(*net.TCPAddr).Port, PID: os.Getpid(), StartedAt: time.Now()}
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
		if err := proxyStatus(context.Background(), o, &all, time.Now); err != nil {
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

// renderRep is the page for rep alone; removed names servers whose entry
// left the Claude config.
func renderRep(rep mcpproxy.StatusReport, color, all bool, removed ...string) string {
	p := statusPage{addr: "127.0.0.1:7381", rep: &rep, now: statusNow, color: color, all: all}
	if len(removed) > 0 {
		p.install = &mcpproxy.InstallState{State: mcpproxy.InstallInPlace, Hooks: true, Removed: removed}
	}
	return render(p)
}

func TestPrintProxyStatus_ColorStatesAndSummary(t *testing.T) {
	got := renderRep(styledReport(), true, true)
	for _, want := range []string{
		"\x1b[32m● running", "\x1b[31m● failed", "\x1b[2m○ idle", "\x1b[2m○ ready",
		"4 servers: ", "1 running", "1 idle", "1 ready", "1 failed",
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
	got := renderRep(styledReport(), true, true)
	row := regexp.MustCompile(`^(\s*\S+\s+)([●○◐] [a-z ]+?)\s{2,}(\d+)\s`)
	var stateCols, toolCols []int
	for _, line := range strings.Split(ansiRE.ReplaceAllString(got, ""), "\n") {
		m := row.FindStringSubmatchIndex(line)
		if m == nil {
			continue
		}
		stateCols = append(stateCols, len([]rune(line[:m[4]])))
		toolCols = append(toolCols, len([]rune(line[:m[6]])))
	}
	if len(stateCols) != 4 {
		t.Fatalf("found %d table rows, want 4:\n%s", len(stateCols), got)
	}
	for i := range stateCols {
		if stateCols[i] != stateCols[0] || toolCols[i] != toolCols[0] {
			t.Fatalf("columns misaligned with colour on: state %v, tools %v\n%s", stateCols, toolCols, got)
		}
	}
}

func TestPrintProxyStatus_PlainHasNoEscapes(t *testing.T) {
	if got := renderRep(styledReport(), false, false); strings.Contains(got, "\x1b") || !strings.Contains(got, "● running") || !strings.Contains(got, "4 servers") {
		t.Fatalf("plain status:\n%q", got)
	}
}

func TestPrintInstallState_CountsRemovedDriftAndUpgrade(t *testing.T) {
	st := mcpproxy.InstallState{State: mcpproxy.InstallDrifted, Moved: []string{"aimux", "github", "minutes", "slack"},
		Drifted: []string{"slack"}, Removed: []string{"minutes"}, UpgradePending: true, DaemonVersion: "v1"}
	got := render(statusPage{addr: "a", install: &st, claudeConfig: "/home/x/.claude.json", now: statusNow,
		upgradeText: "restart with `launchctl kickstart -k gui/501/com.aimux.mcp-proxy`"})
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
	want := []string{"github", "aimux", "slack", "nanobanana-image-creation", "brief"}
	if got := tableNames(t, renderRep(rep, false, true)); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("rows %v, want %v", got, want)
	}
}

// With --all, a server removed from ~/.claude.json shows as removed, last,
// and counts as removed rather than in its live state.
func TestPrintProxyStatus_RemovedServerIsMarkedAndLast(t *testing.T) {
	got := renderRep(styledReport(), false, true, "nanobanana-image-creation")
	names := tableNames(t, got)
	if names[len(names)-1] != "nanobanana-image-creation" {
		t.Fatalf("removed server not last: %v", names)
	}
	if !strings.Contains(got, "○ removed") || !strings.Contains(got, "1 removed") || strings.Contains(got, "1 idle") {
		t.Fatalf("removed server not marked or counted apart:\n%s", got)
	}
}

func TestPrintInstallState_Hooks(t *testing.T) {
	for _, tc := range []struct {
		on   bool
		want string
	}{
		{true, "hooks on: a server you add in a session moves behind the proxy"},
		{false, "hooks off: servers you add later stay direct; `aimux mcp proxy install` adds the hooks"},
	} {
		got := render(statusPage{addr: "a", install: &mcpproxy.InstallState{State: mcpproxy.InstallInPlace, Moved: []string{"a"}, Hooks: tc.on}, now: statusNow})
		if !strings.Contains(got, tc.want) {
			t.Fatalf("Hooks=%v: install section lacks %q:\n%s", tc.on, tc.want, got)
		}
	}
}

// Something else on the port that answers 200 without a running proxy's
// report is no status, never a green running page.
func TestProxyStatus_AnswerWithoutARunningProxyIsAnError(t *testing.T) {
	for _, body := range []string{"{}", "null", `{"running":true}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		for _, js := range []bool{false, true} {
			var out bytes.Buffer
			err := proxyStatus(context.Background(), proxyStatusOpts{addr: strings.TrimPrefix(srv.URL, "http://"), json: js}, &out, fixedClock(statusNow))
			if err == nil || strings.Contains(out.String(), "● running") {
				t.Fatalf("body %s json=%v: err %v, output:\n%s", body, js, err, out.String())
			}
		}
		srv.Close()
	}
}

// A never-started (lazy) server reads as ready on the page, never as "not
// started", while --json keeps the state name not_started for scripts.
func TestProxyStatus_NeverStartedReadsReadyJSONKeepsName(t *testing.T) {
	rep := fixtureReport()
	rep.Servers = append(rep.Servers, mcpproxy.ServerReport{ServerStatus: mcpproxy.ServerStatus{Server: "context7", State: mcpproxy.StateNotStarted}, Tools: 2})
	addr, _ := fakeDaemon(t, rep)
	for _, all := range []bool{false, true} {
		var out bytes.Buffer
		if err := proxyStatus(context.Background(), proxyStatusOpts{addr: addr, all: all}, &out, fixedClock(statusNow)); err != nil {
			t.Fatal(err)
		}
		got := out.String()
		if strings.Contains(got, "not started") || !strings.Contains(got, "1 ready") {
			t.Fatalf("all=%v: page should say ready, not \"not started\":\n%s", all, got)
		}
	}
	var out bytes.Buffer
	if err := proxyStatus(context.Background(), proxyStatusOpts{addr: addr, json: true}, &out, fixedClock(statusNow)); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Servers []struct{ Server, State string }
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if n := len(got.Servers); n != 3 || got.Servers[2].Server != "context7" || got.Servers[2].State != "not_started" {
		t.Fatalf("--json lost the not_started state name: %+v", got.Servers)
	}
}

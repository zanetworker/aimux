package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
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

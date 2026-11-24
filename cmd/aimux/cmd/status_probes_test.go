package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/modelproxy"
	"github.com/zanetworker/aimux/internal/otel"
	"github.com/zanetworker/aimux/internal/toolgateway"
)

var probeTestStartedAt = time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)

const probeTestBuild = "v0.15.0-184-gabc123"

func TestProbe_ModelProxyOK(t *testing.T) {
	lastTurn := probeTestStartedAt.Add(time.Minute)
	live := modelproxy.Liveness{
		Service: "aimux-model-proxy", Status: "ok", Version: probeTestBuild, StartedAt: probeTestStartedAt, LastOKAt: &lastTurn,
	}
	addr := probeHTTPServer(t, probeJSON(t, "/healthz", "", http.StatusOK, live))
	env := statusEnv{modelAddr: addr, plistDir: t.TempDir()}
	assertProbeRow(t, env, controller.ServiceStatus{
		Name: "model proxy", State: controller.ServiceOK, Build: probeTestBuild,
		Address: addr, StartedAt: &probeTestStartedAt, LastActivity: &lastTurn, ActivityNote: "turn",
		RestartFix: "scripts/model-proxy-promote promote",
	})
}

func TestProbe_ModelProxyDegraded(t *testing.T) {
	live := modelproxy.Liveness{Service: "aimux-model-proxy", Status: "degraded", Version: probeTestBuild, StartedAt: probeTestStartedAt}
	addr := probeHTTPServer(t, probeJSON(t, "/healthz", "", http.StatusOK, live))
	env := statusEnv{modelAddr: addr, plistDir: t.TempDir()}
	assertProbeRow(t, env, controller.ServiceStatus{
		Name: "model proxy", State: controller.ServiceDegraded, Reason: "degraded", Build: probeTestBuild,
		Address: addr, StartedAt: &probeTestStartedAt, ActivityNote: "turn",
		RestartFix: "scripts/model-proxy-promote promote",
		// The keyless answer has no detail; the keyed command does.
		Attention: []controller.Attention{{Problem: "degraded", Fix: "aimux model proxy status"}},
	})
}

func TestProbe_ModelProxySendsNoKey(t *testing.T) {
	headers := make(chan http.Header, 1)
	live := modelproxy.Liveness{Service: "aimux-model-proxy", Status: "ok", StartedAt: probeTestStartedAt}
	addr := probeHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Clone()
		_ = json.NewEncoder(w).Encode(live)
	}))
	probes := defaultStatusProbes(statusEnv{modelAddr: addr, plistDir: t.TempDir()})
	if row := probes[0].Run(t.Context()); row.Name != "model proxy" || row.State != controller.ServiceOK {
		t.Fatalf("model proxy row = %+v, want ok", row)
	}
	got := <-headers
	for _, name := range []string{"X-Api-Key", "Authorization"} {
		if _, ok := got[name]; ok {
			t.Errorf("the probe sent %s to the model proxy port", name)
		}
	}
}

func TestProbe_ModelProxyWithoutHealthz(t *testing.T) {
	// An older proxy build has no keyless path: it answers 401 (key check first) or 404 there.
	fix := "scripts/model-proxy-promote promote"
	plists := t.TempDir()
	if err := os.WriteFile(filepath.Join(plists, "com.aimux.model-proxy.plist"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, code := range []int{http.StatusUnauthorized, http.StatusNotFound} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			addr := probeHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "nope", code)
			}))
			assertProbeRow(t, statusEnv{modelAddr: addr, plistDir: plists}, controller.ServiceStatus{
				Name: "model proxy", Address: addr, RestartFix: fix, State: controller.ServiceDegraded, Reason: "no health answer",
			})
			assertProbeRow(t, statusEnv{modelAddr: addr, plistDir: t.TempDir()}, controller.ServiceStatus{
				Name: "model proxy", Address: addr, RestartFix: fix, State: controller.ServiceDown, Reason: "wrong process",
			})
		})
	}
}

func TestProbe_ModelProxyWrongService(t *testing.T) {
	live := modelproxy.Liveness{Service: "something-else", Status: "ok", Version: probeTestBuild, StartedAt: probeTestStartedAt}
	addr := probeHTTPServer(t, probeJSON(t, "/healthz", "", http.StatusOK, live))
	assertProbeRow(t, statusEnv{modelAddr: addr, plistDir: t.TempDir()}, controller.ServiceStatus{
		Name: "model proxy", Address: addr, RestartFix: "scripts/model-proxy-promote promote",
		State: controller.ServiceDown, Reason: "wrong process",
	})
}

func TestProbe_NotInstalled(t *testing.T) {
	addr := probeClosedAddress(t)
	dir := t.TempDir()
	gateway := filepath.Join(dir, "missing.sock")
	env := statusEnv{modelAddr: addr, mcpAddr: addr, gatewayAddr: gateway, collectorAddr: addr, plistDir: dir}
	rows := []controller.ServiceStatus{
		{Name: "model proxy", Address: addr, RestartFix: "scripts/model-proxy-promote promote"},
		{Name: "mcp proxy", Address: addr, RestartFix: "aimux mcp proxy restart"},
		{Name: "tool gateway", Address: gateway, RestartFix: "aimux mcp proxy gateway install"},
		{Name: "collector", Address: addr, RestartFix: "launchctl kickstart -k gui/$(id -u)/com.azaalouk.aimux-collect"},
	}
	probes := defaultStatusProbes(env)
	if len(probes) != len(rows) {
		t.Fatalf("got %d probes, want %d", len(probes), len(rows))
	}
	for i, want := range rows {
		if probes[i].Name != want.Name {
			t.Fatalf("probe %d is %q, want %q", i, probes[i].Name, want.Name)
		}
		// The controller fills a timed-out row from these.
		if probes[i].Address != want.Address || probes[i].RestartFix != want.RestartFix {
			t.Errorf("probe %q carries address %q and fix %q, want %q and %q",
				want.Name, probes[i].Address, probes[i].RestartFix, want.Address, want.RestartFix)
		}
		want.State = controller.ServiceNotInstalled
		assertProbeRow(t, env, want)
	}
}

func TestProbe_DownNotRunning(t *testing.T) {
	addr := probeClosedAddress(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "com.aimux.model-proxy.plist"), []byte("<plist/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := statusEnv{modelAddr: addr, plistDir: dir}
	assertProbeRow(t, env, controller.ServiceStatus{
		Name: "model proxy", State: controller.ServiceDown, Reason: "not running", Address: addr,
		RestartFix: "scripts/model-proxy-promote promote",
	})
}

func TestProbe_WrongProcess(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"plain text", "hello"},
		{"empty object", "{}"},
		{"generic health", `{"status":"ok"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr := probeHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, tc.body)
			}))
			env := statusEnv{modelAddr: addr, mcpAddr: addr, collectorAddr: addr, plistDir: t.TempDir()}
			for _, row := range []controller.ServiceStatus{
				{Name: "model proxy", RestartFix: "scripts/model-proxy-promote promote"},
				{Name: "mcp proxy", RestartFix: "aimux mcp proxy restart"},
				{Name: "collector", RestartFix: "launchctl kickstart -k gui/$(id -u)/com.azaalouk.aimux-collect"},
			} {
				row.State, row.Reason, row.Address = controller.ServiceDown, "wrong process", addr
				assertProbeRow(t, env, row)
			}
		})
	}
}

func TestProbe_KeyRejected(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			addr := probeHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "rejected "+r.Header.Get("x-api-key"), code)
			}))
			env := statusEnv{mcpAddr: addr, collectorAddr: addr, plistDir: t.TempDir()}
			for _, row := range []controller.ServiceStatus{
				{Name: "mcp proxy", RestartFix: "aimux mcp proxy restart"},
				{Name: "collector", RestartFix: "launchctl kickstart -k gui/$(id -u)/com.azaalouk.aimux-collect"},
			} {
				row.State, row.Reason, row.Address = controller.ServiceDown, "key rejected", addr
				assertProbeRow(t, env, row)
			}
		})
	}
}

func TestProbe_GatewayBackendsDown(t *testing.T) {
	st := toolgateway.ControlStatus{
		Version: "sha256:1234abcd", StartedAt: probeTestStartedAt, Leases: 3,
		Backends: []string{"slack", "jira", "docs", "mail", "browser"}, Down: []string{"slack"},
	}
	// Use a short path because macOS limits Unix socket addresses to 103 bytes.
	dir, err := os.MkdirTemp("/tmp", "aimux-status-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "control.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &httptest.Server{
		Listener: listener,
		Config:   &http.Server{Handler: probeJSON(t, "/v1/status", "", http.StatusOK, st), ReadHeaderTimeout: 5 * time.Second},
	}
	server.Start()
	t.Cleanup(server.Close)
	env := statusEnv{gatewayAddr: "127.0.0.1:7390", plistDir: t.TempDir(),
		gatewayStatus: (toolgateway.ControlClient{Path: socket}).Status}
	assertProbeRow(t, env, controller.ServiceStatus{
		Name: "tool gateway", State: controller.ServiceDegraded, // no Build: the gateway reports a snapshot hash, not a build

		Address: env.gatewayAddr, StartedAt: &probeTestStartedAt, ActivityNote: "3 leases",
		RestartFix: "aimux mcp proxy gateway install",
		Attention:  []controller.Attention{{Problem: "1 of 5 backends down (slack)", Fix: "aimux mcp proxy status"}},
	})
}

func TestProbe_CollectorWrongService(t *testing.T) {
	lastSpan := probeTestStartedAt.Add(time.Minute)
	for _, service := range []string{"something-else", "aimux-collector", "aimux-receiver"} {
		t.Run(service, func(t *testing.T) {
			health := otel.ReceiverHealth{Service: service, Version: probeTestBuild, StartedAt: probeTestStartedAt, LastSpanAt: &lastSpan}
			addr := probeHTTPServer(t, probeJSON(t, "/health", "", http.StatusOK, health))
			env := statusEnv{collectorAddr: addr, plistDir: t.TempDir()}
			want := controller.ServiceStatus{
				Name: "collector", Address: addr, RestartFix: "launchctl kickstart -k gui/$(id -u)/com.azaalouk.aimux-collect",
			}
			switch service {
			case "aimux-collector":
				want.State, want.Build, want.StartedAt = controller.ServiceOK, probeTestBuild, &probeTestStartedAt
				want.LastActivity, want.ActivityNote = &lastSpan, "span"
			case "aimux-receiver":
				// The TUI's receiver holds the port: spans arrive, but the collector is not running.
				want.State, want.Reason = controller.ServiceDown, "not the collector"
			default:
				want.State, want.Reason = controller.ServiceDown, "wrong process"
			}
			assertProbeRow(t, env, want)
		})
	}
}

func TestProbe_CollectorOlderBuildHasNoHealthAnswer(t *testing.T) {
	// A collector from before /health existed answers 404 there. With its launchd job installed that is an
	// older build to restart, not a stranger on the port.
	fix := "launchctl kickstart -k gui/$(id -u)/com.azaalouk.aimux-collect"
	plists := t.TempDir()
	if err := os.WriteFile(filepath.Join(plists, "com.azaalouk.aimux-collect.plist"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The collector running on the owner's machine on 2026-11-24 answers 200 with an empty body on any path.
	empty := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	var addr string
	for _, handler := range []http.Handler{http.NotFoundHandler(), empty} {
		addr = probeHTTPServer(t, handler)
		assertProbeRow(t, statusEnv{collectorAddr: addr, plistDir: plists}, controller.ServiceStatus{
			Name: "collector", Address: addr, RestartFix: fix,
			State: controller.ServiceDegraded, Reason: "no health answer",
		})
	}
	// Without the launchd job, the same answer on the port is somebody else.
	assertProbeRow(t, statusEnv{collectorAddr: addr, plistDir: t.TempDir()}, controller.ServiceStatus{
		Name: "collector", Address: addr, RestartFix: fix, State: controller.ServiceDown, Reason: "wrong process",
	})
}

func TestProbe_McpRestartPending(t *testing.T) {
	lastCall := probeTestStartedAt.Add(2 * time.Minute)
	rep := mcpproxy.StatusReport{
		Running: true, PID: 123, StartedAt: probeTestStartedAt, Version: probeTestBuild,
		Servers: []mcpproxy.ServerReport{
			{ServerStatus: mcpproxy.ServerStatus{Server: "newest", LastCall: lastCall}},
			{ServerStatus: mcpproxy.ServerStatus{Server: "older", LastCall: probeTestStartedAt.Add(time.Minute), LastOK: lastCall.Add(time.Second)}},
			{ServerStatus: mcpproxy.ServerStatus{Server: "idle"}},
		},
	}
	addr := probeHTTPServer(t, probeJSON(t, "/status", "", http.StatusOK, rep))
	env := statusEnv{mcpAddr: addr, plistDir: t.TempDir()}
	want := controller.ServiceStatus{
		Name: "mcp proxy", State: controller.ServiceOK, Build: probeTestBuild,
		Address: addr, StartedAt: &probeTestStartedAt, LastActivity: &lastCall, ActivityNote: "call",
		RestartFix: "aimux mcp proxy restart",
	}
	env.cliVersion = "v0.15.0-185-gdef456"
	want.BuildNote = "restart pending"
	assertProbeRow(t, env, want)
	env.cliVersion, want.BuildNote = probeTestBuild, ""
	assertProbeRow(t, env, want)
}

func TestProbe_McpFailedServersAreDegraded(t *testing.T) {
	// A server whose last start failed cannot answer; an idle or never-started one is not a failure.
	past := true
	rep := mcpproxy.StatusReport{
		Running: true, PID: 123, StartedAt: probeTestStartedAt, Version: probeTestBuild,
		Servers: []mcpproxy.ServerReport{
			{ServerStatus: mcpproxy.ServerStatus{Server: "slack", State: mcpproxy.StateFailed}},
			{ServerStatus: mcpproxy.ServerStatus{Server: "jira", State: mcpproxy.StateFailed}},
			{ServerStatus: mcpproxy.ServerStatus{Server: "wiki", State: mcpproxy.StateIdleStopped, ErrorPast: &past}},
			{ServerStatus: mcpproxy.ServerStatus{Server: "never-started"}},
			{ServerStatus: mcpproxy.ServerStatus{Server: "github", State: mcpproxy.StateRunning}},
		},
	}
	addr := probeHTTPServer(t, probeJSON(t, "/status", "", http.StatusOK, rep))
	assertProbeRow(t, statusEnv{mcpAddr: addr, plistDir: t.TempDir(), cliVersion: probeTestBuild}, controller.ServiceStatus{
		Name: "mcp proxy", State: controller.ServiceDegraded, Build: probeTestBuild,
		Address: addr, StartedAt: &probeTestStartedAt, ActivityNote: "call",
		RestartFix: "aimux mcp proxy restart",
		Attention:  []controller.Attention{{Problem: "2 of 5 servers failed (jira, slack)", Fix: "aimux mcp proxy status"}},
	})
}

func assertProbeRow(t *testing.T, env statusEnv, want controller.ServiceStatus) {
	t.Helper()
	for _, probe := range defaultStatusProbes(env) {
		if probe.Name == want.Name {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if got := probe.Run(ctx); !reflect.DeepEqual(got, want) {
				t.Errorf("probe row = %+v, want %+v", got, want)
			}
			return
		}
	}
	t.Fatalf("missing probe %q", want.Name)
}

func probeHTTPServer(t *testing.T, handler http.Handler) string {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "http://")
}

func probeJSON(t *testing.T, path, key string, code int, body any) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.RequestURI() != path || r.Header.Get("x-api-key") != key {
			t.Errorf("unexpected request: %s %s or wrong key", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Error(err)
		}
	})
}

func probeClosedAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/modelproxy"
)

var healthNow = time.Unix(1_700_000_000, 0).UTC()

func fixtureHealth() modelproxy.Health {
	ok := healthNow.Add(-90 * time.Second)
	return modelproxy.Health{
		Status: "ok", StartedAt: healthNow.Add(-3 * time.Hour), UptimeSeconds: 10800,
		Codex:    modelproxy.CodexHealth{Command: "codex-lean", AppServerRunning: true},
		Requests: modelproxy.StageHealth{Total: 42, LastOKAt: &ok},
		Probe:    modelproxy.StageHealth{Total: 90, LastOKAt: &ok},
	}
}

// fakeProxy serves one health report and records the keys it was asked with.
func fakeProxy(t *testing.T, status int, body string) (addr string, asked func() []string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Method+" "+r.URL.Path+" key="+r.Header.Get("x-api-key"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

func TestModelProxyStatusReportsAHealthyProxy(t *testing.T) {
	addr, asked := fakeProxy(t, http.StatusOK, healthJSON(t, fixtureHealth()))
	var out bytes.Buffer
	if err := modelProxyStatus(context.Background(), addr, "secret-key", "", &out, func() time.Time { return healthNow }); err != nil {
		t.Fatalf("status returned %v", err)
	}
	text := out.String()
	for _, want := range []string{"ok", addr, "codex-lean", "running", "42", "1m30s ago", "3h0m0s"} {
		if !strings.Contains(text, want) {
			t.Errorf("output does not mention %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "42 turns") {
		t.Errorf("output does not count turns:\n%s", text)
	}
	if requests := asked(); len(requests) != 1 || requests[0] != "GET /health key=secret-key" {
		t.Errorf("requests %v, want one authenticated GET /health", requests)
	}
}

func TestModelProxyStatusReadsNaturallyForOneTurn(t *testing.T) {
	health := fixtureHealth()
	justNow := healthNow.Add(-200 * time.Millisecond)
	health.Requests.Total, health.Requests.LastOKAt = 1, &justNow
	addr, _ := fakeProxy(t, http.StatusOK, healthJSON(t, health))
	var out bytes.Buffer
	if err := modelProxyStatus(context.Background(), addr, "k", "", &out, func() time.Time { return healthNow }); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1 turn,", "last ok just now"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not contain %q:\n%s", want, out.String())
		}
	}
}

func TestModelProxyStatusFailsWhenDegraded(t *testing.T) {
	health := fixtureHealth()
	failed := healthNow.Add(-20 * time.Second)
	health.Status, health.Reason = "degraded", "the last request failed: codex app-server exited"
	health.Requests.Failed, health.Requests.LastErrorAt = 1, &failed
	health.Requests.LastError = "codex app-server exited"
	addr, _ := fakeProxy(t, http.StatusServiceUnavailable, healthJSON(t, health))
	var out bytes.Buffer
	err := modelProxyStatus(context.Background(), addr, "secret-key", "", &out, func() time.Time { return healthNow })
	if err == nil {
		t.Fatal("a degraded proxy returned no error, so the command would exit 0")
	}
	if !strings.Contains(out.String(), "the last request failed: codex app-server exited") {
		t.Errorf("output does not show the reason:\n%s", out.String())
	}
}

func TestModelProxyStatusReportsNotRunning(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = modelProxyStatus(context.Background(), addr, "secret-key", "", &out, time.Now)
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("err = %v, want it to say the proxy is not running", err)
	}
	if !strings.Contains(out.String(), "aimux model proxy serve") {
		t.Errorf("output does not say how to start it:\n%s", out.String())
	}
}

func TestModelProxyStatusRefusesARedirect(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the status command followed a redirect and sent %q", r.Header.Get("x-api-key"))
	}))
	t.Cleanup(elsewhere.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/health", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)
	var out bytes.Buffer
	err := modelProxyStatus(context.Background(), strings.TrimPrefix(srv.URL, "http://"), "secret-key", "", &out, time.Now)
	if err == nil || !strings.Contains(err.Error(), "redirected") {
		t.Fatalf("err = %v, want a refused redirect", err)
	}
}

func TestModelProxyStatusReportsARejectedKey(t *testing.T) {
	addr, _ := fakeProxy(t, http.StatusUnauthorized, `{"type":"error","error":{"message":"invalid or missing proxy key"}}`)
	var out bytes.Buffer
	err := modelProxyStatus(context.Background(), addr, "wrong-key", "", &out, time.Now)
	if err == nil || !strings.Contains(err.Error(), "key") {
		t.Fatalf("err = %v, want it to name the key", err)
	}
}

func healthJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestModelProxyServeRejectsAnUnknownServiceTier(t *testing.T) {
	root := newModelCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"proxy", "serve", "--service-tier", "turbo", "--port", "0"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "priority") || !strings.Contains(err.Error(), "standard") {
		t.Fatalf("err = %v, want it to name the two tiers", err)
	}
}

func TestLedgerLine(t *testing.T) {
	for _, tc := range []struct {
		ledger modelproxy.LedgerHealth
		want   string
	}{
		{modelproxy.LedgerHealth{}, ""},
		{modelproxy.LedgerHealth{Seq: 1}, "latest line 1"},
		{modelproxy.LedgerHealth{Seq: 1840}, "latest line 1840"},
		{modelproxy.LedgerHealth{Seq: 1840, Failed: 3}, "latest line 1840, 3 not written"},
		{modelproxy.LedgerHealth{Off: true}, "off: the ledger file could not be opened at start (see the proxy's log); model calls are not recorded"},
	} {
		if got := ledgerLine(tc.ledger); got != tc.want {
			t.Errorf("ledgerLine(%+v) = %q, want %q", tc.ledger, got, tc.want)
		}
	}
}

// Calls passed on to another engine are counted beside Codex's turns: a proxy that has only served Anthropic is
// not shown as idle.
func TestModelProxyStatusCountsWhatItPassedOn(t *testing.T) {
	history := t.TempDir() + "/requests.jsonl"
	log, err := modelproxy.OpenRequestLog(history, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, r := range []modelproxy.Record{
		{At: now.Add(-20 * time.Minute), Outcome: "forwarded", Detail: "anthropic", Status: 200},
		{At: now.Add(-10 * time.Minute), Outcome: "forwarded", Detail: "anthropic", Status: 429, Error: "upstream_error"},
		{At: now.Add(-5 * time.Minute), Outcome: "forwarded", Detail: "lab", Status: 200},
		{At: now.Add(-4 * time.Minute), Outcome: "new_session", Status: 200},                  // a Codex turn: not passed on
		{At: now.Add(-9 * time.Hour), Outcome: "forwarded", Detail: "anthropic", Status: 200}, // before this proxy started
	} {
		log.Record(r)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	health := fixtureHealth()
	health.Requests = modelproxy.StageHealth{}
	health.StartedAt = now.Add(-3 * time.Hour)
	body, _ := json.Marshal(health)
	addr, _ := fakeProxy(t, http.StatusOK, string(body))
	var out bytes.Buffer
	if err := modelProxyStatus(context.Background(), addr, "secret-key", history, &out, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"Codex turns", "0 turns", "Passed on", "3 calls since it started", "anthropic 2 (1 failed)", "lab 1"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in:\n%s", want, text)
		}
	}
	// With nothing passed on the row is left out, as before.
	out.Reset()
	if err := modelProxyStatus(context.Background(), addr, "secret-key", t.TempDir()+"/none.jsonl", &out, func() time.Time { return now }); err != nil || strings.Contains(out.String(), "Passed on") {
		t.Fatalf("%v\n%s", err, out.String())
	}
}

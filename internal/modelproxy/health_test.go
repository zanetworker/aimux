package modelproxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const healthTurn = `{"model":"codex:gpt-6.2-sol","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`

func TestHealthStatusRules(t *testing.T) {
	boom, login := errors.New("boom"), errors.New("no login")
	for _, tc := range []struct {
		name           string
		requests, prob []error
		status, reason string
	}{
		{name: "nothing attempted yet", status: "ok"},
		{name: "every turn succeeded", requests: []error{nil, nil}, status: "ok"},
		{name: "a failure with a success after it is history", requests: []error{boom, nil}, status: "ok"},
		{name: "the last turn failed", requests: []error{nil, boom}, status: "degraded", reason: "the last request failed: boom"},
		{name: "the last Codex check failed", prob: []error{login}, status: "degraded", reason: "the last Codex check failed: no login"},
		{
			name: "a failed turn is reported before a failed check", requests: []error{boom}, prob: []error{login},
			status: "degraded", reason: "the last request failed: boom",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &healthState{}
			for _, err := range tc.requests {
				state.record(&state.requests, err)
			}
			for _, err := range tc.prob {
				state.record(&state.probe, err)
			}
			got := state.snapshot("codex", false)
			if got.Status != tc.status || got.Reason != tc.reason {
				t.Errorf("status %q reason %q, want %q and %q", got.Status, got.Reason, tc.status, tc.reason)
			}
		})
	}
}

// A wall clock that steps backwards must not make a new failure look older than the last success.
func TestHealthIgnoresTheWallClockOrder(t *testing.T) {
	state := &healthState{}
	state.record(&state.requests, nil)
	state.requests.lastErrorAt = state.requests.lastOK.Add(-time.Hour)
	state.record(&state.requests, errors.New("boom"))
	state.requests.lastErrorAt = state.requests.lastOK.Add(-time.Hour)
	if got := state.snapshot("codex", false); got.Status != "degraded" {
		t.Fatalf("status %q, want degraded after the latest attempt failed", got.Status)
	}
}

// Codex going quiet is exactly what health is for: a check that times out is a failure, while one cancelled
// because the proxy is shutting down says nothing about Codex.
func TestHealthRecordsACheckThatTimedOut(t *testing.T) {
	backend, _ := newFakeBackend(t)
	expired, cancelDeadline := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelDeadline()
	if _, err := backend.ReadUsage(expired); err == nil {
		t.Fatal("a read past its deadline returned no error")
	}
	if got := backend.vitals.snapshot("codex", false); got.Status != "degraded" || got.Probe.Failed != 1 {
		t.Fatalf("after a timed-out check: %+v", got)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := backend.ReadUsage(cancelled); err == nil {
		t.Fatal("a cancelled read returned no error")
	}
	if got := backend.vitals.snapshot("codex", false); got.Probe.Total != 1 {
		t.Fatalf("a cancelled read was counted: %+v", got.Probe)
	}
}

func TestHealthCountsOnlyTurnsSentToCodex(t *testing.T) {
	backend, _ := newFakeBackend(t)
	handler := NewHandler(backend, testKey)
	if res := request(handler, http.MethodPost, "/v1/messages", `{"messages":[`); res.Code != http.StatusBadRequest {
		t.Fatalf("malformed request status %d, want 400", res.Code)
	}
	health := readHealth(t, handler, http.StatusOK)
	if health.Status != "ok" || health.Requests.Total != 0 || health.Codex.AppServerRunning {
		t.Fatalf("after a malformed request: %+v", health)
	}
	if res := request(handler, http.MethodPost, "/v1/messages", healthTurn); res.Code != http.StatusOK {
		t.Fatalf("turn status %d: %s", res.Code, res.Body)
	}
	health = readHealth(t, handler, http.StatusOK)
	if health.Status != "ok" || health.Requests.Total != 1 || health.Requests.Failed != 0 {
		t.Fatalf("after one turn: %+v", health)
	}
	if health.Requests.LastOKAt == nil {
		t.Error("the successful turn left no last_ok_at")
	}
	if !health.Codex.AppServerRunning || health.Codex.Command != fakeCodex {
		t.Errorf("codex %+v, want the fake app-server reported as running", health.Codex)
	}
	if health.UptimeSeconds < 0 || health.StartedAt.IsZero() {
		t.Errorf("uptime %d started %v", health.UptimeSeconds, health.StartedAt)
	}
}

func TestHealthReportsAFailedTurn(t *testing.T) {
	backend := NewCodexBackend(CodexOptions{Command: t.TempDir() + "/missing-codex", Stderr: io.Discard})
	t.Cleanup(backend.Close)
	handler := NewHandler(backend, testKey)
	if res := request(handler, http.MethodPost, "/v1/messages", healthTurn); res.Code != http.StatusBadGateway {
		t.Fatalf("turn status %d, want 502: %s", res.Code, res.Body)
	}
	health := readHealth(t, handler, http.StatusServiceUnavailable)
	if health.Status != "degraded" || health.Requests.Failed != 1 || health.Requests.Total != 1 {
		t.Fatalf("after a failed turn: %+v", health)
	}
	if !strings.HasPrefix(health.Reason, "the last request failed: ") || health.Requests.LastErrorAt == nil {
		t.Errorf("reason %q, last_error %q", health.Reason, health.Requests.LastError)
	}
}

func TestHealthReportsTheCodexCheck(t *testing.T) {
	backend, _ := newFakeBackend(t)
	handler := NewHandler(backend, testKey)
	if _, err := backend.ReadUsage(context.Background()); err != nil {
		t.Fatal(err)
	}
	health := readHealth(t, handler, http.StatusOK)
	if health.Probe.Total != 1 || health.Probe.Failed != 0 || health.Probe.LastOKAt == nil {
		t.Fatalf("after a usage read: %+v", health.Probe)
	}
	backend.vitals.record(&backend.vitals.probe, errors.New("rate limits unavailable"))
	health = readHealth(t, handler, http.StatusServiceUnavailable)
	if health.Status != "degraded" || health.Reason != "the last Codex check failed: rate limits unavailable" {
		t.Fatalf("after a failed check: %+v", health)
	}
}

func TestHealthNeedsTheProxyKey(t *testing.T) {
	backend, _ := newFakeBackend(t)
	res := httptest.NewRecorder()
	NewHandler(backend, testKey).ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/health", nil))
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", res.Code)
	}
}

func readHealth(t *testing.T, handler http.Handler, status int) Health {
	t.Helper()
	res := request(handler, http.MethodGet, "/health", "")
	if res.Code != status {
		t.Fatalf("health status %d, want %d: %s", res.Code, status, res.Body)
	}
	var health Health
	if err := json.Unmarshal(res.Body.Bytes(), &health); err != nil {
		t.Fatalf("decode health %s: %v", res.Body, err)
	}
	return health
}

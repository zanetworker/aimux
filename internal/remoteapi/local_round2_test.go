package remoteapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/profilerun"
	"github.com/zanetworker/aimux/internal/remoteapi/devicelogin"
)

// With device login on, a local client is still confined and audited:
// device login wraps the confined, audited chain, never the bare mux.
func TestLocalClient_ConfinedWithDeviceLoginOn(t *testing.T) {
	r := newStepUpRig(t, false)
	buf := &syncBuffer{}
	r.api.Audit = devicelogin.NewAudit(buf, time.Now, nil)
	r.api.Session = func(_ context.Context, id string, _ int) (SessionDetail, error) {
		return SessionDetail{Session: Session{ID: id, Provider: "claude", State: StateIdle}, Turns: []Turn{}}, nil
	}
	tok := r.login.pair("iphone")
	r.as("wingman")
	if st, out := r.login.do("GET", "/v1/sessions/s1", "", tok); st != 404 || out["code"] != "not_found" {
		t.Fatalf("host session: %d %v", st, out)
	}
	if st, out := r.login.do("GET", "/v1/search?q=x", "", tok); st != 403 || out["code"] != "forbidden_for_client" {
		t.Fatalf("search: %d %v", st, out)
	}
	if lines := auditLines(t, buf.String()); len(lines) != 2 || lines[0]["client"] != "wingman" {
		t.Fatalf("audit lines %v", lines)
	}
}

// A local client streams its own session's events through the audit and
// scope wrappers.
func TestLocalClient_StreamsOwnSessionEvents(t *testing.T) {
	r := confinedRig(t)
	if resp, out := r.do("POST", "/v1/sessions/w1/messages", `{"text":"go on"}`); resp.StatusCode != 202 {
		t.Fatalf("message: %d %v", resp.StatusCode, out)
	}
	r.idle("w1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", r.srv.URL+"/v1/sessions/w1/events?after=0", nil)
	req.Header.Set("Tailscale-User-Login", testLogin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if evs := readSSE(t, resp.Body, KindTurnDone); len(evs) < 2 {
		t.Fatalf("events %v", kindsOf(evs))
	}
}

// An idempotency key names one request of one client: another client (or
// another profile or page) reusing it gets its own session, never the
// first one's.
func TestNewSession_KeyFromAnotherClientIsNotShared(t *testing.T) {
	r := newProfileRig(t).as("device").serve()
	if resp, out := r.do("POST", "/v1/sessions", `{"cwd":"/w/app","text":"host work","key":"k1"}`); resp.StatusCode != 202 || out["id"] != "new-1" {
		t.Fatalf("device: %d %v", resp.StatusCode, out)
	}
	r.idle("new-1")
	r.as("wingman")
	resp, out := r.do("POST", "/v1/sessions", `{"profile":"page","url":"https://a.example/1","text":"hi","key":"k1"}`)
	if resp.StatusCode != 202 || out["id"] != "p-1" {
		t.Fatalf("wingman with the device's key: %d %v, want its own p-1", resp.StatusCode, out)
	}
	r.idle("p-1")
	// Same client, another page, same key: another session.
	if resp, out := r.do("POST", "/v1/sessions", `{"profile":"page","url":"https://a.example/2","text":"hi","key":"k1"}`); out["id"] != "p-2" {
		t.Fatalf("another page: %d %v", resp.StatusCode, out)
	}
	r.idle("p-2")
	// A retry of the same request is still the same session.
	if resp, out := r.do("POST", "/v1/sessions", `{"profile":"page","url":"https://a.example/2","text":"hi","key":"k1"}`); out["id"] != "p-2" || r.started() != 2 {
		t.Fatalf("retry: %d %v (started %d)", resp.StatusCode, out, r.started())
	}
}

// cli is the owner's own terminal: unconfined, like a device.
func TestLocalClient_CLIListsAndEditsHostSessions(t *testing.T) {
	r := newProfileRig(t).as("cli")
	file := filepath.Join(t.TempDir(), "s1.jsonl")
	if err := os.WriteFile(file, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.api.Record = func(_ context.Context, id string) (history.Session, error) {
		return history.Session{ID: id, FilePath: file}, nil
	}
	r.serve()
	r.put("w1", profilerun.Meta{Profile: "page", Client: "wingman", Created: time.Now()})
	_, body := r.do("GET", "/v1/sessions", "")
	if got := fmt.Sprint(listIDs(t, body)); got != "[s1]" {
		t.Fatalf("cli list: %s, want the host's [s1]", got)
	}
	_, body = r.do("GET", "/v1/sessions?client=wingman", "")
	if got := fmt.Sprint(listIDs(t, body)); got != "[w1]" {
		t.Fatalf("cli lists wingman's: %s, want [w1]", got)
	}
	if resp, out := r.do("PATCH", "/v1/sessions/s1/meta", `{"pinned":true}`); resp.StatusCode != 200 || out["pinned"] != true {
		t.Fatalf("cli pins s1: %d %v", resp.StatusCode, out)
	}
}

// The refusals that matter most are audited with their route and session.
func TestAudit_ScopeRefusalsCarryRouteAndSession(t *testing.T) {
	r := confinedRig(t)
	buf := &syncBuffer{}
	r.api.Audit = devicelogin.NewAudit(buf, time.Now, nil)
	r.do("POST", "/v1/sessions/d1/messages", `{"text":"hi"}`)
	r.do("GET", "/v1/search?q=x", "")
	lines := auditLines(t, buf.String())
	if len(lines) != 2 {
		t.Fatalf("lines %v", lines)
	}
	if l := lines[0]; l["action"] != "POST /v1/sessions/{id}/messages" || l["session"] != "d1" || l["result"] != "http_404" {
		t.Errorf("404 line %v", l)
	}
	if l := lines[1]; l["action"] != "GET /v1/search" || l["result"] != "http_403" {
		t.Errorf("403 line %v", l)
	}
}

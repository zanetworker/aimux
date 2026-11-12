package remoteapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/remoteapi/devicelogin"
)

// localDo sends one request to h as a local socket client would: Host
// aimux.local, no tailnet headers, a unix socket peer.
func localDo(t *testing.T, h http.Handler, method, path, body string, header map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "aimux.local"
	r.RemoteAddr = "@"
	for k, v := range header {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

// The socket names the client: a header claiming another client (one that
// the test client seam would believe) changes nothing, and no tailnet identity or device
// login is asked for. Confinement and the local audit still apply.
func TestLocalHandler_ClientIsFixed(t *testing.T) {
	r := newProfileRig(t)
	setClientOf(r.api, func(req *http.Request) string { return req.Header.Get("X-Aimux-Client") })
	buf := &syncBuffer{}
	r.api.Audit = devicelogin.NewAudit(buf, time.Now, nil)
	h := r.api.LocalHandler("wingman")

	claim := map[string]string{"X-Aimux-Client": "cli"}
	if rec, out := localDo(t, h, "GET", "/v1/sessions/s1", "", claim); rec.Code != http.StatusNotFound || out["code"] != "not_found" {
		t.Fatalf("wingman claiming cli read a host session: %d %v", rec.Code, out)
	}
	if rec, out := localDo(t, h, "GET", "/v1/search?q=x", "", claim); rec.Code != http.StatusForbidden || out["code"] != "forbidden_for_client" {
		t.Fatalf("wingman claiming cli searched: %d %v", rec.Code, out)
	}
	rec, out := localDo(t, h, "GET", "/v1/profiles", "", claim)
	if rec.Code != http.StatusOK {
		t.Fatalf("profiles over the socket: %d %v", rec.Code, out)
	}
	for _, p := range out["profiles"].([]any) {
		if p := p.(map[string]any); p["name"] == "coding" && p["allowed"] != false {
			t.Fatalf("coding allowed for the socket's client: %v", p)
		}
	}
	lines := auditLines(t, buf.String())
	if len(lines) != 3 {
		t.Fatalf("%d audit lines, want 3:\n%s", len(lines), buf.String())
	}
	for _, l := range lines {
		if l["client"] != "wingman" {
			t.Fatalf("audit line names client %v, want wingman: %v", l["client"], l)
		}
	}
}

// Host must be aimux.local: anything that reaches the handler with another
// Host, or from a web page, is refused before any route runs.
func TestLocalHandler_RejectsForeignHost(t *testing.T) {
	r := newProfileRig(t)
	h := r.api.LocalHandler("wingman")
	for _, host := range []string{"evil.example", "localhost", "127.0.0.1:7382", "mac.tail1.ts.net", "aimux.local.evil.example"} {
		req := httptest.NewRequest("GET", "/v1/profiles", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("Host %q: %d %s, want 403", host, rec.Code, rec.Body.String())
		}
	}
	if rec, _ := localDo(t, h, "GET", "/v1/profiles", "", map[string]string{"Origin": "https://evil.example"}); rec.Code != http.StatusForbidden {
		t.Fatalf("a web page's Origin: %d, want 403", rec.Code)
	}
	if rec, out := localDo(t, h, "GET", "/v1/profiles", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("aimux.local: %d %v", rec.Code, out)
	}
}

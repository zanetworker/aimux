package remoteapi

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/profilerun"
)

// confinedRig is a profileRig whose Session reads any id, so a refusal can
// only come from the client scoping. It holds a plain host session s1, a
// device profile session d1 and Wingman's own w1.
func confinedRig(t *testing.T) *profileRig {
	t.Helper()
	r := newProfileRig(t)
	r.api.Session = func(_ context.Context, id string, _ int) (SessionDetail, error) {
		return SessionDetail{Session: Session{ID: id, Provider: "claude", State: StateIdle}, Turns: []Turn{}}, nil
	}
	r.serve()
	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	r.put("w1", profilerun.Meta{Profile: "page", Client: "wingman", URL: "https://a.example/x", Created: at})
	r.put("d1", profilerun.Meta{Profile: "page", Client: "device", URL: "https://a.example/x", Created: at})
	return r
}

func TestLocalClient_CannotReadHostSession(t *testing.T) {
	r := confinedRig(t)
	for _, id := range []string{"s1", "d1", "nope"} {
		for _, path := range []string{"/v1/sessions/" + id, "/v1/sessions/" + id + "/events?wait=0"} {
			if resp, out := r.do("GET", path, ""); resp.StatusCode != 404 || out["code"] != "not_found" {
				t.Errorf("wingman GET %s: %d %v, want 404 not_found", path, resp.StatusCode, out)
			}
		}
	}
	// The device and cli callers are unchanged.
	for _, client := range []string{"device", "cli"} {
		if resp, out := r.as(client).do("GET", "/v1/sessions/s1", ""); resp.StatusCode != 200 {
			t.Errorf("%s reads s1: %d %v", client, resp.StatusCode, out)
		}
	}
}

func TestLocalClient_CannotMessageHostSession(t *testing.T) {
	r := confinedRig(t)
	for _, id := range []string{"s1", "d1", "nope", "w1%2Fs1"} {
		if resp, out := r.do("POST", "/v1/sessions/"+id+"/messages", `{"text":"rm -rf ~"}`); resp.StatusCode != 404 || out["code"] != "not_found" {
			t.Errorf("message %s: %d %v", id, resp.StatusCode, out)
		}
		if resp, out := r.do("POST", "/v1/sessions/"+id+"/cancel", ""); resp.StatusCode != 404 {
			t.Errorf("cancel %s: %d %v", id, resp.StatusCode, out)
		}
	}
	if n := r.f.sends.Load(); n != 0 {
		t.Fatalf("%d turns sent", n)
	}
}

func TestLocalClient_OwnSessionAllowed(t *testing.T) {
	r := confinedRig(t)
	if resp, out := r.do("GET", "/v1/sessions/w1", ""); resp.StatusCode != 200 || out["id"] != "w1" {
		t.Fatalf("read w1: %d %v", resp.StatusCode, out)
	}
	if resp, out := r.do("POST", "/v1/sessions/w1/messages", `{"text":"and then?"}`); resp.StatusCode != 202 {
		t.Fatalf("message w1: %d %v", resp.StatusCode, out)
	}
	r.idle("w1")
	if resp, out := r.do("GET", "/v1/sessions/w1/events?wait=0", ""); resp.StatusCode != 200 || len(out["events"].([]any)) == 0 {
		t.Fatalf("events w1: %d %v", resp.StatusCode, out)
	}
	for _, path := range []string{"/v1/health", "/v1/profiles", "/v1/profiles/page", "/v1/sessions"} {
		if resp, out := r.do("GET", path, ""); resp.StatusCode != 200 {
			t.Errorf("GET %s: %d %v", path, resp.StatusCode, out)
		}
	}
}

func TestLocalClient_SearchForbidden(t *testing.T) {
	r := confinedRig(t)
	var searches atomic.Int32
	r.api.Search = func(context.Context, string, int) ([]SearchHit, error) {
		searches.Add(1)
		return nil, nil
	}
	if resp, out := r.do("GET", "/v1/search?q=password", ""); resp.StatusCode != 403 || out["code"] != "forbidden_for_client" || searches.Load() != 0 {
		t.Fatalf("search: %d %v (searches run %d)", resp.StatusCode, out, searches.Load())
	}
	if resp, _ := r.as("cli").do("GET", "/v1/search?q=password", ""); resp.StatusCode != 200 || searches.Load() != 1 {
		t.Fatalf("cli search: %d", resp.StatusCode)
	}
}

// Every route a confined client has no business with is refused before its
// handler runs; the device is unaffected.
func TestLocalClient_ForbiddenRoutes(t *testing.T) {
	r := confinedRig(t)
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "/v1/search?q=x", ""},
		{"GET", "/v1/projects", ""},
		{"POST", "/v1/sessions/w1/terminal/messages", `{"text":"hi"}`},
		{"POST", "/v1/sessions/w1/terminal/keys", `{"promptId":"p","option":"1"}`},
		{"POST", "/v1/sessions/w1/open-on-mac", ""},
		{"POST", "/v1/approvals/a1", `{"option":"allow"}`},
		{"POST", "/v1/devices", `{"token":"t","environment":"sandbox"}`},
		{"DELETE", "/v1/devices/t", ""},
		// Queue and describe are not Wingman's, even on its own session.
		{"POST", "/v1/sessions/w1/describe", ""},
		{"GET", "/v1/sessions/w1/queue", ""},
		{"POST", "/v1/sessions/w1/queue", `{"text":"later"}`},
		{"PATCH", "/v1/sessions/w1/queue/q1", `{"text":"x"}`},
		{"DELETE", "/v1/sessions/w1/queue/q1", ""},
		{"POST", "/v1/sessions/w1/queue/resume", ""},
		{"POST", "/v1/sessions/w1/queue/q1/send-now", ""},
		{"GET", "/v1/sessions/s1/queue", ""},
	} {
		resp, out := r.as("wingman").do(rt.method, rt.path, rt.body)
		if resp.StatusCode != 403 || out["code"] != "forbidden_for_client" {
			t.Errorf("wingman %s %s: %d %v", rt.method, rt.path, resp.StatusCode, out)
		}
		if resp, out := r.as("device").do(rt.method, rt.path, rt.body); out["code"] == "forbidden_for_client" {
			t.Errorf("device %s %s: %d %v", rt.method, rt.path, resp.StatusCode, out)
		}
	}
}

func TestLocalClient_FollowUpCountsTowardsLimit(t *testing.T) {
	r := confinedRig(t)
	w := r.api.Scopes.Clients["wingman"]
	w.TurnsPerMinute = 2
	r.api.Scopes.Clients["wingman"] = w
	clock := &testClock{t: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)}
	r.api.Now = clock.now
	if resp, out := r.do("POST", "/v1/sessions", ask("https://a.example/1")); resp.StatusCode != 202 {
		t.Fatalf("new: %d %v", resp.StatusCode, out)
	}
	r.idle("p-1")
	if resp, out := r.do("POST", "/v1/sessions/w1/messages", `{"text":"more"}`); resp.StatusCode != 202 {
		t.Fatalf("follow-up: %d %v", resp.StatusCode, out)
	}
	r.idle("w1")
	resp, out := r.do("POST", "/v1/sessions/w1/messages", `{"text":"more"}`)
	if resp.StatusCode != 429 || out["code"] != "client_limit" || resp.Header.Get("Retry-After") != "60" {
		t.Fatalf("third turn: %d %v Retry-After %q", resp.StatusCode, out, resp.Header.Get("Retry-After"))
	}
	if n := r.f.sends.Load(); n != 2 {
		t.Fatalf("%d turns sent, want 2", n)
	}
}

// A device's follow-ups on a profile session count against its configured
// turn rate as a new question would; on a host session they never did.
func TestDevice_FollowUpOnProfileSessionCountsTowardsLimit(t *testing.T) {
	r := confinedRig(t).as("device")
	d := r.api.Scopes.Clients["device"]
	d.TurnsPerMinute = 2
	r.api.Scopes.Clients["device"] = d
	clock := &testClock{t: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)}
	r.api.Now = clock.now
	for i := range 2 {
		if resp, out := r.do("POST", "/v1/sessions/d1/messages", `{"text":"more"}`); resp.StatusCode != 202 {
			t.Fatalf("follow-up %d: %d %v", i, resp.StatusCode, out)
		}
		r.idle("d1")
	}
	resp, out := r.do("POST", "/v1/sessions/d1/messages", `{"text":"more"}`)
	if resp.StatusCode != 429 || out["code"] != "client_limit" || out["error"] != errClientLimit.Error() || resp.Header.Get("Retry-After") != "60" {
		t.Fatalf("third turn: %d %v Retry-After %q", resp.StatusCode, out, resp.Header.Get("Retry-After"))
	}
	if n := r.f.sends.Load(); n != 2 {
		t.Fatalf("%d turns sent, want 2", n)
	}
	// Host-session turns are not profile turns: no limit applies.
	if resp, out := r.do("POST", "/v1/sessions/s1/messages", `{"text":"more"}`); resp.StatusCode != 202 {
		t.Fatalf("host session: %d %v", resp.StatusCode, out)
	}
}

func TestDevice_FollowUpWithoutLimitsUnlimited(t *testing.T) {
	r := confinedRig(t).as("device")
	if m, n := r.api.Scopes.Limit("device"); m != 0 || n != 0 {
		t.Fatalf("device defaults %d/%d, want unlimited", m, n)
	}
	for i := range 5 {
		if resp, out := r.do("POST", "/v1/sessions/d1/messages", `{"text":"more"}`); resp.StatusCode != 202 {
			t.Fatalf("follow-up %d: %d %v", i, resp.StatusCode, out)
		}
		r.idle("d1")
	}
}

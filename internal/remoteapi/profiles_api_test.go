package remoteapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/clientscope"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/profilerun"
)

// testProfiles are resolved manifests as Task 11's catalog would give them.
func testProfiles() map[string]profile.Manifest {
	m := func(name, runs string, available bool) profile.Manifest {
		return profile.Manifest{Name: name, Harness: "claude-code", Runs: runs, Available: available,
			Network: profile.NetworkSpec{Default: "deny"}}
	}
	return map[string]profile.Manifest{
		"page":          m("page", "acp", true),
		"research":      m("research", "acp", false),
		"work-readonly": m("work-readonly", "sandbox", true),
		"coding":        m("coding", "host", true),
		"coding-v1":     m("coding-v1", "host", true), // a migrated v1 host profile
	}
}

// profileRig is an API with profiles on: fakes for the engine and the
// client, a real profile store, and the default scopes plus coding-v1
// granted to wingman (the host rule must refuse it anyway).
type profileRig struct {
	t     *testing.T
	api   *API
	srv   *httptest.Server
	store *profilerun.Store
	f     *fakeBackend

	mu     sync.Mutex
	client string
	starts []profilerun.Meta // NewProfileSession calls
	pages  []string
}

func newProfileRig(t *testing.T) *profileRig {
	t.Helper()
	r := &profileRig{t: t, client: "wingman", f: &fakeBackend{},
		store: &profilerun.Store{Path: filepath.Join(t.TempDir(), "profiles.json")}}
	scopes := clientscope.Defaults()
	w := scopes.Clients["wingman"]
	w.Profiles = append(w.Profiles, "coding-v1")
	scopes.Clients["wingman"] = w
	var b *Broker
	b = NewBroker(r.f.deps(&b), BrokerOptions{ApprovalTimeout: 5 * time.Second})
	r.api = &API{Broker: b, Scopes: scopes, ProfileSessions: r.store,
		Sessions: func(context.Context, int, bool) ([]Session, error) {
			return []Session{{ID: "s1", Provider: "claude", Title: "Plain", State: StateIdle}}, nil
		},
		Projects: func(context.Context) ([]Project, error) { return []Project{{Path: "/w/app", Name: "app"}}, nil },
		Create:   func(context.Context, string, string) (string, error) { return "new-1", nil },
		Profiles: func() (map[string]profile.Manifest, error) { return testProfiles(), nil },
		NewProfileSession: func(_ context.Context, m profilerun.Meta, page string) (string, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.starts = append(r.starts, m)
			r.pages = append(r.pages, page)
			return fmt.Sprintf("p-%d", len(r.starts)), nil
		},
	}
	setClientOf(r.api, func(*http.Request) string { r.mu.Lock(); defer r.mu.Unlock(); return r.client })
	return r
}

// serve starts the server; call after changing api fields.
func (r *profileRig) serve() *profileRig {
	r.srv = httptest.NewServer(r.api.Handler([]string{testLogin}, []string{"127.0.0.1"}))
	r.t.Cleanup(r.srv.Close)
	return r
}

func (r *profileRig) as(client string) *profileRig {
	r.mu.Lock()
	r.client = client
	r.mu.Unlock()
	return r
}

func (r *profileRig) do(method, path, body string) (*http.Response, map[string]any) {
	r.t.Helper()
	return do(r.t, r.srv, method, path, body)
}

func (r *profileRig) started() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.starts)
}

func TestProfiles_ListMarksAllowedForCaller(t *testing.T) {
	r := newProfileRig(t).serve()
	resp, body := r.do("GET", "/v1/profiles", "")
	if resp.StatusCode != 200 {
		t.Fatalf("list: %d %v", resp.StatusCode, body)
	}
	list, _ := body["profiles"].([]any)
	got := map[string]map[string]any{}
	for _, p := range list {
		e := p.(map[string]any)
		got[e["name"].(string)] = e
	}
	if len(got) != len(testProfiles()) {
		t.Fatalf("listed %d profiles, want %d: %v", len(got), len(testProfiles()), body)
	}
	want := map[string]bool{"page": true, "research": true, "work-readonly": true, "coding": false, "coding-v1": false}
	for name, allowed := range want {
		e := got[name]
		if e["allowed"] != allowed {
			t.Errorf("%s allowed %v for wingman, want %v", name, e["allowed"], allowed)
		}
		if d, _ := e["digest"].(string); d != testProfiles()[name].Digest() {
			t.Errorf("%s digest %q", name, d)
		}
	}
	if got["research"]["available"] != false || got["page"]["runs"] != "acp" {
		t.Errorf("research %v, page %v", got["research"], got["page"])
	}
	// The same list for a device caller allows coding.
	_, body = r.as("device").do("GET", "/v1/profiles", "")
	for _, p := range body["profiles"].([]any) {
		if e := p.(map[string]any); e["name"] == "coding" && e["allowed"] != true {
			t.Errorf("coding for device: %v", e)
		}
	}
}

func TestProfiles_GetUnknown404(t *testing.T) {
	r := newProfileRig(t).serve()
	resp, body := r.do("GET", "/v1/profiles/nope", "")
	if resp.StatusCode != 404 || body["code"] != "unknown_profile" {
		t.Fatalf("unknown: %d %v", resp.StatusCode, body)
	}
	resp, body = r.do("GET", "/v1/profiles/work-readonly", "")
	want := testProfiles()["work-readonly"]
	if resp.StatusCode != 200 || body["name"] != "work-readonly" || body["runs"] != "sandbox" ||
		body["digest"] != want.Digest() || body["available"] != true || body["allowed"] != true {
		t.Fatalf("known: %d %v", resp.StatusCode, body)
	}
	if n, _ := body["network"].(map[string]any); n["default"] != "deny" {
		t.Fatalf("manifest fields missing: %v", body)
	}
}

// expectRefused checks a refused new session: status, code, and that no
// engine and no turn was started.
func (r *profileRig) expectRefused(body string, status int, code string) map[string]any {
	r.t.Helper()
	resp, out := r.do("POST", "/v1/sessions", body)
	what := body[:min(len(body), 120)]
	if resp.StatusCode != status || out["code"] != code {
		r.t.Fatalf("%s: %d %v, want %d %s", what, resp.StatusCode, out, status, code)
	}
	if n := r.started(); n != 0 {
		r.t.Fatalf("%s: %d profile sessions started, want none", what, n)
	}
	if n := r.f.sends.Load(); n != 0 {
		r.t.Fatalf("%s: %d turns sent, want none", what, n)
	}
	return out
}

func TestNewSession_ProfileOutsideScope403NothingStarted(t *testing.T) {
	r := newProfileRig(t).serve()
	r.expectRefused(`{"profile":"coding","text":"rm -rf"}`, 403, "profile_not_allowed")
	// In wingman's scope, but it runs on the host: refused whatever the scopes say.
	r.expectRefused(`{"profile":"coding-v1","text":"rm -rf"}`, 403, "profile_not_allowed")
	// A client the scopes do not know gets nothing.
	r.as("stranger").expectRefused(`{"profile":"page","text":"hi"}`, 403, "profile_not_allowed")
}

func TestNewSession_UnknownProfile400(t *testing.T) {
	r := newProfileRig(t).serve()
	r.expectRefused(`{"profile":"nope","text":"hi"}`, 400, "unknown_profile")
	r.as("device").expectRefused(`{"profile":"nope","text":"hi","cwd":"/w/app"}`, 400, "unknown_profile")
}

func TestNewSession_UnavailableProfile409(t *testing.T) {
	r := newProfileRig(t).serve()
	r.expectRefused(`{"profile":"research","text":"hi"}`, 409, "profile_not_available")
}

func TestNewSession_PageTooLarge413(t *testing.T) {
	r := newProfileRig(t).serve()
	big := strings.Repeat("a", maxPageText+1)
	r.expectRefused(`{"profile":"page","text":"sum up","url":"https://example.com/a","pageText":"`+big+`"}`, 413, "page_too_large")
	// Quotes and newlines escaped in JSON still count as one byte each: a
	// page of exactly the limit is not refused for its escaping.
	exact := strings.Repeat(`\"`, maxPageText/2)
	resp, out := r.do("POST", "/v1/sessions", `{"profile":"page","text":"sum up","pageText":"`+exact+`"}`)
	if resp.StatusCode == 413 || resp.StatusCode == 400 {
		t.Fatalf("a page at the limit: %d %v", resp.StatusCode, out)
	}
}

// idle waits until session has no running turn.
func (r *profileRig) idle(session string) {
	r.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for r.api.Broker.Busy(session) {
		if time.Now().After(deadline) {
			r.t.Fatalf("%s still busy", session)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestNewSession_SamePageReusesSession(t *testing.T) {
	r := newProfileRig(t).serve()
	ask := func(profile, url, want string) {
		t.Helper()
		resp, out := r.do("POST", "/v1/sessions",
			`{"profile":"`+profile+`","url":"`+url+`","title":"A page","pageText":"page body","text":"what is this?"}`)
		if resp.StatusCode != 202 || out["id"] != want || out["turn"] == "" {
			t.Fatalf("%s on %s: %d %v, want 202 %s", profile, url, resp.StatusCode, out, want)
		}
		r.idle(want)
	}
	ask("page", "https://example.com/a", "p-1")
	m, ok := r.store.Get("p-1")
	if !ok || m.Profile != "page" || m.Client != "wingman" || m.URL != "https://example.com/a" || m.Title != "A page" ||
		m.Digest != testProfiles()["page"].Digest() || m.Created.IsZero() {
		t.Fatalf("recorded meta %+v %v", m, ok)
	}
	if r.pages[0] != "page body" {
		t.Fatalf("engine got page %q", r.pages[0])
	}
	ask("page", "https://example.com/a", "p-1") // the same page: one conversation
	if r.started() != 1 || r.f.sends.Load() != 2 {
		t.Fatalf("started %d sessions and %d turns, want 1 and 2", r.started(), r.f.sends.Load())
	}
	ask("work-readonly", "https://example.com/a", "p-2") // another profile
	ask("page", "https://example.com/b", "p-3")          // another page
	r.as("device")
	ask("page", "https://example.com/a", "p-4") // another client
}

// Today's request (tests in api_test.go and the clients' fixtures) from a
// device: unchanged, profiles or not.
func TestNewSession_WithoutProfileUnchanged(t *testing.T) {
	r := newProfileRig(t).as("device").serve()
	resp, out := r.do("POST", "/v1/sessions", `{"cwd":"/w/app","text":"plan the release","key":"k1"}`)
	if resp.StatusCode != 202 || out["id"] != "new-1" || r.started() != 0 {
		t.Fatalf("legacy new session: %d %v (profile sessions %d)", resp.StatusCode, out, r.started())
	}
	r.idle("new-1")
	resp, out = r.do("POST", "/v1/sessions", `{"cwd":"/etc","text":"x"}`)
	if resp.StatusCode != 400 || out["code"] != "bad_request" {
		t.Fatalf("outside the projects: %d %v", resp.StatusCode, out)
	}
	// Without a profile a session runs on the host: a local client gets it
	// only if its scope allows the host profile (never Wingman).
	r.as("wingman")
	r.f.sends.Store(0)
	r.expectRefused(`{"cwd":"/w/app","text":"x"}`, 403, "profile_not_allowed")
	r.as("cli")
	if resp, out := r.do("POST", "/v1/sessions", `{"cwd":"/w/app","text":"x","key":"k2"}`); resp.StatusCode != 202 {
		t.Fatalf("cli without a profile: %d %v", resp.StatusCode, out)
	}
}

func (r *profileRig) put(id string, m profilerun.Meta) {
	r.t.Helper()
	if err := r.store.Put(id, m); err != nil {
		r.t.Fatal(err)
	}
}

func listIDs(t *testing.T, body map[string]any) []string {
	t.Helper()
	var ids []string
	for _, s := range body["sessions"].([]any) {
		ids = append(ids, s.(map[string]any)["id"].(string))
	}
	return ids
}

func TestListSessions_FilterByClientAndURL(t *testing.T) {
	r := newProfileRig(t).as("device").serve()
	t0 := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	r.put("w1", profilerun.Meta{Profile: "page", Client: "wingman", URL: "https://a.example/x", Title: "X", Created: t0})
	r.put("w2", profilerun.Meta{Profile: "work-readonly", Client: "wingman", URL: "https://b.example/y", Created: t0.Add(time.Minute)})
	r.put("d1", profilerun.Meta{Profile: "page", Client: "device", URL: "https://a.example/x", Created: t0})

	_, body := r.do("GET", "/v1/sessions?client=wingman", "")
	if got := fmt.Sprint(listIDs(t, body)); got != "[w2 w1]" {
		t.Fatalf("client=wingman: %s, want newest first [w2 w1]", got)
	}
	first := body["sessions"].([]any)[1].(map[string]any)
	page, _ := first["page"].(map[string]any)
	if first["profile"] != "page" || first["client"] != "wingman" || page["url"] != "https://a.example/x" || page["title"] != "X" || first["title"] != "X" {
		t.Fatalf("w1 entry %v", first)
	}
	_, body = r.do("GET", "/v1/sessions?client=wingman&url=https://a.example/x", "")
	if got := fmt.Sprint(listIDs(t, body)); got != "[w1]" {
		t.Fatalf("client and url: %s", got)
	}
	_, body = r.do("GET", "/v1/sessions?url=https://a.example/x", "")
	if got := fmt.Sprint(listIDs(t, body)); got != "[d1 w1]" && got != "[w1 d1]" {
		t.Fatalf("url only: %s", got)
	}
	// No filter: today's list, without profile fields.
	_, body = r.do("GET", "/v1/sessions", "")
	if got := fmt.Sprint(listIDs(t, body)); got != "[s1]" {
		t.Fatalf("no filter: %s", got)
	}
	if s := body["sessions"].([]any)[0].(map[string]any); s["profile"] != nil || s["page"] != nil {
		t.Fatalf("plain entry has profile fields: %v", s)
	}
	// A local client lists only its own sessions, whatever it asks for.
	r.as("wingman")
	for _, q := range []string{"", "?client=device", "?url=https://a.example/x"} {
		_, body = r.do("GET", "/v1/sessions"+q, "")
		for _, id := range listIDs(t, body) {
			if id != "w1" && id != "w2" {
				t.Fatalf("wingman %q sees %s", q, id)
			}
		}
	}
}

func TestPatchMeta_HiddenHidesFromList(t *testing.T) {
	r := newProfileRig(t)
	file := filepath.Join(t.TempDir(), "s1.jsonl")
	if err := os.WriteFile(file, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.api.Record = func(_ context.Context, id string) (history.Session, error) {
		if id != "s1" {
			return history.Session{}, acprun.ErrUnknownSession
		}
		return history.Session{ID: id, FilePath: file}, nil
	}
	r.serve()
	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	r.put("w1", profilerun.Meta{Profile: "page", Client: "wingman", URL: "https://a.example/x", Created: at})
	r.put("w2", profilerun.Meta{Profile: "page", Client: "wingman", URL: "https://a.example/y", Created: at})
	r.put("d1", profilerun.Meta{Profile: "page", Client: "device", URL: "https://a.example/x", Created: at})

	resp, out := r.do("PATCH", "/v1/sessions/w1/meta", `{"hidden":true}`)
	if resp.StatusCode != 200 || out["hidden"] != true {
		t.Fatalf("hide w1: %d %v", resp.StatusCode, out)
	}
	_, body := r.do("GET", "/v1/sessions?client=wingman", "")
	if got := fmt.Sprint(listIDs(t, body)); got != "[w2]" {
		t.Fatalf("after hiding w1: %s", got)
	}
	// The next question on that page starts a new conversation.
	if resp, out := r.do("POST", "/v1/sessions", `{"profile":"page","url":"https://a.example/x","text":"again"}`); resp.StatusCode != 202 || out["id"] != "p-1" {
		t.Fatalf("ask after hiding: %d %v", resp.StatusCode, out)
	}
	// Wingman may hide only its own sessions: others do not exist for it.
	for _, id := range []string{"d1", "s1"} {
		if resp, out := r.do("PATCH", "/v1/sessions/"+id+"/meta", `{"hidden":true}`); resp.StatusCode != 404 || out["code"] != "not_found" {
			t.Fatalf("wingman hides %s: %d %v", id, resp.StatusCode, out)
		}
	}
	if m, _ := r.store.Get("d1"); m.Hidden {
		t.Fatal("d1 hidden by wingman")
	}
	// A plain session from a device: its history meta, which the remote lists honour.
	r.as("device")
	if resp, out := r.do("PATCH", "/v1/sessions/s1/meta", `{"hidden":true}`); resp.StatusCode != 200 || out["hidden"] != true {
		t.Fatalf("hide s1: %d %v", resp.StatusCode, out)
	}
	if !history.LoadMeta(file).Hidden {
		t.Fatal("s1's meta is not hidden")
	}
	if resp, out := r.do("PATCH", "/v1/sessions/s1/meta", `{"hidden":false}`); resp.StatusCode != 400 {
		t.Fatalf("unhide: %d %v, want 400 (hidden takes only true)", resp.StatusCode, out)
	}
}

func ask(url string) string {
	return `{"profile":"page","url":"` + url + `","text":"what is this?"}`
}

func TestLimits_ThirdWingmanSession429(t *testing.T) {
	r := newProfileRig(t) // wingman: max_sessions 2
	release := make(chan struct{})
	r.f.sendF = func(context.Context, string, string, func(acprun.Event)) (string, error) {
		<-release
		return "end_turn", nil
	}
	r.serve()
	defer close(release)
	for _, url := range []string{"https://a.example/1", "https://a.example/2"} {
		if resp, out := r.do("POST", "/v1/sessions", ask(url)); resp.StatusCode != 202 {
			t.Fatalf("%s: %d %v", url, resp.StatusCode, out)
		}
	}
	resp, out := r.do("POST", "/v1/sessions", ask("https://a.example/3"))
	if resp.StatusCode != 429 || out["code"] != "client_limit" || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("third: %d %v Retry-After %q", resp.StatusCode, out, resp.Header.Get("Retry-After"))
	}
	if r.started() != 2 {
		t.Fatalf("started %d sessions, want 2", r.started())
	}
	// Another client has its own count.
	if resp, out := r.as("device").do("POST", "/v1/sessions", ask("https://a.example/3")); resp.StatusCode != 202 {
		t.Fatalf("device: %d %v", resp.StatusCode, out)
	}
}

func TestLimits_TurnsPerMinute429(t *testing.T) {
	r := newProfileRig(t)
	w := r.api.Scopes.Clients["wingman"]
	w.TurnsPerMinute = 2
	r.api.Scopes.Clients["wingman"] = w
	clock := &testClock{t: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)}
	r.api.Now = clock.now
	r.serve()
	for i := range 2 {
		if resp, out := r.do("POST", "/v1/sessions", ask("https://a.example/1")); resp.StatusCode != 202 {
			t.Fatalf("turn %d: %d %v", i, resp.StatusCode, out)
		}
		r.idle("p-1")
	}
	clock.add(20 * time.Second)
	resp, out := r.do("POST", "/v1/sessions", ask("https://a.example/1"))
	if resp.StatusCode != 429 || out["code"] != "client_limit" || resp.Header.Get("Retry-After") != "40" {
		t.Fatalf("third turn in a minute: %d %v Retry-After %q", resp.StatusCode, out, resp.Header.Get("Retry-After"))
	}
	if r.f.sends.Load() != 2 {
		t.Fatalf("%d turns sent, want 2", r.f.sends.Load())
	}
	clock.add(41 * time.Second)
	if resp, out := r.do("POST", "/v1/sessions", ask("https://a.example/1")); resp.StatusCode != 202 {
		t.Fatalf("after the minute: %d %v", resp.StatusCode, out)
	}
}

func TestHealth_ReportsUnreadableProfileStore(t *testing.T) {
	r := newProfileRig(t)
	var storeErr error
	r.api.ProfileHealth = func() error { return storeErr }
	r.serve()
	if _, body := r.do("GET", "/v1/health", ""); body["profileStore"] != nil || body["status"] != "ok" ||
		!strings.Contains(fmt.Sprint(body["capabilities"]), CapProfiles) {
		t.Fatalf("healthy store: %v", body)
	}
	storeErr = errors.New("profile sessions file /h/.aimux/sessions/profiles.json cannot be read; move it aside to recover")
	_, body := r.do("GET", "/v1/health", "")
	if body["profileStore"] != storeErr.Error() || body["status"] != "ok" {
		t.Fatalf("broken store: %v", body)
	}
}

// The larger body a profile request may have is not given to requests
// without one: they keep the 1 MiB limit.
func TestNewSession_LegacyBodyLimitUnchanged(t *testing.T) {
	r := newProfileRig(t).as("device").serve()
	big := strings.Repeat("a", maxSessionBody)
	resp, out := r.do("POST", "/v1/sessions", `{"cwd":"/w/app","text":"`+big+`"}`)
	if resp.StatusCode != 400 || out["code"] != "bad_request" {
		t.Fatalf("a 1 MiB legacy body: %d %v", resp.StatusCode, out)
	}
}

// A session the store cannot record would be routed as a plain host
// session: it is cancelled, and nothing is sent.
func TestNewSession_UnrecordedSessionCancelledNothingSent(t *testing.T) {
	r := newProfileRig(t)
	blocker := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r.store.Path = filepath.Join(blocker, "profiles.json") // its directory is a file: Put fails
	var cancelled []string
	r.api.CancelProfileSession = func(id string) error { cancelled = append(cancelled, id); return nil }
	r.serve()
	resp, out := r.do("POST", "/v1/sessions", `{"profile":"page","text":"hi","key":"k1"}`)
	if resp.StatusCode != 500 || out["code"] != "internal" {
		t.Fatalf("unrecorded: %d %v", resp.StatusCode, out)
	}
	if fmt.Sprint(cancelled) != "[p-1]" || r.f.sends.Load() != 0 {
		t.Fatalf("cancelled %v, sends %d; want [p-1] and none", cancelled, r.f.sends.Load())
	}
	// The key is not kept: a retry makes a new attempt, not a turn into p-1.
	r.do("POST", "/v1/sessions", `{"profile":"page","text":"hi","key":"k1"}`)
	if r.started() != 2 || r.f.sends.Load() != 0 {
		t.Fatalf("retry: started %d, sends %d", r.started(), r.f.sends.Load())
	}
}

// A turn's model choice is sonnet or opus, on a new profile session and on
// a follow-up; it reaches the turn (profilerun.ModelFrom) and nothing else
// is accepted. No choice reaches the turn as "".
func TestModel_OnlySonnetOrOpusReachesTheTurn(t *testing.T) {
	r := newProfileRig(t)
	var mu sync.Mutex
	var got []string
	r.f.sendF = func(ctx context.Context, _, _ string, _ func(acprun.Event)) (string, error) {
		mu.Lock()
		got = append(got, profilerun.ModelFrom(ctx))
		mu.Unlock()
		return "end_turn", nil
	}
	r.serve()
	r.expectRefused(`{"profile":"page","url":"https://e.example/a","text":"q","model":"gpt"}`, 400, "bad_request")

	if resp, out := r.do("POST", "/v1/sessions", `{"profile":"page","url":"https://e.example/a","text":"q1","model":"opus"}`); resp.StatusCode != 202 || out["id"] != "p-1" {
		t.Fatalf("opus: %d %v", resp.StatusCode, out)
	}
	r.idle("p-1")
	if resp, out := r.do("POST", "/v1/sessions/p-1/messages", `{"text":"q2","model":"gpt"}`); resp.StatusCode != 400 || out["code"] != "bad_request" {
		t.Fatalf("follow-up gpt: %d %v", resp.StatusCode, out)
	}
	for _, body := range []string{`{"text":"q3","model":"sonnet"}`, `{"text":"q4"}`} {
		if resp, out := r.do("POST", "/v1/sessions/p-1/messages", body); resp.StatusCode != 202 {
			t.Fatalf("%s: %d %v", body, resp.StatusCode, out)
		}
		r.idle("p-1")
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(got, ",") != "opus,sonnet," {
		t.Fatalf("turn models = %q, want opus, sonnet, then none", got)
	}
}

// Package secondclient proves the spec's promise that reuse is demonstrated,
// not promised: a client that is not Wingman drives profiles through remote
// API v1 over a Unix socket, importing no internal/browserhost code. The
// server is the real remoteapi API behind its real LocalHandler, with the
// real profilerun Router and Store and the real builtin profiles; only the
// engines are fakes.
package secondclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/clientscope"
	"github.com/zanetworker/aimux/internal/profilerun"
	"github.com/zanetworker/aimux/internal/remoteapi"
	"github.com/zanetworker/aimux/internal/remoteapp"
)

// fakeEngine records every call and emits an update delta per turn. The
// broker adds turn_started and turn_done around it.
type fakeEngine struct {
	kind string

	mu    sync.Mutex
	news  []profilerun.Meta
	pages []string
	sends []string // "session: text"
	calls int
}

func (f *fakeEngine) New(_ context.Context, m profilerun.Meta, page string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.news = append(f.news, m)
	f.pages = append(f.pages, page)
	return fmt.Sprintf("%s-%d", f.kind, len(f.news)), nil
}

func (f *fakeEngine) Send(_ context.Context, session, text string, on func(acprun.Event)) (string, error) {
	f.mu.Lock()
	f.calls++
	f.sends = append(f.sends, session+": "+text)
	f.mu.Unlock()
	on(acprun.Event{Kind: acprun.KindUpdate, Data: json.RawMessage(
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"from ` + f.kind + `"}}`)})
	return "end_turn", nil
}

func (f *fakeEngine) Cancel(string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return nil
}

func (f *fakeEngine) seen() (calls int, news []profilerun.Meta, pages, sends []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, append([]profilerun.Meta(nil), f.news...), append([]string(nil), f.pages...), append([]string(nil), f.sends...)
}

// rig is the server: one Unix socket per client, as remote serve makes them.
type rig struct {
	t             *testing.T
	acp, sandbox  *fakeEngine
	socks         map[string]string
	fallbackCalls int
	mu            sync.Mutex
}

func newRig(t *testing.T) *rig { return newRigWith(t, clientscope.Defaults()) }

// newRigWith builds the server from remoteapp's own profile wiring, the one
// remote serve runs (catalog, runs, store, session start and cancel, health,
// router), with fake engines and the given scopes.
func newRigWith(t *testing.T, scopes clientscope.Scopes) *rig {
	t.Helper()
	r := &rig{t: t, acp: &fakeEngine{kind: "acp"}, sandbox: &fakeEngine{kind: "sandbox"}, socks: map[string]string{}}
	wiring, closeAll, err := remoteapp.NewProfileWiring(t.TempDir(), func(string) string { return "" }, io.Discard,
		map[string]profilerun.Engine{"acp": r.acp, "sandbox": r.sandbox})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeAll)
	// Sessions without a profile go to the host in serve; here that is a failure.
	fallback := func(string) error {
		r.mu.Lock()
		r.fallbackCalls++
		r.mu.Unlock()
		return fmt.Errorf("a profile-less session reached the host fallback")
	}
	router := wiring.Router()
	router.Fallback = func(context.Context, string, string, func(acprun.Event)) (string, error) { return "", fallback("send") }
	router.FallbackCheck, router.FallbackCancel = fallback, fallback
	broker := remoteapi.NewBroker(remoteapi.BrokerDeps{Check: router.Check, Send: router.Send, Cancel: router.Cancel},
		remoteapi.BrokerOptions{ApprovalTimeout: 5 * time.Second})
	api := &remoteapi.API{
		Broker:   broker,
		Sessions: func(context.Context, int, bool) ([]remoteapi.Session, error) { return nil, nil },
		// Any id reads: a refusal in these tests can only come from the scoping.
		Session: func(_ context.Context, id string, _ int) (remoteapi.SessionDetail, error) {
			return remoteapi.SessionDetail{Session: remoteapi.Session{ID: id, Provider: "claude", State: remoteapi.StateIdle}, Turns: []remoteapi.Turn{}}, nil
		},
	}
	wiring.Install(api, scopes, false)

	// A short directory: Unix socket paths are limited to about 100 bytes.
	dir, err := os.MkdirTemp("/tmp", "sc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	for _, client := range []string{"cli", "wingman"} {
		path := filepath.Join(dir, client+".sock")
		ln, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: api.LocalHandler(client), ReadHeaderTimeout: 10 * time.Second}
		go func() { _ = srv.Serve(ln) }()
		t.Cleanup(func() { _ = srv.Close() })
		r.socks[client] = path
	}
	return r
}

// call is one request as a client of the socket: Host aimux.local, no Origin.
func (r *rig) call(client, method, path, body string) (int, map[string]any) {
	r.t.Helper()
	hc := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", r.socks[client])
		}}}
	defer hc.CloseIdleConnections()
	req, err := http.NewRequest(method, "http://"+remoteapi.LocalHost+path, strings.NewReader(body))
	if err != nil {
		r.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		r.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		r.t.Fatalf("%s %s: %d not JSON: %q", method, path, resp.StatusCode, raw)
	}
	return resp.StatusCode, out
}

// follow reads the session's events until turn_done and returns their kinds
// and the update texts.
func (r *rig) follow(client, session string) (kinds []string, updates string) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		code, out := r.call(client, "GET", "/v1/sessions/"+session+"/events?wait=0", "")
		if code != 200 {
			r.t.Fatalf("events: %d %v", code, out)
		}
		kinds, updates = nil, ""
		done := false
		for _, e := range out["events"].([]any) {
			ev := e.(map[string]any)
			k := ev["kind"].(string)
			kinds = append(kinds, k)
			if k == "update" {
				updates += string(mustJSON(ev["data"]))
			}
			if k == "turn_done" {
				done = true
			}
		}
		if done {
			return kinds, updates
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatalf("no turn_done for %s; saw %v", session, kinds)
	return nil, ""
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestSecondClient_PageSessionRoundTrip(t *testing.T) {
	r := newRig(t)

	code, out := r.call("cli", "GET", "/v1/profiles", "")
	if code != 200 {
		t.Fatalf("profiles: %d %v", code, out)
	}
	allowed := map[string]bool{}
	for _, p := range out["profiles"].([]any) {
		e := p.(map[string]any)
		allowed[e["name"].(string)] = e["allowed"].(bool)
	}
	if !allowed["page"] || !allowed["coding"] || !allowed["work-readonly"] {
		t.Fatalf("cli should be allowed page, coding and work-readonly: %v", allowed)
	}

	body := `{"profile":"page","url":"https://a.example/x","title":"A page","pageText":"the page text","text":"what is this?"}`
	code, out = r.call("cli", "POST", "/v1/sessions", body)
	if code != 202 {
		t.Fatalf("new page session: %d %v", code, out)
	}
	id, _ := out["id"].(string)
	if id == "" {
		t.Fatalf("no session id: %v", out)
	}

	kinds, updates := r.follow("cli", id)
	if len(kinds) < 3 || kinds[0] != "turn_started" || kinds[len(kinds)-1] != "turn_done" {
		t.Fatalf("event kinds %v, want turn_started, update, turn_done", kinds)
	}
	if !strings.Contains(updates, "from acp") {
		t.Fatalf("update delta %q does not come from the page engine", updates)
	}

	calls, news, pages, sends := r.acp.seen()
	if len(news) != 1 || news[0].Profile != "page" || news[0].Client != "cli" || news[0].URL != "https://a.example/x" {
		t.Fatalf("page engine New: %+v", news)
	}
	if pages[0] != "the page text" || len(sends) != 1 || sends[0] != id+": what is this?" || calls != 2 {
		t.Fatalf("page engine saw pages %v sends %v calls %d", pages, sends, calls)
	}
	if c, _, _, _ := r.sandbox.seen(); c != 0 {
		t.Fatalf("sandbox engine called %d times for a page session", c)
	}

	code, out = r.call("cli", "GET", "/v1/sessions?client=cli", "")
	if code != 200 {
		t.Fatalf("list: %d %v", code, out)
	}
	list := out["sessions"].([]any)
	if len(list) != 1 {
		t.Fatalf("listed %d sessions, want 1: %v", len(list), list)
	}
	s := list[0].(map[string]any)
	if s["id"] != id || s["profile"] != "page" || s["client"] != "cli" {
		t.Fatalf("listed session %v", s)
	}
}

func TestSecondClient_WorkReadonlyRoutesToSandboxEngine(t *testing.T) {
	r := newRig(t)
	code, out := r.call("cli", "POST", "/v1/sessions", `{"profile":"work-readonly","text":"what is open in my queue?"}`)
	if code != 202 {
		t.Fatalf("new work-readonly session: %d %v", code, out)
	}
	id := out["id"].(string)
	_, updates := r.follow("cli", id)
	if !strings.Contains(updates, "from sandbox") {
		t.Fatalf("update delta %q does not come from the sandbox engine", updates)
	}
	_, news, _, sends := r.sandbox.seen()
	if len(news) != 1 || news[0].Profile != "work-readonly" || len(sends) != 1 {
		t.Fatalf("sandbox engine New %+v sends %v", news, sends)
	}
	if c, _, _, _ := r.acp.seen(); c != 0 {
		t.Fatalf("page engine called %d times for a sandbox session", c)
	}
}

func TestSecondClient_WingmanSocketCannotUseCoding(t *testing.T) {
	r := newRig(t)
	for _, body := range []string{
		`{"profile":"coding","text":"rm -rf ~"}`,
		`{"cwd":"/tmp","text":"no profile is the host profile"}`,
	} {
		code, out := r.call("wingman", "POST", "/v1/sessions", body)
		if code != 403 || out["code"] != "profile_not_allowed" {
			t.Fatalf("wingman %s: %d %v, want 403 profile_not_allowed", body, code, out)
		}
	}
	if c, _, _, _ := r.acp.seen(); c != 0 {
		t.Fatalf("page engine saw %d calls", c)
	}
	if c, _, _, _ := r.sandbox.seen(); c != 0 {
		t.Fatalf("sandbox engine saw %d calls", c)
	}
	if r.fallbackCalls != 0 {
		t.Fatalf("host fallback reached %d times", r.fallbackCalls)
	}
}

// The host rule also refuses coding, so this gives wingman a scope without a
// non-host profile and asks for it: only the scope check can refuse.
func TestSecondClient_ScopeAloneRefuses(t *testing.T) {
	scopes := clientscope.Defaults()
	w := scopes.Clients["wingman"]
	w.Profiles = []string{"page"}
	scopes.Clients["wingman"] = w
	r := newRigWith(t, scopes)
	code, out := r.call("wingman", "POST", "/v1/sessions", `{"profile":"work-readonly","text":"hi"}`)
	if code != 403 || out["code"] != "profile_not_allowed" {
		t.Fatalf("wingman work-readonly outside its scope: %d %v, want 403 profile_not_allowed", code, out)
	}
	if c, _, _, _ := r.sandbox.seen(); c != 0 {
		t.Fatalf("sandbox engine saw %d calls", c)
	}
	if code, out := r.call("wingman", "POST", "/v1/sessions", `{"profile":"page","url":"https://a.example/x","text":"hi"}`); code != 202 {
		t.Fatalf("page is in scope: %d %v", code, out)
	}
}

func TestSecondClient_WingmanCannotSeeCLISession(t *testing.T) {
	r := newRig(t)
	code, out := r.call("cli", "POST", "/v1/sessions", `{"profile":"page","url":"https://a.example/x","text":"hi"}`)
	if code != 202 {
		t.Fatalf("cli new: %d %v", code, out)
	}
	id := out["id"].(string)
	r.follow("cli", id)

	if code, out := r.call("cli", "GET", "/v1/sessions/"+id, ""); code != 200 {
		t.Fatalf("cli reads its own session: %d %v", code, out)
	}
	code, out = r.call("wingman", "GET", "/v1/sessions/"+id, "")
	if code != 404 || out["code"] != "not_found" {
		t.Fatalf("wingman GET cli session: %d %v, want 404 not_found", code, out)
	}
	code, out = r.call("wingman", "POST", "/v1/sessions/"+id+"/messages", `{"text":"hijack"}`)
	if code != 404 {
		t.Fatalf("wingman message to cli session: %d %v, want 404", code, out)
	}
	if _, _, _, sends := r.acp.seen(); len(sends) != 1 {
		t.Fatalf("page engine sends %v, want only the cli turn", sends)
	}
}

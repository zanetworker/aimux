package remoteapi

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/push"
	"github.com/zanetworker/aimux/internal/remoteapi/chatqueue"
	"github.com/zanetworker/aimux/internal/remoteapi/devicelogin"
)

type loginRig struct {
	t     *testing.T
	clock *testClock
	srv   *httptest.Server
	dir   string
	dl    *devicelogin.Server
	audit *syncBuffer
	term  *fakeTerminal
	b     *Broker
	queue *chatqueue.Store
}

// newLoginAPI is a server with every capability and device login on.
func newLoginAPI(t *testing.T, f *fakeBackend) *loginRig {
	t.Helper()
	r := &loginRig{t: t, dir: t.TempDir(), audit: &syncBuffer{}, term: &fakeTerminal{}, clock: &testClock{t: time.Now()}}
	dl, err := devicelogin.NewServer(devicelogin.Options{
		Store: devicelogin.Open(r.dir),
		Now:   r.clock.now,
		Audit: devicelogin.NewAudit(r.audit, r.clock.now, nil),
		Login: func(req *http.Request) string { return LoginFrom(req.Context()) },
	})
	if err != nil {
		t.Fatal(err)
	}
	r.dl = dl
	r.b = NewBroker(f.deps(&r.b), BrokerOptions{ApprovalTimeout: 5 * time.Second})
	queue, err := chatqueue.Open(filepath.Join(r.dir, "remote-queue.json"), time.Now, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(queue.Close)
	r.queue = queue
	file := filepath.Join(r.dir, "s1.jsonl")
	if err := os.WriteFile(file, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	api := &API{Broker: r.b, DeviceLogin: dl, Queue: queue, TerminalInput: r.term, TerminalEnabled: true,
		Record: func(_ context.Context, id string) (history.Session, error) {
			return history.Session{ID: id, FilePath: file}, nil
		},
		Naming:   &fakeNaming{result: history.SummaryResult{Title: "A title", Changed: []string{"title"}}},
		Client:   http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) }),
		Sessions: func(context.Context, int, bool) ([]Session, error) { return []Session{{ID: "s1"}}, nil },
		Session: func(_ context.Context, id string, _ int) (SessionDetail, error) {
			return SessionDetail{Session: Session{ID: id, State: StateIdle}, Turns: []Turn{}}, nil
		},
		Search:           func(context.Context, string, int) ([]SearchHit, error) { return nil, nil },
		Projects:         func(context.Context) ([]Project, error) { return []Project{{Path: "/w/app", Name: "app"}}, nil },
		Create:           func(context.Context, string, string) (string, error) { return "new-1", nil },
		RegisterDevice:   func(push.Device) error { return nil },
		UnregisterDevice: func(string) error { return nil },
	}
	r.srv = httptest.NewServer(api.Handler([]string{testLogin}, []string{"127.0.0.1"}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *loginRig) req(method, path, body, token string, headers ...string) *http.Request {
	req, _ := http.NewRequest(method, r.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Tailscale-User-Login", testLogin)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for _, h := range headers {
		k, v, _ := strings.Cut(h, ": ")
		req.Header.Set(k, v)
	}
	return req
}

func (r *loginRig) do(method, path, body, token string) (int, map[string]any) {
	r.t.Helper()
	resp, err := http.DefaultClient.Do(r.req(method, path, body, token))
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// pair pairs an iPhone-style key under name and returns its session token.
func (r *loginRig) pair(name string) string {
	r.t.Helper()
	code, hash := devicelogin.NewCode()
	if err := devicelogin.Open(r.dir).SetPending(name, hash, time.Now().Add(5*time.Minute)); err != nil {
		r.t.Fatal(err)
	}
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	canon, _ := devicelogin.CanonicalCode(code)
	h := sha256.Sum256(devicelogin.PairMessage("127.0.0.1", canon))
	sig, _ := ecdsa.SignASN1(rand.Reader, k, h[:])
	body, _ := json.Marshal(map[string]string{"code": code, "kind": devicelogin.KindSecureEnclave,
		"publicKey": base64.StdEncoding.EncodeToString(der), "signature": base64.StdEncoding.EncodeToString(sig)})
	status, out := r.do("POST", "/v1/auth/pair", string(body), "")
	tok, _ := out["token"].(string)
	if status != 200 || tok == "" {
		r.t.Fatalf("pair: %d %v", status, out)
	}
	return tok
}

// routes is every route the API serves, and whether it is open without a
// session. Bodies are valid enough to reach the handler.
var routes = []struct {
	method, path, body string
	open               bool
}{
	{"GET", "/v1/health", "", true},
	{"GET", "/v1/openapi.yaml", "", true},
	{"GET", "/app/", "", true},
	{"GET", "/app/app.js", "", true},
	{"POST", "/v1/auth/pair", "{}", true},
	{"POST", "/v1/auth/challenge", "{}", true},
	{"POST", "/v1/auth/login", "{}", true},
	{"POST", "/v1/auth/logout", "{}", false},
	{"GET", "/v1/sessions", "", false},
	{"GET", "/v1/sessions/s1", "", false},
	{"POST", "/v1/sessions", `{"cwd":"/w/app","text":"hi"}`, false},
	{"GET", "/v1/search?q=x", "", false},
	{"GET", "/v1/projects", "", false},
	{"POST", "/v1/sessions/s1/messages", `{"text":"hi"}`, false},
	{"POST", "/v1/sessions/s1/terminal/messages", `{"text":"hi"}`, false},
	{"POST", "/v1/sessions/s1/terminal/keys", `{"promptId":"p","option":"1"}`, false},
	{"GET", "/v1/sessions/s1/events?wait=0", "", false},
	{"POST", "/v1/sessions/s1/cancel", "", false},
	{"POST", "/v1/approvals/a1", `{"option":"allow"}`, false},
	{"GET", "/v1/sessions/s1/queue", "", false},
	{"POST", "/v1/sessions/s1/queue", `{"text":"hi"}`, false},
	{"PATCH", "/v1/sessions/s1/queue/i1", `{"position":0}`, false},
	{"DELETE", "/v1/sessions/s1/queue/i1", "", false},
	{"POST", "/v1/sessions/s1/queue/resume", "", false},
	{"PATCH", "/v1/sessions/s1/meta", `{"pinned":true}`, false},
	{"POST", "/v1/sessions/s1/describe", "", false},
	{"POST", "/v1/devices", `{"token":"t","environment":"sandbox"}`, false},
	{"DELETE", "/v1/devices/t", "", false},
	{"GET", "/v1/unknown", "", false},
}

func TestDeviceLogin_RouteTable(t *testing.T) {
	r := newLoginAPI(t, &fakeBackend{})
	for _, rt := range routes {
		status, body := r.do(rt.method, rt.path, rt.body, "")
		required := status == 401 && body["code"] == "login_required"
		if required == rt.open {
			t.Errorf("%s %s without a session: %d %v; open=%v", rt.method, rt.path, status, body, rt.open)
		}
	}
	tok := r.pair("iphone")
	for _, rt := range routes {
		if rt.path == "/v1/auth/logout" {
			continue
		}
		if status, body := r.do(rt.method, rt.path, rt.body, tok); body["code"] == "login_required" {
			t.Errorf("%s %s with a session: %d %v", rt.method, rt.path, status, body)
		}
	}
}

func TestDeviceLogin_HealthCapability(t *testing.T) {
	r := newLoginAPI(t, &fakeBackend{})
	_, body := r.do("GET", "/v1/health", "", "")
	caps, _ := body["capabilities"].([]any)
	if !slices.Contains(caps, any(CapDeviceLogin)) {
		t.Fatalf("capabilities %v lack %s", caps, CapDeviceLogin)
	}
}

// With device_login false (DeviceLogin nil) the API is as before: no
// capability, no /v1/auth, every route behind the tailnet check only.
func TestDeviceLogin_OffIsTodaysBehaviour(t *testing.T) {
	srv, _ := newTestAPI(t, &fakeBackend{})
	resp, body := do(t, srv, "GET", "/v1/health", "")
	if caps, _ := body["capabilities"].([]any); resp.StatusCode != 200 || slices.Contains(caps, any(CapDeviceLogin)) {
		t.Fatalf("health: %d %v", resp.StatusCode, body)
	}
	if resp, _ := do(t, srv, "POST", "/v1/auth/pair", "{}"); resp.StatusCode != 404 {
		t.Fatalf("/v1/auth/pair with device login off: %d, want 404", resp.StatusCode)
	}
	if resp, _ := do(t, srv, "GET", "/v1/sessions", ""); resp.StatusCode != 200 {
		t.Fatalf("sessions without a session: %d, want 200", resp.StatusCode)
	}
}

// openStream starts an events stream and returns a channel closed when the
// server ends it.
func (r *loginRig) openStream(tok string) <-chan struct{} {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r.t.Cleanup(cancel) // runs before srv.Close, which waits for open requests
	resp, err := http.DefaultClient.Do(r.req("GET", "/v1/sessions/s1/events", "", tok).WithContext(ctx))
	if err != nil {
		r.t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		r.t.Fatalf("stream: %d", resp.StatusCode)
	}
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		defer func() { _ = resp.Body.Close() }()
		buf := make([]byte, 512)
		for {
			if _, err := resp.Body.Read(buf); err != nil {
				return
			}
		}
	}()
	return ended
}

func TestDeviceLogin_StreamEndsOnLogout(t *testing.T) {
	r := newLoginAPI(t, &fakeBackend{})
	tok := r.pair("iphone")
	ended := r.openStream(tok)
	if status, _ := r.do("POST", "/v1/auth/logout", "{}", tok); status != 200 {
		t.Fatalf("logout: %d", status)
	}
	select {
	case <-ended:
	case <-time.After(3 * time.Second):
		t.Fatal("the stream outlived its logout")
	}
}

func TestDeviceLogin_StreamEndsOnRevoke(t *testing.T) {
	r := newLoginAPI(t, &fakeBackend{})
	tok := r.pair("iphone")
	ended := r.openStream(tok)
	if _, err := devicelogin.Open(r.dir).Remove("iphone"); err != nil { // the CLI
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second) // Store.Changed looks once a second
	for {
		r.dl.Tick()
		select {
		case <-ended:
			return
		case <-deadline:
			t.Fatal("the stream outlived its device")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func TestDeviceLogin_AuditActionsCarryNoText(t *testing.T) {
	var b *Broker
	f := &fakeBackend{}
	f.sendF = func(ctx context.Context, s, text string, on func(acprun.Event)) (string, error) {
		_, _ = b.Approver()(ctx, permissionRequest())
		return "end_turn", nil
	}
	r := newLoginAPI(t, f)
	b = r.b
	tok := r.pair("iphone")
	const secretText = "the launch codes are 0000"
	if status, _ := r.do("POST", "/v1/sessions/s1/messages", `{"text":"`+secretText+`"}`, tok); status != 202 {
		t.Fatalf("send: %d", status)
	}
	evs := waitFor(t, b, "s1", KindApproval)
	var ap ApprovalData
	_ = json.Unmarshal(evs[len(evs)-1].Data, &ap)
	r.do("POST", "/v1/approvals/"+ap.ApprovalID, `{"option":"allow"}`, tok)
	waitFor(t, b, "s1", KindTurnDone)
	r.do("POST", "/v1/sessions", `{"cwd":"/w/app","text":"`+secretText+`"}`, tok)
	r.do("POST", "/v1/sessions/s1/cancel", "", tok)
	r.do("POST", "/v1/sessions/s1/terminal/messages", `{"text":"`+secretText+`"}`, tok)
	r.do("POST", "/v1/sessions/s1/terminal/keys", `{"promptId":"p1","option":"1"}`, tok)
	_, item := r.do("POST", "/v1/sessions/s1/queue", `{"text":"`+secretText+`"}`, tok)
	id, _ := item["id"].(string)
	r.do("PATCH", "/v1/sessions/s1/queue/"+id, `{"text":"`+secretText+` again"}`, tok)
	r.do("PATCH", "/v1/sessions/s1/queue/"+id, `{"position":0}`, tok)
	r.do("PATCH", "/v1/sessions/s1/queue/"+id, `{"text":"!`+secretText+`"}`, tok)
	r.do("DELETE", "/v1/sessions/s1/queue/"+id, "", tok)
	r.do("POST", "/v1/sessions/s1/queue/resume", "", tok)
	r.do("GET", "/v1/sessions/s1/queue", "", tok)
	r.do("PATCH", "/v1/sessions/s1/meta", `{"title":"`+secretText+`","description":"`+secretText+`","pinned":true}`, tok)
	r.do("PATCH", "/v1/sessions/s1/meta", `{"pinned":false}`, tok)
	r.do("PATCH", "/v1/sessions/s1/meta", `{"title":"`+secretText+`\nmore"}`, tok)
	r.do("POST", "/v1/sessions/s1/describe", "", tok)

	log := r.audit.String()
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		var e devicelogin.AuditEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		if e.Login != testLogin || (e.Action != devicelogin.ActionPair && e.Device != "iphone") {
			t.Errorf("line lacks login or device: %s", line)
		}
		got = append(got, e.Action+"/"+e.Session+"/"+e.Result)
	}
	want := []string{"pair//ok", "send/s1/ok", "approve//ok", "new_session//ok", "cancel/s1/http_409", "terminal_message/s1/ok", "terminal_key/s1/ok",
		"queue_add/s1/ok", "queue_edit/s1/ok", "queue_move/s1/ok", "queue_edit/s1/http_409", "queue_delete/s1/ok", "queue_resume/s1/ok",
		"meta_edit/s1/ok", "pin/s1/ok", "unpin/s1/ok", "meta_edit/s1/http_400", "describe/s1/ok"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("audit %v\nwant  %v", got, want)
	}
	for _, secret := range []string{secretText, "launch", tok, ap.ApprovalID} {
		if strings.Contains(log, secret) {
			t.Errorf("audit log holds %q:\n%s", secret, log)
		}
	}
}

// testClock is a settable time for the session limits.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *testClock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func TestDeviceLogin_StreamEndsOnIdleExpiry(t *testing.T) {
	r := newLoginAPI(t, &fakeBackend{})
	tok := r.pair("iphone")
	ended := r.openStream(tok)
	r.clock.add(59 * time.Minute)
	r.dl.Tick()
	select {
	case <-ended:
		t.Fatal("the stream ended before the idle limit")
	case <-time.After(100 * time.Millisecond):
	}
	r.clock.add(time.Minute)
	r.dl.Tick()
	select {
	case <-ended:
	case <-time.After(3 * time.Second):
		t.Fatal("the stream outlived its idle login")
	}
}

func TestDeviceLogin_HealthSessionState(t *testing.T) {
	r := newLoginAPI(t, &fakeBackend{})
	if _, body := r.do("GET", "/v1/health", "", ""); body["session"] != "none" {
		t.Fatalf("health without a session: session=%v", body["session"])
	}
	tok := r.pair("iphone")
	if _, body := r.do("GET", "/v1/health", "", tok); body["session"] != "live" {
		t.Fatalf("health with a session: session=%v", body["session"])
	}
	srv, _ := newTestAPI(t, &fakeBackend{})
	if _, body := do(t, srv, "GET", "/v1/health", ""); body["session"] != nil {
		t.Fatalf("device login off: session=%v, want the field left out", body["session"])
	}
}

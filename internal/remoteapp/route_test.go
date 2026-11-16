package remoteapp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/cost"
	"github.com/zanetworker/aimux/internal/decide"
	"github.com/zanetworker/aimux/internal/keyring"
	"github.com/zanetworker/aimux/internal/llm"
)

// The mode router is the built-in mode-router decider when there is no
// deciders.yaml, logging to ~/.aimux/logs/decisions.jsonl. Building it
// reads no key and calls nothing.
func TestBuildModeRouter_DefaultWithoutFile(t *testing.T) {
	home := t.TempDir()
	var errOut bytes.Buffer
	d := buildModeRouter(home, "", &errOut)
	if d == nil || d.Name != "mode-router" || len(d.Backends) == 0 || d.Threshold != 0.25 {
		t.Fatalf("decider %+v (stderr %q)", d, errOut.String())
	}
	if d.Log == nil || d.Log.Path != filepath.Join(home, ".aimux", "logs", "decisions.jsonl") {
		t.Fatalf("log %+v", d.Log)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr %q", errOut.String())
	}
}

// fakeLLM is a model backend for chat deciders.
type fakeLLM struct {
	text  string
	calls int
}

func (f *fakeLLM) Complete(context.Context, llm.Request) (llm.Reply, error) {
	f.calls++
	return llm.Reply{Text: f.text, Model: "claude-haiku"}, nil
}

// A deciders.yaml with an openrouter backend and a chat fallback builds
// (the chat backend gets the model client), and when openrouter cannot
// answer the chat backend decides.
func TestBuildModeRouter_ChatFallbackBuildsAndAnswers(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".config", "aimux", "deciders.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "mode-router:\n  backends:\n    - kind: openrouter\n      model: cloudflare/clef-flash\n    - kind: chat\n      model: haiku\n"
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	d := buildModeRouter(home, "", &errOut)
	if d == nil || len(d.Backends) != 2 {
		t.Fatalf("decider %+v, stderr %q: want openrouter then chat", d, errOut.String())
	}
	or, ok := d.Backends[0].(*decide.OpenRouter)
	if !ok {
		t.Fatalf("backend 0 is %T", d.Backends[0])
	}
	chat, ok := d.Backends[1].(*decide.Chat)
	if !ok || chat.Client == nil {
		t.Fatalf("backend 1 is %T %+v: want a chat backend with a model client", d.Backends[1], d.Backends[1])
	}
	or.Key = func(context.Context) (string, error) { return "", keyring.ErrNotFound }
	fake := &fakeLLM{text: `{"choice":"web","confidence":0.9}`}
	chat.Client.Primary, chat.Client.Fallback = fake, nil
	d.Log, d.Usage = nil, func(cost.Usage) error { return nil }
	a := d.Ask(context.Background(), decide.Request{State: map[string]string{"q": "x"},
		Question: decide.Question{Name: "mode", Instructions: "pick", Options: map[string]string{"page": "p", "web": "w"}}})
	if a.Backend != "chat" || a.Choice != "web" || !a.Decided || fake.calls != 1 {
		t.Fatalf("answer %+v, chat calls %d: want the chat backend's decided web", a, fake.calls)
	}
}

// Serve warms the mode router once at start, in the background (the key
// read and the connection), so the first Auto question does not pay for
// them.
func TestServe_WarmsModeRouterOnceAtStart(t *testing.T) {
	var out, errOut syncBuffer
	off := false
	cfg := config.Config{}
	cfg.RemoteAccess.DeviceLogin = &off
	s, _ := testServer(t, cfg, &out, &errOut)
	s.cfg.RemoteAccess.Hostnames = nil
	s.opts.Allowed, s.opts.LocalOnly = nil, true
	s.opts.Scopes = socketScopes(shortDir(t))
	var mu sync.Mutex
	var warmed []*decide.Decider
	s.warmRouter = func(_ context.Context, d *decide.Decider) {
		mu.Lock()
		warmed = append(warmed, d)
		mu.Unlock()
	}
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(warmed) }
	if err := runUntil(t, s, func() bool { return count() > 0 }); err != nil {
		t.Fatalf("Run: %v (stderr %q)", err, errOut.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(warmed) != 1 || warmed[0] == nil || warmed[0].Name != "mode-router" {
		t.Fatalf("warmed %v: want the mode router once", warmed)
	}
}

// warmBackend counts its warm-ups; it never decides.
type warmBackend struct{ warms int }

func (b *warmBackend) Warm(context.Context) error { b.warms++; return nil }
func (*warmBackend) Decide(context.Context, decide.Request) (decide.Answer, error) {
	return decide.Answer{}, decide.ErrCall
}

// The default warm-up warms each backend once and logs how long it took.
func TestWarmModeRouter_WarmsOnceAndLogs(t *testing.T) {
	b := &warmBackend{}
	var errOut bytes.Buffer
	warmModeRouter(context.Background(), &decide.Decider{Name: "mode-router", Backends: []decide.Backend{b}}, &errOut)
	if b.warms != 1 || !strings.Contains(errOut.String(), "mode router warm-up took") {
		t.Fatalf("%d warm-ups, log %q", b.warms, errOut.String())
	}
}

// A bad deciders.yaml is reported and leaves Auto off: serve still starts
// and /v1/route answers 200 undecided.
func TestServe_BadDecidersFileStillServesRoute(t *testing.T) {
	var out, errOut syncBuffer
	off := false
	cfg := config.Config{}
	cfg.RemoteAccess.DeviceLogin = &off
	s, home := testServer(t, cfg, &out, &errOut)
	s.cfg.RemoteAccess.Hostnames = nil
	s.opts.Allowed, s.opts.LocalOnly = nil, true
	s.opts.Scopes = socketScopes(shortDir(t))
	path := filepath.Join(home, ".config", "aimux", "deciders.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("mode-router:\n  backendz: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sock := s.opts.Scopes.Clients["wingman"].Socket
	var status int
	var body map[string]any
	err := runUntil(t, s, func() bool {
		resp, err := unixClient(sock).Post("http://aimux.local/v1/route", "application/json",
			strings.NewReader(`{"text":"what is this page about?","facts":{"pageReadable":true,"usePage":true}}`))
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		status = resp.StatusCode
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return true
	})
	if err != nil {
		t.Fatalf("Run: %v (stderr %q)", err, errOut.String())
	}
	if status != http.StatusOK || body["decided"] != false || body["source"] != "unavailable" {
		t.Fatalf("route: %d %v", status, body)
	}
	if !strings.Contains(errOut.String(), "deciders.yaml") || !strings.Contains(errOut.String(), "Auto") {
		t.Fatalf("stderr %q: want the deciders.yaml problem", errOut.String())
	}
}

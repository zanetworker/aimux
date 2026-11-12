package remoteapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/config"
)

// fakeServeOnSocket answers GET /v1/health on the wingman socket of s as a
// running serve of the given kind would.
func fakeServeOnSocket(t *testing.T, s *Server, localOnly bool) {
	t.Helper()
	ln, err := listenSocket(s.opts.Scopes.Clients["wingman"].Socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "localOnly": localOnly})
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// A full serve that finds a local-only one (started by Wingman) on the
// socket must not report success: phone and web would be offline. It exits
// with an error naming the pid and how to stop it.
func TestServe_FullServeRefusesLocalOnlyOwner(t *testing.T) {
	var out, errOut syncBuffer
	s, _ := testServer(t, config.Config{}, &out, &errOut)
	s.opts.Scopes = socketScopes(shortDir(t))
	fakeServeOnSocket(t, s, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := s.Run(ctx)
	pid := os.Getpid()
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("kill %d", pid)) || !strings.Contains(err.Error(), "local-only") {
		t.Fatalf("Run = %v, want an error naming the local-only serve and `kill %d`", err, pid)
	}
	if strings.Contains(out.String(), "already running") {
		t.Fatalf("stdout %q claims success", out.String())
	}
}

// Same kinds keep today's answer: already running, exit 0.
func TestServe_SameKindSecondInstanceExitsZero(t *testing.T) {
	for _, localOnly := range []bool{true, false} {
		var out, errOut syncBuffer
		off := false
		cfg := config.Config{}
		cfg.RemoteAccess.DeviceLogin = &off
		s, _ := testServer(t, cfg, &out, &errOut)
		s.opts.LocalOnly, s.opts.UnsafeNoDeviceLogin = localOnly, true
		s.opts.Scopes = socketScopes(shortDir(t))
		fakeServeOnSocket(t, s, localOnly)
		if err := s.Run(context.Background()); err != nil {
			t.Fatalf("localOnly=%v: %v", localOnly, err)
		}
		if want := fmt.Sprintf("already running (pid %d)\n", os.Getpid()); out.String() != want {
			t.Fatalf("localOnly=%v: stdout %q, want %q", localOnly, out.String(), want)
		}
	}
}

// The idle watch counts a local-only serve idle after the timeout with no
// request (none in flight) and no running profile session.
func TestIdleWatch_IdleOnlyWithNoRequestsAndNothingRunning(t *testing.T) {
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	busy := false
	iw := newIdleWatch(30*time.Minute, func() time.Time { mu.Lock(); defer mu.Unlock(); return now },
		func() bool { mu.Lock(); defer mu.Unlock(); return busy })
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	if iw.idle() {
		t.Fatal("idle at start")
	}
	advance(29 * time.Minute)
	if iw.idle() {
		t.Fatal("idle before 30 minutes")
	}
	advance(2 * time.Minute)
	if !iw.idle() {
		t.Fatal("not idle after 31 quiet minutes")
	}
	mu.Lock()
	busy = true
	mu.Unlock()
	if iw.idle() {
		t.Fatal("idle with a profile session running")
	}
	mu.Lock()
	busy = false
	mu.Unlock()

	release := make(chan struct{})
	h := iw.wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	ended := make(chan struct{})
	go func() { h.ServeHTTP(nopWriter{}, &http.Request{}); close(ended) }()
	deadline := time.Now().Add(2 * time.Second)
	for iw.idle() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	advance(40 * time.Minute)
	if iw.idle() {
		t.Fatal("idle with a request in flight (an open event stream)")
	}
	close(release)
	<-ended
	if iw.idle() {
		t.Fatal("idle right after a request ended")
	}
	advance(31 * time.Minute)
	if !iw.idle() {
		t.Fatal("not idle 31 minutes after the last request")
	}
}

type nopWriter struct{}

func (nopWriter) Header() http.Header         { return http.Header{} }
func (nopWriter) Write(b []byte) (int, error) { return len(b), nil }
func (nopWriter) WriteHeader(int)             {}

// A local-only serve exits on its own once idle, removing its socket.
func TestServe_LocalOnlyExitsWhenIdle(t *testing.T) {
	var out, errOut syncBuffer
	off := false
	cfg := config.Config{}
	cfg.RemoteAccess.DeviceLogin = &off
	s, _ := testServer(t, cfg, &out, &errOut)
	s.cfg.RemoteAccess.Hostnames = nil
	s.opts.Allowed, s.opts.LocalOnly = nil, true
	s.opts.Scopes = socketScopes(shortDir(t))
	s.idleAfter, s.idleEvery = 50*time.Millisecond, 10*time.Millisecond
	sock := s.opts.Scopes.Clients["wingman"].Socket
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("a local-only serve with nothing to do never exited")
	}
	if _, err := os.Lstat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket left behind: %v", err)
	}
	if !strings.Contains(out.String(), "idle") {
		t.Fatalf("stdout %q: want a line saying it exits when idle", out.String())
	}
}

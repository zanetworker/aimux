package mcpproxy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

type fakeBackend struct {
	id      int
	callFn  func(ctx context.Context) (*mcp.CallToolResult, error)
	closed  atomic.Bool
	mu      sync.Mutex
	lastReq mcp.CallToolRequest
}

func (f *fakeBackend) ListToolsRaw(ctx context.Context) ([]json.RawMessage, error) { return nil, nil }
func (f *fakeBackend) CallTool(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	f.mu.Lock()
	f.lastReq = req
	f.mu.Unlock()
	if f.callFn != nil {
		return f.callFn(ctx)
	}
	return mcp.NewToolResultText("ok"), nil
}
func (f *fakeBackend) Close() error { f.closed.Store(true); return nil }

type fakeStarter struct {
	mu       sync.Mutex
	starts   map[string]int
	backends []*fakeBackend
	fail     map[string]error
	callFn   func(ctx context.Context) (*mcp.CallToolResult, error)
	delay    time.Duration
}

func newFakeStarter() *fakeStarter {
	return &fakeStarter{starts: map[string]int{}, fail: map[string]error{}}
}

func (s *fakeStarter) start(ctx context.Context, server string) (Backend, error) {
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.starts[server]++
	if err := s.fail[server]; err != nil {
		return nil, err
	}
	b := &fakeBackend{id: len(s.backends), callFn: s.callFn}
	s.backends = append(s.backends, b)
	return b, nil
}

func (s *fakeStarter) count(server string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts[server]
}

func req(tool string) mcp.CallToolRequest {
	r := mcp.CallToolRequest{}
	r.Params.Name = tool
	return r
}

func resultText(t *testing.T, r *mcp.CallToolResult) string {
	t.Helper()
	if r == nil || len(r.Content) == 0 {
		t.Fatalf("empty result: %+v", r)
	}
	tc, ok := r.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("first content is %T, want text", r.Content[0])
	}
	return tc.Text
}

func TestPool_StartsLazilyAndReuses(t *testing.T) {
	fs := newFakeStarter()
	p := NewPool(fs.start, PoolOptions{})
	defer p.Close()
	if fs.count("slack") != 0 {
		t.Fatal("backend started before any call")
	}
	for i := 0; i < 3; i++ {
		r := p.Call(context.Background(), "slack", "", req("post"))
		if r.IsError {
			t.Fatalf("call %d failed: %s", i, resultText(t, r))
		}
	}
	if got := fs.count("slack"); got != 1 {
		t.Fatalf("started %d times, want 1", got)
	}
	if fs.backends[0].lastReq.Params.Name != "post" {
		t.Fatalf("forwarded tool name %q, want post", fs.backends[0].lastReq.Params.Name)
	}
}

func TestPool_ConcurrentFirstCallsStartOnce(t *testing.T) {
	fs := newFakeStarter()
	fs.delay = 20 * time.Millisecond
	p := NewPool(fs.start, PoolOptions{})
	defer p.Close()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); p.Call(context.Background(), "github", "", req("search")) }()
	}
	wg.Wait()
	if got := fs.count("github"); got != 1 {
		t.Fatalf("started %d times under concurrency, want 1", got)
	}
}

func TestPool_StartFailureIsIsolatedAndRetried(t *testing.T) {
	fs := newFakeStarter()
	fs.fail["broken"] = errors.New("exec: no such file")
	p := NewPool(fs.start, PoolOptions{})
	defer p.Close()

	r := p.Call(context.Background(), "broken", "", req("x"))
	if !r.IsError || !strings.Contains(resultText(t, r), "broken") || !strings.Contains(resultText(t, r), "no such file") {
		t.Fatalf("start failure result = %+v, want an error naming the server and cause", r)
	}
	if ok := p.Call(context.Background(), "slack", "", req("post")); ok.IsError {
		t.Fatal("a failing backend broke an unrelated one")
	}
	delete(fs.fail, "broken")
	if r := p.Call(context.Background(), "broken", "", req("x")); r.IsError {
		t.Fatalf("retry after the failure cleared still failed: %s", resultText(t, r))
	}
	if got := fs.count("broken"); got != 2 {
		t.Fatalf("broken started %d times, want 2 (failure not cached)", got)
	}
}

func TestPool_TimeoutClosesBackendAndNextCallRestarts(t *testing.T) {
	fs := newFakeStarter()
	fs.callFn = func(ctx context.Context) (*mcp.CallToolResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	p := NewPool(fs.start, PoolOptions{CallTimeout: 30 * time.Millisecond})
	defer p.Close()

	r := p.Call(context.Background(), "hung", "", req("x"))
	if !r.IsError || !strings.Contains(resultText(t, r), "timed out") {
		t.Fatalf("got %+v, want a timed out error result", r)
	}
	if !fs.backends[0].closed.Load() {
		t.Fatal("hung backend was not closed")
	}
	fs.callFn = nil
	fs.backends[0].callFn = nil
	if r := p.Call(context.Background(), "hung", "", req("x")); r.IsError {
		t.Fatalf("call after restart failed: %s", resultText(t, r))
	}
	if got := fs.count("hung"); got != 2 {
		t.Fatalf("started %d times, want 2", got)
	}
}

func TestPool_TransportErrorDropsBackend(t *testing.T) {
	fs := newFakeStarter()
	fs.callFn = func(ctx context.Context) (*mcp.CallToolResult, error) { return nil, errors.New("broken pipe") }
	p := NewPool(fs.start, PoolOptions{})
	defer p.Close()
	r := p.Call(context.Background(), "s", "", req("x"))
	if !r.IsError || !strings.Contains(resultText(t, r), "broken pipe") {
		t.Fatalf("got %+v", r)
	}
	if !fs.backends[0].closed.Load() {
		t.Fatal("backend not closed after transport error")
	}
}

func TestPool_ToolErrorResultKeepsBackend(t *testing.T) {
	fs := newFakeStarter()
	fs.callFn = func(ctx context.Context) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultError("bad input"), nil
	}
	p := NewPool(fs.start, PoolOptions{})
	defer p.Close()
	p.Call(context.Background(), "s", "", req("x"))
	p.Call(context.Background(), "s", "", req("x"))
	if got := fs.count("s"); got != 1 {
		t.Fatalf("a tool-level error restarted the backend (%d starts)", got)
	}
}

func TestPool_ReapClosesOnlyIdleBackends(t *testing.T) {
	fs := newFakeStarter()
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }

	release := make(chan struct{})
	p := NewPool(fs.start, PoolOptions{Now: clock})
	defer p.Close()

	// backends[0] "busy": started long ago, but a call is still in flight.
	fs.callFn = func(ctx context.Context) (*mcp.CallToolResult, error) {
		<-release
		return mcp.NewToolResultText("ok"), nil
	}
	done := make(chan struct{})
	go func() { p.Call(context.Background(), "busy", "", req("x")); close(done) }()
	for fs.count("busy") == 0 {
		time.Sleep(time.Millisecond)
	}
	fs.callFn = nil
	// backends[1] "old": used once long ago.
	p.Call(context.Background(), "old", "", req("x"))
	advance(10 * time.Minute)
	// backends[2] "fresh": used just now.
	p.Call(context.Background(), "fresh", "", req("x"))

	if n := p.Reap(5 * time.Minute); n != 1 {
		t.Fatalf("reaped %d, want 1 (only old)", n)
	}
	busy, old, fresh := fs.backends[0].closed.Load(), fs.backends[1].closed.Load(), fs.backends[2].closed.Load()
	if busy || !old || fresh {
		t.Fatalf("closed busy=%v old=%v fresh=%v; want only old closed", busy, old, fresh)
	}
	close(release)
	<-done
}

func TestPool_PerSessionServersAreNotShared(t *testing.T) {
	fs := newFakeStarter()
	p := NewPool(fs.start, PoolOptions{PerSession: map[string]bool{"playwright": true}})
	defer p.Close()

	p.Call(context.Background(), "playwright", "session-a", req("click"))
	p.Call(context.Background(), "playwright", "session-b", req("click"))
	p.Call(context.Background(), "playwright", "session-a", req("click"))
	p.Call(context.Background(), "slack", "session-a", req("post"))
	p.Call(context.Background(), "slack", "session-b", req("post"))
	if got := fs.count("playwright"); got != 2 {
		t.Fatalf("playwright started %d times, want one per session (2)", got)
	}
	if got := fs.count("slack"); got != 1 {
		t.Fatalf("slack started %d times, want 1 shared", got)
	}

	p.CloseSession("session-a")
	if !fs.backends[0].closed.Load() {
		t.Fatal("session-a's playwright backend not closed")
	}
	if fs.backends[1].closed.Load() {
		t.Fatal("session-b's playwright backend closed by another session")
	}
	if fs.backends[2].closed.Load() {
		t.Fatal("shared slack backend closed when one session ended")
	}
}

func TestPool_CloseClosesEverything(t *testing.T) {
	fs := newFakeStarter()
	p := NewPool(fs.start, PoolOptions{})
	p.Call(context.Background(), "a", "", req("x"))
	p.Call(context.Background(), "b", "", req("x"))
	p.Close()
	for i, b := range fs.backends {
		if !b.closed.Load() {
			t.Fatalf("backend %d not closed by Close", i)
		}
	}
	if r := p.Call(context.Background(), "a", "", req("x")); !r.IsError {
		t.Fatal("call after Close succeeded")
	}
}

func TestPool_CloseSessionWhileStartingClosesBackend(t *testing.T) {
	fs := newFakeStarter()
	fs.delay = 100 * time.Millisecond
	p := NewPool(fs.start, PoolOptions{PerSession: map[string]bool{"playwright": true}})
	defer p.Close()

	done := make(chan *mcp.CallToolResult)
	go func() { done <- p.Call(context.Background(), "playwright", "s1", req("click")) }()
	time.Sleep(20 * time.Millisecond) // start is in progress
	p.CloseSession("s1")
	r := <-done

	if !r.IsError {
		t.Fatal("call on a session closed during start reported success")
	}
	if len(fs.backends) != 1 || !fs.backends[0].closed.Load() {
		t.Fatal("backend that finished starting after its session closed was left running")
	}
	if p.Reap(0) != 0 {
		t.Fatal("closed session's backend is still tracked by the pool")
	}
}

func TestPool_StartThatNeverFinishesTimesOut(t *testing.T) {
	var starts atomic.Int32
	hang := func(ctx context.Context, server string) (Backend, error) {
		starts.Add(1)
		<-ctx.Done() // a server that never answers initialize
		return nil, ctx.Err()
	}
	p := NewPool(hang, PoolOptions{StartTimeout: 50 * time.Millisecond})
	defer p.Close()
	done := make(chan *mcp.CallToolResult)
	go func() { done <- p.Call(context.Background(), "stuck", "", req("x")) }()
	select {
	case r := <-done:
		if !r.IsError || !strings.Contains(resultText(t, r), "stuck") {
			t.Fatalf("got %+v, want an error result naming the server", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a backend that never finishes starting held the call forever")
	}
	p.Call(context.Background(), "stuck", "", req("x"))
	if starts.Load() != 2 {
		t.Fatalf("started %d times, want a retry after the timeout", starts.Load())
	}
}

// A client session can expire while one of its calls is still running (a
// long tool call with a short --idle). The backend must finish that call.
func TestPool_CloseSessionWaitsForInFlightCall(t *testing.T) {
	release := make(chan struct{})
	fs := newFakeStarter()
	fs.callFn = func(ctx context.Context) (*mcp.CallToolResult, error) {
		<-release
		return mcp.NewToolResultText("finished"), nil
	}
	p := NewPool(fs.start, PoolOptions{PerSession: map[string]bool{"playwright": true}})
	defer p.Close()
	done := make(chan *mcp.CallToolResult)
	go func() { done <- p.Call(context.Background(), "playwright", "s1", req("long")) }()
	for fs.count("playwright") == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(10 * time.Millisecond)

	p.CloseSession("s1") // the session expired mid-call
	if fs.backends[0].closed.Load() {
		t.Fatal("backend closed under a running call")
	}
	close(release)
	if r := <-done; r.IsError || resultText(t, r) != "finished" {
		t.Fatalf("in-flight call got %+v, want it to finish", r)
	}
	deadline := time.Now().Add(time.Second)
	for !fs.backends[0].closed.Load() {
		if time.Now().After(deadline) {
			t.Fatal("backend of the closed session was never closed after its call finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Two clients share a backend. One cancels its request; the other's call and
// the backend itself must be unaffected.
func TestPool_CallerCancelKeepsSharedBackend(t *testing.T) {
	release := make(chan struct{})
	fs := newFakeStarter()
	fs.callFn = func(ctx context.Context) (*mcp.CallToolResult, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return mcp.NewToolResultText("done"), nil
		}
	}
	p := NewPool(fs.start, PoolOptions{})
	defer p.Close()
	ctxA, cancelA := context.WithCancel(context.Background())
	resA, resB := make(chan *mcp.CallToolResult), make(chan *mcp.CallToolResult)
	go func() { resA <- p.Call(ctxA, "slack", "", req("x")) }()
	go func() { resB <- p.Call(context.Background(), "slack", "", req("x")) }()
	for fs.count("slack") == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	cancelA()
	if r := <-resA; !r.IsError {
		t.Fatal("cancelled call reported success")
	}
	if fs.backends[0].closed.Load() {
		t.Fatal("one caller's cancel closed the shared backend")
	}
	close(release)
	if r := <-resB; r.IsError {
		t.Fatalf("the other client's call was aborted: %s", resultText(t, r))
	}
	if fs.count("slack") != 1 {
		t.Fatal("backend was restarted")
	}
}

type slowCloseBackend struct {
	fakeBackend
	closing chan struct{}
}

func (s *slowCloseBackend) Close() error { <-s.closing; return s.fakeBackend.Close() }

// Closing an abandoned backend can take seconds (graceful shutdown, then
// kill). Other clients must not wait for it.
func TestPool_SlowBackendCloseDoesNotBlockOthers(t *testing.T) {
	closing := make(chan struct{})
	defer close(closing)
	startRelease := make(chan struct{})
	start := func(ctx context.Context, server string) (Backend, error) {
		if server == "browser" {
			<-startRelease
			return &slowCloseBackend{closing: closing}, nil
		}
		return &fakeBackend{}, nil
	}
	p := NewPool(start, PoolOptions{PerSession: map[string]bool{"browser": true}})
	go func() { p.Call(context.Background(), "browser", "s1", req("x")) }()
	time.Sleep(20 * time.Millisecond)
	p.CloseSession("s1") // the session goes while its backend starts
	close(startRelease)  // the start finishes; the pool must close it, slowly
	time.Sleep(20 * time.Millisecond)
	done := make(chan *mcp.CallToolResult)
	go func() { done <- p.Call(context.Background(), "slack", "", req("post")) }()
	select {
	case r := <-done:
		if r.IsError {
			t.Fatalf("unrelated call failed: %s", resultText(t, r))
		}
	case <-time.After(time.Second):
		t.Fatal("an unrelated tool call waited for another backend's shutdown")
	}
}

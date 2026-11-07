package mcpproxy

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// pidBackend is a fake backend that reports a process id.
type pidBackend struct {
	fakeBackend
	pid int
}

func (b *pidBackend) PID() int { return b.pid }

// tailErr is a failed start that carries the server's stderr tail.
type tailErr struct {
	msg  string
	tail []string
}

func (e *tailErr) Error() string        { return e.msg }
func (e *tailErr) StderrTail() []string { return e.tail }

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func statusOf(t *testing.T, p *Pool, server string) ServerStatus {
	t.Helper()
	st := p.Status([]string{server})
	if len(st) != 1 || st[0].Server != server {
		t.Fatalf("Status(%q) = %+v, want one entry for it", server, st)
	}
	return st[0]
}

func TestStatus_LifeCycle(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_000_000, 0)}
	gate := make(chan struct{})
	entered := make(chan struct{})
	start := func(ctx context.Context, server string) (Backend, error) {
		close(entered)
		<-gate
		return &pidBackend{pid: 4242}, nil
	}
	p := NewPool(start, PoolOptions{Now: clock.Now})
	defer p.Close()

	if s := statusOf(t, p, "slack"); s.State != StateNotStarted || s.Instances != 0 || s.PID != 0 {
		t.Fatalf("before any call: %+v, want not_started", s)
	}
	done := make(chan *mcp.CallToolResult)
	go func() { done <- p.Call(context.Background(), "slack", "", req("post")) }()
	<-entered
	if s := statusOf(t, p, "slack"); s.State != StateStarting {
		t.Fatalf("during start: %+v, want starting", s)
	}
	close(gate)
	if r := <-done; r.IsError {
		t.Fatalf("call failed: %s", resultText(t, r))
	}
	s := statusOf(t, p, "slack")
	if s.State != StateRunning || s.Instances != 1 || s.PID != 4242 || s.Calls != 1 || s.Failures != 0 {
		t.Fatalf("after start: %+v, want running, 1 instance, pid 4242, 1 call", s)
	}
	if !s.StartedAt.Equal(clock.Now()) || !s.LastCall.Equal(clock.Now()) {
		t.Fatalf("StartedAt %v LastCall %v, want both %v", s.StartedAt, s.LastCall, clock.Now())
	}

	clock.Add(10 * time.Minute)
	if n := p.Reap(time.Minute); n != 1 {
		t.Fatalf("reaped %d, want 1", n)
	}
	s = statusOf(t, p, "slack")
	if s.State != StateIdleStopped || s.Instances != 0 || s.PID != 0 {
		t.Fatalf("after reap: %+v, want idle_stopped with no instance", s)
	}
	if s.Calls != 1 || s.StartedAt.IsZero() || s.LastCall.IsZero() {
		t.Fatalf("stats lost on reap: %+v", s)
	}
}

func TestStatus_FailedStartKeepsStderrTail(t *testing.T) {
	fs := newFakeStarter()
	fs.fail["gh"] = &tailErr{
		msg:  "initialize MCP server gh: EOF\nmore detail",
		tail: []string{"l1", "l2", "l3", "l4", "l5", "l6", "l7"},
	}
	p := NewPool(fs.start, PoolOptions{})
	defer p.Close()
	if r := p.Call(context.Background(), "gh", "", req("search")); !r.IsError {
		t.Fatal("call to a server that cannot start succeeded")
	}
	s := statusOf(t, p, "gh")
	if s.State != StateFailed || s.Failures != 1 || s.Calls != 1 {
		t.Fatalf("after failed start: %+v, want failed, 1 call, 1 failure", s)
	}
	if s.LastErrorAt.IsZero() {
		t.Fatalf("LastErrorAt unset after a failed start: %+v", s)
	}
	if s.LastError != "initialize MCP server gh: EOF" {
		t.Fatalf("LastError %q, want the first line only", s.LastError)
	}
	if want := []string{"l3", "l4", "l5", "l6", "l7"}; !reflect.DeepEqual(s.StderrTail, want) {
		t.Fatalf("StderrTail %q, want the last 5 lines %q", s.StderrTail, want)
	}

	// The next start succeeds: running again, the failed start's tail gone.
	fs.mu.Lock()
	delete(fs.fail, "gh")
	fs.mu.Unlock()
	if r := p.Call(context.Background(), "gh", "", req("search")); r.IsError {
		t.Fatalf("retry failed: %s", resultText(t, r))
	}
	s = statusOf(t, p, "gh")
	if s.State != StateRunning || len(s.StderrTail) != 0 || s.Failures != 1 || s.Calls != 2 {
		t.Fatalf("after a good restart: %+v, want running, no tail, 1 failure of 2 calls", s)
	}
}

func TestStatus_CallTimeoutMarksFailedUntilRestart(t *testing.T) {
	fs := newFakeStarter()
	fs.callFn = func(ctx context.Context) (*mcp.CallToolResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	p := NewPool(fs.start, PoolOptions{CallTimeout: 30 * time.Millisecond})
	defer p.Close()
	if r := p.Call(context.Background(), "slow", "", req("run")); !r.IsError {
		t.Fatal("call that should time out succeeded")
	}
	s := statusOf(t, p, "slow")
	if s.State != StateFailed || s.Instances != 0 || s.Failures != 1 {
		t.Fatalf("after timeout: %+v, want failed, no instance, 1 failure", s)
	}
	if !strings.Contains(s.LastError, "timed out") {
		t.Fatalf("LastError %q, want it to say timed out", s.LastError)
	}

	fs.mu.Lock()
	fs.callFn = nil
	fs.mu.Unlock()
	if r := p.Call(context.Background(), "slow", "", req("run")); r.IsError {
		t.Fatalf("call after restart failed: %s", resultText(t, r))
	}
	if s := statusOf(t, p, "slow"); s.State != StateRunning || s.Instances != 1 {
		t.Fatalf("after restart: %+v, want running", s)
	}
	if got := fs.count("slow"); got != 2 {
		t.Fatalf("started %d times, want 2", got)
	}
}

func TestStatus_PerSessionCountsInstances(t *testing.T) {
	var mu sync.Mutex
	pid := 100
	start := func(ctx context.Context, server string) (Backend, error) {
		mu.Lock()
		defer mu.Unlock()
		pid++
		return &pidBackend{pid: pid}, nil
	}
	p := NewPool(start, PoolOptions{PerSession: map[string]bool{"browser": true}})
	defer p.Close()
	for _, session := range []string{"s1", "s2", "s1"} {
		if r := p.Call(context.Background(), "browser", session, req("click")); r.IsError {
			t.Fatalf("call in %s failed: %s", session, resultText(t, r))
		}
	}
	s := statusOf(t, p, "browser")
	if s.State != StateRunning || s.Instances != 2 || s.PID != 0 || s.Calls != 3 {
		t.Fatalf("two sessions: %+v, want running, 2 instances, no single pid, 3 calls", s)
	}
	p.CloseSession("s1")
	s = statusOf(t, p, "browser")
	if s.Instances != 1 || s.PID != 102 {
		t.Fatalf("after closing s1: %+v, want 1 instance with pid 102", s)
	}
}

func TestStatus_CountsCallsAndFailures(t *testing.T) {
	fs := newFakeStarter()
	var mu sync.Mutex
	n := 0
	fs.callFn = func(ctx context.Context) (*mcp.CallToolResult, error) {
		mu.Lock()
		n++
		i := n
		mu.Unlock()
		switch i {
		case 3:
			return mcp.NewToolResultError("tool said no"), nil // tool-level: server is fine
		case 4:
			return nil, &RequestError{Err: errors.New("unknown tool")} // server is fine
		case 5:
			return nil, errors.New("broken pipe\nwith a second line") // transport: server is not
		}
		return mcp.NewToolResultText("ok"), nil
	}
	fs.fail["bad"] = errors.New("exec: not found")
	p := NewPool(fs.start, PoolOptions{})
	defer p.Close()
	for i := 0; i < 5; i++ {
		p.Call(context.Background(), "good", "", req("t"))
	}
	p.Call(context.Background(), "bad", "", req("t"))

	st := p.Status([]string{"zeta", "good", "bad"})
	var names []string
	for _, s := range st {
		names = append(names, s.Server)
	}
	if want := []string{"bad", "good", "zeta"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("servers %q, want sorted %q", names, want)
	}
	good := st[1]
	if good.Calls != 5 || good.Failures != 1 || good.LastError != "broken pipe" || good.State != StateFailed {
		t.Fatalf("good: %+v, want 5 calls, 1 failure, LastError broken pipe, failed", good)
	}
	bad := st[0]
	if bad.Calls != 1 || bad.Failures != 1 || bad.State != StateFailed || bad.LastError != "exec: not found" {
		t.Fatalf("bad: %+v, want 1 call, 1 failure, failed", bad)
	}
}

func TestStatus_CallerCancelIsNotAFailure(t *testing.T) {
	fs := newFakeStarter()
	fs.callFn = func(ctx context.Context) (*mcp.CallToolResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	p := NewPool(fs.start, PoolOptions{})
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	p.Call(ctx, "s", "", req("t"))
	if s := statusOf(t, p, "s"); s.Failures != 0 || s.State != StateRunning || s.Calls != 1 {
		t.Fatalf("after caller cancel: %+v, want running, 1 call, no failure", s)
	}
}

func TestStatus_UnknownServerIsNotStarted(t *testing.T) {
	p := NewPool(newFakeStarter().start, PoolOptions{})
	defer p.Close()
	p.Call(context.Background(), "other", "", req("t"))
	st := p.Status([]string{"never", "never"})
	if len(st) != 1 {
		t.Fatalf("Status = %+v, want one entry per distinct configured server", st)
	}
	want := ServerStatus{Server: "never", State: StateNotStarted}
	if !reflect.DeepEqual(st[0], want) {
		t.Fatalf("Status = %+v, want %+v", st[0], want)
	}
	if got := p.Status(nil); len(got) != 0 {
		t.Fatalf("Status(nil) = %+v, want empty (an unconfigured server is not listed)", got)
	}
}

// exitBackend is a fake backend whose process can exit on its own.
type exitBackend struct {
	fakeBackend
	done chan struct{}
}

func (b *exitBackend) Done() <-chan struct{} { return b.done }

func waitState(t *testing.T, p *Pool, server string, want ServerState) ServerStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		s := statusOf(t, p, server)
		if s.State == want {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %+v, never reached %s", server, s, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStatus_ReasonSaysHowItFailed(t *testing.T) {
	t.Run("start failed", func(t *testing.T) {
		fs := newFakeStarter()
		fs.fail["gh"] = errors.New("exec: not found")
		p := NewPool(fs.start, PoolOptions{})
		defer p.Close()
		p.Call(context.Background(), "gh", "", req("t"))
		if s := statusOf(t, p, "gh"); s.Reason != ReasonStartFailed {
			t.Fatalf("Reason %q, want %q", s.Reason, ReasonStartFailed)
		}
	})
	t.Run("start timed out", func(t *testing.T) {
		start := func(ctx context.Context, server string) (Backend, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		p := NewPool(start, PoolOptions{StartTimeout: 20 * time.Millisecond})
		defer p.Close()
		p.Call(context.Background(), "slow", "", req("t"))
		if s := statusOf(t, p, "slow"); s.Reason != ReasonTimedOut {
			t.Fatalf("Reason %q, want %q", s.Reason, ReasonTimedOut)
		}
	})
	t.Run("call timed out", func(t *testing.T) {
		fs := newFakeStarter()
		fs.callFn = func(ctx context.Context) (*mcp.CallToolResult, error) { <-ctx.Done(); return nil, ctx.Err() }
		p := NewPool(fs.start, PoolOptions{CallTimeout: 20 * time.Millisecond})
		defer p.Close()
		p.Call(context.Background(), "slow", "", req("t"))
		if s := statusOf(t, p, "slow"); s.Reason != ReasonTimedOut {
			t.Fatalf("Reason %q, want %q", s.Reason, ReasonTimedOut)
		}
	})
	t.Run("transport error", func(t *testing.T) {
		fs := newFakeStarter()
		fs.callFn = func(ctx context.Context) (*mcp.CallToolResult, error) { return nil, errors.New("broken pipe") }
		p := NewPool(fs.start, PoolOptions{})
		defer p.Close()
		p.Call(context.Background(), "s", "", req("t"))
		if s := statusOf(t, p, "s"); s.Reason != ReasonTransportError {
			t.Fatalf("Reason %q, want %q", s.Reason, ReasonTransportError)
		}
	})
	t.Run("process exited", func(t *testing.T) {
		b := &exitBackend{done: make(chan struct{})}
		p := NewPool(func(context.Context, string) (Backend, error) { return b, nil }, PoolOptions{})
		defer p.Close()
		p.Call(context.Background(), "s", "", req("t"))
		if s := statusOf(t, p, "s"); s.Reason != ReasonNone {
			t.Fatalf("healthy server has Reason %q, want none", s.Reason)
		}
		close(b.done)
		if s := waitState(t, p, "s", StateFailed); s.Reason != ReasonExited {
			t.Fatalf("Reason %q, want %q", s.Reason, ReasonExited)
		}
	})
}

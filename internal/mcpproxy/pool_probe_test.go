package mcpproxy

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// deafListBackend is a backend that stopped reading stdin: tools/list
// blocks until Close.
type deafListBackend struct {
	lists  atomic.Int32
	closed chan struct{}
	once   sync.Once
}

func newDeafListBackend() *deafListBackend { return &deafListBackend{closed: make(chan struct{})} }

func (b *deafListBackend) ListToolsRaw(context.Context) ([]json.RawMessage, error) {
	b.lists.Add(1)
	<-b.closed
	return nil, errors.New("transport closed")
}
func (b *deafListBackend) CallTool(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultText("ok"), nil
}
func (b *deafListBackend) Close() error { b.once.Do(func() { close(b.closed) }); return nil }

func (b *deafListBackend) isClosed() bool {
	select {
	case <-b.closed:
		return true
	default:
		return false
	}
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *testClock) Add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func TestLease_AbandonedListingIsNotRepeatedAndKeepsLastUsed(t *testing.T) {
	b := newDeafListBackend()
	clock := &testClock{now: time.Unix(1_000_000, 0)}
	p := NewPool(func(context.Context, string) (Backend, error) { return b, nil },
		PoolOptions{CallTimeout: time.Minute, Now: clock.Now})
	defer p.Close()

	l, err := p.Lease(context.Background(), "s")
	must(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := l.ListToolsRaw(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the probe's deadline", err)
	}
	// A second probe does not queue another write behind the stuck one.
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	start := time.Now()
	if _, err := l.ListToolsRaw(ctx2); err == nil || errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("err = %v after %v, want an immediate stuck-listing error", err, time.Since(start))
	}
	if n := b.lists.Load(); n != 1 {
		t.Fatalf("%d listings sent, want 1", n)
	}
	// Closing the lease does not count as use: the reaper still sees the
	// backend idle since it started.
	clock.Add(time.Hour)
	must(t, l.Close())
	if n := p.Reap(30 * time.Minute); n != 1 {
		t.Fatalf("reaped %d, want the backend whose only use was a stuck listing", n)
	}
}

func TestLease_AbandonedListingWatchdogDropsTheBackend(t *testing.T) {
	b := newDeafListBackend()
	// The listing watchdog follows ListTimeout, not the (long) call limit.
	p := NewPool(func(context.Context, string) (Backend, error) { return b, nil }, PoolOptions{CallTimeout: time.Hour, ListTimeout: 100 * time.Millisecond})
	defer p.Close()
	l, err := p.Lease(context.Background(), "s")
	must(t, err)
	defer func() { _ = l.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, _ = l.ListToolsRaw(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for !b.isClosed() {
		if time.Now().After(deadline) {
			t.Fatal("the backend under an abandoned listing was never closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	st := p.Status([]string{"s"})[0]
	if st.State != StateFailed || st.Reason != ReasonTimedOut {
		t.Fatalf("status = %+v, want failed, timed out", st)
	}
}

func TestLease_ListingThatFinishesAfterTheProbeFreesTheSlot(t *testing.T) {
	release := make(chan struct{})
	slow := &slowListBackend{release: release}
	p := NewPool(func(context.Context, string) (Backend, error) { return slow, nil }, PoolOptions{CallTimeout: time.Minute})
	defer p.Close()
	l, err := p.Lease(context.Background(), "s")
	must(t, err)
	defer func() { _ = l.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, _ = l.ListToolsRaw(ctx)
	close(release) // the late answer arrives
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := l.ListToolsRaw(context.Background()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("listing never allowed again after the stuck one returned")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type slowListBackend struct {
	release chan struct{}
}

func (b *slowListBackend) ListToolsRaw(context.Context) ([]json.RawMessage, error) {
	<-b.release
	return []json.RawMessage{json.RawMessage(`{"name":"a"}`)}, nil
}
func (b *slowListBackend) CallTool(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultText("ok"), nil
}
func (b *slowListBackend) Close() error { return nil }

func TestPool_AbandonedPerSessionStartDoesNotUpdateHealth(t *testing.T) {
	first := make(chan struct{})
	var starts atomic.Int32
	firstBackend := &fakeBackend{}
	start := func(ctx context.Context, server string) (Backend, error) {
		if starts.Add(1) == 1 {
			<-first // a slow start the probe gives up on
			return firstBackend, nil
		}
		return nil, &scrubbedError{msg: "boom", tail: []string{"newer stderr line"}}
	}
	p := NewPool(start, PoolOptions{PerSession: map[string]bool{"browser": true}})
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := p.Lease(ctx, "browser"); err == nil {
		t.Fatal("lease of a start that did not finish in time succeeded")
	}
	if _, err := p.Lease(context.Background(), "browser"); err == nil {
		t.Fatal("second start should fail")
	}
	close(first)
	deadline := time.Now().Add(2 * time.Second)
	for !firstBackend.closed.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the abandoned start's backend was not closed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	st := p.Status([]string{"browser"})[0]
	if st.State != StateFailed || len(st.StderrTail) != 1 || st.StderrTail[0] != "newer stderr line" || st.LastError != "boom" {
		t.Fatalf("status = %+v, want the newer failure kept", st)
	}
}

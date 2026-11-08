package remoteapi

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/terminput"
)

// countingStatus counts Status computations; each one waits for release
// when set, so a test can hold one in flight.
type countingStatus struct {
	n       atomic.Int32
	release chan struct{}
}

func (c *countingStatus) Status(_ context.Context, id, reg string) terminput.Status {
	n := c.n.Add(1)
	if c.release != nil {
		<-c.release
	}
	return terminput.Status{State: reg, Reason: id, CanType: n > 0}
}

func TestStatusCache_ReusesWithinTTL(t *testing.T) {
	inner := &countingStatus{}
	now := time.Unix(1000, 0)
	c := NewStatusCache(inner, time.Second)
	c.now = func() time.Time { return now }
	ctx := context.Background()
	a := c.Status(ctx, "s1", "idle")
	now = now.Add(900 * time.Millisecond)
	b := c.Status(ctx, "s1", "idle")
	if inner.n.Load() != 1 || a.State != "idle" || b.Reason != "s1" {
		t.Fatalf("computed %d times (%+v %+v), want once within 1 s", inner.n.Load(), a, b)
	}
	now = now.Add(200 * time.Millisecond) // 1.1 s after the first
	c.Status(ctx, "s1", "idle")
	if inner.n.Load() != 2 {
		t.Fatalf("computed %d times, want again after 1 s", inner.n.Load())
	}
}

func TestStatusCache_KeysBySessionAndRegistryStatus(t *testing.T) {
	inner := &countingStatus{}
	c := NewStatusCache(inner, time.Second)
	ctx := context.Background()
	c.Status(ctx, "s1", "idle")
	c.Status(ctx, "s2", "idle")
	if st := c.Status(ctx, "s1", "busy"); st.State != "busy" {
		t.Fatalf("status %+v, want the registry status it was asked with", st)
	}
	if inner.n.Load() != 3 {
		t.Fatalf("computed %d times, want one per session and registry status", inner.n.Load())
	}
}

func TestStatusCache_SharesAnInFlightComputation(t *testing.T) {
	inner := &countingStatus{release: make(chan struct{})}
	c := NewStatusCache(inner, time.Second)
	var wg sync.WaitGroup
	results := make([]terminput.Status, 2)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = c.Status(context.Background(), "s1", "busy")
		}()
	}
	for inner.n.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // let the second caller arrive
	close(inner.release)
	wg.Wait()
	if inner.n.Load() != 1 || results[0] != results[1] {
		t.Fatalf("computed %d times, results %+v; want one shared computation", inner.n.Load(), results)
	}
}

// A waiter whose request ends returns at once instead of waiting for the
// computation it shares.
func TestStatusCache_WaiterHonoursItsContext(t *testing.T) {
	inner := &countingStatus{release: make(chan struct{})}
	defer close(inner.release)
	c := NewStatusCache(inner, time.Second)
	go c.Status(context.Background(), "s1", "idle")
	for inner.n.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	st := c.Status(ctx, "s1", "idle")
	if st.CanType || st.State != "idle" || st.Reason != "error" {
		t.Fatalf("status %+v, want a can't-type answer for a cancelled request", st)
	}
}

type panickingStatus struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (p *panickingStatus) Status(context.Context, string, string) terminput.Status {
	if p.calls.Add(1) == 1 {
		close(p.started)
		<-p.release
		panic("driver bug")
	}
	return terminput.Status{State: "idle", CanType: true}
}

// A panicking computation releases its waiters with a can't-type answer,
// and the next read computes again instead of blocking forever.
func TestStatusCache_PanicReleasesWaiters(t *testing.T) {
	inner := &panickingStatus{started: make(chan struct{}), release: make(chan struct{})}
	c := NewStatusCache(inner, time.Second)
	leaderPanicked := make(chan any, 1)
	go func() {
		defer func() { leaderPanicked <- recover() }()
		c.Status(context.Background(), "s1", "idle")
	}()
	<-inner.started
	waiter := make(chan terminput.Status, 1)
	go func() { waiter <- c.Status(context.Background(), "s1", "idle") }()
	time.Sleep(20 * time.Millisecond)
	close(inner.release)
	if p := <-leaderPanicked; p == nil {
		t.Fatal("the leader's panic was swallowed")
	}
	select {
	case st := <-waiter:
		if st.CanType || st.Reason != "error" {
			t.Fatalf("waiter got %+v, want a can't-type answer", st)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter blocked after the leader panicked")
	}
	if st := c.Status(context.Background(), "s1", "idle"); !st.CanType || inner.calls.Load() != 2 {
		t.Fatalf("after the panic: %+v after %d calls, want a fresh computation", st, inner.calls.Load())
	}
}

func TestStatusCache_Invalidate(t *testing.T) {
	inner := &countingStatus{}
	c := NewStatusCache(inner, time.Hour)
	ctx := context.Background()
	c.Status(ctx, "s1", "idle")
	c.Status(ctx, "s1", "busy")
	c.Status(ctx, "s2", "idle")
	c.Invalidate("s1")
	c.Status(ctx, "s1", "idle")
	c.Status(ctx, "s1", "busy")
	c.Status(ctx, "s2", "idle")
	if inner.n.Load() != 5 {
		t.Fatalf("computed %d times, want s1's two entries recomputed and s2's kept", inner.n.Load())
	}
}

package mcpproxy

import (
	"context"

	"sync/atomic"
	"testing"
	"time"
)

// ownedDaemon serves a per-session "browser" server whose sessions' owners
// are decided by the test: every client port belongs to pid 100, which is
// alive until the test says otherwise; known=false makes owners unknown.
func ownedDaemon(t *testing.T, known bool) (base string, fs *fakeStarter, alive *atomic.Bool) {
	t.Helper()
	fs = newFakeStarter()
	pool := NewPool(fs.start, PoolOptions{PerSession: map[string]bool{"browser": true}})
	t.Cleanup(pool.Close)
	cat := &Catalog{}
	cat.Set("browser", toolJSON("snapshot"), time.Now())
	alive = &atomic.Bool{}
	alive.Store(true)
	opt := DaemonOptions{IdleTimeout: 150 * time.Millisecond, ReapEvery: 20 * time.Millisecond,
		lookupPeers: func(int) map[int]int {
			if !known {
				return nil
			}
			return anyPort(100)
		},
		processIdentity: func(pid int) (int64, bool) { return 1, pid == 100 && alive.Load() },
	}
	base, _ = serveCatalog(t, cat, pool, opt)
	return base, fs, alive
}

// anyPort maps every port to pid (lookups are by the caller's client port).
func anyPort(pid int) map[int]int {
	m := map[int]int{}
	for p := 1; p < 65536; p++ {
		m[p] = pid
	}
	return m
}

func browserBackendClosed(fs *fakeStarter) bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return len(fs.backends) == 1 && fs.backends[0].closed.Load()
}

func TestDaemon_PerSessionBackendLivesAsLongAsItsOwner(t *testing.T) {
	base, fs, alive := ownedDaemon(t, true)
	c := httpClient(t, endpointURL(base, "browser"))
	defer func() { _ = c.Close() }()
	if _, err := c.CallTool(context.Background(), callReq("snapshot", nil)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond) // four times the idle limit
	if browserBackendClosed(fs) {
		t.Fatal("the browser closed while its Claude Code process was still running")
	}
	alive.Store(false) // the session's Claude Code process exits
	deadline := time.Now().Add(3 * time.Second)
	for !browserBackendClosed(fs) {
		if time.Now().After(deadline) {
			t.Fatal("the browser outlived its owner")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDaemon_UnknownOwnerFallsBackToTheIdleLimit(t *testing.T) {
	base, fs, _ := ownedDaemon(t, false)
	c := httpClient(t, endpointURL(base, "browser"))
	defer func() { _ = c.Close() }()
	if _, err := c.CallTool(context.Background(), callReq("snapshot", nil)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !browserBackendClosed(fs) {
		if time.Now().After(deadline) {
			t.Fatal("a session with no known owner never hit the idle limit")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// When its owner exits, the session ends in the transport: the sweep only
// terminates it there, and the transport's cleanup (UnregisterSession and
// its hook) is what stops the per-session backend, so a closed backend shows
// the transport dropped the session's state.
func TestDaemon_OwnerExitEndsTheTransportSession(t *testing.T) {
	base, fs, alive := ownedDaemon(t, true)
	c := httpClient(t, endpointURL(base, "browser"))
	defer func() { _ = c.Close() }()
	if _, err := c.CallTool(context.Background(), callReq("snapshot", nil)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // sweeps find the owner running
	alive.Store(false)
	deadline := time.Now().Add(3 * time.Second)
	for !browserBackendClosed(fs) {
		if time.Now().After(deadline) {
			t.Fatal("the transport session was not ended when its owner exited")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

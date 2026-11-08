package remoteapi

import (
	"context"
	"sync"
	"time"

	"github.com/zanetworker/aimux/internal/terminput"
)

// StatusCache shares a terminal status between detail reads for ttl, so
// several polling clients do not repeat the ps, ioreg and Apple Event work.
// A read while one is computing waits for it. It is for the detail only:
// Send and Press read the screen fresh.
type StatusCache struct {
	inner TerminalStatusReader
	ttl   time.Duration
	now   func() time.Time

	mu      sync.Mutex
	entries map[statusKey]*statusEntry
}

type statusKey struct{ id, registryStatus string }

type statusEntry struct {
	done chan struct{} // closed when st is set
	st   terminput.Status
	at   time.Time // when st was computed
}

// NewStatusCache wraps inner, reusing each result for ttl.
func NewStatusCache(inner TerminalStatusReader, ttl time.Duration) *StatusCache {
	return &StatusCache{inner: inner, ttl: ttl, now: time.Now, entries: map[statusKey]*statusEntry{}}
}

// Status returns a result at most ttl old, computing it once for all
// callers asking at the same time.
func (c *StatusCache) Status(ctx context.Context, sessionID, registryStatus string) terminput.Status {
	key := statusKey{sessionID, registryStatus}
	c.mu.Lock()
	now := c.now()
	for k, e := range c.entries {
		if isDone(e) && now.Sub(e.at) >= c.ttl {
			delete(c.entries, k)
		}
	}
	e, ok := c.entries[key]
	if !ok {
		e = &statusEntry{done: make(chan struct{})}
		c.entries[key] = e
		c.mu.Unlock()
		return c.compute(ctx, key, e)
	}
	c.mu.Unlock()
	select {
	case <-e.done:
		return e.st
	case <-ctx.Done():
		return cannotType(registryStatus)
	}
}

// compute runs the shared computation for e. If it panics, waiters get a
// can't-type answer, the entry is dropped so the next read computes again,
// and the panic goes on to the caller.
func (c *StatusCache) compute(ctx context.Context, key statusKey, e *statusEntry) terminput.Status {
	finished := false
	defer func() {
		c.mu.Lock()
		e.at = c.now()
		if !finished {
			e.st = cannotType(key.registryStatus)
			if c.entries[key] == e {
				delete(c.entries, key)
			}
		}
		c.mu.Unlock()
		close(e.done)
	}()
	// Shared by every waiter, so one caller's cancelled request does not
	// spoil the result for the rest.
	st := c.inner.Status(context.WithoutCancel(ctx), key.id, key.registryStatus)
	c.mu.Lock()
	e.st = st
	c.mu.Unlock()
	finished = true
	return st
}

// cannotType is the answer when no status could be read.
func cannotType(registryStatus string) terminput.Status {
	st := terminput.Status{State: "unknown", Reason: "error"}
	switch registryStatus {
	case "busy", "idle":
		st.State = registryStatus
	}
	return st
}

func isDone(e *statusEntry) bool {
	select {
	case <-e.done:
		return true
	default:
		return false
	}
}

// Invalidate drops every cached status of sessionID, after a send or key
// press changed its screen. A computation in flight still answers its own
// waiters; later reads compute afresh.
func (c *StatusCache) Invalidate(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.entries {
		if k.id == sessionID {
			delete(c.entries, k)
		}
	}
}

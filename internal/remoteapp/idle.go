package remoteapp

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// localIdleAfter is how long a local-only serve (the one Wingman starts)
// runs with no local request and no running profile session before it
// exits on its own.
const localIdleAfter = 30 * time.Minute

// idleWatch tracks the local clients' requests: idle once none is in
// flight (an open event stream counts), the last ended at least after ago,
// and busy (a running profile session) is false.
type idleWatch struct {
	after time.Duration
	now   func() time.Time
	busy  func() bool

	mu       sync.Mutex
	last     time.Time
	inflight int
}

func newIdleWatch(after time.Duration, now func() time.Time, busy func() bool) *idleWatch {
	return &idleWatch{after: after, now: now, busy: busy, last: now()}
}

// wrap counts h's requests.
func (i *idleWatch) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i.mu.Lock()
		i.inflight++
		i.last = i.now()
		i.mu.Unlock()
		defer func() {
			i.mu.Lock()
			i.inflight--
			i.last = i.now()
			i.mu.Unlock()
		}()
		h.ServeHTTP(w, r)
	})
}

// idle reports whether the serve has had nothing to do for after.
func (i *idleWatch) idle() bool {
	i.mu.Lock()
	quiet := i.inflight == 0 && i.now().Sub(i.last) >= i.after
	i.mu.Unlock()
	return quiet && !i.busy()
}

// run checks idle every period and calls exit once it is.
func (i *idleWatch) run(period time.Duration, exit func()) func(ctx context.Context) {
	return func(ctx context.Context) {
		t := time.NewTicker(period)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if i.idle() {
					exit()
					return
				}
			}
		}
	}
}

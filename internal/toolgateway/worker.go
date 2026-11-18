package toolgateway

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"slices"
	"time"

	"github.com/zanetworker/aimux/internal/toolreg"
)

// leaseRefreshTimeout bounds the refresh a lease under a newer snapshot
// triggers; workerRefreshEvery is how often the worker reloads the registry
// and pins on its own (the backstop remote serve's turns used to be).
const (
	leaseRefreshTimeout = 15 * time.Second
	workerRefreshEvery  = 60 * time.Second
)

// gatewayServer is what the worker needs of ReadonlyServer.
type gatewayServer interface {
	Applied() toolreg.Snapshot
	Refresh(ctx context.Context)
	RelistNow(ctx context.Context, servers []string) error
	Observed() map[string]Observed
	Backends() (all, down []string)
}

// snapshotStore is what the worker needs of toolreg.Store.
type snapshotStore interface {
	Reload() (toolreg.Snapshot, bool, error)
}

// Worker is the sandbox gateway worker: the read-only gateway it serves and
// the leases remote serve puts on it over the control socket.
type Worker struct {
	Server gatewayServer
	Store  snapshotStore
	Tokens *Tokens
	Log    *log.Logger
	// FreshListTimeout bounds the listing of a new lease's servers; 0: 5 s.
	FreshListTimeout time.Duration
	// RefreshEvery is how often RunWorker reloads and refreshes; 0:
	// workerRefreshEvery.
	RefreshEvery time.Duration

	started time.Time
}

var _ ControlHandler = (*Worker)(nil)

// PutLease loads the lease's snapshot when it is newer than the one served,
// stores the lease, then lists the lease's servers afresh (bounded) so a
// tool a backend changed since the last listing is not served to this turn.
// A listing that times out is logged; the lease stands, and each call is
// still checked against its pin.
func (w *Worker) PutLease(ctx context.Context, l Lease) error {
	if l.Version != "" && l.Version != w.Server.Applied().Version() {
		if _, _, err := w.Store.Reload(); err != nil {
			w.Log.Printf("tool gateway: reload for session %s: %v", l.Session, err)
		}
		rctx, cancel := context.WithTimeout(ctx, leaseRefreshTimeout)
		w.Server.Refresh(rctx)
		cancel()
	}
	// A caller that stopped waiting (its turn ended, the client timed out)
	// may already have deleted this lease: storing it now would outlive the
	// turn.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("lease for session %s not stored: %w", l.Session, err)
	}
	if err := w.Tokens.Put(l); err != nil {
		return err
	}
	w.Log.Printf("tool gateway: lease put for session %s (%d servers, until %s)", l.Session, len(l.Allow), l.Expires.Format(time.RFC3339))
	servers := slices.Sorted(maps.Keys(l.Allow))
	timeout := w.FreshListTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	lctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := w.Server.RelistNow(lctx, servers); err != nil {
		w.Log.Printf("tool gateway: fresh listing for session %s: %v (calls are still checked against their pins)", l.Session, err)
	}
	return nil
}

// Renew moves a lease's expiry.
func (w *Worker) Renew(session string, until time.Time) error { return w.Tokens.Renew(session, until) }

// DeleteLease ends a lease.
func (w *Worker) DeleteLease(session string) {
	w.Tokens.Revoke(session)
	w.Log.Printf("tool gateway: lease deleted for session %s", session)
}

// Observed is the gateway's listings.
func (w *Worker) Observed() map[string]Observed { return w.Server.Observed() }

// Status is the worker at a glance.
func (w *Worker) Status() ControlStatus {
	all, down := w.Server.Backends()
	return ControlStatus{Version: w.Server.Applied().Version(), Leases: w.Tokens.Len(), Backends: all, Down: down, StartedAt: w.started}
}

// RunWorker serves w's gateway (start) and its control socket at path until
// ctx ends, reloading the registry and pins every minute.
func RunWorker(ctx context.Context, w *Worker, start func(context.Context) (func(), error), path string) error {
	w.started = time.Now()
	stop, err := start(ctx)
	if err != nil {
		return fmt.Errorf("start the tool gateway: %w", err)
	}
	defer stop()
	ln, err := ListenControl(path)
	if err != nil {
		return err
	}
	// Building the backends can take a while: the grace window for calls
	// whose lease is not back yet counts from when leases can be put.
	w.Tokens.StartGrace(time.Now())
	go func() {
		every := w.RefreshEvery
		if every <= 0 {
			every = workerRefreshEvery
		}
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, _, err := w.Store.Reload(); err != nil {
					w.Log.Printf("tool gateway: reload: %v; nothing new is granted or served until it is fixed", err)
				}
				rctx, cancel := context.WithTimeout(ctx, leaseRefreshTimeout)
				w.Server.Refresh(rctx)
				cancel()
			}
		}
	}()
	w.Log.Printf("tool gateway: control socket %s", path)
	if err := ServeControl(ctx, ln, w); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

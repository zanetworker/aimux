package toolgateway

import (
	"bytes"
	"context"
	"log"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/toolreg"
)

type fakeGateway struct {
	mu        sync.Mutex
	applied   toolreg.Snapshot
	refreshed int
	relisted  [][]string
	relistDur time.Duration
	store     *fakeStore
	onRefresh func()
}

func (g *fakeGateway) Applied() toolreg.Snapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.applied
}

func (g *fakeGateway) Refresh(context.Context) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.refreshed++
	g.applied = g.store.cur
	if g.onRefresh != nil {
		g.onRefresh()
	}
}

func (g *fakeGateway) RelistNow(ctx context.Context, servers []string) error {
	g.mu.Lock()
	g.relisted = append(g.relisted, slices.Clone(servers))
	d := g.relistDur
	g.mu.Unlock()
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *fakeGateway) Observed() map[string]Observed { return nil }

func (g *fakeGateway) Backends() (all, down []string) {
	return []string{"jira", "wiki"}, []string{"wiki"}
}

type fakeStore struct {
	cur     toolreg.Snapshot
	next    toolreg.Snapshot
	reloads int
}

func (s *fakeStore) Reload() (toolreg.Snapshot, bool, error) {
	s.reloads++
	changed := s.next.Version() != s.cur.Version()
	s.cur = s.next
	return s.cur, changed, nil
}

func snapWith(hash string) toolreg.Snapshot {
	return toolreg.NewSnapshot(toolreg.Registry{}, toolreg.PinTable{"jira": {"search": {Hash: hash}}}, nil)
}

func newTestWorker(t *testing.T) (*Worker, *fakeGateway, *fakeStore, *bytes.Buffer) {
	t.Helper()
	old := snapWith("sha256:a")
	st := &fakeStore{cur: old, next: old}
	gw := &fakeGateway{applied: old, store: st}
	var logs bytes.Buffer
	w := &Worker{Server: gw, Store: st, Tokens: &Tokens{Current: gw.Applied}, Log: log.New(&logs, "", 0), FreshListTimeout: 5 * time.Second}
	return w, gw, st, &logs
}

func leaseUnder(snap toolreg.Snapshot) Lease {
	l := testLease(time.Now())
	l.Allow = map[string][]string{"jira": {"search"}}
	l.Version = snap.Version()
	l.Pins = PinsFor(snap, l.Allow)
	return l
}

func TestWorker_PutLeaseReloadsOnNewerVersion(t *testing.T) {
	w, gw, st, _ := newTestWorker(t)
	newer := snapWith("sha256:b")
	st.next = newer
	if err := w.PutLease(context.Background(), leaseUnder(newer)); err != nil {
		t.Fatal(err)
	}
	if st.reloads != 1 || gw.refreshed != 1 || gw.Applied().Version() != newer.Version() {
		t.Fatalf("reloads %d refreshed %d: the worker must load the lease's newer snapshot first", st.reloads, gw.refreshed)
	}
	if _, allow, ok := w.Tokens.Lookup(leaseTok); !ok || len(allow["jira"]) != 1 {
		t.Fatalf("lease under the served snapshot must keep its tool: %v %v", allow, ok)
	}
}

func TestWorker_PutLeaseSameVersionNoReload(t *testing.T) {
	w, gw, st, _ := newTestWorker(t)
	if err := w.PutLease(context.Background(), leaseUnder(gw.Applied())); err != nil {
		t.Fatal(err)
	}
	if st.reloads != 0 || gw.refreshed != 0 {
		t.Fatalf("reloads %d refreshed %d for a lease under the served snapshot", st.reloads, gw.refreshed)
	}
}

func TestWorker_PutLeaseRelistsLeaseServers(t *testing.T) {
	w, gw, _, _ := newTestWorker(t)
	l := leaseUnder(gw.Applied())
	l.Allow["wiki"] = []string{"wiki_search"}
	if err := w.PutLease(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	if len(gw.relisted) != 1 || !slices.Equal(gw.relisted[0], []string{"jira", "wiki"}) {
		t.Fatalf("relisted %v, want [[jira wiki]]", gw.relisted)
	}
}

func TestWorker_FreshListTimeoutStillAcceptsLease(t *testing.T) {
	w, gw, _, logs := newTestWorker(t)
	gw.relistDur = time.Hour
	w.FreshListTimeout = 50 * time.Millisecond
	start := time.Now()
	if err := w.PutLease(context.Background(), leaseUnder(gw.Applied())); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 50*time.Millisecond+200*time.Millisecond {
		t.Fatalf("PutLease took %v; the fresh listing must be bounded", d)
	}
	if _, _, ok := w.Tokens.Lookup(leaseTok); !ok {
		t.Fatal("a slow listing must not refuse the lease")
	}
	if n := strings.Count(logs.String(), "fresh listing"); n != 1 {
		t.Fatalf("want one log line about the fresh listing, got %d: %q", n, logs.String())
	}
	if strings.Contains(logs.String(), leaseTok) {
		t.Fatal("the token reached the log")
	}
}

func TestWorker_RenewDeleteStatus(t *testing.T) {
	w, gw, _, _ := newTestWorker(t)
	if err := w.PutLease(context.Background(), leaseUnder(gw.Applied())); err != nil {
		t.Fatal(err)
	}
	if err := w.Renew("sess-1", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	st := w.Status()
	if st.Leases != 1 || st.Version != gw.Applied().Version() || !slices.Equal(st.Down, []string{"wiki"}) {
		t.Fatalf("Status = %+v", st)
	}
	w.DeleteLease("sess-1")
	if _, _, ok := w.Tokens.Lookup(leaseTok); ok || w.Status().Leases != 0 {
		t.Fatal("deleted lease still live")
	}
}

func TestRunWorker_LeaseOverTheSocket(t *testing.T) {
	w, gw, _, _ := newTestWorker(t)
	path := shortSock(t)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	start := func(context.Context) (func(), error) { return func() { close(stopped) }, nil }
	done := make(chan error, 1)
	go func() { done <- RunWorker(ctx, w, start, path) }()
	c := ControlClient{Path: path}
	var err error
	for i := 0; i < 100; i++ { // the socket appears once RunWorker listens
		if err = c.PutLease(context.Background(), leaseUnder(gw.Applied())); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	if st, err := c.Status(context.Background()); err != nil || st.Leases != 1 {
		t.Fatalf("Status = %+v %v", st, err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("RunWorker = %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("the gateway was not stopped")
	}
}

// A put whose caller is gone by the time the snapshot is loaded stores
// nothing: the turn may already have deleted it.
func TestWorker_PutLeaseCancelledDoesNotStore(t *testing.T) {
	w, gw, st, _ := newTestWorker(t)
	newer := snapWith("sha256:b")
	st.next = newer
	ctx, cancel := context.WithCancel(context.Background())
	gw.onRefresh = cancel // the client gives up while the worker loads
	if err := w.PutLease(ctx, leaseUnder(newer)); err == nil {
		t.Fatal("a cancelled put succeeded")
	}
	if w.Tokens.Len() != 0 {
		t.Fatal("a cancelled put stored its lease")
	}
}

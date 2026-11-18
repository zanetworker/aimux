package profilerun

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/sandboxlaunch"
	"github.com/zanetworker/aimux/internal/toolgateway"
)

// fakeLeases records the lease calls a turn makes, in order.
type fakeLeases struct {
	mu       sync.Mutex
	ops      []string
	puts     []toolgateway.Lease
	putErr   error
	renewErr func(n int) error // n counts renews from 1
	renews   int
	// table is the token table the fake backend reads the session from.
	table *toolgateway.Tokens
}

func (f *fakeLeases) PutLease(_ context.Context, l toolgateway.Lease) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, "put")
	if f.putErr != nil {
		return f.putErr
	}
	f.puts = append(f.puts, l)
	return f.table.Put(l)
}

func (f *fakeLeases) Renew(_ context.Context, _ string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renews++
	f.ops = append(f.ops, "renew")
	if f.renewErr != nil {
		return f.renewErr(f.renews)
	}
	return nil
}

func (f *fakeLeases) DeleteLease(_ context.Context, session string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, "delete")
	f.table.Revoke(session)
	return nil
}

func (f *fakeLeases) snapshot() ([]string, []toolgateway.Lease) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ops...), append([]toolgateway.Lease(nil), f.puts...)
}

func newLeaseSandbox(t *testing.T, leases *fakeLeases) (*SandboxEngine, *sbBackend) {
	t.Helper()
	leases.table = &toolgateway.Tokens{}
	b := &sbBackend{tokens: leases.table, script: func(s string) (string, error, error) { return okStream(s), nil, nil }}
	e := NewSandboxEngine(&sandboxlaunch.Launcher{Backend: b},
		func(p string) (profile.Compiled, error) { return compiledFor(p), nil },
		func() (string, Leases, error) { return "http://host.containers.internal:7390", leases, nil })
	e.ArchiveDir = filepath.Join(t.TempDir(), "archive")
	return e, b
}

func TestSandboxTurn_PutsLeaseThenDeletes(t *testing.T) {
	f := &fakeLeases{}
	e, b := newLeaseSandbox(t, f)
	sid, _ := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	before := time.Now()
	if _, _, err := sendCollect(t, e, sid, "q"); err != nil {
		t.Fatal(err)
	}
	ops, puts := f.snapshot()
	if len(ops) != 2 || ops[0] != "put" || ops[1] != "delete" {
		t.Fatalf("ops %v, want [put delete]", ops)
	}
	l := puts[0]
	if l.Session != sid || len(l.Token) != 64 || l.Token != b.execs[0].token {
		t.Fatalf("lease session %q token len %d; the sandbox must get the leased token", l.Session, len(l.Token))
	}
	if len(l.Allow) == 0 {
		t.Fatal("the lease allows nothing")
	}
	if d := l.Expires.Sub(before); d < toolgateway.LeaseTTL-time.Second || d > toolgateway.LeaseTTL+5*time.Second {
		t.Fatalf("lease expires in %v, want about %v", d, toolgateway.LeaseTTL)
	}
}

func TestSandboxTurn_RenewsLongTurn(t *testing.T) {
	f := &fakeLeases{}
	e, b := newLeaseSandbox(t, f)
	e.RenewEvery = 20 * time.Millisecond
	b.block, b.closed = make(chan struct{}), make(chan struct{})
	sid, _ := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	go func() { time.Sleep(150 * time.Millisecond); close(b.block) }()
	if _, _, err := sendCollect(t, e, sid, "q"); err != nil {
		t.Fatal(err)
	}
	ops, _ := f.snapshot()
	if n := strings.Count(strings.Join(ops, " "), "renew"); n < 2 {
		t.Fatalf("ops %v: a long turn must renew its lease", ops)
	}
	if ops[len(ops)-1] != "delete" {
		t.Fatalf("ops %v: the lease must be deleted last", ops)
	}
}

func TestSandboxTurn_RePutsOnUnknownLease(t *testing.T) {
	f := &fakeLeases{renewErr: func(n int) error {
		if n == 1 {
			return toolgateway.ErrUnknownLease // the worker restarted
		}
		return nil
	}}
	e, b := newLeaseSandbox(t, f)
	e.RenewEvery = 20 * time.Millisecond
	b.block, b.closed = make(chan struct{}), make(chan struct{})
	sid, _ := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	go func() { time.Sleep(120 * time.Millisecond); close(b.block) }()
	if _, _, err := sendCollect(t, e, sid, "q"); err != nil {
		t.Fatal(err)
	}
	_, puts := f.snapshot()
	if len(puts) < 2 || puts[1].Token != puts[0].Token {
		t.Fatalf("%d puts: a lost lease must be put again with the same token", len(puts))
	}
}

func TestSandboxTurn_GatewayNotRunningFailsFast(t *testing.T) {
	f := &fakeLeases{putErr: toolgateway.ErrGatewayNotRunning}
	e, b := newLeaseSandbox(t, f)
	sid, _ := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	_, _, err := sendCollect(t, e, sid, "q")
	if err == nil || !strings.Contains(err.Error(), "aimux mcp proxy gateway install") {
		t.Fatalf("turn error %v, want the install hint", err)
	}
	if !errors.Is(err, toolgateway.ErrGatewayNotRunning) && !strings.Contains(err.Error(), "not running") {
		t.Fatalf("turn error %v does not say the gateway is not running", err)
	}
	if len(b.execs) != 0 {
		t.Fatal("claude started without a lease")
	}
}

// A turn whose profile has held-back tools says so once, before claude's
// first words; a turn with none says nothing.
func TestSandboxTurn_NoticeBeforeFirstDelta(t *testing.T) {
	e, _ := newLeaseSandbox(t, &fakeLeases{})
	e.Review = func(c profile.Compiled) TurnReview {
		if c.Profile != "work-readonly" {
			t.Errorf("notice asked for %s", c.Profile)
		}
		return TurnReview{Notice: "1 tool held for review (jira: search changed): run aimux tools status"}
	}
	sid, _ := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	_, evs, err := sendCollect(t, e, sid, "q")
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, ev := range evs {
		kinds = append(kinds, ev["sessionUpdate"].(string))
	}
	if len(kinds) == 0 || kinds[0] != "aimux_notice" || strings.Count(strings.Join(kinds, " "), "aimux_notice") != 1 {
		t.Fatalf("updates %v: want one aimux_notice, first", kinds)
	}
	text := evs[0]["content"].(map[string]any)["text"]
	if text != "1 tool held for review (jira: search changed): run aimux tools status" {
		t.Fatalf("notice text %q", text)
	}
}

func TestSandboxTurn_NoNoticeWhenNothingHeld(t *testing.T) {
	e, _ := newLeaseSandbox(t, &fakeLeases{})
	e.Review = func(profile.Compiled) TurnReview { return TurnReview{} }
	sid, _ := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	_, evs, err := sendCollect(t, e, sid, "q")
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev["sessionUpdate"] == "aimux_notice" {
			t.Fatalf("notice with nothing held: %v", ev)
		}
	}
}

// slowServerLeases behaves like the worker over the socket: a put that the
// caller abandons (its context ends) still lands on the worker later.
type slowServerLeases struct {
	*fakeLeases
	delay time.Duration
	wg    sync.WaitGroup
}

func (s *slowServerLeases) PutLease(ctx context.Context, l toolgateway.Lease) error {
	s.mu.Lock()
	first := len(s.puts) == 0
	s.mu.Unlock()
	if first { // the first put is quick: the turn starts
		return s.fakeLeases.PutLease(ctx, l)
	}
	s.wg.Add(1)
	done := make(chan struct{})
	go func() { //nolint:gosec // G118: the worker's handler, which the client cannot stop
		defer s.wg.Done()
		time.Sleep(s.delay)
		_ = s.fakeLeases.PutLease(context.Background(), l)
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// A re-put still in flight when the turn ends must not leave a lease behind.
func TestSandboxTurn_SlowReputRacingTurnEndLeavesNoLease(t *testing.T) {
	f := &fakeLeases{renewErr: func(int) error { return toolgateway.ErrUnknownLease }}
	s := &slowServerLeases{fakeLeases: f, delay: 150 * time.Millisecond}
	f.table = &toolgateway.Tokens{}
	b := &sbBackend{tokens: f.table, script: func(x string) (string, error, error) { return okStream(x), nil, nil }}
	e := NewSandboxEngine(&sandboxlaunch.Launcher{Backend: b},
		func(p string) (profile.Compiled, error) { return compiledFor(p), nil },
		func() (string, Leases, error) { return "http://host.containers.internal:7390", s, nil })
	e.ArchiveDir = filepath.Join(t.TempDir(), "archive")
	e.RenewEvery = 20 * time.Millisecond
	b.block, b.closed = make(chan struct{}), make(chan struct{})
	sid, _ := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	go func() { time.Sleep(40 * time.Millisecond); close(b.block) }() // ends while the re-put runs
	if _, _, err := sendCollect(t, e, sid, "q"); err != nil {
		t.Fatal(err)
	}
	s.wg.Wait()
	if n := f.table.Len(); n != 0 {
		t.Fatalf("%d lease(s) left after the turn ended", n)
	}
}

// restartingLeases is a worker that restarts during the turn: its status
// shows a new start time and no leases until the lease is put again.
type restartingLeases struct {
	*fakeLeases
	mu        sync.Mutex
	started   time.Time
	restarted bool
}

func (r *restartingLeases) Status(context.Context) (toolgateway.ControlStatus, error) {
	r.mu.Lock()
	started := r.started
	r.mu.Unlock()
	r.fakeLeases.mu.Lock()
	n := r.table.Len()
	r.fakeLeases.mu.Unlock()
	return toolgateway.ControlStatus{StartedAt: started, Leases: n}, nil
}

func (r *restartingLeases) restart() {
	r.mu.Lock()
	r.started = r.started.Add(time.Hour)
	r.restarted = true
	r.mu.Unlock()
	r.fakeLeases.mu.Lock()
	r.table.Revoke("") // no-op; the table below replaces it
	for _, l := range r.puts {
		r.table.Revoke(l.Session) // a restarted worker knows no leases
	}
	r.fakeLeases.mu.Unlock()
}

// After the worker restarts mid-turn, the turn's lease is put again at the
// next status probe, long before the 2-minute renewal.
func TestSandboxTurn_RePutsAfterWorkerRestart(t *testing.T) {
	f := &fakeLeases{}
	r := &restartingLeases{fakeLeases: f, started: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}
	e, b := newLeaseSandbox(t, f)
	e.Gateway = func() (string, Leases, error) { return "http://host.containers.internal:7390", r, nil }
	e.ProbeEvery = 20 * time.Millisecond // stands in for the 10 s probe
	b.block, b.closed = make(chan struct{}), make(chan struct{})
	sid, _ := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	var putsAfter int
	go func() {
		time.Sleep(40 * time.Millisecond)
		r.restart()
		time.Sleep(100 * time.Millisecond) // several probes, no renewal (2 min)
		f.mu.Lock()
		putsAfter = len(f.puts)
		f.mu.Unlock()
		close(b.block)
	}()
	if _, _, err := sendCollect(t, e, sid, "q"); err != nil {
		t.Fatal(err)
	}
	if putsAfter < 2 {
		t.Fatalf("%d puts before the turn ended: the lease was not put again after the worker restarted", putsAfter)
	}
	if ops, _ := f.snapshot(); strings.Contains(strings.Join(ops, " "), "renew") {
		t.Fatalf("ops %v: the 2-minute renewal should not have run", ops)
	}
}

// failingOncePut fails the first re-put after a restart; another turn's
// lease keeps the worker's count above zero, so only the start time says
// this turn's lease is still missing.
type failingOncePut struct {
	*restartingLeases
	failed bool
}

func (f *failingOncePut) PutLease(ctx context.Context, l toolgateway.Lease) error {
	f.mu.Lock()
	restarted, failed := f.restarted, f.failed
	if restarted && !failed {
		f.failed = true
	}
	f.mu.Unlock()
	if restarted && !failed {
		return errors.New("worker still starting")
	}
	return f.fakeLeases.PutLease(ctx, l)
}

func (f *failingOncePut) Status(ctx context.Context) (toolgateway.ControlStatus, error) {
	s, err := f.restartingLeases.Status(ctx)
	s.Leases++ // another turn's lease
	return s, err
}

func TestSandboxTurn_RePutRetriedAfterAFailedOne(t *testing.T) {
	f := &fakeLeases{}
	r := &failingOncePut{restartingLeases: &restartingLeases{fakeLeases: f, started: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}}
	e, b := newLeaseSandbox(t, f)
	e.Gateway = func() (string, Leases, error) { return "http://host.containers.internal:7390", r, nil }
	e.ProbeEvery = 20 * time.Millisecond
	e.ProbeRetryEvery = 10 * time.Millisecond
	b.block, b.closed = make(chan struct{}), make(chan struct{})
	sid, _ := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	var puts int
	go func() {
		time.Sleep(40 * time.Millisecond)
		r.restart()
		time.Sleep(150 * time.Millisecond)
		f.mu.Lock()
		puts = len(f.puts)
		f.mu.Unlock()
		close(b.block)
	}()
	if _, _, err := sendCollect(t, e, sid, "q"); err != nil {
		t.Fatal(err)
	}
	if puts < 2 {
		t.Fatalf("%d puts: a failed re-put after the restart was not retried", puts)
	}
}

// statusFailsFirst cannot report the worker before the first put, then the
// worker restarts at once: the first good probe puts the lease again.
type statusFailsFirst struct {
	*failingOncePut
	calls int
}

func (s *statusFailsFirst) Status(ctx context.Context) (toolgateway.ControlStatus, error) {
	s.calls++
	if s.calls == 1 {
		return toolgateway.ControlStatus{}, errors.New("not answering yet")
	}
	return s.failingOncePut.Status(ctx)
}

func TestSandboxTurn_RePutWhenTheFirstStatusFailed(t *testing.T) {
	f := &fakeLeases{}
	inner := &failingOncePut{restartingLeases: &restartingLeases{fakeLeases: f, started: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}, failed: true}
	r := &statusFailsFirst{failingOncePut: inner}
	e, b := newLeaseSandbox(t, f)
	e.Gateway = func() (string, Leases, error) { return "http://host.containers.internal:7390", r, nil }
	e.ProbeEvery = 20 * time.Millisecond
	e.ProbeRetryEvery = 10 * time.Millisecond
	b.block, b.closed = make(chan struct{}), make(chan struct{})
	sid, _ := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	go func() { time.Sleep(100 * time.Millisecond); close(b.block) }()
	if _, _, err := sendCollect(t, e, sid, "q"); err != nil {
		t.Fatal(err)
	}
	if _, puts := f.snapshot(); len(puts) < 2 {
		t.Fatalf("%d puts: with the worker unknown at the first put, the first good probe must put the lease again", len(puts))
	}
}

// downThenUp is a worker that is down for a moment (renew and status fail)
// and comes back restarted.
type downThenUp struct {
	*restartingLeases
	mu2  sync.Mutex
	down bool
}

func (d *downThenUp) isDown() bool { d.mu2.Lock(); defer d.mu2.Unlock(); return d.down }

func (d *downThenUp) Renew(ctx context.Context, s string, u time.Time) error {
	if d.isDown() {
		return toolgateway.ErrGatewayNotRunning
	}
	return d.fakeLeases.Renew(ctx, s, u)
}

func (d *downThenUp) Status(ctx context.Context) (toolgateway.ControlStatus, error) {
	if d.isDown() {
		return toolgateway.ControlStatus{}, toolgateway.ErrGatewayNotRunning
	}
	return d.restartingLeases.Status(ctx)
}

// A failed renewal makes the next status check come quickly, so the lease
// is back soon after the worker answers again, not a full probe interval
// later.
func TestSandboxTurn_FailedRenewProbesSoon(t *testing.T) {
	f := &fakeLeases{}
	d := &downThenUp{restartingLeases: &restartingLeases{fakeLeases: f, started: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}}
	e, b := newLeaseSandbox(t, f)
	e.Gateway = func() (string, Leases, error) { return "http://host.containers.internal:7390", d, nil }
	e.ProbeEvery = time.Hour // only the quick re-check can catch the restart
	e.RenewEvery = 30 * time.Millisecond
	e.ProbeRetryEvery = 10 * time.Millisecond
	b.block, b.closed = make(chan struct{}), make(chan struct{})
	sid, _ := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	var puts int
	go func() {
		time.Sleep(20 * time.Millisecond)
		d.mu2.Lock()
		d.down = true
		d.mu2.Unlock()
		d.restart()
		time.Sleep(50 * time.Millisecond) // a renewal fails meanwhile
		d.mu2.Lock()
		d.down = false
		d.mu2.Unlock()
		time.Sleep(60 * time.Millisecond)
		f.mu.Lock()
		puts = len(f.puts)
		f.mu.Unlock()
		close(b.block)
	}()
	if _, _, err := sendCollect(t, e, sid, "q"); err != nil {
		t.Fatal(err)
	}
	if puts < 2 {
		t.Fatalf("%d puts: the lease was not put again soon after the worker came back", puts)
	}
}

// A server whose every granted tool is held back is left out of the turn's
// MCP config and --allowedTools; the lease still carries the whole grant.
func TestSandboxTurn_LeavesOutAServerWithEveryToolHeld(t *testing.T) {
	f := &fakeLeases{}
	e, b := newLeaseSandbox(t, f)
	e.Review = func(profile.Compiled) TurnReview {
		return TurnReview{Notice: "wiki: 1 tool changed, held for review, left out of this turn: run aimux tools status", LeftOut: []string{"wiki"}}
	}
	sid, _ := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	if _, _, err := sendCollect(t, e, sid, "q"); err != nil {
		t.Fatal(err)
	}
	ex := b.execs[0]
	if strings.Contains(ex.cfg, "wiki") || !strings.Contains(ex.cfg, "/mcp/jira") {
		t.Fatalf("mcp config %s: want jira only", ex.cfg)
	}
	args := strings.Join(ex.args, " ")
	if strings.Contains(args, "mcp__wiki__") || !strings.Contains(args, "mcp__jira__search") {
		t.Fatalf("args %s: want jira's tool allowed, wiki's not", args)
	}
	_, puts := f.snapshot()
	if len(puts[0].Allow["wiki"]) == 0 {
		t.Fatalf("lease %v: the grant itself is unchanged", puts[0].Allow)
	}
}

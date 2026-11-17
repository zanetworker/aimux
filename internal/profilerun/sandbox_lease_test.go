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
	e.Notice = func(c profile.Compiled) string {
		if c.Profile != "work-readonly" {
			t.Errorf("notice asked for %s", c.Profile)
		}
		return "1 tool held for review (jira: search changed): run aimux tools status"
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
	e.Notice = func(profile.Compiled) string { return "" }
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

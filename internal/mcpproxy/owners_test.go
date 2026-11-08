package mcpproxy

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

type ownersFX struct {
	o       *sessionOwners
	clock   time.Time
	pids    map[int]int    // client port -> pid, what lsof would report
	alive   map[int]bool   // pid -> running
	started map[int]int64  // pid -> start time (0: 1)
	conns   map[int]uint64 // port -> open connection id (absent: 1)
	closed  map[int]bool   // port -> no open connection
	lookups int
}

func newOwnersFX() *ownersFX {
	fx := &ownersFX{clock: time.Unix(1_000_000, 0), pids: map[int]int{}, alive: map[int]bool{}, started: map[int]int64{}, conns: map[int]uint64{}, closed: map[int]bool{}}
	fx.o = &sessionOwners{
		idle: 10 * time.Minute,
		now:  func() time.Time { return fx.clock },
		lookup: func() map[int]int {
			fx.lookups++
			out := map[int]int{}
			for k, v := range fx.pids {
				out[k] = v
			}
			return out
		},
		identity: func(pid int) (int64, bool) {
			if !fx.alive[pid] {
				return 0, false
			}
			if st, ok := fx.started[pid]; ok {
				return st, true
			}
			return 1, true
		},
		busy: func(string) bool { return false },
		open: func(port int) (uint64, bool) {
			if fx.closed[port] {
				return 0, false
			}
			if id, ok := fx.conns[port]; ok {
				return id, true
			}
			return 1, true
		},
	}
	return fx
}

func sorted(s []string) []string { sort.Strings(s); return s }

// ids is the ended sessions' ids (all tests use the endpoint "browser").
func ids(t *testing.T, ended []endedSession) []string {
	t.Helper()
	var out []string
	for _, e := range ended {
		if e.Endpoint != "browser" {
			t.Fatalf("ended %+v, want its endpoint browser", e)
		}
		out = append(out, e.ID)
	}
	return out
}

// A session lives as long as the Claude Code process that opened it, however
// long it is quiet: a browser left alone for an hour is still there.
func TestSessionOwners_LiveOwnerKeepsTheSession(t *testing.T) {
	fx := newOwnersFX()
	fx.pids[50001], fx.alive[4242] = 4242, true
	fx.o.seen("s1", "browser", "127.0.0.1:50001", 1)
	if got := ids(t, fx.o.sweep()); len(got) != 0 {
		t.Fatalf("first sweep ended %v", got)
	}
	fx.clock = fx.clock.Add(3 * time.Hour)
	if got := ids(t, fx.o.sweep()); len(got) != 0 {
		t.Fatalf("a session whose owner runs was ended after 3 idle hours: %v", got)
	}
}

// When the owner exits (a nightly `claude -p` run finishing), its session
// ends at the next sweep, not 10 minutes later.
func TestSessionOwners_OwnerExitEndsTheSession(t *testing.T) {
	fx := newOwnersFX()
	fx.pids[50001], fx.alive[4242] = 4242, true
	fx.pids[50002], fx.alive[5353] = 5353, true
	fx.o.seen("s1", "browser", "127.0.0.1:50001", 1)
	fx.o.seen("s2", "browser", "127.0.0.1:50002", 1)
	fx.o.sweep()
	fx.alive[4242] = false
	if got := ids(t, fx.o.sweep()); !reflect.DeepEqual(got, []string{"s1"}) {
		t.Fatalf("ended %v, want [s1] (its owner exited)", got)
	}
	if got := ids(t, fx.o.sweep()); len(got) != 0 {
		t.Fatalf("an ended session was ended again: %v", got)
	}
}

// Without a known owner, today's rule stays: 10 idle minutes, but never
// while a call is in flight.
func TestSessionOwners_UnknownOwnerFallsBackToIdleLimit(t *testing.T) {
	fx := newOwnersFX()
	busy := true
	fx.o.busy = func(s string) bool { return s == "s1" && busy }
	fx.o.seen("s1", "browser", "127.0.0.1:50009", 1)
	fx.clock = fx.clock.Add(9 * time.Minute)
	if got := ids(t, fx.o.sweep()); len(got) != 0 {
		t.Fatalf("ended %v before the idle limit", got)
	}
	fx.clock = fx.clock.Add(2 * time.Minute)
	if got := ids(t, fx.o.sweep()); len(got) != 0 {
		t.Fatalf("ended %v with a call in flight", got)
	}
	busy = false
	if got := ids(t, fx.o.sweep()); len(got) != 0 {
		t.Fatalf("ended %v right after its call finished: time busy is not idle time", got)
	}
	fx.clock = fx.clock.Add(11 * time.Minute)
	if got := ids(t, fx.o.sweep()); !reflect.DeepEqual(got, []string{"s1"}) {
		t.Fatalf("ended %v, want [s1] idle past the limit after its call", got)
	}
}

// A call longer than the idle limit, on a session with no known owner,
// leaves the session (its browser) for the next call: idle time starts
// when the call ends, not when it was sent.
func TestSessionOwners_LongCallDoesNotUseUpTheIdleLimit(t *testing.T) {
	fx := newOwnersFX()
	busy := true
	fx.o.busy = func(string) bool { return busy }
	fx.o.seen("s1", "browser", "127.0.0.1:50009", 1)
	for i := 0; i < 15; i++ { // a 15-minute call, one sweep a minute
		fx.clock = fx.clock.Add(time.Minute)
		if got := ids(t, fx.o.sweep()); len(got) != 0 {
			t.Fatalf("ended %v during its call", got)
		}
	}
	busy = false
	fx.clock = fx.clock.Add(time.Minute)
	if got := ids(t, fx.o.sweep()); len(got) != 0 {
		t.Fatalf("ended %v a minute after a long call", got)
	}
}

// Activity on any of the owner's connections keeps an unknown-owner session
// alive, and a later lookup that finds the owner switches it to the pid rule.
func TestSessionOwners_SeenRefreshesAndLateLookupFindsTheOwner(t *testing.T) {
	fx := newOwnersFX()
	fx.o.seen("s1", "browser", "127.0.0.1:50001", 1)
	fx.o.sweep() // lsof does not list it yet
	fx.clock = fx.clock.Add(8 * time.Minute)
	fx.o.seen("s1", "browser", "127.0.0.1:50003", 1) // a new keep-alive connection
	fx.pids[50003], fx.alive[7777] = 7777, true
	fx.clock = fx.clock.Add(30 * time.Minute)
	if got := ids(t, fx.o.sweep()); len(got) != 0 {
		t.Fatalf("ended %v although its owner was found running", got)
	}
}

// One lsof per sweep at most, and none when every session's owner is known.
func TestSessionOwners_LookupOnlyForUnknownOwners(t *testing.T) {
	fx := newOwnersFX()
	fx.pids[50001], fx.alive[4242] = 4242, true
	fx.o.seen("s1", "browser", "127.0.0.1:50001", 1)
	fx.o.seen("s2", "browser", "127.0.0.1:50001", 1)
	fx.o.sweep()
	if fx.lookups != 1 {
		t.Fatalf("lookups = %d, want 1 for two unknown sessions", fx.lookups)
	}
	fx.o.sweep()
	fx.o.seen("s1", "browser", "127.0.0.1:50001", 1)
	fx.o.sweep()
	if fx.lookups != 1 {
		t.Fatalf("lookups = %d, want no more once every owner is known", fx.lookups)
	}
	if got := sorted(fx.o.sessions()); !reflect.DeepEqual(got, []string{"s1", "s2"}) {
		t.Fatalf("tracked %v", got)
	}
}

func TestSessionOwners_IgnoresEmptySessionAndBadAddress(t *testing.T) {
	fx := newOwnersFX()
	fx.o.seen("", "browser", "127.0.0.1:50001", 1)
	fx.o.seen("s1", "browser", "not-an-address", 1)
	if got := fx.o.sessions(); !reflect.DeepEqual(got, []string{"s1"}) {
		t.Fatalf("tracked %v, want only s1", got)
	}
	fx.clock = fx.clock.Add(11 * time.Minute)
	if got := ids(t, fx.o.sweep()); !reflect.DeepEqual(got, []string{"s1"}) {
		t.Fatalf("a session with no port falls back to the idle limit; ended %v", got)
	}
}

func TestParseLsofPeers(t *testing.T) {
	out := "p3008\nc2.1.319\nf15\nn127.0.0.1:63717->127.0.0.1:7381\nf16\nn127.0.0.1:63718->127.0.0.1:7381\n" +
		"p72446\ncaimux\nf9\nn127.0.0.1:7381->127.0.0.1:63717\n" +
		"p67908\nc2.1.319\nf20\nn127.0.0.1:50100->127.0.0.1:7381\nf21\nn127.0.0.1:50101->127.0.0.1:9999\n" +
		"p1\ncweird\nnnot a socket\n"
	got := parseLsofPeers(out, 7381)
	want := map[int]int{63717: 3008, 63718: 3008, 50100: 67908}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseLsofPeers = %v, want %v (the proxy's own side and other ports left out)", got, want)
	}
	if got := parseLsofPeers("", 7381); len(got) != 0 {
		t.Fatalf("empty output = %v", got)
	}
}

func TestPool_SessionBusyCountsSharedAndPerSessionCalls(t *testing.T) {
	fs := newFakeStarter()
	p := NewPool(fs.start, PoolOptions{PerSession: map[string]bool{"browser": true}})
	defer p.Close()
	release := make(chan struct{})
	fs.callFn = func(context.Context) (*mcp.CallToolResult, error) {
		<-release
		return mcp.NewToolResultText("ok"), nil
	}
	done := make(chan struct{})
	go func() { p.Call(context.Background(), "slack", "s1", callReq("post", nil)); close(done) }()
	deadline := time.Now().Add(3 * time.Second)
	for !p.SessionBusy("s1") {
		if time.Now().After(deadline) {
			t.Fatal("a call in flight on a shared server did not make its session busy")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if p.SessionBusy("s2") || p.SessionBusy("") {
		t.Fatal("another session (or none) counted as busy")
	}
	close(release)
	<-done
	if p.SessionBusy("s1") {
		t.Fatal("still busy after the call returned")
	}
}

// A pid the system reuses for another process after the owner exits is not
// the owner: the session ends (the new process has another start time).
func TestSessionOwners_ReusedPIDIsNotTheOwner(t *testing.T) {
	fx := newOwnersFX()
	fx.pids[50001], fx.alive[4242], fx.started[4242] = 4242, true, 100
	fx.o.seen("s1", "browser", "127.0.0.1:50001", 1)
	fx.o.sweep()
	fx.started[4242] = 200 // the owner exited; an unrelated process got its pid
	if got := ids(t, fx.o.sweep()); !reflect.DeepEqual(got, []string{"s1"}) {
		t.Fatalf("ended %v, want [s1]: its pid now belongs to another process", got)
	}
}

// A port whose connection closed between sweeps may belong to another
// client later (port reuse): it never identifies the session's owner.
func TestSessionOwners_ClosedConnectionsPortIsNotUsedLater(t *testing.T) {
	fx := newOwnersFX()
	fx.o.seen("s1", "browser", "127.0.0.1:50001", 1)
	fx.o.sweep()                                // not listed: its connection closed before the lookup
	fx.conns[50001] = 2                         // its connection closed; another client's connection got the port
	fx.pids[50001], fx.alive[9999] = 9999, true // and that client's process holds it
	fx.o.sweep()
	fx.o.seen("s2", "browser", "127.0.0.1:50002", 1) // forces a lookup
	fx.pids[50002], fx.alive[4242] = 4242, true
	fx.o.sweep()
	fx.alive[9999] = false
	fx.clock = fx.clock.Add(time.Minute)
	if got := ids(t, fx.o.sweep()); len(got) != 0 {
		t.Fatalf("ended %v: s1 was pinned to the process that reused its old port", got)
	}
}

// A port identifies its owner only while the very connection that carried
// the session's request is still open: a port now held by another
// connection (closed, then reused) is not used.
func TestSessionOwners_PortOfAnotherConnectionIsNotUsed(t *testing.T) {
	fx := newOwnersFX()
	fx.o.seen("s1", "browser", "127.0.0.1:50001", 7)
	fx.conns[50001] = 8 // connection 7 closed; another client's connection 8 got the port
	fx.pids[50001], fx.alive[9999] = 9999, true
	fx.o.sweep()
	fx.alive[9999] = false
	if got := ids(t, fx.o.sweep()); len(got) != 0 {
		t.Fatalf("ended %v: s1 was pinned to the process holding another connection on its old port", got)
	}
}

// A connection that closes while lsof runs may have handed its port on, so
// the answer for it is not used either.
func TestSessionOwners_ConnectionClosedDuringLookupIsNotUsed(t *testing.T) {
	fx := newOwnersFX()
	fx.o.seen("s1", "browser", "127.0.0.1:50001", 1)
	fx.pids[50001], fx.alive[9999] = 9999, true
	lookup := fx.o.lookup
	fx.o.lookup = func() map[int]int { m := lookup(); fx.conns[50001] = 2; return m }
	fx.o.sweep()
	fx.alive[9999] = false
	if got := ids(t, fx.o.sweep()); len(got) != 0 {
		t.Fatalf("ended %v: the lookup's answer for a connection that changed during it was used", got)
	}
}

// A lookup that misses a client (lsof timed out, or omitted it) is retried
// at the next sweep while the session's connection stays open, without a
// new request: a quiet session keeps its browser once its owner is found.
func TestSessionOwners_MissedLookupIsRetriedWhileTheConnectionIsOpen(t *testing.T) {
	fx := newOwnersFX()
	fx.o.seen("s1", "browser", "127.0.0.1:50001", 1)
	fx.o.sweep() // lsof does not list it this time
	fx.pids[50001], fx.alive[4242] = 4242, true
	fx.o.sweep() // no new request in between
	fx.clock = fx.clock.Add(time.Hour)
	if got := ids(t, fx.o.sweep()); len(got) != 0 {
		t.Fatalf("ended %v: the owner was never found after one missed lookup", got)
	}
	fx.closed[50001] = true // the connection closes; the owner is known, so it still counts
	fx.alive[4242] = false
	if got := ids(t, fx.o.sweep()); len(got) != 1 {
		t.Fatalf("ended %v, want s1 once its owner exits", got)
	}
}

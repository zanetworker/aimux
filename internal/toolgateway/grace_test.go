package toolgateway

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/mcpproxy"
)

func graceTokens(started time.Time) *Tokens {
	tk := &Tokens{Grace: 400 * time.Millisecond, MaxWaiting: 2}
	tk.StartGrace(started)
	return tk
}

// Just after a restart, a request with a token remote serve has not put
// again yet waits for it instead of failing.
func TestWaitLookup_TokenPutDuringGrace(t *testing.T) {
	tk := graceTokens(time.Now())
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = tk.Put(testLease(time.Now()))
	}()
	start := time.Now()
	sess, _, ok, err := tk.WaitLookup(context.Background(), leaseTok)
	if err != nil || !ok || sess != "sess-1" {
		t.Fatalf("WaitLookup = %q %v %v", sess, ok, err)
	}
	if d := time.Since(start); d > 350*time.Millisecond {
		t.Fatalf("waited %v: should return as soon as the lease is put", d)
	}
}

func TestWaitLookup_NeverPutEndsWithTheWindow(t *testing.T) {
	tk := graceTokens(time.Now())
	start := time.Now()
	_, _, ok, err := tk.WaitLookup(context.Background(), leaseTok)
	if ok || err != nil {
		t.Fatalf("ok %v err %v, want unknown", ok, err)
	}
	if d := time.Since(start); d < 300*time.Millisecond || d > 900*time.Millisecond {
		t.Fatalf("returned after %v, want at the window's end (about 400ms)", d)
	}
}

func TestWaitLookup_OutsideTheWindowAtOnce(t *testing.T) {
	tk := graceTokens(time.Now().Add(-time.Hour))
	start := time.Now()
	if _, _, ok, _ := tk.WaitLookup(context.Background(), leaseTok); ok {
		t.Fatal("unknown token accepted")
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("waited %v outside the window", d)
	}
}

func TestWaitLookup_OverTheBoundIsBusy(t *testing.T) {
	tk := graceTokens(time.Now())
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ { // fill the two slots
		wg.Add(1)
		go func() { defer wg.Done(); _, _, _, _ = tk.WaitLookup(context.Background(), leaseTok) }()
	}
	time.Sleep(50 * time.Millisecond)
	_, _, ok, err := tk.WaitLookup(context.Background(), leaseTok)
	if ok || !errors.Is(err, mcpproxy.ErrTokenWaitBusy) {
		t.Fatalf("third waiter: ok %v err %v, want ErrTokenWaitBusy", ok, err)
	}
	wg.Wait()
}

func TestWaitLookup_CallerGoneStopsWaiting(t *testing.T) {
	tk := graceTokens(time.Now())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, _, ok, _ := tk.WaitLookup(ctx, leaseTok); ok {
		t.Fatal("accepted")
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("kept waiting %v after the caller left", d)
	}
}

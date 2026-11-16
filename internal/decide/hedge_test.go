package decide

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/cost"
)

// seqBackend answers its n-th call after delays[n] (the last delay repeats),
// honouring its context, as OpenRouter's bursty latency did on 2026-11-16.
type seqBackend struct {
	delays []time.Duration
	a      Answer
	err    error
	calls  atomic.Int32
	mu     sync.Mutex
	starts []time.Time
}

func (s *seqBackend) Decide(ctx context.Context, _ Request) (Answer, error) {
	n := int(s.calls.Add(1)) - 1
	s.mu.Lock()
	s.starts = append(s.starts, time.Now())
	s.mu.Unlock()
	d := s.delays[min(n, len(s.delays)-1)]
	select {
	case <-time.After(d):
		return s.a, s.err
	case <-ctx.Done():
		return Answer{}, ctx.Err()
	}
}

func hedgeDecider(b Backend, hedge time.Duration) *Decider {
	return &Decider{Name: "t", Backends: []Backend{b}, Threshold: 0.25, Timeout: 5 * time.Second, Hedge: hedge, Usage: noUsage}
}

func TestHedge_SlowFirstCallFastSecondWins(t *testing.T) {
	b := &seqBackend{delays: []time.Duration{4 * time.Second, 100 * time.Millisecond}, a: ans("web", 0.8, map[string]float64{"web": 0.8, "page": 0.2})}
	start := time.Now()
	a := hedgeDecider(b, 300*time.Millisecond).Ask(context.Background(), Request{Question: Question{Name: "mode", Options: map[string]string{"web": "", "page": ""}}})
	took := time.Since(start)
	if !a.Decided || a.Choice != "web" {
		t.Fatalf("answer %+v", a)
	}
	if took > 1500*time.Millisecond {
		t.Fatalf("took %v; the hedged call should answer at about 0.4 s", took)
	}
	if got := b.calls.Load(); got != 2 {
		t.Fatalf("calls = %d, want 2", got)
	}
}

func TestHedge_FastFirstCallSendsNoSecond(t *testing.T) {
	b := &seqBackend{delays: []time.Duration{50 * time.Millisecond}, a: ans("page", 0.9, map[string]float64{"page": 0.9, "web": 0.1})}
	a := hedgeDecider(b, 300*time.Millisecond).Ask(context.Background(), Request{Question: Question{Name: "mode", Options: map[string]string{"web": "", "page": ""}}})
	time.Sleep(400 * time.Millisecond) // past the hedge point: nothing may start late
	if !a.Decided || b.calls.Load() != 1 {
		t.Fatalf("answer %+v, calls %d, want decided with 1 call", a, b.calls.Load())
	}
}

func TestHedge_ZeroIsOff(t *testing.T) {
	b := &seqBackend{delays: []time.Duration{800 * time.Millisecond}, a: ans("page", 0.9, map[string]float64{"page": 0.9})}
	a := hedgeDecider(b, 0).Ask(context.Background(), Request{Question: Question{Name: "mode", Options: map[string]string{"page": ""}}})
	if !a.Decided || b.calls.Load() != 1 {
		t.Fatalf("answer %+v, calls %d, want 1 call with hedging off", a, b.calls.Load())
	}
}

func TestHedge_FirstErrorBeforeHedgeIsNotHedged(t *testing.T) {
	b := &seqBackend{delays: []time.Duration{20 * time.Millisecond}, err: errors.New("boom")}
	a := hedgeDecider(b, 300*time.Millisecond).Ask(context.Background(), Request{Question: Question{Name: "mode", Options: map[string]string{"page": ""}}})
	time.Sleep(400 * time.Millisecond)
	if a.Decided || a.Undecided != UndecidedError || b.calls.Load() != 1 {
		t.Fatalf("answer %+v, calls %d", a, b.calls.Load())
	}
}

func TestHedge_BothSlowStillTimesOut(t *testing.T) {
	b := &seqBackend{delays: []time.Duration{10 * time.Second}, a: ans("page", 0.9, nil)}
	d := hedgeDecider(b, 200*time.Millisecond)
	d.Timeout = 700 * time.Millisecond
	start := time.Now()
	a := d.Ask(context.Background(), Request{Question: Question{Name: "mode", Options: map[string]string{"page": ""}}})
	if a.Undecided != UndecidedTimeout || time.Since(start) > 2*time.Second || b.calls.Load() != 2 {
		t.Fatalf("answer %+v after %v, calls %d", a, time.Since(start), b.calls.Load())
	}
}

func TestHedge_ErrorFromOneWaitsForTheOther(t *testing.T) {
	// first call fails after the hedge started; the second answers
	b := &errThenOK{}
	a := hedgeDecider(b, 100*time.Millisecond).Ask(context.Background(), Request{Question: Question{Name: "mode", Options: map[string]string{"web": ""}}})
	if !a.Decided || a.Choice != "web" {
		t.Fatalf("answer %+v", a)
	}
}

type errThenOK struct{ n atomic.Int32 }

func (e *errThenOK) Decide(ctx context.Context, _ Request) (Answer, error) {
	if e.n.Add(1) == 1 {
		time.Sleep(300 * time.Millisecond) // fails after the hedged call started
		return Answer{}, errors.New("upstream 502")
	}
	time.Sleep(400 * time.Millisecond)
	return ans("web", 0.9, map[string]float64{"web": 1}), nil
}

func TestConfig_HedgeDefaultKeptAndOverridable(t *testing.T) {
	cfgs, err := LoadConfig(t.TempDir()+"/absent.yaml", map[string]DeciderConfig{"r": {Backends: []BackendConfig{{Kind: "openrouter", Model: "m"}}, Threshold: 0.25, Timeout: 5 * time.Second, Hedge: 1200 * time.Millisecond}})
	if err != nil || cfgs["r"].Hedge != 1200*time.Millisecond {
		t.Fatalf("default hedge lost: %+v %v", cfgs["r"], err)
	}
	if _, err := Build("r", DeciderConfig{Backends: []BackendConfig{{Kind: "openrouter", Model: "m"}}, Threshold: 0.25, Timeout: time.Second, Hedge: 2 * time.Second}, Deps{Key: func(context.Context) (string, error) { return "", nil }}); err == nil {
		t.Fatal("a hedge at or past the timeout must be refused")
	}
	if _, err := Build("r", DeciderConfig{Backends: []BackendConfig{{Kind: "openrouter", Model: "m"}}, Threshold: 0.25, Timeout: time.Second, Hedge: -time.Second}, Deps{Key: func(context.Context) (string, error) { return "", nil }}); err == nil {
		t.Fatal("a negative hedge must be refused")
	}
}

func hedgeBuiltin() map[string]DeciderConfig {
	return map[string]DeciderConfig{"r": {Backends: []BackendConfig{{Kind: "openrouter", Model: "m"}}, Threshold: 0.25, Timeout: 5 * time.Second, Hedge: 1200 * time.Millisecond}}
}

func writeDeciders(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "deciders.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

var hedgeKey = Deps{Key: func(context.Context) (string, error) { return "", nil }}

func TestConfig_InheritedHedgeNotShorterThanTimeoutIsTurnedOff(t *testing.T) {
	cfgs, err := LoadConfig(writeDeciders(t, "r:\n  timeout: 1s\n"), hedgeBuiltin())
	if err != nil {
		t.Fatal(err)
	}
	if cfgs["r"].Hedge != 0 {
		t.Fatalf("hedge = %v, want 0 (inherited, not shorter than the 1s timeout)", cfgs["r"].Hedge)
	}
	if _, err := Build("r", cfgs["r"], hedgeKey); err != nil {
		t.Fatalf("build: %v", err)
	}
	// a shorter-than-hedge-but-above timeout keeps the default
	cfgs, err = LoadConfig(writeDeciders(t, "r:\n  timeout: 3s\n"), hedgeBuiltin())
	if err != nil || cfgs["r"].Hedge != 1200*time.Millisecond {
		t.Fatalf("hedge kept? %+v %v", cfgs["r"], err)
	}
}

func TestConfig_ExplicitHedgeNotShorterThanTimeoutIsALoadError(t *testing.T) {
	_, err := LoadConfig(writeDeciders(t, "r:\n  timeout: 1s\n  hedge: 2s\n"), hedgeBuiltin())
	if err == nil || !strings.Contains(err.Error(), "hedge") {
		t.Fatalf("err = %v, want a hedge error from LoadConfig", err)
	}
	if _, err := LoadConfig(writeDeciders(t, "r:\n  hedge: -1s\n"), hedgeBuiltin()); err == nil {
		t.Fatal("a negative explicit hedge must be a load error")
	}
	cfgs, err := LoadConfig(writeDeciders(t, "r:\n  timeout: 1s\n  hedge: 0s\n"), hedgeBuiltin())
	if err != nil || cfgs["r"].Hedge != 0 {
		t.Fatalf("explicit hedge 0 should load: %+v %v", cfgs["r"], err)
	}
}

// noHedgeBackend counts calls and, like Chat, must never be hedged.
type slowChat struct{ seqBackend }

func (*slowChat) noHedge() {}

func TestHedge_ChatBackendIsNeverHedged(t *testing.T) {
	b := &slowChat{seqBackend{delays: []time.Duration{500 * time.Millisecond}, a: ans("web", 0.9, map[string]float64{"web": 1})}}
	a := hedgeDecider(b, 100*time.Millisecond).Ask(context.Background(), Request{Question: Question{Name: "mode", Options: map[string]string{"web": ""}}})
	if !a.Decided || b.calls.Load() != 1 {
		t.Fatalf("answer %+v, calls %d, want 1", a, b.calls.Load())
	}
	var _ unhedgeable = (*Chat)(nil)
}

func TestHedge_BothComplete_RecordsEveryAnsweredRequest(t *testing.T) {
	c := 0.000003
	a0 := ans("web", 0.8, map[string]float64{"web": 1})
	a0.CostUSD = &c
	b := &seqBackend{delays: []time.Duration{300 * time.Millisecond, 200 * time.Millisecond}, a: a0}
	var n atomic.Int32
	d := hedgeDecider(b, 100*time.Millisecond)
	d.Usage = func(cost.Usage) error { n.Add(1); return nil }
	// both requests finish at ~300ms: the first started at 0 (300ms), the hedge at 100ms (200ms)
	a := d.Ask(context.Background(), Request{Question: Question{Name: "mode", Options: map[string]string{"web": ""}}})
	if !a.Decided {
		t.Fatalf("answer %+v", a)
	}
	deadline := time.Now().Add(2 * time.Second)
	for n.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n.Load() != 2 {
		t.Fatalf("usage lines = %d, want 2 (both requests completed and are billed)", n.Load())
	}
}

func TestHedge_DecisionLineHedgedOnlyWhenSecondRequestWentOut(t *testing.T) {
	read := func(p string) string {
		data, err := os.ReadFile(p) //nolint:gosec // G304: a path the test made
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	run := func(delay time.Duration) string {
		p := filepath.Join(t.TempDir(), "decisions.jsonl")
		b := &seqBackend{delays: []time.Duration{delay}, a: ans("web", 0.9, map[string]float64{"web": 1})}
		d := hedgeDecider(b, 100*time.Millisecond)
		d.Log = &Log{Path: p}
		d.Ask(context.Background(), Request{Question: Question{Name: "mode", Options: map[string]string{"web": ""}}})
		return read(p)
	}
	if got := run(400 * time.Millisecond); !strings.Contains(got, `"hedged":true`) {
		t.Fatalf("slow first call: line lacks hedged:true: %s", got)
	}
	if got := run(10 * time.Millisecond); strings.Contains(got, "hedged") {
		t.Fatalf("fast first call: line must not carry hedged: %s", got)
	}
}

// ignoresCtx never returns before its delay, whatever its context says.
type ignoresCtx struct{ calls atomic.Int32 }

func (i *ignoresCtx) Decide(context.Context, Request) (Answer, error) {
	i.calls.Add(1)
	time.Sleep(5 * time.Second)
	return Answer{}, nil
}

func TestHedge_BackendIgnoringContextCannotHoldAskPastTimeout(t *testing.T) {
	b := &ignoresCtx{}
	d := hedgeDecider(b, 100*time.Millisecond)
	d.Timeout = 500 * time.Millisecond
	start := time.Now()
	a := d.Ask(context.Background(), Request{Question: Question{Name: "mode", Options: map[string]string{"web": ""}}})
	if a.Undecided != UndecidedTimeout || time.Since(start) > 2*time.Second {
		t.Fatalf("answer %+v after %v", a, time.Since(start))
	}
}

func TestHedge_FirstBackendBothFailThenSecondBackendAnswers(t *testing.T) {
	first := &seqBackend{delays: []time.Duration{150 * time.Millisecond}, err: errors.New("502")}
	second := &seqBackend{delays: []time.Duration{10 * time.Millisecond}, a: ans("web", 0.9, map[string]float64{"web": 1})}
	d := hedgeDecider(first, 50*time.Millisecond)
	d.Backends = []Backend{first, second}
	a := d.Ask(context.Background(), Request{Question: Question{Name: "mode", Options: map[string]string{"web": ""}}})
	if !a.Decided || a.Choice != "web" || first.calls.Load() != 2 || second.calls.Load() != 1 {
		t.Fatalf("answer %+v, first calls %d, second calls %d", a, first.calls.Load(), second.calls.Load())
	}
}

// nearDeadline answers its first call never (it waits for its context) and
// later calls just before the context's deadline, so a hedged win lands
// inside billGrace of the timeout whatever the scheduling jitter.
type nearDeadline struct {
	a     Answer
	calls atomic.Int32
}

func (n *nearDeadline) Decide(ctx context.Context, _ Request) (Answer, error) {
	if n.calls.Add(1) == 1 {
		<-ctx.Done()
		return Answer{}, ctx.Err()
	}
	dl, _ := ctx.Deadline()
	select {
	case <-time.After(time.Until(dl) - 12*time.Millisecond):
		return n.a, nil
	case <-ctx.Done():
		return Answer{}, ctx.Err()
	}
}

func TestHedge_WinJustBeforeTimeoutIsDecidedAndBilled(t *testing.T) {
	c := 0.000003
	a0 := ans("web", 0.9, map[string]float64{"web": 1})
	a0.CostUSD = &c
	b := &nearDeadline{a: a0}
	var n atomic.Int32
	d := hedgeDecider(b, 100*time.Millisecond)
	d.Timeout = 400 * time.Millisecond
	d.Usage = func(cost.Usage) error { n.Add(1); return nil }
	a := d.Ask(context.Background(), Request{Question: Question{Name: "mode", Options: map[string]string{"web": ""}}})
	if !a.Decided || a.Choice != "web" || !a.Hedged {
		t.Fatalf("answer %+v; a hedged win received before the deadline must be Decided", a)
	}
	if n.Load() != 1 {
		t.Fatalf("usage lines = %d, want 1 (the winner is billed)", n.Load())
	}
}

package decide

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/cost"
)

// fakeBackend returns a fixed answer or error, optionally after a delay
// during which it ignores its context.
type fakeBackend struct {
	a     Answer
	err   error
	delay time.Duration
	calls int
}

func (f *fakeBackend) Decide(_ context.Context, _ Request) (Answer, error) {
	f.calls++
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return f.a, f.err
}

func ans(choice string, conf float64, probs map[string]float64) Answer {
	c := 0.001
	return Answer{Choice: choice, Confidence: conf, Probabilities: probs, Backend: "fake", Model: "fake-model", CostUSD: &c}
}

func noUsage(cost.Usage) error { return nil }

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestDecider_AllowedRenormalized(t *testing.T) {
	b := &fakeBackend{a: ans("web", 0.6, map[string]float64{"page": 0.2, "web": 0.6, "workspace": 0.2})}
	d := &Decider{Name: "t", Backends: []Backend{b}, Threshold: 0.25, Timeout: time.Second, Usage: noUsage}
	a := d.Ask(context.Background(), Request{State: "s", Question: testQuestion, Allowed: []string{"page", "workspace"}})
	if _, ok := a.Probabilities["web"]; ok {
		t.Fatalf("disallowed option kept in probabilities: %v", a.Probabilities)
	}
	if !near(a.Probabilities["page"], 0.5) || !near(a.Probabilities["workspace"], 0.5) {
		t.Fatalf("not renormalized: %v", a.Probabilities)
	}
	if a.Choice != "page" || a.Decided || a.Undecided != UndecidedLowConfidence {
		t.Fatalf("answer %+v", a)
	}
}

// The model's own pick is filtered out: its second pick is offered as a
// suggestion but never auto-decided, however its renormalized share looks.
func TestDecider_FilteredChoiceNeverDecides(t *testing.T) {
	b := &fakeBackend{a: ans("page", 0.45, map[string]float64{"page": 0.68, "web": 0.20, "workspace": 0.12})}
	d := &Decider{Name: "t", Backends: []Backend{b}, Threshold: 0.25, Timeout: time.Second, Usage: noUsage}
	a := d.Ask(context.Background(), Request{State: "s", Question: testQuestion, Allowed: []string{"web", "workspace"}})
	if a.Choice != "web" || a.Decided || a.Undecided != UndecidedLowConfidence {
		t.Fatalf("answer %+v", a)
	}
	if !near(a.Confidence, 0.20/0.32) {
		t.Fatalf("confidence %v, want the renormalized top probability %v", a.Confidence, 0.20/0.32)
	}
}

func TestDecider_AllowedKeepsBackendConfidence(t *testing.T) {
	b := &fakeBackend{a: ans("workspace", 0.4, map[string]float64{"page": 0.1, "web": 0.2, "workspace": 0.7})}
	d := &Decider{Name: "t", Backends: []Backend{b}, Threshold: 0.25, Timeout: time.Second, Usage: noUsage}
	a := d.Ask(context.Background(), Request{State: "s", Question: testQuestion, Allowed: []string{"workspace", "web"}})
	if a.Choice != "workspace" || a.Confidence != 0.4 || !a.Decided {
		t.Fatalf("answer %+v", a)
	}
	if !near(a.Probabilities["workspace"], 0.7/0.9) {
		t.Fatalf("probabilities %v", a.Probabilities)
	}
}

func TestDecider_Threshold(t *testing.T) {
	for _, tc := range []struct {
		conf    float64
		decided bool
	}{{0.25, true}, {0.249, false}, {0.9, true}} {
		b := &fakeBackend{a: ans("web", tc.conf, map[string]float64{"web": 0.6, "page": 0.4})}
		d := &Decider{Name: "t", Backends: []Backend{b}, Threshold: 0.25, Timeout: time.Second, Usage: noUsage}
		a := d.Ask(context.Background(), Request{State: "s", Question: testQuestion})
		if a.Decided != tc.decided || a.Choice != "web" {
			t.Errorf("confidence %v: %+v", tc.conf, a)
		}
		if tc.decided != (a.Undecided == "") {
			t.Errorf("confidence %v: undecided %q", tc.conf, a.Undecided)
		}
	}
}

// A caller that gives up (its ctx canceled) is not a timeout: the log
// must not count it against the backend.
func TestDecider_ParentCanceledIsNotTimeout(t *testing.T) {
	slow := &fakeBackend{a: ans("web", 0.9, nil), delay: 3 * time.Second}
	d := &Decider{Name: "t", Backends: []Backend{slow}, Threshold: 0.25, Timeout: time.Second, Usage: noUsage}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	a := d.Ask(ctx, Request{State: "s", Question: testQuestion})
	if a.Decided || a.Undecided != UndecidedCanceled {
		t.Fatalf("answer %+v, want undecided %q", a, UndecidedCanceled)
	}
}

func TestDecider_TimeoutTotal(t *testing.T) {
	// two slow backends that ignore ctx: the total, not each, is bounded
	slow1 := &fakeBackend{a: ans("web", 0.9, nil), delay: 3 * time.Second}
	slow2 := &fakeBackend{a: ans("web", 0.9, nil), delay: 3 * time.Second}
	d := &Decider{Name: "t", Backends: []Backend{slow1, slow2}, Threshold: 0.25, Timeout: 300 * time.Millisecond, Usage: noUsage}
	start := time.Now()
	a := d.Ask(context.Background(), Request{State: "s", Question: testQuestion})
	if el := time.Since(start); el > 300*time.Millisecond+200*time.Millisecond {
		t.Fatalf("Ask took %v", el)
	}
	if a.Decided || a.Undecided != UndecidedTimeout || a.Choice != "" {
		t.Fatalf("answer %+v", a)
	}
}

func TestDecider_FallbackChain(t *testing.T) {
	first := &fakeBackend{err: errors.New("boom")}
	second := &fakeBackend{a: ans("page", 0.8, map[string]float64{"page": 0.9, "web": 0.1})}
	var lines []cost.Usage
	d := &Decider{Name: "t", Backends: []Backend{first, second}, Threshold: 0.25, Timeout: time.Second,
		Usage: func(u cost.Usage) error { lines = append(lines, u); return nil }}
	a := d.Ask(context.Background(), Request{State: "s", Question: testQuestion})
	if first.calls != 1 || second.calls != 1 || a.Choice != "page" || !a.Decided {
		t.Fatalf("calls %d/%d, answer %+v", first.calls, second.calls, a)
	}
	if len(lines) != 1 {
		t.Fatalf("usage lines %d, want 1 (failed calls record nothing)", len(lines))
	}
}

func TestDecider_AllFail(t *testing.T) {
	noKey := &fakeBackend{a: Answer{Undecided: UndecidedNoKey}, err: ErrNoKey}
	d := &Decider{Name: "t", Backends: []Backend{noKey}, Timeout: time.Second, Usage: noUsage}
	if a := d.Ask(context.Background(), Request{State: "s", Question: testQuestion}); a.Undecided != UndecidedNoKey || a.Decided {
		t.Fatalf("answer %+v", a)
	}
	d.Backends = []Backend{noKey, &fakeBackend{err: errors.New("x")}}
	if a := d.Ask(context.Background(), Request{State: "s", Question: testQuestion}); a.Undecided != UndecidedError {
		t.Fatalf("answer %+v", a)
	}
	keyErr := &fakeBackend{a: Answer{Undecided: UndecidedKeyError}, err: ErrKeyRead}
	d.Backends = []Backend{noKey, keyErr}
	if a := d.Ask(context.Background(), Request{State: "s", Question: testQuestion}); a.Undecided != UndecidedKeyError {
		t.Fatalf("key read failure must not look like a missing key: %+v", a)
	}
	d.Backends = nil
	if a := d.Ask(context.Background(), Request{State: "s", Question: testQuestion}); a.Undecided != UndecidedError || a.ID == "" {
		t.Fatalf("answer %+v", a)
	}
}

func TestDecider_UsageLabel(t *testing.T) {
	var lines []cost.Usage
	b := &fakeBackend{a: ans("web", 0.9, map[string]float64{"web": 1})}
	d := &Decider{Name: "picker", Backends: []Backend{b}, Threshold: 0.25, Timeout: time.Second,
		Usage: func(u cost.Usage) error { lines = append(lines, u); return nil }}
	a := d.Ask(context.Background(), Request{State: "s", Question: testQuestion})
	if len(lines) != 1 || lines[0].Label != "decide:picker" || lines[0].Model != "fake-model" {
		t.Fatalf("usage %+v", lines)
	}
	if lines[0].CostUSD == nil || *lines[0].CostUSD != 0.001 {
		t.Fatalf("cost %v", lines[0].CostUSD)
	}
	if !strings.HasPrefix(a.ID, "dec_") || a.Backend != "fake" {
		t.Fatalf("answer %+v", a)
	}
}

func TestDecider_ChatAnswerWithoutProbabilities(t *testing.T) {
	b := &fakeBackend{a: ans("web", 0.8, nil)}
	d := &Decider{Name: "t", Backends: []Backend{b}, Threshold: 0.25, Timeout: time.Second, Usage: noUsage}
	if a := d.Ask(context.Background(), Request{State: "s", Question: testQuestion}); a.Choice != "web" || !a.Decided {
		t.Fatalf("answer %+v", a)
	}
	a := d.Ask(context.Background(), Request{State: "s", Question: testQuestion, Allowed: []string{"page"}})
	if a.Choice != "" || a.Decided || a.Undecided != UndecidedLowConfidence {
		t.Fatalf("disallowed choice without probabilities must be undecided: %+v", a)
	}
}

func TestDecider_PerBackendThreshold(t *testing.T) {
	failing := &fakeBackend{err: errors.New("down")}
	haiku := &fakeBackend{a: ans("web", 0.5, nil)}
	d := &Decider{Name: "t", Backends: []Backend{failing, WithThreshold(haiku, 0.7)}, Threshold: 0.25, Timeout: time.Second, Usage: noUsage}
	a := d.Ask(context.Background(), Request{State: "s", Question: testQuestion})
	if a.Decided || a.Threshold != 0.7 || a.Backend != "fake" {
		t.Fatalf("0.5 under the fallback's 0.7 must be undecided: %+v", a)
	}
	haiku.a.Confidence = 0.7
	if a := d.Ask(context.Background(), Request{State: "s", Question: testQuestion}); !a.Decided {
		t.Fatalf("answer %+v", a)
	}
	d.Backends = []Backend{haiku}
	haiku.a.Confidence = 0.3
	if a := d.Ask(context.Background(), Request{State: "s", Question: testQuestion}); !a.Decided || a.Threshold != 0.25 {
		t.Fatalf("without a backend threshold the decider's applies: %+v", a)
	}
}

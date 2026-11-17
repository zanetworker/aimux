package toolgateway

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

const (
	searchV1      = `{"name":"search","description":"v1","inputSchema":{"type":"object","properties":{"q":{"type":"string"}}}}`
	searchChanged = `{"name":"search","description":"v1","inputSchema":{"type":"object","properties":{"q":{"type":"number"}}}}`
	searchReword  = `{"name":"search","description":"reworded","title":"New","inputSchema":{"type":"object","properties":{"q":{"type":"string"}}}}`
)

type fakeList struct {
	mu    sync.Mutex
	calls int
	tools []string
	err   error
}

func (f *fakeList) list(_ context.Context, _ string) ([]json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	var out []json.RawMessage
	for _, t := range f.tools {
		out = append(out, json.RawMessage(t))
	}
	return out, nil
}

func (f *fakeList) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newVerifier(t *testing.T, f *fakeList) (*Verifier, *clock) {
	t.Helper()
	clk := &clock{t: time.Unix(1000, 0)}
	return &Verifier{
		Pins: Pins{"jira": {"search": mustPin(t, searchV1)}},
		List: f.list,
		Now:  clk.now,
	}, clk
}

func TestVerifier_SameNameChangedSchemaIsRefused(t *testing.T) {
	f := &fakeList{tools: []string{searchV1}}
	v, clk := newVerifier(t, f)
	if err := v.Check(context.Background(), "jira", "search"); err != nil {
		t.Fatalf("unchanged tool refused: %v", err)
	}
	f.tools = []string{searchChanged} // the backend swaps the schema under the same name
	clk.t = clk.t.Add(61 * time.Second)
	err := v.Check(context.Background(), "jira", "search")
	if !errors.Is(err, ErrToolChanged) {
		t.Fatalf("Check = %v, want ErrToolChanged", err)
	}
}

func TestVerifier_ToolGoneFromBackendIsRefused(t *testing.T) {
	f := &fakeList{tools: []string{`{"name":"other","inputSchema":{}}`}}
	v, _ := newVerifier(t, f)
	if err := v.Check(context.Background(), "jira", "search"); !errors.Is(err, ErrToolChanged) {
		t.Fatalf("Check = %v, want ErrToolChanged", err)
	}
}

func TestVerifier_DescriptionOnlyChangeStaysAllowed(t *testing.T) {
	f := &fakeList{tools: []string{searchReword}}
	v, _ := newVerifier(t, f)
	if err := v.Check(context.Background(), "jira", "search"); err != nil {
		t.Fatalf("description/title change refused: %v", err)
	}
}

func TestVerifier_CachesForMaxAge(t *testing.T) {
	f := &fakeList{tools: []string{searchV1}}
	v, clk := newVerifier(t, f)
	ctx := context.Background()
	_ = v.Check(ctx, "jira", "search")
	clk.t = clk.t.Add(10 * time.Second)
	_ = v.Check(ctx, "jira", "search")
	if n := f.count(); n != 1 {
		t.Fatalf("List called %d times within MaxAge, want 1", n)
	}
	clk.t = clk.t.Add(51 * time.Second) // 61s since the listing
	_ = v.Check(ctx, "jira", "search")
	if n := f.count(); n != 2 {
		t.Fatalf("List called %d times after MaxAge, want 2", n)
	}
}

func TestVerifier_InvalidateForcesRelist(t *testing.T) {
	f := &fakeList{tools: []string{searchV1}}
	v, _ := newVerifier(t, f)
	ctx := context.Background()
	_ = v.Check(ctx, "jira", "search")
	v.Invalidate("jira")
	_ = v.Check(ctx, "jira", "search")
	if n := f.count(); n != 2 {
		t.Fatalf("List called %d times, want 2", n)
	}
}

func TestVerifier_ListErrorFailsClosedThenRecovers(t *testing.T) {
	f := &fakeList{err: errors.New("backend down")}
	v, _ := newVerifier(t, f)
	ctx := context.Background()
	err := v.Check(ctx, "jira", "search")
	if !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("Check = %v, want ErrVerifyFailed", err)
	}
	f.mu.Lock()
	f.err, f.tools = nil, []string{searchV1}
	f.mu.Unlock()
	if err := v.Check(ctx, "jira", "search"); err != nil {
		t.Fatalf("Check after recovery = %v", err)
	}
}

func TestVerifier_UnpinnedToolRefused(t *testing.T) {
	f := &fakeList{tools: []string{searchV1, `{"name":"extra","inputSchema":{}}`}}
	v, _ := newVerifier(t, f)
	if err := v.Check(context.Background(), "jira", "extra"); !errors.Is(err, ErrToolNotPinned) {
		t.Fatalf("Check = %v, want ErrToolNotPinned", err)
	}
	if err := v.Check(context.Background(), "nowhere", "search"); !errors.Is(err, ErrToolNotPinned) {
		t.Fatalf("unknown server: Check = %v, want ErrToolNotPinned", err)
	}
}

func TestVerifier_ConcurrentChecksShareOneListing(t *testing.T) {
	f := &fakeList{tools: []string{searchV1}}
	v, _ := newVerifier(t, f)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := v.Check(context.Background(), "jira", "search"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := f.count(); n != 1 {
		t.Fatalf("List called %d times, want 1", n)
	}
}

func TestPins_GuardChecksWithListFunc(t *testing.T) {
	f := &fakeList{tools: []string{searchChanged}}
	pins := Pins{"jira": {"search": mustPin(t, searchV1)}}
	guard := pins.Guard(f.list)
	if err := guard(context.Background(), "jira", "search"); !errors.Is(err, ErrToolChanged) {
		t.Fatalf("guard = %v, want ErrToolChanged", err)
	}
}

// An Invalidate that arrives while a Check is listing does not wait for
// the listing, and the next Check lists again (no MaxAge wait) and sees the
// changed tool.
func TestVerifier_InvalidateDuringCheck_NextCallRelists(t *testing.T) {
	f := &fakeList{tools: []string{searchV1}}
	entered, release := make(chan struct{}), make(chan struct{})
	var first sync.Once
	v := Pins{"jira": {"search": mustPin(t, searchV1)}}.Verifier(func(ctx context.Context, s string) ([]json.RawMessage, error) {
		raws, err := f.list(ctx, s) // the listing as it was when it was read
		first.Do(func() { close(entered); <-release })
		return raws, err
	})
	clk := &clock{t: time.Unix(1000, 0)}
	v.Now = clk.now // never advances: only Invalidate can force a new listing
	if v.MaxAge != 0 {
		t.Fatalf("MaxAge = %v, want the default", v.MaxAge)
	}
	checked := make(chan error, 1)
	go func() { checked <- v.Check(context.Background(), "jira", "search") }()
	<-entered
	f.mu.Lock()
	f.tools = []string{searchChanged} // the backend changes the tool mid-listing
	f.mu.Unlock()
	invalidated := make(chan struct{})
	go func() { v.Invalidate("jira"); close(invalidated) }()
	select {
	case <-invalidated:
	case <-time.After(time.Second):
		t.Fatal("Invalidate waited for the listing in flight")
	}
	close(release)
	if err := <-checked; err != nil {
		t.Fatalf("first Check = %v, want nil (it listed the tool before the change)", err)
	}
	if err := v.Check(context.Background(), "jira", "search"); !errors.Is(err, ErrToolChanged) {
		t.Fatalf("Check after Invalidate = %v, want ErrToolChanged", err)
	}
	if n := f.count(); n != 2 {
		t.Fatalf("listed %d times, want 2", n)
	}
	// Without another Invalidate the new listing is reused.
	_ = v.Check(context.Background(), "jira", "search")
	if n := f.count(); n != 2 {
		t.Fatalf("listed %d times after a cached Check, want 2", n)
	}
}

// SnapshotPins.Changed invalidates the verifier its Guard checks with, so
// the next call lists again, and tells onChanged; pins built without
// GatewayPins (no verifier set) still take the call.
func TestSnapshotPins_ChangedRelistsOnNextCall(t *testing.T) {
	f := &fakeList{tools: []string{searchV1}}
	var heard []string
	p := SnapshotPins{Pins: Pins{"jira": {"search": mustPin(t, searchV1)}}, verifiers: &verifierSet{}, onChanged: func(s string) { heard = append(heard, s) }}
	check := p.Guard(f.list)
	if err := check(context.Background(), "jira", "search"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.tools = []string{searchChanged}
	f.mu.Unlock()
	if err := check(context.Background(), "jira", "search"); err != nil {
		t.Fatalf("cached Check = %v, want nil before any notification", err)
	}
	p.Changed("other") // another server's change keeps jira's listing
	if err := check(context.Background(), "jira", "search"); err != nil || f.count() != 1 {
		t.Fatalf("after Changed(other): %v, %d listings, want nil and 1", err, f.count())
	}
	p.Changed("jira")
	if err := check(context.Background(), "jira", "search"); !errors.Is(err, ErrToolChanged) {
		t.Fatalf("Check after Changed = %v, want ErrToolChanged", err)
	}
	if len(heard) != 2 || heard[0] != "other" || heard[1] != "jira" {
		t.Fatalf("onChanged heard %v", heard)
	}
	SnapshotPins{Pins: p.Pins}.Changed("jira") // no verifier set, no hook: a no-op
}

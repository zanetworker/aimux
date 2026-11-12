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

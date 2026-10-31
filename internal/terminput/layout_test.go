package terminput

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeLayoutSource struct {
	mu    sync.Mutex
	panes []PaneRef
	err   error
	calls int
	block chan struct{} // when set, Layout waits for it to close
}

func (f *fakeLayoutSource) Layout(context.Context) ([]PaneRef, error) {
	f.mu.Lock()
	f.calls++
	panes, err, block := f.panes, f.err, f.block
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	return panes, err
}

func (f *fakeLayoutSource) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeLayoutSource) setErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

// settle waits for the running background refresh, if any.
func (r *LayoutReader) settle() {
	r.mu.Lock()
	ch := r.inflight
	r.mu.Unlock()
	if ch != nil {
		<-ch
	}
}

type fakeLayoutSys struct {
	ttys map[int]string
	fg   map[int]bool
}

func (f *fakeLayoutSys) TTYs([]int) (map[int]string, error)     { return f.ttys, nil }
func (f *fakeLayoutSys) Foreground([]int) (map[int]bool, error) { return f.fg, nil }

var layoutNow = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func newReader(src *fakeLayoutSource, sys *fakeLayoutSys, sessions map[string]int) *LayoutReader {
	return &LayoutReader{
		Source:   src,
		Sys:      sys,
		Sessions: func() map[string]int { return sessions },
		Now:      func() time.Time { return layoutNow },
	}
}

func TestPlaces_JoinsByTTY(t *testing.T) {
	src := &fakeLayoutSource{panes: []PaneRef{
		{WindowID: 9, WindowName: "Work", ActiveName: "a", Tab: 1, TabName: "build", Pane: 1, Panes: 2, TTY: "/dev/ttys001"},
		{WindowID: 9, WindowName: "Work", ActiveName: "a", Tab: 1, TabName: "build", Pane: 2, Panes: 2, TTY: "/dev/ttys002"},
	}}
	sys := &fakeLayoutSys{ttys: map[int]string{11: "/dev/ttys001", 12: "/dev/ttys002"}, fg: map[int]bool{11: true, 12: true}}
	got := newReader(src, sys, map[string]int{"s1": 11, "s2": 12}).Places(context.Background())
	if len(got) != 2 {
		t.Fatalf("got %d places: %+v", len(got), got)
	}
	want := MacPlace{Window: MacWindow{ID: "w9", Index: 1, Name: "Work"}, Tab: MacTab{Index: 1, Name: "build"}, Pane: 2, Panes: 2}
	if got["s2"] != want {
		t.Errorf("s2 = %+v, want %+v", got["s2"], want)
	}
	if got["s1"].Pane != 1 || got["s1"].Window.ID != "w9" {
		t.Errorf("s1 = %+v", got["s1"])
	}
}

func TestPlaces_UnmatchedOrShellIsAbsent(t *testing.T) {
	src := &fakeLayoutSource{panes: []PaneRef{
		{WindowID: 1, Tab: 1, Pane: 1, Panes: 1, TTY: "/dev/ttys001"},
		{WindowID: 1, Tab: 2, Pane: 1, Panes: 1, TTY: "/dev/ttys002"},
	}}
	sys := &fakeLayoutSys{
		ttys: map[int]string{11: "/dev/ttys001", 12: "/dev/ttys099", 14: "/dev/ttys002"}, // 13 has no tty
		fg:   map[int]bool{11: true, 12: true, 14: false},
	}
	got := newReader(src, sys, map[string]int{"ok": 11, "nopane": 12, "notty": 13, "shell": 14}).Places(context.Background())
	if len(got) != 1 {
		t.Fatalf("want only ok, got %+v", got)
	}
	if _, ok := got["ok"]; !ok {
		t.Errorf("ok missing: %+v", got)
	}
}

func TestPlaces_WindowNames(t *testing.T) {
	src := &fakeLayoutSource{panes: []PaneRef{
		{WindowID: 9, WindowName: "claude", ActiveName: "claude", Tab: 1, Pane: 1, Panes: 1, TTY: "/dev/ttys001"},
		{WindowID: 4, WindowName: "Project X", ActiveName: "claude", Tab: 1, Pane: 1, Panes: 1, TTY: "/dev/ttys002"},
		{WindowID: 7, WindowName: "claude (zsh)", ActiveName: "claude", Tab: 1, Pane: 1, Panes: 1, TTY: "/dev/ttys003"},
	}}
	sys := &fakeLayoutSys{
		ttys: map[int]string{1: "/dev/ttys001", 2: "/dev/ttys002", 3: "/dev/ttys003"},
		fg:   map[int]bool{1: true, 2: true, 3: true},
	}
	got := newReader(src, sys, map[string]int{"a": 1, "b": 2, "c": 3}).Places(context.Background())
	if w := got["b"].Window; w.Index != 1 || w.Name != "Project X" {
		t.Errorf("renamed window = %+v", w)
	}
	if w := got["c"].Window; w.Index != 2 || w.Name != "Window 2" {
		t.Errorf("suffix-echo window = %+v", w)
	}
	if w := got["a"].Window; w.Index != 3 || w.Name != "Window 3" {
		t.Errorf("echo window = %+v", w)
	}
}

func TestPlaces_LastInFrontWithin12h(t *testing.T) {
	src := &fakeLayoutSource{panes: []PaneRef{{WindowID: 1, Tab: 1, Pane: 1, Panes: 1, TTY: "/dev/ttys001"}}}
	sys := &fakeLayoutSys{ttys: map[int]string{11: "/dev/ttys001"}, fg: map[int]bool{11: true}}
	for _, c := range []struct {
		age  time.Duration
		want bool
	}{{43200 * time.Second, true}, {43201 * time.Second, false}} {
		r := newReader(src, sys, map[string]int{"s1": 11})
		r.LastOnMac = func() (string, time.Time, bool) { return "s1", layoutNow.Add(-c.age), true }
		if got := r.Places(context.Background())["s1"].LastInFront; got != c.want {
			t.Errorf("age %v: LastInFront = %v, want %v", c.age, got, c.want)
		}
	}
	r := newReader(src, sys, map[string]int{"s1": 11})
	r.LastOnMac = func() (string, time.Time, bool) { return "other", layoutNow, true }
	if r.Places(context.Background())["s1"].LastInFront {
		t.Error("another session in front must not set LastInFront")
	}
}

func TestPlaces_CachesAndHoldsLastGood(t *testing.T) {
	src := &fakeLayoutSource{panes: []PaneRef{{WindowID: 1, WindowName: "SECRETWIN", Tab: 1, Pane: 1, Panes: 1, TTY: "/dev/ttys001"}}}
	sys := &fakeLayoutSys{ttys: map[int]string{11: "/dev/ttys001"}, fg: map[int]bool{11: true}}
	clock := layoutNow
	var log bytes.Buffer
	r := newReader(src, sys, map[string]int{"s1": 11})
	r.Now = func() time.Time { return clock }
	r.Log = &log

	r.Places(context.Background())
	clock = clock.Add(2 * time.Second)
	r.Places(context.Background())
	if src.count() != 1 {
		t.Fatalf("source read %d times within 3s, want 1", src.count())
	}

	src.setErr(errors.New("boom SECRETWIN"))
	clock = layoutNow.Add(10 * time.Second)
	if got := r.Places(context.Background()); len(got) != 1 {
		t.Errorf("10s after good, failing read gave %+v, want last good", got)
	}
	r.settle()
	clock = layoutNow.Add(31 * time.Second)
	if got := r.Places(context.Background()); len(got) != 0 {
		t.Errorf("31s after good gave %+v, want nil (served stale one last time or nil)", got)
	}
	r.settle()
	clock = layoutNow.Add(40 * time.Second)
	if got := r.Places(context.Background()); got != nil {
		t.Errorf("31s after good, failing read gave %+v, want nil", got)
	}
	r.settle()
	if log.Len() == 0 {
		t.Error("failure not logged")
	}
	if strings.Contains(log.String(), "SECRETWIN") {
		t.Errorf("log leaks names: %q", log.String())
	}
}

// testClock is a clock tests move while a background read may read it.
type testClock struct{ ns atomic.Int64 }

func newTestClock() *testClock               { c := &testClock{}; c.ns.Store(layoutNow.UnixNano()); return c }
func (c *testClock) now() time.Time          { return time.Unix(0, c.ns.Load()).UTC() }
func (c *testClock) advance(d time.Duration) { c.ns.Add(int64(d)) }

func slowReader(src *fakeLayoutSource, clock *testClock) *LayoutReader {
	sys := &fakeLayoutSys{ttys: map[int]string{11: "/dev/ttys001"}, fg: map[int]bool{11: true}}
	r := newReader(src, sys, map[string]int{"s1": 11})
	r.Now = clock.now
	r.FirstWait = 30 * time.Millisecond
	return r
}

func onePane() []PaneRef {
	return []PaneRef{{WindowID: 1, Tab: 1, Pane: 1, Panes: 1, TTY: "/dev/ttys001"}}
}

func TestPlaces_SlowRefreshNeverBlocksAndRunsOnce(t *testing.T) {
	src := &fakeLayoutSource{panes: onePane()}
	clock := newTestClock()
	r := slowReader(src, clock)
	r.Places(context.Background()) // first read, quick
	src.mu.Lock()
	src.block = make(chan struct{})
	src.mu.Unlock()
	clock.advance(5 * time.Second)

	var wg sync.WaitGroup
	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := r.Places(context.Background()); len(got) != 1 {
				t.Errorf("during a slow refresh got %+v, want the cached place", got)
			}
		}()
	}
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Places waited on the slow read")
	}
	for i := 0; i < 200 && src.count() < 2; i++ {
		time.Sleep(5 * time.Millisecond) // the refresh goroutine starts
	}
	time.Sleep(20 * time.Millisecond)
	if src.count() != 2 {
		t.Errorf("Layout called %d times, want 2 (first read plus one refresh)", src.count())
	}
	close(src.block)
	r.settle()
}

func TestPlaces_FailureBacksOffThreeSeconds(t *testing.T) {
	src := &fakeLayoutSource{panes: onePane()}
	clock := newTestClock()
	r := slowReader(src, clock)
	r.Places(context.Background())
	src.setErr(errors.New("down"))
	clock.advance(5 * time.Second)
	r.Places(context.Background())
	r.settle()
	if src.count() != 2 {
		t.Fatalf("calls = %d, want 2", src.count())
	}
	clock.advance(2 * time.Second)
	if got := r.Places(context.Background()); len(got) != 1 {
		t.Errorf("stale within 30s during failure gave %+v, want last good", got)
	}
	r.settle()
	if src.count() != 2 {
		t.Errorf("read again %v after a failure: calls = %d, want 2", 2*time.Second, src.count())
	}
	clock.advance(2 * time.Second)
	r.Places(context.Background())
	r.settle()
	if src.count() != 3 {
		t.Errorf("calls = %d after the back-off, want 3", src.count())
	}
}

func TestPlaces_FirstCallBoundedWhenSourceHangs(t *testing.T) {
	src := &fakeLayoutSource{panes: onePane(), block: make(chan struct{})}
	clock := newTestClock()
	r := slowReader(src, clock)
	start := time.Now()
	if got := r.Places(context.Background()); got != nil {
		t.Errorf("hung source gave %+v, want nil", got)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("first call took %v, want about FirstWait", d)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start = time.Now()
	r.FirstWait = time.Minute
	r.Places(ctx)
	if d := time.Since(start); d > time.Second {
		t.Errorf("cancelled ctx still waited %v", d)
	}
	close(src.block)
	r.settle()
}

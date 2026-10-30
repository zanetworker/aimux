package terminput

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// focusSystem answers idle time and a tty per pid, counting tty lookups.
type focusSystem struct {
	idle    float64
	idleErr error
	ttys    map[int]string
	lookups int
}

func (f *focusSystem) IdleSeconds() (float64, error) { return f.idle, f.idleErr }
func (f *focusSystem) TTYs(pids []int) (map[int]string, error) {
	f.lookups++
	out := map[int]string{}
	for _, pid := range pids {
		if t, ok := f.ttys[pid]; ok {
			out[pid] = t
		}
	}
	return out, nil
}

// focusReporter answers a fixed focus and counts the questions.
type focusReporter struct {
	front bool
	tty   string
	err   error
	asked int
}

func (f *focusReporter) Focused(context.Context) (bool, string, error) {
	f.asked++
	return f.front, f.tty, f.err
}

func newLastFocus(sys *focusSystem, fr *focusReporter, now *time.Time) *LastFocus {
	return &LastFocus{
		Enabled: true, Sys: sys, Focus: fr,
		Sessions: func() map[string]int { return map[string]int{"s-a": 10, "s-b": 20} },
		Now:      func() time.Time { return *now },
	}
}

func TestLastFocus_RecordsTheFocusedSessionWhileTheMacIsInUse(t *testing.T) {
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	sys := &focusSystem{idle: 3, ttys: map[int]string{10: "/dev/ttys001", 20: "/dev/ttys002"}}
	fr := &focusReporter{front: true, tty: "/dev/ttys002"}
	f := newLastFocus(sys, fr, &now)
	if _, _, ok := f.Last(); ok {
		t.Fatal("Last before any probe: want unknown")
	}
	f.Probe(context.Background())
	id, at, ok := f.Last()
	if !ok || id != "s-b" || !at.Equal(now) {
		t.Fatalf("Last = %q %v %v, want s-b at %v", id, at, ok, now)
	}
	if sys.lookups != 1 {
		t.Fatalf("%d tty lookups for one probe, want one batched lookup", sys.lookups)
	}
	// the owner moves to the other pane
	now = now.Add(15 * time.Second)
	fr.tty = "/dev/ttys001"
	f.Probe(context.Background())
	if id, at, _ := f.Last(); id != "s-a" || !at.Equal(now) {
		t.Fatalf("Last = %q %v, want s-a at %v", id, at, now)
	}
}

func TestLastFocus_KeepsTheLastValueWhenNothingIsKnownNow(t *testing.T) {
	start := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		change    func(*focusSystem, *focusReporter)
		wantAsked bool // whether the terminal was asked at all
	}{
		{"idle at the limit", func(s *focusSystem, _ *focusReporter) { s.idle = 120 }, false},
		{"idle for hours", func(s *focusSystem, _ *focusReporter) { s.idle = 4 * 3600 }, false},
		{"idle probe fails", func(s *focusSystem, _ *focusReporter) { s.idleErr = errors.New("ioreg") }, false},
		{"iTerm2 not in front", func(_ *focusSystem, r *focusReporter) { r.front = false }, true},
		{"focus probe fails", func(_ *focusSystem, r *focusReporter) { r.err = errors.New("jxa") }, true},
		{"focused pane holds no session", func(_ *focusSystem, r *focusReporter) { r.tty = "/dev/ttys009" }, true},
		{"no focused pane", func(_ *focusSystem, r *focusReporter) { r.tty = "" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := start
			sys := &focusSystem{idle: 1, ttys: map[int]string{10: "/dev/ttys001", 20: "/dev/ttys002"}}
			fr := &focusReporter{front: true, tty: "/dev/ttys001"}
			f := newLastFocus(sys, fr, &now)
			f.Probe(context.Background())
			now = now.Add(time.Minute)
			fr.tty = "/dev/ttys002"
			tc.change(sys, fr)
			fr.asked = 0
			f.Probe(context.Background())
			if id, at, ok := f.Last(); !ok || id != "s-a" || !at.Equal(start) {
				t.Fatalf("Last = %q %v %v, want s-a kept from %v", id, at, ok, start)
			}
			if (fr.asked > 0) != tc.wantAsked {
				t.Fatalf("terminal asked %d times, want asked=%v", fr.asked, tc.wantAsked)
			}
		})
	}
}

func TestLastFocus_JustUnderTheIdleLimitCounts(t *testing.T) {
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	sys := &focusSystem{idle: 119.9, ttys: map[int]string{10: "/dev/ttys001"}}
	f := newLastFocus(sys, &focusReporter{front: true, tty: "/dev/ttys001"}, &now)
	f.Probe(context.Background())
	if id, _, ok := f.Last(); !ok || id != "s-a" {
		t.Fatalf("Last = %q %v, want s-a", id, ok)
	}
}

func TestLastFocus_OffAsksNothing(t *testing.T) {
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	sys := &focusSystem{idle: 1, ttys: map[int]string{10: "/dev/ttys001"}}
	fr := &focusReporter{front: true, tty: "/dev/ttys001"}
	f := newLastFocus(sys, fr, &now)
	f.Enabled = false
	f.Probe(context.Background())
	if _, _, ok := f.Last(); ok || fr.asked != 0 {
		t.Fatalf("terminal input off: Last known=%v, asked %d; want nothing", ok, fr.asked)
	}
}

func TestLastFocus_RunProbesUntilCancelled(t *testing.T) {
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	sys := &focusSystem{idle: 1, ttys: map[int]string{10: "/dev/ttys001"}}
	f := newLastFocus(sys, &focusReporter{front: true, tty: "/dev/ttys001"}, &now)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); f.Run(ctx, time.Millisecond) }()
	deadline := time.After(2 * time.Second)
	for {
		if _, _, ok := f.Last(); ok {
			break
		}
		select {
		case <-deadline:
			t.Fatal("Run never probed")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop when cancelled")
	}
}

func TestTTYsFrom_OnePSForAllPids(t *testing.T) {
	var calls [][]string
	fake := func(name string, args ...string) (string, error) {
		calls = append(calls, append([]string{name}, args...))
		// pid 30 has no tty; pid 40 is gone, so ps exits 1 but still lists the others
		return "   10 ttys001\n   20 ttys002\n   30 ??\n", errors.New("exit status 1")
	}
	got, err := ttysFrom(fake, []int{10, 20, 30, 40})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || strings.Join(calls[0], " ") != "ps -o pid=,tty= -p 10,20,30,40" {
		t.Fatalf("calls %v, want one ps for every pid", calls)
	}
	if len(got) != 2 || got[10] != "/dev/ttys001" || got[20] != "/dev/ttys002" {
		t.Fatalf("ttys %v", got)
	}
	if _, err := ttysFrom(func(string, ...string) (string, error) { return "", errors.New("exit status 1") }, []int{40}); err == nil {
		t.Fatal("no output and an error: want the error")
	}
	n := 0
	if got, err := ttysFrom(func(string, ...string) (string, error) { n++; return "", nil }, nil); err != nil || len(got) != 0 || n != 0 {
		t.Fatalf("no pids: %v %v, ran ps %d times; want nothing run", got, err, n)
	}
}

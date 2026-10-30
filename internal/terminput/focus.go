package terminput

import (
	"context"
	"sync"
	"time"
)

// FocusSystem is what LastFocus asks the machine: how long it has been
// idle and the ttys of some processes (SystemProbe: one ps for all).
type FocusSystem interface {
	IdleSeconds() (float64, error)
	TTYs(pids []int) (map[int]string, error)
}

// LastFocus remembers the session last in front on the Mac: the terminal
// session with keyboard focus while the Mac was in use (idle under 2
// minutes). It only reads: it never types, focuses or starts anything.
// Nothing is stored on disk.
type LastFocus struct {
	Enabled bool // remote_access.terminal_input.enabled; off, Probe asks nothing
	Sys     FocusSystem
	Focus   FocusReporter
	// Sessions maps each session open in a terminal to the pid holding it,
	// by the registry rule the terminal routes use.
	Sessions func() map[string]int
	Now      func() time.Time

	mu sync.Mutex
	id string
	at time.Time
}

// Last is the session last in front and when it was seen; ok is false
// until one was seen.
func (f *LastFocus) Last() (id string, at time.Time, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.id, f.at, f.id != ""
}

// Probe looks once. Anything unknown (the Mac idle, a probe error, the
// terminal not in front, a pane holding no session) keeps the last value.
func (f *LastFocus) Probe(ctx context.Context) {
	if !f.Enabled {
		return
	}
	idle, err := f.Sys.IdleSeconds()
	if err != nil || idle >= idleRequired.Seconds() {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, driverTimeout)
	front, tty, err := f.Focus.Focused(cctx)
	cancel()
	if err != nil || !front || tty == "" {
		return
	}
	sessions := f.Sessions()
	pids := make([]int, 0, len(sessions))
	for _, pid := range sessions {
		pids = append(pids, pid)
	}
	ttys, err := f.Sys.TTYs(pids)
	if err != nil {
		return
	}
	for id, pid := range sessions {
		if ttys[pid] == tty {
			f.mu.Lock()
			f.id, f.at = id, f.Now()
			f.mu.Unlock()
			return
		}
	}
}

// Run probes every interval until ctx ends.
func (f *LastFocus) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		f.Probe(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

package terminput

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	layoutFresh     = 3 * time.Second  // a read newer than this is reused
	layoutHold      = 30 * time.Second // a failed read falls back to a good one this young
	lastInFrontMax  = 12 * time.Hour
	layoutFirstWait = 1500 * time.Millisecond // a call with no usable layout waits this long at most
	layoutReadMax   = 10 * time.Second        // a background read is cut off here
)

// LayoutSource reads the terminal's windows, tabs and panes.
type LayoutSource interface {
	Layout(ctx context.Context) ([]PaneRef, error)
}

// LayoutSystem is what the join asks the machine, each in one call for
// all pids (SystemProbe: one ps each).
type LayoutSystem interface {
	TTYs(pids []int) (map[int]string, error)
	Foreground(pids []int) (map[int]bool, error)
}

type MacWindow struct {
	ID    string `json:"id"`
	Index int    `json:"index"`
	Name  string `json:"name"`
}

type MacTab struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
}

// MacPlace is where a session sits in the terminal on the Mac.
type MacPlace struct {
	Window      MacWindow `json:"window"`
	Tab         MacTab    `json:"tab"`
	Pane        int       `json:"pane"`
	Panes       int       `json:"panes"`
	LastInFront bool      `json:"lastInFront"`
}

// LayoutReader joins the terminal layout to open sessions by tty. It only
// reads. A read is reused for 3 s, and a failed read falls back to the
// last good one for 30 s.
type LayoutReader struct {
	Source LayoutSource
	Sys    LayoutSystem
	// Sessions maps each open session to the pid holding it.
	Sessions  func() map[string]int
	LastOnMac func() (string, time.Time, bool) // may be nil
	Now       func() time.Time                 // nil means time.Now
	Log       io.Writer                        // may be nil

	// FirstWait bounds how long a call with no usable layout (none yet, or
	// one older than 30 s) waits for the refresh (0 means 1.5 s).
	FirstWait time.Duration

	mu       sync.Mutex
	places   map[string]MacPlace
	at       time.Time
	good     bool          // places holds a read
	inflight chan struct{} // closed when the running refresh ends; nil when none
	failed   bool
	failAt   time.Time
	logged   bool // this run of failures has been logged
}

func (r *LayoutReader) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Places returns each placed session's place; nil when unknown. A session
// whose pid has no tty, whose tty is in no pane, or which is not its
// tty's foreground is absent.
//
// While the cached layout is at most 30 s old, Places never waits: it
// returns it and, when it is older than 3 s, starts one background
// refresh. A failed read backs off for 3 s. With no layout yet, or one
// older than 30 s, it waits for the running refresh (one for all callers),
// for the request ctx and at most FirstWait.
func (r *LayoutReader) Places(ctx context.Context) map[string]MacPlace {
	t := r.now()
	r.mu.Lock()
	if r.good && t.Sub(r.at) < layoutFresh {
		defer r.mu.Unlock()
		return r.places
	}
	backingOff := r.failed && t.Sub(r.failAt) < layoutFresh
	if r.inflight == nil && !backingOff {
		r.inflight = make(chan struct{})
		go r.refresh(r.inflight) //nolint:gosec // the read must outlive the request that started it
	}
	wait, stale := r.inflight, !r.good || t.Sub(r.at) > layoutHold
	r.mu.Unlock()

	if stale && wait != nil {
		bound := r.FirstWait
		if bound <= 0 {
			bound = layoutFirstWait
		}
		timer := time.NewTimer(bound)
		defer timer.Stop()
		select {
		case <-wait:
		case <-ctx.Done():
		case <-timer.C:
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.good && r.now().Sub(r.at) <= layoutHold {
		return r.places
	}
	return nil
}

// refresh reads the layout with its own context, then records the result.
func (r *LayoutReader) refresh(done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), layoutReadMax)
	defer cancel()
	places, err := r.read(ctx, r.now())
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		if r.Log != nil && !r.logged {
			_, _ = fmt.Fprintf(r.Log, "terminput: mac layout read failed (%T)\n", err)
		}
		r.failed, r.failAt, r.logged = true, r.now(), true
	} else {
		r.places, r.at, r.good, r.failed, r.logged = places, r.now(), true, false, false
	}
	r.inflight = nil
	close(done)
}

func (r *LayoutReader) read(ctx context.Context, now time.Time) (map[string]MacPlace, error) {
	panes, err := r.Source.Layout(ctx)
	if err != nil {
		return nil, err
	}
	sessions := r.Sessions()
	pids := make([]int, 0, len(sessions))
	for _, pid := range sessions {
		pids = append(pids, pid)
	}
	ttys, err := r.Sys.TTYs(pids)
	if err != nil {
		return nil, err
	}
	fg, err := r.Sys.Foreground(pids)
	if err != nil {
		return nil, err
	}

	byTTY := map[string]PaneRef{}
	ids := map[int]bool{}
	for _, p := range panes {
		if p.TTY != "" {
			byTTY[p.TTY] = p
		}
		ids[p.WindowID] = true
	}
	order := make([]int, 0, len(ids))
	for id := range ids {
		order = append(order, id)
	}
	sort.Ints(order)
	index := make(map[int]int, len(order))
	for i, id := range order {
		index[id] = i + 1
	}

	var lastID string
	if r.LastOnMac != nil {
		if id, at, ok := r.LastOnMac(); ok && now.Sub(at) <= lastInFrontMax {
			lastID = id
		}
	}

	out := map[string]MacPlace{}
	for id, pid := range sessions {
		tty := ttys[pid]
		if tty == "" || !fg[pid] {
			continue
		}
		p, ok := byTTY[tty]
		if !ok {
			continue
		}
		name, active := cleanName(p.WindowName), cleanName(p.ActiveName)
		if name == "" || (active != "" && strings.HasPrefix(name, active)) {
			name = "Window " + strconv.Itoa(index[p.WindowID])
		}
		out[id] = MacPlace{
			Window:      MacWindow{ID: "w" + strconv.Itoa(p.WindowID), Index: index[p.WindowID], Name: name},
			Tab:         MacTab{Index: p.Tab, Name: cleanName(p.TabName)},
			Pane:        p.Pane,
			Panes:       p.Panes,
			LastInFront: id == lastID,
		}
	}
	return out, nil
}

// cleanName drops a leading run of symbol glyphs and the spaces after it,
// such as the status mark Claude Code puts in its title ("✳ ", a spinner
// frame "◐ " or "⠋ "), so a name stays the same while the session works.
// A name that is only symbols is kept as it is.
func cleanName(s string) string {
	rest := strings.TrimLeftFunc(s, func(c rune) bool {
		return unicode.IsSymbol(c) || unicode.Is(unicode.Variation_Selector, c) || unicode.IsSpace(c)
	})
	if rest == "" || rest == s {
		return s
	}
	if first := []rune(s)[0]; !unicode.IsSymbol(first) {
		return s // only leading spaces: not a status mark
	}
	return rest
}

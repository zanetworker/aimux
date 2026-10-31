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
)

const (
	layoutFresh    = 3 * time.Second  // a read newer than this is reused
	layoutHold     = 30 * time.Second // a failed read falls back to a good one this young
	lastInFrontMax = 12 * time.Hour
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

	mu     sync.Mutex
	places map[string]MacPlace
	at     time.Time
	good   bool
}

// Places returns each placed session's place; nil when unknown. A session
// whose pid has no tty, whose tty is in no pane, or which is not its
// tty's foreground is absent.
func (r *LayoutReader) Places(ctx context.Context) map[string]MacPlace {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	t := now()
	if r.good && t.Sub(r.at) < layoutFresh {
		return r.places
	}
	places, err := r.read(ctx, t)
	if err != nil {
		if r.Log != nil {
			_, _ = fmt.Fprintf(r.Log, "terminput: mac layout read failed (%T)\n", err)
		}
		if r.good && t.Sub(r.at) <= layoutHold {
			return r.places
		}
		r.places, r.good = nil, false
		return nil
	}
	r.places, r.at, r.good = places, t, true
	return places
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
		name := p.WindowName
		if name == "" || (p.ActiveName != "" && strings.HasPrefix(name, p.ActiveName)) {
			name = "Window " + strconv.Itoa(index[p.WindowID])
		}
		out[id] = MacPlace{
			Window:      MacWindow{ID: "w" + strconv.Itoa(p.WindowID), Index: index[p.WindowID], Name: name},
			Tab:         MacTab{Index: p.Tab, Name: p.TabName},
			Pane:        p.Pane,
			Panes:       p.Panes,
			LastInFront: id == lastID,
		}
	}
	return out, nil
}

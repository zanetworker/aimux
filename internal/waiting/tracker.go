package waiting

import (
	"errors"
	"os"
	"sort"
	"sync"
	"syscall"
	"time"
)

// StaleAfter is how long a marker of a session that is not open may stay:
// long enough for Claude's registry to show a session that just started.
const StaleAfter = 30 * time.Second

// MissFor is how long a session whose process aimux never saw must be
// missing from the registry before its marker goes, so one unreadable
// registry file (caught mid-rewrite) does not remove it.
const MissFor = 5 * time.Second

// TranscriptGrace: a transcript written later than this after the prompt
// was reported means the session moved on (a tool ran after an answer, or
// Esc wrote "[Request interrupted by user]").
const TranscriptGrace = 2 * time.Second

// SkipOlderAtStart: on the first Poll, markers older than this count as
// already pushed, so restarting the server does not push them again.
const SkipOlderAtStart = 2 * time.Minute

// Entry is one waiting session.
type Entry struct {
	SessionID string
	Waiting
}

// Tracker reads the markers for aimux remote serve. Only sessions Live
// reports (open in a terminal) count; markers of sessions that closed, and
// of sessions whose transcript moved on since the prompt, are removed. Poll
// is the push loop's view of what changed.
type Tracker struct {
	Dir   string
	Live  func() map[string]int // session id -> pid, e.g. LocalIndex.TerminalPIDs
	Now   func() time.Time      // nil: time.Now
	Alive func(pid int) bool    // nil: signal 0 to the pid
	// ReadOnly: never delete a marker, only leave out the ones that do not
	// count. For the TUI and the dashboard, whose Live may leave out a
	// harness the owner disabled there, so "not open" is no proof the
	// session closed; remote serve's Tracker (every harness) cleans up.
	ReadOnly bool

	mu       sync.Mutex
	last     map[string]Waiting
	polled   bool
	pids     map[string]int       // last pid seen holding each marked session
	firstMis map[string]time.Time // when an unseen session was first missing
}

func (t *Tracker) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *Tracker) alive(pid int) bool {
	if t.Alive != nil {
		return t.Alive(pid)
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Current returns the open sessions' markers by session id.
func (t *Tracker) Current() map[string]Waiting {
	out := map[string]Waiting{}
	if t.Live == nil {
		return out
	}
	live := t.Live()
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pids == nil {
		t.pids, t.firstMis = map[string]int{}, map[string]time.Time{}
	}
	markers := listMarkers(t.Dir)
	for id, m := range markers {
		if pid, ok := live[id]; ok {
			t.pids[id] = pid
			delete(t.firstMis, id)
			if movedOn(m) {
				t.remove(id, m) // best effort; the next read tries again
				continue
			}
			out[id] = m.Waiting
			continue
		}
		if t.closed(id, m, now) {
			// Only the marker read: a hook or another process may have
			// written a newer one since (the TUI, the dashboard and remote
			// serve each run a Tracker).
			t.remove(id, m)
			delete(t.pids, id)
			delete(t.firstMis, id)
		}
	}
	for id := range t.pids {
		if _, ok := markers[id]; !ok {
			delete(t.pids, id)
			delete(t.firstMis, id)
		}
	}
	return out
}

// remove deletes the marker read, unless the Tracker is ReadOnly.
func (t *Tracker) remove(id string, m marker) {
	if !t.ReadOnly {
		_ = removeIfSince(t.Dir, id, m.Since)
	}
}

// closed: a marker of a session missing from the registry belongs to a
// session that ended. A session aimux saw is ended once its process is
// gone; one it never saw, once it has been missing for MissFor. Either way
// the marker is older than StaleAfter.
func (t *Tracker) closed(id string, m marker, now time.Time) bool {
	if now.Sub(m.Since) <= StaleAfter {
		return false
	}
	if pid, ok := t.pids[id]; ok {
		return !t.alive(pid)
	}
	first, ok := t.firstMis[id]
	if !ok {
		t.firstMis[id] = now
		return false
	}
	return now.Sub(first) >= MissFor
}

// movedOn: the session's transcript was written after the prompt (plus the
// grace). An unreadable transcript, or none, says nothing.
func movedOn(m marker) bool {
	if m.TranscriptPath == "" {
		return false
	}
	fi, err := os.Stat(m.TranscriptPath)
	if err != nil {
		return false
	}
	return fi.ModTime().After(m.Since.Add(TranscriptGrace))
}

// Poll compares Current with the previous Poll: the markers that appeared
// and the ones that went (answered, replaced by a newer prompt, or their
// session closed). The first Poll reports the current markers except those
// older than SkipOlderAtStart, which a previous server run already pushed.
func (t *Tracker) Poll() (appeared, gone []Entry) {
	cur := t.Current()
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	first := !t.polled
	t.polled = true
	for id, w := range cur {
		if first && now.Sub(w.Since) > SkipOlderAtStart {
			continue
		}
		if old, ok := t.last[id]; !ok || !old.Since.Equal(w.Since) {
			appeared = append(appeared, Entry{id, w})
		}
	}
	for id, old := range t.last {
		if w, ok := cur[id]; !ok || !old.Since.Equal(w.Since) {
			gone = append(gone, Entry{id, old})
		}
	}
	t.last = cur
	sortEntries(appeared)
	sortEntries(gone)
	return appeared, gone
}

func sortEntries(es []Entry) {
	sort.Slice(es, func(i, j int) bool { return es[i].SessionID < es[j].SessionID })
}

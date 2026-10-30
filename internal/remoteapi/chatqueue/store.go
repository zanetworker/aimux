package chatqueue

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// Store holds every session's queue and saves each change to its file
// before the change takes effect: a change that cannot be saved is refused
// and leaves the queue as it was. Safe for concurrent use.
type Store struct {
	path       string
	now        func() time.Time
	allowSlash bool

	mu     sync.Mutex
	queues map[string]*queue // never changed in place; see queue

	pending   map[string]bool // sessions changed since the pump last ran; under mu
	wake      chan struct{}   // capacity 1: the pump has work
	out       chan string
	quit      chan struct{}
	closeOnce sync.Once
}

// file is the stored form. Unknown fields are ignored so an older server
// reads a newer file.
type file struct {
	Sessions map[string]*queue `json:"sessions"`
}

// Open loads the queues stored at path (none when it does not exist) and
// starts the Changes feed; Close stops it. allowSlash lets items start with
// "/". A file that cannot be read or holds an unknown state is an error
// naming path: the caller decides, the store never starts empty silently.
// Items in flight when the file was written wait again in a queue paused
// with ReasonMaybeSent.
func Open(path string, now func() time.Time, allowSlash bool) (*Store, error) {
	queues, err := load(path)
	if err != nil {
		return nil, err
	}
	s := &Store{
		path: path, now: now, allowSlash: allowSlash,
		queues:  queues,
		pending: map[string]bool{},
		wake:    make(chan struct{}, 1),
		out:     make(chan string),
		quit:    make(chan struct{}),
	}
	go s.pump()
	return s, nil
}

func load(path string) (map[string]*queue, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the server's own queue file path
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]*queue{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read chat queue file %s: %w", path, err)
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("read chat queue file %s: %w", path, err)
	}
	queues := map[string]*queue{}
	for session, q := range f.Sessions {
		if q == nil {
			continue
		}
		if q.State != QueueRunning && q.State != QueuePaused {
			return nil, fmt.Errorf("read chat queue file %s: session %s has unknown queue state %q", path, session, q.State)
		}
		for i, it := range q.Items {
			if it.ID == "" {
				return nil, fmt.Errorf("read chat queue file %s: session %s item %d has no id", path, session, i)
			}
			switch it.State {
			case StateWaiting, StateSending, StateAnswering:
			default:
				return nil, fmt.Errorf("read chat queue file %s: session %s item %s has unknown state %q", path, session, it.ID, it.State)
			}
		}
		q = q.clone()
		q.recoverAfterRestart()
		queues[session] = q
	}
	return queues, nil
}

// Close stops the Changes feed and closes its channel. The store keeps
// working without it.
func (s *Store) Close() { s.closeOnce.Do(func() { close(s.quit) }) }

// Changes yields the id of each session whose queue changed. Changes made
// while nobody reads are coalesced per session, so the store never waits on
// the reader. One reader only.
func (s *Store) Changes() <-chan string { return s.out }

func (s *Store) pump() {
	defer close(s.out)
	for {
		select {
		case <-s.wake:
		case <-s.quit:
			return
		}
		s.mu.Lock()
		ids := slices.Sorted(maps.Keys(s.pending))
		clear(s.pending)
		s.mu.Unlock()
		for _, id := range ids {
			select {
			case s.out <- id:
			case <-s.quit:
				return
			}
		}
	}
}

// Get returns session's queue; a session with none has an empty running one.
func (s *Store) Get(session string) Queue {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q := s.queues[session]; q != nil {
		return q.view()
	}
	return newQueue().Queue
}

// Add appends a waiting item. A retry with the key of an item already in
// the queue returns that item; with the key of one already sent or deleted,
// a copy in StateDone without text. Errors: ErrBadText, ErrFull.
func (s *Store) Add(session, text, key string) (Item, error) {
	var out Item
	err := s.mutate(session, func(q *queue) error {
		// A retry answers its item even if the rules changed since.
		if it, ok := q.byKey(key); ok {
			out = it
			return errNoChange
		}
		if err := checkText(text, s.allowSlash); err != nil {
			return err
		}
		if len(q.Items) >= MaxItems {
			return ErrFull
		}
		out = Item{ID: newID(), Text: text, Key: key, Created: s.now(), State: StateWaiting}
		q.Items = append(q.Items, out)
		return nil
	})
	return out, err
}

// Edit replaces a waiting item's text. Errors: ErrBadText, ErrNotFound,
// ErrItemSending.
func (s *Store) Edit(session, item, text string) error {
	if err := checkText(text, s.allowSlash); err != nil {
		return err
	}
	return s.mutate(session, func(q *queue) error { return q.edit(item, text) })
}

// Move puts a waiting item at position, 0-based among the items not in
// flight and clamped to them. Errors: ErrNotFound, ErrItemSending.
func (s *Store) Move(session, item string, position int) error {
	return s.mutate(session, func(q *queue) error { return q.move(item, position) })
}

// Delete removes an item; the item in flight only while the queue is
// paused. Errors: ErrNotFound, ErrItemSending.
func (s *Store) Delete(session, item string) error {
	return s.mutate(session, func(q *queue) error { return q.del(item) })
}

// Pause stops the queue with reason (the latest reason wins). Items keep
// their states; only Resume starts the queue again, or its last item
// going: a queue with no items is always running.
func (s *Store) Pause(session, reason string) error {
	return s.mutate(session, func(q *queue) error {
		if q.State == QueuePaused && q.Reason == reason {
			return errNoChange
		}
		q.State, q.Reason = QueuePaused, reason
		return nil
	})
}

// Resume starts a paused queue.
func (s *Store) Resume(session string) error {
	return s.mutate(session, func(q *queue) error {
		if q.State == QueueRunning {
			return errNoChange
		}
		q.State, q.Reason = QueueRunning, ""
		return nil
	})
}

// Next returns the item to send now: the first, when the queue runs and no
// item is in flight. The dispatcher claims it with MarkSending.
func (s *Store) Next(session string) (Item, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q := s.queues[session]; q != nil {
		return q.next()
	}
	return Item{}, false
}

// MarkSending claims item for sending and returns it as it is now (its text
// may have been edited since Next): send that text. Only the item Next
// would return can be claimed. Text the rules no longer allow pauses the
// queue with ReasonNotAllowed and answers ErrBadText. Errors: ErrNotFound,
// ErrWrongState, ErrBadText.
func (s *Store) MarkSending(session, item string) (Item, error) {
	var out Item
	err := s.mutate(session, func(q *queue) error {
		var err error
		out, err = q.markSending(item, s.allowSlash)
		return err
	})
	return out, err
}

// Release returns a sending item to waiting: the dispatcher claimed it but
// sent nothing (the session was not free yet). Errors: ErrNotFound,
// ErrWrongState.
func (s *Store) Release(session, item string) error {
	return s.mutate(session, func(q *queue) error { return q.release(item) })
}

// Sessions lists, sorted, the sessions whose queue holds items.
func (s *Store) Sessions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for id, q := range s.queues {
		if len(q.Items) > 0 {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// BindTerminal records that session's queue sends through a terminal. It
// persists for as long as the queue holds items: a terminal queue never
// moves to ACP.
func (s *Store) BindTerminal(session string) error {
	return s.mutate(session, func(q *queue) error {
		if q.Terminal {
			return errNoChange
		}
		q.Terminal = true
		return nil
	})
}

// Terminal reports whether session's queue is bound to a terminal.
func (s *Store) Terminal(session string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.queues[session]
	return q != nil && q.Terminal
}

// MarkAnswering records that a sending item was sent and its answer is
// running. Errors: ErrNotFound, ErrWrongState.
func (s *Store) MarkAnswering(session, item string) error {
	return s.mutate(session, func(q *queue) error { return q.markAnswering(item) })
}

// Done removes an item in flight whose answer finished. Errors:
// ErrNotFound, ErrWrongState.
func (s *Store) Done(session, item string) error {
	return s.mutate(session, func(q *queue) error { return q.done(item) })
}

// mutate applies change to a clone of session's queue, saves every queue
// with it, and only then makes it current and reports the change.
func (s *Store) mutate(session string, change func(q *queue) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := newQueue()
	if cur := s.queues[session]; cur != nil {
		next = cur.clone()
	}
	var answer error
	if err := change(next); err != nil {
		var saved savedErr
		switch {
		case errors.Is(err, errNoChange):
			return nil
		case errors.As(err, &saved):
			answer = saved.err
		default:
			return err
		}
	}
	if len(next.Items) == 0 {
		// Nothing to hold back: an empty queue runs, unbound, so the next
		// item never waits behind a pause nobody can see.
		next.State, next.Reason, next.Terminal = QueueRunning, "", false
	}
	all := maps.Clone(s.queues)
	if next.empty() {
		delete(all, session)
	} else {
		all[session] = next
	}
	if err := s.save(all); err != nil {
		return err
	}
	s.queues = all
	s.pending[session] = true
	select {
	case s.wake <- struct{}{}:
	default: // the pump already has work and will see this session
	}
	return answer
}

// save writes queues to a temp file in the same directory, syncs it and
// renames it over the file, so a crash leaves the old file or the new one.
// The server is the only writer (one Store per process, changes under mu),
// so no file lock is needed.
func (s *Store) save(queues map[string]*queue) error {
	raw, err := json.Marshal(file{Sessions: queues})
	if err != nil {
		return fmt.Errorf("encode chat queue file %s: %w", s.path, err)
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("save chat queue file %s: %w", s.path, err)
	}
	tmp, err := os.CreateTemp(dir, ".remote-queue-*.tmp") // created 0600
	if err != nil {
		return fmt.Errorf("save chat queue file %s: %w", s.path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // fails harmlessly after the rename
	_, err = tmp.Write(raw)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), s.path)
	}
	if err != nil {
		return fmt.Errorf("save chat queue file %s: %w", s.path, err)
	}
	return nil
}

// newID is a short, URL-safe random id (72 bits).
func newID() string {
	b := make([]byte, 9)
	_, _ = rand.Read(b) // crypto/rand.Read never fails (Go 1.24+)
	return base64.RawURLEncoding.EncodeToString(b)
}

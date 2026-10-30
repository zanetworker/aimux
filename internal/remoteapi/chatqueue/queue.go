// Package chatqueue holds each session's queue of messages waiting to be
// sent from the phone, persisted on the Mac, and the rules for how an item
// moves from waiting to sent. The dispatcher (another file) decides when;
// this package decides what is allowed.
package chatqueue

import (
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/terminput"
)

// Item states. An item is in flight while sending or answering; at most one
// per session is, and it is always the first item.
const (
	StateWaiting   = "waiting"
	StateSending   = "sending"   // being typed or sent
	StateAnswering = "answering" // sent; its answer is not finished
	// StateDone is never stored. Add answers it for a retry whose key
	// belongs to an item already sent or deleted, so a retry never queues
	// the same message twice.
	StateDone = "done"
)

// Queue states.
const (
	QueueRunning = "running"
	QueuePaused  = "paused"
)

// ReasonMaybeSent pauses a queue whose item was in flight when the server
// stopped.
const ReasonMaybeSent = "This message may have been sent. Check the session, then resume or delete it."

// ReasonNotAllowed pauses a queue whose next item fails the text rules
// when it is about to be sent (a hand-edited file, or slash commands
// turned off since it was added).
const ReasonNotAllowed = "This message is no longer allowed. Edit or delete it."

const (
	// MaxItems is the most items one session's queue holds.
	MaxItems = 20
	// MaxTextBytes caps an item's text: the cap on a terminal message body
	// (remoteapi.maxTerminalBody), so an item fits any send path.
	MaxTextBytes = 64 << 10
	// maxRetired is how many keys of finished items a session remembers.
	maxRetired = 64
)

var (
	ErrFull        = errors.New("chatqueue: the queue holds the most items it can")
	ErrItemSending = errors.New("chatqueue: the item is being sent")
	ErrBadText     = errors.New("chatqueue: the text is empty, too long, holds control characters, or starts with ! or /")
	ErrNotFound    = errors.New("chatqueue: no such item in this session's queue")
	ErrWrongState  = errors.New("chatqueue: the item is not in the state this step needs")
)

// errNoChange ends a mutation that succeeds without changing anything.
var errNoChange = errors.New("no change")

// savedErr is an error a mutation answers after its change is saved.
type savedErr struct{ err error }

func (e savedErr) Error() string { return e.err.Error() }

// Item is one queued message.
type Item struct {
	ID      string    `json:"id"`
	Text    string    `json:"text"`
	Key     string    `json:"key,omitempty"` // the client's idempotency key
	Created time.Time `json:"created"`
	State   string    `json:"state"`
}

// Queue is one session's queue as clients see it.
type Queue struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	Items  []Item `json:"items"`
}

// queue is a session's stored queue: what clients see, plus the keys of
// items already sent or deleted. Stored queues are never changed in place;
// a mutation works on a clone and replaces it once saved.
type queue struct {
	Queue
	Retired []retired `json:"retired,omitempty"`
	// Terminal is set once the dispatcher has seen the session open in a
	// terminal: its items then never go another way (ACP). It goes with
	// the queue once that is empty.
	Terminal bool `json:"terminal,omitempty"`
}

type retired struct {
	ID      string    `json:"id"`
	Key     string    `json:"key"`
	Created time.Time `json:"created"`
}

func newQueue() *queue { return &queue{Queue: Queue{State: QueueRunning, Items: []Item{}}} }

func (q *queue) clone() *queue {
	c := *q
	c.Items = slices.Clone(q.Items)
	if c.Items == nil {
		c.Items = []Item{}
	}
	c.Retired = slices.Clone(q.Retired)
	return &c
}

func (q *queue) view() Queue { return q.clone().Queue }

// empty reports whether the queue holds nothing worth storing.
func (q *queue) empty() bool {
	return q.State == QueueRunning && len(q.Items) == 0 && len(q.Retired) == 0
}

func (q *queue) find(id string) int {
	return slices.IndexFunc(q.Items, func(it Item) bool { return it.ID == id })
}

func inFlight(it Item) bool { return it.State == StateSending || it.State == StateAnswering }

// remove drops item i and remembers its key.
func (q *queue) remove(i int) {
	it := q.Items[i]
	q.Items = slices.Delete(q.Items, i, i+1)
	if it.Key == "" {
		return
	}
	q.Retired = append(q.Retired, retired{ID: it.ID, Key: it.Key, Created: it.Created})
	if n := len(q.Retired); n > maxRetired {
		q.Retired = slices.Delete(q.Retired, 0, n-maxRetired)
	}
}

// byKey finds the item a retry with key stands for: a queued one, or a
// finished one with StateDone.
func (q *queue) byKey(key string) (Item, bool) {
	if key == "" {
		return Item{}, false
	}
	for _, it := range q.Items {
		if it.Key == key {
			return it, true
		}
	}
	for _, r := range q.Retired {
		if r.Key == key {
			return Item{ID: r.ID, Key: r.Key, Created: r.Created, State: StateDone}, true
		}
	}
	return Item{}, false
}

func (q *queue) move(id string, pos int) error {
	i := q.find(id)
	if i < 0 {
		return ErrNotFound
	}
	if inFlight(q.Items[i]) {
		return ErrItemSending
	}
	lo := 0
	if inFlight(q.Items[0]) {
		lo = 1 // nothing goes before the item in flight
	}
	// pos counts the items not in flight, so a client's position holds
	// even if the first item went in flight since it read the queue.
	// Clamped before adding lo, so a huge position cannot overflow.
	pos = lo + max(0, min(pos, len(q.Items)-1-lo))
	if pos == i {
		return errNoChange
	}
	it := q.Items[i]
	q.Items = slices.Insert(slices.Delete(q.Items, i, i+1), pos, it)
	return nil
}

func (q *queue) edit(id, text string) error {
	i := q.find(id)
	if i < 0 {
		return ErrNotFound
	}
	if inFlight(q.Items[i]) {
		return ErrItemSending
	}
	if q.Items[i].Text == text {
		return errNoChange
	}
	q.Items[i].Text = text
	return nil
}

// del removes an item. The item in flight goes only from a paused queue:
// the dispatcher has stopped acting on it, and the owner must be able to
// clear an item a queue is stuck on.
func (q *queue) del(id string) error {
	i := q.find(id)
	if i < 0 {
		return ErrNotFound
	}
	if inFlight(q.Items[i]) && q.State == QueueRunning {
		return ErrItemSending
	}
	q.remove(i)
	return nil
}

// markSending claims the first item for sending; only a waiting first item
// of a running queue can be claimed, so two claims never both succeed. Text
// the rules refuse now pauses the queue with ReasonNotAllowed instead.
func (q *queue) markSending(id string, allowSlash bool) (Item, error) {
	i := q.find(id)
	if i < 0 {
		return Item{}, ErrNotFound
	}
	if q.State != QueueRunning || i != 0 || q.Items[0].State != StateWaiting {
		return Item{}, ErrWrongState
	}
	if err := checkText(q.Items[0].Text, allowSlash); err != nil {
		q.State, q.Reason = QueuePaused, ReasonNotAllowed
		return Item{}, savedErr{err}
	}
	q.Items[0].State = StateSending
	return q.Items[0], nil
}

// release returns a claimed item that was not sent to waiting.
func (q *queue) release(id string) error {
	i := q.find(id)
	if i < 0 {
		return ErrNotFound
	}
	if q.Items[i].State != StateSending {
		return ErrWrongState
	}
	q.Items[i].State = StateWaiting
	return nil
}

func (q *queue) markAnswering(id string) error {
	i := q.find(id)
	if i < 0 {
		return ErrNotFound
	}
	if q.Items[i].State != StateSending {
		return ErrWrongState
	}
	q.Items[i].State = StateAnswering
	return nil
}

func (q *queue) done(id string) error {
	i := q.find(id)
	if i < 0 {
		return ErrNotFound
	}
	if !inFlight(q.Items[i]) {
		return ErrWrongState
	}
	q.remove(i)
	return nil
}

// next is the item to send now: the first one, when the queue runs and
// nothing is in flight.
func (q *queue) next() (Item, bool) {
	if q.State != QueueRunning || len(q.Items) == 0 || q.Items[0].State != StateWaiting {
		return Item{}, false
	}
	return q.Items[0], true
}

// recoverAfterRestart pauses a queue whose item was in flight when the
// server stopped; the item waits again so the owner can resume or delete it.
func (q *queue) recoverAfterRestart() {
	for i := range q.Items {
		if inFlight(q.Items[i]) {
			q.Items[i].State = StateWaiting
			q.State, q.Reason = QueuePaused, ReasonMaybeSent
		}
	}
}

// checkText applies the message rules: not empty, at most MaxTextBytes, and
// the terminal text rules (no control characters, no leading "!", no
// leading "/" unless allowSlash).
func checkText(text string, allowSlash bool) error {
	if strings.TrimSpace(text) == "" || len(text) > MaxTextBytes {
		return ErrBadText
	}
	if terminput.CheckMessage(text, allowSlash) != nil {
		return ErrBadText
	}
	return nil
}

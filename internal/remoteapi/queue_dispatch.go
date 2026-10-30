package remoteapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/remoteapi/chatqueue"
	"github.com/zanetworker/aimux/internal/terminput"
)

// QueueACP is the chat queue dispatcher's way into sessions started from
// the phone: the broker's own send path (the one POST …/messages uses).
type QueueACP struct{ Broker *Broker }

var _ chatqueue.ACP = QueueACP{}

// Busy reports a running turn.
func (q QueueACP) Busy(session string) bool { return q.Broker.Busy(session) }

// Start begins a turn with key as its idempotency key. A busy session
// answers chatqueue.ErrNotFree, one open in a terminal
// chatqueue.ErrInTerminal, an unknown one chatqueue.ErrNotRunning.
func (q QueueACP) Start(session, text, key string) (string, error) {
	turn, err := q.Broker.Start(session, text, key)
	switch {
	case errors.Is(err, acprun.ErrBusy):
		return "", fmt.Errorf("%w: %w", chatqueue.ErrNotFree, err)
	case errors.Is(err, acprun.ErrOpenInTerminal):
		return "", fmt.Errorf("%w: %w", chatqueue.ErrInTerminal, err)
	case errors.Is(err, acprun.ErrUnknownSession):
		return "", fmt.Errorf("%w: %w", chatqueue.ErrNotRunning, err)
	}
	return turn, err
}

// TurnEnded reports the broker's turn-done or turn-error for turn.
func (q QueueACP) TurnEnded(session, turn string) (bool, bool) {
	return q.Broker.TurnEnded(session, turn)
}

// QueueInput is what the dispatcher types with (terminput.Service).
type QueueInput interface {
	Status(ctx context.Context, sessionID, registryStatus string) terminput.Status
	Send(ctx context.Context, sessionID, text, idemKey string) error
	Submit(ctx context.Context, sessionID, text string) error
}

// QueueTerminal is the dispatcher's way into sessions open in a terminal:
// Claude's registry and the session history as GET /v1/sessions/{id} reads
// them, and the terminal input service the API types with.
type QueueTerminal struct {
	Index *LocalIndex
	Input QueueInput
}

var (
	_ chatqueue.Terminal = QueueTerminal{}
	_ QueueInput         = (*terminput.Service)(nil)
)

// queueTurns is how many of the latest turns the dispatcher reads.
const queueTurns = 10

// Open reports whether a terminal process holds session (the rule that
// makes it open_in_terminal) and its registry status.
func (q QueueTerminal) Open(session string) (string, bool) {
	e, ok := q.Index.openSessions()[session]
	return e.Status, ok
}

// Status reads the terminal fresh, not through the detail's cache.
func (q QueueTerminal) Status(ctx context.Context, session, registryStatus string) terminput.Status {
	return q.Input.Status(ctx, session, registryStatus)
}

// Send types text and presses Enter (terminput.Service.Send).
func (q QueueTerminal) Send(ctx context.Context, session, text, key string) error {
	return q.Input.Send(ctx, session, text, key)
}

// Submit presses Enter for text left in the box (terminput.Service.Submit).
func (q QueueTerminal) Submit(ctx context.Context, session, text string) error {
	return q.Input.Submit(ctx, session, text)
}

// Turns returns the session's latest turns the owner (or the queue) typed,
// leaving out the ones Claude Code writes in the user's place.
func (q QueueTerminal) Turns(ctx context.Context, session string) ([]chatqueue.Turn, error) {
	d, err := q.Index.session(ctx, session, queueTurns, false)
	if err != nil {
		return nil, err
	}
	out := make([]chatqueue.Turn, 0, len(d.Turns))
	for _, t := range d.Turns {
		switch cleanPrompt(t.User) {
		case "(hook feedback)", "(message from another session)", "(notification)":
			continue
		}
		out = append(out, chatqueue.Turn{Time: t.Time, User: t.User})
	}
	return out, nil
}

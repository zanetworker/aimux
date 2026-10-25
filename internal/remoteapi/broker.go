package remoteapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/zanetworker/aimux/internal/acprun"
)

// Event kinds beyond acprun.KindUpdate, which the broker passes through.
const (
	KindTurnStarted      = "turn_started"      // {"text"}
	KindTurnDone         = "turn_done"         // {"stopReason"}
	KindTurnError        = "turn_error"        // {"error"}
	KindApproval         = "approval"          // {"approvalId","title","options"}
	KindApprovalResolved = "approval_resolved" // {"approvalId","outcome": allowed|denied|expired}
)

// Broker errors.
var (
	ErrNoSuchApproval = errors.New("no pending approval with that id")
	ErrInvalidOption  = errors.New("that option was not offered for this approval")
)

const maxEventsPerSession = 2000

// Event is one entry in a session's log, numbered per session so a client
// can reconnect and ask for everything after the last number it saw.
type Event struct {
	Seq  int64           `json:"seq"`
	Turn string          `json:"turn"`
	Kind string          `json:"kind"`
	Time time.Time       `json:"time"`
	Data json.RawMessage `json:"data"`
}

// BrokerDeps is what the broker drives. Check rejects a session quickly
// (unknown, open in a terminal); Send runs one turn; Cancel stops it.
type BrokerDeps struct {
	Check  func(session string) error
	Send   func(ctx context.Context, session, text string, onEvent func(acprun.Event)) (string, error)
	Cancel func(session string) error
}

// BrokerOptions configures a Broker.
type BrokerOptions struct {
	// ApprovalTimeout is how long a permission request waits for an answer
	// before it is denied. Default 10 minutes.
	ApprovalTimeout time.Duration
}

type sessionLog struct {
	events []Event
	seq    int64
	active string             // running turn id, "" when idle
	cancel context.CancelFunc // stops the running turn, even before its run exists
	keys   map[string]string  // client key -> turn id
	subs   map[chan Event]struct{}
}

type pendingApproval struct {
	session string
	turn    string
	options []acp.PermissionOption
	answer  chan string   // option id, "" for deny
	ended   chan struct{} // closed when the turn that asked ends
}

// Broker runs turns in the background, keeps each session's event log, and
// holds permission requests until a client answers them.
type Broker struct {
	deps      BrokerDeps
	opt       BrokerOptions
	logID     string // names this run's event log; sequence numbers restart with it
	mu        sync.Mutex
	sessions  map[string]*sessionLog
	approvals map[string]*pendingApproval
}

// NewBroker returns a Broker.
func NewBroker(deps BrokerDeps, opt BrokerOptions) *Broker {
	if opt.ApprovalTimeout <= 0 {
		opt.ApprovalTimeout = 10 * time.Minute
	}
	return &Broker{deps: deps, opt: opt, logID: newID(), sessions: map[string]*sessionLog{}, approvals: map[string]*pendingApproval{}}
}

func (b *Broker) log(session string) *sessionLog {
	l, ok := b.sessions[session]
	if !ok {
		l = &sessionLog{keys: map[string]string{}, subs: map[chan Event]struct{}{}}
		b.sessions[session] = l
	}
	return l
}

// appendLocked adds an event and fans it out. Subscribers that fall behind
// are dropped; they reconnect with the last seq they saw.
func (b *Broker) appendLocked(session, turn, kind string, data any) {
	raw, ok := data.(json.RawMessage)
	if !ok {
		raw, _ = json.Marshal(data)
	}
	l := b.log(session)
	l.seq++
	e := Event{Seq: l.seq, Turn: turn, Kind: kind, Time: time.Now().UTC(), Data: raw}
	l.events = append(l.events, e)
	if len(l.events) > maxEventsPerSession {
		l.events = l.events[len(l.events)-maxEventsPerSession:]
	}
	for ch := range l.subs {
		select {
		case ch <- e:
		default:
			delete(l.subs, ch)
			close(ch)
		}
	}
}

func (b *Broker) emit(session, turn, kind string, data any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.appendLocked(session, turn, kind, data)
}

// Start begins a turn and returns its id. The same non-empty key returns
// the same turn, so a client may retry a send safely. It fails fast with
// acprun.ErrBusy while the session has a running turn, or with Check's
// error.
func (b *Broker) Start(session, text, key string) (string, error) {
	b.mu.Lock()
	l := b.log(session)
	if id, ok := l.keys[key]; ok && key != "" {
		b.mu.Unlock()
		return id, nil
	}
	if l.active != "" {
		b.mu.Unlock()
		return "", acprun.ErrBusy
	}
	b.mu.Unlock()

	if err := b.deps.Check(session); err != nil {
		return "", err
	}

	b.mu.Lock()
	// Another Start may have won the race while Check ran unlocked: the
	// same key means the same turn, even if that turn already finished.
	if id, ok := l.keys[key]; ok && key != "" {
		b.mu.Unlock()
		return id, nil
	}
	if l.active != "" {
		b.mu.Unlock()
		return "", acprun.ErrBusy
	}
	turn := newID()
	ctx, cancel := context.WithCancel(context.Background())
	l.active = turn
	l.cancel = cancel
	if key != "" {
		l.keys[key] = turn
	}
	b.appendLocked(session, turn, KindTurnStarted, map[string]string{"text": text})
	b.mu.Unlock()

	go b.run(ctx, cancel, session, turn, text)
	return turn, nil
}

// run executes a turn. Its context outlives the request that started it (the
// client follows the turn through the event log) and is cancelled by Cancel.
func (b *Broker) run(ctx context.Context, cancel context.CancelFunc, session, turn, text string) {
	defer cancel()
	stop, err := b.deps.Send(ctx, session, text, func(e acprun.Event) {
		if e.Kind == acprun.KindPermission {
			return // the broker's own approval event replaces it
		}
		if e.Kind == acprun.KindUpdate && isAvailableCommands(e.Data) {
			return // the agent's slash-command list: large, and of no use to remote clients
		}
		b.emit(session, turn, e.Kind, e.Data)
	})
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case errors.Is(err, context.Canceled):
		// Stopped by Cancel before the agent got the prompt.
		b.appendLocked(session, turn, KindTurnDone, map[string]string{"stopReason": string(acp.StopReasonCancelled)})
	case err != nil:
		b.appendLocked(session, turn, KindTurnError, map[string]string{"error": err.Error()})
	default:
		b.appendLocked(session, turn, KindTurnDone, map[string]string{"stopReason": stop})
	}
	l := b.log(session)
	l.active = ""
	l.cancel = nil
	// Approvals belong to their turn: once it ends they cannot be answered.
	for id, p := range b.approvals {
		if p.session == session && p.turn == turn {
			delete(b.approvals, id)
			close(p.ended)
		}
	}
}

// LogID names this broker's event log. Sequence numbers start over in a new
// log, so a cursor is only meaningful with the log it came from.
func (b *Broker) LogID() string { return b.logID }

// Events returns the session's events after seq.
func (b *Broker) Events(session string, after int64) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []Event{}
	for _, e := range b.log(session).events {
		if e.Seq > after {
			out = append(out, e)
		}
	}
	return out
}

// Subscribe delivers the session's events after seq, then new ones as they
// happen. The channel closes if the subscriber falls too far behind; call
// stop when done.
func (b *Broker) Subscribe(session string, after int64) (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	l := b.log(session)
	var backlog []Event
	for _, e := range l.events {
		if e.Seq > after {
			backlog = append(backlog, e)
		}
	}
	ch := make(chan Event, len(backlog)+256)
	for _, e := range backlog {
		ch <- e
	}
	l.subs[ch] = struct{}{}
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if _, ok := l.subs[ch]; ok {
				delete(l.subs, ch)
				close(ch)
			}
		})
	}
}

// Busy reports whether the session has a running turn.
func (b *Broker) Busy(session string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.log(session).active != ""
}

// Cancel stops the session's running turn wherever it is: before the agent
// has the prompt, or during it. With no turn running it returns
// acprun.ErrNotRunning.
func (b *Broker) Cancel(session string) error {
	// Hold the lock throughout: no turn can end and a new one start between
	// choosing the turn and telling the agent, or the agent's cancel would
	// stop the new turn instead.
	b.mu.Lock()
	defer b.mu.Unlock()
	stopTurn := b.log(session).cancel
	if stopTurn == nil {
		return acprun.ErrNotRunning // no turn: leave any idle adapter alone
	}
	// Both, always: the turn's context stops it if it has not reached the
	// agent yet; the agent's own cancel stops it mid-prompt.
	stopTurn()
	_ = b.deps.Cancel(session)
	return nil
}

// Approver is the acprun.Approver for runs started on the broker's behalf:
// it posts an approval event and waits for Answer, the timeout, or the end
// of the turn.
func (b *Broker) Approver() acprun.Approver {
	return func(ctx context.Context, req acp.RequestPermissionRequest) (string, bool) {
		session := string(req.SessionId)
		id := newID()
		p := &pendingApproval{session: session, options: req.Options, answer: make(chan string, 1), ended: make(chan struct{})}
		title := ""
		if req.ToolCall.Title != nil {
			title = *req.ToolCall.Title
		}
		b.mu.Lock()
		p.turn = b.log(session).active
		b.approvals[id] = p
		b.appendLocked(session, p.turn, KindApproval, map[string]any{
			"approvalId": id, "title": title, "options": req.Options, "toolCall": req.ToolCall,
		})
		b.mu.Unlock()

		outcome, option := "expired", ""
		timer := time.NewTimer(b.opt.ApprovalTimeout)
		defer timer.Stop()
		select {
		case option = <-p.answer:
			outcome = "denied"
			if kind, ok := optionKind(req.Options, option); ok && (kind == acp.PermissionOptionKindAllowOnce || kind == acp.PermissionOptionKindAllowAlways) {
				outcome = "allowed"
			}
		case <-timer.C:
		case <-ctx.Done():
		case <-p.ended: // the turn is over; nobody may act on this any more
		}
		b.mu.Lock()
		delete(b.approvals, id)
		b.appendLocked(session, p.turn, KindApprovalResolved, map[string]string{"approvalId": id, "outcome": outcome})
		b.mu.Unlock()
		if outcome == "allowed" {
			return option, true
		}
		if outcome == "denied" && option != "" {
			return option, true // the client chose one of the agent's reject options
		}
		return "", false
	}
}

// Answer resolves a pending approval with one of its option ids, or denies
// it when option is empty.
func (b *Broker) Answer(approvalID, option string) error {
	b.mu.Lock()
	p, ok := b.approvals[approvalID]
	if !ok {
		b.mu.Unlock()
		return ErrNoSuchApproval
	}
	if option != "" {
		if _, offered := optionKind(p.options, option); !offered {
			b.mu.Unlock()
			return ErrInvalidOption
		}
	}
	delete(b.approvals, approvalID)
	b.mu.Unlock()
	p.answer <- option
	return nil
}

func optionKind(opts []acp.PermissionOption, id string) (acp.PermissionOptionKind, bool) {
	for _, o := range opts {
		if string(o.OptionId) == id {
			return o.Kind, true
		}
	}
	return "", false
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read does not fail on supported platforms
	return hex.EncodeToString(b[:])
}

func isAvailableCommands(data json.RawMessage) bool {
	var u struct {
		Kind string `json:"sessionUpdate"`
	}
	return json.Unmarshal(data, &u) == nil && u.Kind == "available_commands_update"
}

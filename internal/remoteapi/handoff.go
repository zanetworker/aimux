package remoteapi

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
)

// Refusals of Open on Mac, mapped to codes by writeFailure.
var (
	// ErrNotStarted: the session has no transcript yet, so there is nothing
	// to resume in a terminal (409 not_started).
	ErrNotStarted = errors.New("the session has no messages yet")
	// ErrHandoffTimeout: the tab opened but the session did not show as
	// open in a terminal within Handoff.Wait (504 timeout).
	ErrHandoffTimeout = errors.New("the terminal did not pick up the session in time")
	// ErrNoTerminal: this server cannot open a terminal tab (503 no_terminal).
	ErrNoTerminal = errors.New("no terminal app to open the session in")
	// ErrNotAuthorized: macOS refused aimux control of the terminal app
	// (403 not_authorized). Handoff.Open returns it (wrapped or not).
	ErrNotAuthorized = errors.New("aimux may not control the terminal app")
)

const (
	defaultHandoffWait = 20 * time.Second
	defaultHandoffPoll = 500 * time.Millisecond
	// handoffOpenTimeout bounds Open, which may have to start iTerm2.
	handoffOpenTimeout = 30 * time.Second
)

// Handoff moves a session the Mac runs in the background (through ACP) into
// a new terminal tab: the run is closed and the harness resumes the session
// in the tab. Every func is required; Wait and Poll default to 20 s and
// 500 ms.
type Handoff struct {
	// Hold keeps turns out of the session until release (Broker.Hold).
	Hold func(session string) (release func(), err error)
	// Drop closes the session's idle run (acprun.Manager.Drop).
	Drop func(session string) error
	// State is the session's catalog state: idle, busy, open_in_terminal.
	State func(ctx context.Context, session string) (state string, err error)
	// Target is the tab's title and the command resuming the session there;
	// ErrNotStarted when the session has no transcript.
	Target func(ctx context.Context, session string) (title string, argv []string, dir string, err error)
	// Open opens the tab; ErrNotAuthorized when macOS refuses, ErrNoTerminal
	// when there is no terminal app.
	Open func(ctx context.Context, title string, argv []string, dir string) error
	Wait time.Duration
	Poll time.Duration
}

// Run hands session to a terminal tab and returns once the terminal holds
// it. Steps, in order: refuse a session already open in a terminal or
// running a turn, build the command, hold the session, look again (a
// terminal may have taken it meanwhile), close its run, open the tab, then
// wait until the session shows as open in a terminal. The hold is released
// on every path; a closed run is not restarted after a failed open or a
// timeout (sending from the phone starts a new one).
//
// Once the session is held, the handoff no longer follows ctx's
// cancellation: a client that goes away while the tab starts must not
// release the hold halfway. It still ends within the open deadline and
// Wait.
func (h *Handoff) Run(ctx context.Context, session string) error {
	state, err := h.State(ctx, session)
	if err != nil {
		return err
	}
	switch state {
	case StateOpenInTerminal:
		return acprun.ErrOpenInTerminal
	case StateBusy:
		return acprun.ErrBusy
	}
	title, argv, dir, err := h.Target(ctx, session)
	if err != nil {
		return err
	}
	release, err := h.Hold(session)
	if err != nil {
		return err
	}
	defer release()
	ctx = context.WithoutCancel(ctx)
	if state, err := h.State(ctx, session); err != nil {
		return err
	} else if state == StateOpenInTerminal {
		return acprun.ErrOpenInTerminal
	}
	if err := h.Drop(session); err != nil {
		return err
	}
	openCtx, cancel := context.WithTimeout(ctx, handoffOpenTimeout)
	err = h.Open(openCtx, title, argv, dir)
	cancel()
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: open a terminal tab: %w", ErrHandoffTimeout, err)
	}
	if err != nil {
		return fmt.Errorf("open a terminal tab: %w", err)
	}
	return h.waitInTerminal(ctx, session)
}

// waitInTerminal polls State until the session is open in a terminal or
// Wait passes (ErrHandoffTimeout).
func (h *Handoff) waitInTerminal(ctx context.Context, session string) error {
	wait, poll := h.Wait, h.Poll
	if wait <= 0 {
		wait = defaultHandoffWait
	}
	if poll <= 0 {
		poll = defaultHandoffPoll
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	tick := time.NewTicker(poll)
	defer tick.Stop()
	for {
		// A failed read is not the answer: the catalog may be rescanning
		// while the terminal starts, so it is asked again until the wait ends.
		if state, err := h.State(ctx, session); err == nil && state == StateOpenInTerminal {
			return nil
		}
		select {
		case <-deadline.C:
			return ErrHandoffTimeout
		case <-tick.C:
		}
	}
}

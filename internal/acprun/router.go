package acprun

import (
	"context"
	"errors"
	"fmt"

	"github.com/zanetworker/aimux/internal/harness"
)

// Router errors.
var (
	ErrUnknownSession = errors.New("no such session")
	// ErrOpenInTerminal: replying to a session open in a terminal is left
	// out on purpose; typing into one can approve a permission prompt the
	// user never saw. Close it there to continue it here.
	ErrOpenInTerminal = errors.New("the session is open in a terminal on this machine; close it there to continue it here")
	// ErrOwnershipUnknown: without Claude's session registry aimux cannot
	// tell whether a terminal holds the session, so it does not risk a
	// second writer.
	ErrOwnershipUnknown = errors.New("cannot tell whether the session is open in a terminal: Claude's session registry is unavailable")
)

// SessionInfo is what the router needs to know about a session.
type SessionInfo struct {
	Provider       string
	Cwd            string
	OpenInTerminal bool  // some live process holds the session
	Holders        []int // those processes
}

// Lookup finds a session by ID.
type Lookup func(sessionID string) (SessionInfo, error)

// Router sends a message to a session the right way for its state.
type Router struct {
	Lookup  Lookup
	Manager *Manager
}

// Send continues sessionID with text through the manager, unless the session
// is unknown or open in a terminal that aimux does not drive.
func (r *Router) Send(ctx context.Context, sessionID, text string, onEvent func(Event)) (string, error) {
	info, err := r.route(sessionID)
	if err != nil {
		return "", err
	}
	return r.Manager.Send(ctx, sessionID, info.Cwd, text, onEvent)
}

// Check reports whether Send would accept sessionID, without sending.
func (r *Router) Check(sessionID string) error {
	_, err := r.route(sessionID)
	return err
}

func (r *Router) route(sessionID string) (SessionInfo, error) {
	info, err := r.Lookup(sessionID)
	if errors.Is(err, ErrUnknownSession) && r.Manager.Running(sessionID) {
		// A session aimux just created: the agent writes its file on the
		// first prompt, and only aimux's run holds it.
		return SessionInfo{Provider: "claude"}, nil
	}
	if err != nil {
		return SessionInfo{}, err
	}
	// Another aimux process first: its adapter would otherwise look like a
	// terminal below. (The manager takes the real reservation.)
	if err := r.Manager.ProbeReserved(sessionID); err != nil {
		return SessionInfo{}, err
	}
	// A session aimux runs shows up in Claude's registry through the
	// adapter's own process; any other holder is a user's terminal, even
	// while aimux keeps an idle run for the session.
	for _, pid := range info.Holders {
		if !r.Manager.Owns(pid) {
			return SessionInfo{}, fmt.Errorf("%w (session %s)", ErrOpenInTerminal, sessionID)
		}
	}
	return info, nil
}

// SessionLookup finds one harness's sessions: its session store gives the
// working directory, and its live sessions under the Strict rule
// (harness.Mode) say which processes hold the session. The session list
// reads the same source under List, so the two can differ only where that
// table says.
type SessionLookup struct {
	Provider string               // SessionInfo.Provider, e.g. "claude"
	Store    harness.SessionStore // provider.Claude for Claude Code
	Live     harness.LiveSessions // discovery.LiveCache for Claude Code
}

// Lookup is a Router.Lookup.
func (l SessionLookup) Lookup(sessionID string) (SessionInfo, error) {
	file := l.Store.SessionFile(sessionID)
	if file == "" {
		return SessionInfo{}, fmt.Errorf("%w: %s", ErrUnknownSession, sessionID)
	}
	cwd, err := l.Store.SessionCwd(file)
	if err != nil {
		return SessionInfo{}, err
	}
	hs, err := l.Live.Holders(harness.Strict)
	if err != nil {
		// A registry aimux cannot fully read may hide the terminal that
		// holds this session.
		return SessionInfo{}, fmt.Errorf("%w: %v", ErrOwnershipUnknown, err)
	}
	info := SessionInfo{Provider: l.Provider, Cwd: cwd}
	seen := map[int]bool{}
	for _, h := range hs {
		if h.SessionID == sessionID && !seen[h.PID] {
			seen[h.PID] = true
			info.Holders = append(info.Holders, h.PID)
		}
	}
	info.OpenInTerminal = len(info.Holders) > 0
	return info, nil
}

package controller

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/jump"
)

var (
	// ErrAgentNotFound means no running agent matches the reference.
	ErrAgentNotFound = errors.New("no running agent matches")
	// ErrAmbiguousAgent means a session ID prefix matches several running agents.
	ErrAmbiguousAgent = errors.New("matches more than one running agent")
	// ErrNoPane means the agent runs, but not in an iTerm2 pane aimux can raise.
	ErrNoPane = errors.New("no iTerm2 pane found for the agent")
)

// FindLiveAgent picks the running agent that ref names: an exact session ID
// or PID first, then a unique session ID prefix.
func FindLiveAgent(agents []agent.Agent, ref string) (agent.Agent, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return agent.Agent{}, fmt.Errorf("%w: empty session reference", ErrAgentNotFound)
	}
	for _, a := range agents {
		if a.SessionID == ref || strconv.Itoa(a.PID) == ref {
			return a, nil
		}
	}
	var matches []agent.Agent
	for _, a := range agents {
		if a.SessionID != "" && strings.HasPrefix(a.SessionID, ref) {
			matches = append(matches, a)
		}
	}
	switch len(matches) {
	case 0:
		return agent.Agent{}, fmt.Errorf("%w %q", ErrAgentNotFound, ref)
	case 1:
		return matches[0], nil
	}
	ids := make([]string, len(matches))
	for i, a := range matches {
		ids[i] = a.SessionID
	}
	return agent.Agent{}, fmt.Errorf("%q %w: %s", ref, ErrAmbiguousAgent, strings.Join(ids, ", "))
}

// Focuser holds the OS calls FocusAgent makes, so callers and tests can swap them.
type Focuser struct {
	TTYForPID func(pid int) string
	FocusTTY  func(tty string) (where string, found bool, err error)
}

// DefaultFocuser raises iTerm2 panes through osascript.
func DefaultFocuser() Focuser {
	return Focuser{TTYForPID: jump.TTYForPID, FocusTTY: jump.ITerm2FocusLocate}
}

// FocusResult says what FocusAgent did.
type FocusResult struct {
	SessionID string `json:"session_id"`
	PID       int    `json:"pid"`
	TTY       string `json:"tty,omitempty"`
	Focused   bool   `json:"focused"`
	Where     string `json:"where,omitempty"` // e.g. "tab 3 · pane 1 of 2"
	Hint      string `json:"hint,omitempty"`  // what to run when aimux cannot focus it
}

// FocusAgent brings the iTerm2 pane running a to the front. It returns
// ErrNoPane when the agent has no iTerm2 pane, with a tmux attach hint in the
// result when the agent runs in tmux.
func FocusAgent(a agent.Agent, f Focuser) (FocusResult, error) {
	res := FocusResult{SessionID: a.SessionID, PID: a.PID, TTY: f.TTYForPID(a.PID)}
	if a.TMuxSession != "" {
		// Set before focusing so the hint survives an osascript failure (Linux, no iTerm2).
		res.Hint = "tmux attach-session -t " + a.TMuxSession
	}
	if res.TTY != "" {
		where, ok, err := f.FocusTTY(res.TTY)
		if err != nil {
			return res, err
		}
		if ok {
			res.Focused, res.Where, res.Hint = true, where, ""
			return res, nil
		}
	}
	return res, fmt.Errorf("%w (pid %d)", ErrNoPane, a.PID)
}

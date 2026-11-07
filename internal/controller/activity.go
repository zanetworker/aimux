package controller

import (
	"sync"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/harness"
	"github.com/zanetworker/aimux/internal/sessionstate"
	"github.com/zanetworker/aimux/internal/waiting"
)

// LocalActivity gives the TUI's and the dashboard's agents the session
// state service's activity, the answer the phone gets: the harnesses'
// registries (live), the discovered agents' processes, the hook markers
// through a read-only waiting.Tracker (remote serve's rule for which
// markers count; only remote serve deletes them), and each
// provider's transcript guess (the agent's discovered Status) as the last
// fallback. These processes run no aimux turns, so there is no Busy or
// Approvals input.
type LocalActivity struct {
	// HooksSettings is Claude Code's settings file the permission hooks
	// live in (~/.claude/settings.json); "": not checked (HooksInstalled
	// reports true, no hint).
	HooksSettings string
	// Now is the clock for HooksRecheck and the markers; nil: time.Now.
	Now func() time.Time

	live    []harness.LiveSessions
	tracker *waiting.Tracker

	hooksMu      sync.Mutex
	hooksAt      time.Time
	hooksChecked bool
	hooksOn      bool

	mu      sync.Mutex
	holders map[string]harness.Holder // the current Apply's, read by the tracker
}

// NewLocalActivity reads live (each harness's harness.LiveSessions) and the
// markers in markersDir (waiting.Dir).
func NewLocalActivity(live []harness.LiveSessions, markersDir string) *LocalActivity {
	l := &LocalActivity{live: live}
	// ReadOnly: live may leave out a harness disabled in this frontend, so
	// "not open here" is no proof a session closed; remote serve's Tracker,
	// which sees every harness, removes markers.
	l.tracker = &waiting.Tracker{Dir: markersDir, Live: l.pids, Now: l.now, ReadOnly: true}
	return l
}

func (l *LocalActivity) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// HooksHint is the muted line the TUI and the dashboard show while
// HooksInstalled is false.
const HooksHint = "Permission alerts need `aimux remote hooks install`"

// HooksRecheck is how long HooksInstalled keeps its answer: installing the
// hooks is rare, and the TUI asks every refresh.
const HooksRecheck = 5 * time.Minute

// HooksInstalled reports whether the hooks that write permission markers
// are installed (waiting.Status: no event missing). Without them a
// permission prompt cannot read "needs you", and the TUI and dashboard say
// so. It reads the settings once, then again after HooksRecheck. Unknown
// (nil, no HooksSettings) counts as installed: no hint.
func (l *LocalActivity) HooksInstalled() bool {
	if l == nil || l.HooksSettings == "" {
		return true
	}
	l.hooksMu.Lock()
	defer l.hooksMu.Unlock()
	if t := l.now(); !l.hooksChecked || t.Sub(l.hooksAt) >= HooksRecheck {
		st, err := waiting.Status(l.HooksSettings)
		l.hooksOn = err == nil && len(st.Missing) == 0
		l.hooksAt, l.hooksChecked = t, true
	}
	return l.hooksOn
}

func (l *LocalActivity) pids() map[string]int {
	out := make(map[string]int, len(l.holders))
	for id, h := range l.holders {
		out[id] = h.PID
	}
	return out
}

// Wrap returns discover with Apply run on its result, for callers that take
// a plain discover func (the CLI's agents, kill and focus). A discover error
// passes through; a nil LocalActivity returns discover unchanged.
func (l *LocalActivity) Wrap(discover func() ([]agent.Agent, error)) func() ([]agent.Agent, error) {
	if l == nil {
		return discover
	}
	return func() ([]agent.Agent, error) {
		agents, err := discover()
		if err != nil {
			return agents, err
		}
		l.Apply(agents)
		return agents, nil
	}
}

// Apply sets Activity on every agent with a session id and moves Status to
// match: needs_you is StatusWaitingPermission, working StatusActive, idle
// StatusIdle (an error status stays: it says more); gone leaves Status as
// discovered. A nil LocalActivity does nothing.
func (l *LocalActivity) Apply(agents []agent.Agent) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var hs []harness.Holder
	for _, src := range l.live {
		got, _ := src.Holders(harness.List) // a registry is optional: none open beats failing the refresh
		hs = append(hs, got...)
	}
	holders := harness.OpenSessions(hs, nil)
	guesses := map[string]sessionstate.Activity{}
	for _, a := range agents {
		if a.SessionID == "" || a.PID <= 0 {
			continue
		}
		if _, ok := holders[a.SessionID]; !ok { // a harness without a registry: its process holds it
			holders[a.SessionID] = harness.Holder{PID: a.PID, SessionID: a.SessionID}
		}
		guesses[a.SessionID] = guessOf(a.Status)
	}
	l.holders = holders
	svc := &sessionstate.Service{
		Holders:  func() map[string]harness.Holder { return holders },
		Markers:  l.tracker.Current,
		Fallback: func() map[string]sessionstate.Activity { return guesses },
	}
	snap := svc.Read()
	for i := range agents {
		a := &agents[i]
		if a.SessionID == "" {
			continue
		}
		st := snap.Of(a.SessionID)
		a.Activity = string(st.Activity)
		if a.Status == agent.StatusError {
			continue // an error the provider found stays visible; the activity is still set
		}
		switch st.Activity {
		case sessionstate.NeedsYou:
			a.Status = agent.StatusWaitingPermission
		case sessionstate.Working:
			a.Status = agent.StatusActive
		case sessionstate.Idle:
			a.Status = agent.StatusIdle
		}
	}
}

// guessOf is a provider's transcript guess as an activity.
func guessOf(s agent.Status) sessionstate.Activity {
	switch s {
	case agent.StatusActive:
		return sessionstate.Working
	case agent.StatusWaitingPermission:
		return sessionstate.NeedsYou
	case agent.StatusIdle:
		return sessionstate.Idle
	}
	return ""
}

// ActivityBySession is the agents' activity by session id (agents with
// both), for views listing sessions rather than agents.
func ActivityBySession(agents []agent.Agent) map[string]string {
	out := map[string]string{}
	for _, a := range agents {
		if a.SessionID != "" && a.Activity != "" {
			out[a.SessionID] = a.Activity
		}
	}
	return out
}

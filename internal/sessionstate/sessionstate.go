// Package sessionstate answers one question for every surface: what is
// this session doing right now. The phone (remote API `activity`), the TUI
// and the web dashboard all show its answer.
//
// The answer combines facts other packages own; each comes in as a small
// input so its source stays where it is (Service):
//
//   - Held: a live process that is not aimux's own holds the session
//     (harness.LiveSessions, through Holders).
//   - RegistryStatus: the harness's own registry status of that process
//     ("busy", "idle", "" when it has none).
//   - Background: aimux runs a turn of the session (the remote API's Broker).
//   - Approval: that turn waits for a permission answer.
//   - Waiting: a permission marker a hook wrote for the session.
//   - Screen: the terminal screen shows the agent working, when read.
//   - Fallback: a per-harness guess from the transcript tail, for a
//     harness without a registry status.
//
// The precedence rule, written once (Decide):
//
//	needs_you  a marker or a pending approval
//	working    an aimux turn, registry busy, or the screen shows working
//	           (else, for a held session nothing above describes, the
//	           fallback's needs_you or working)
//	idle       held, or an aimux run, and not working
//	gone       no process holds it and nothing runs
//
// Place says where it lives: background while aimux runs it, terminal
// while a process holds it, none otherwise.
//
// The package names no harness: harness-specific readers are supplied by
// the callers that build the Service.
package sessionstate

import "time"

// Activity is what a session is doing now.
type Activity string

// The activities, as the remote API sends them.
const (
	NeedsYou Activity = "needs_you" // a permission prompt or an approval waits for the owner
	Working  Activity = "working"
	Idle     Activity = "idle"
	Gone     Activity = "gone" // no process holds it and nothing runs
)

// Label is the activity as the TUI and the dashboard print it: "needs you",
// "working", "idle"; "" for gone or unknown, which they leave unlabelled.
func (a Activity) Label() string {
	switch a {
	case NeedsYou:
		return "needs you"
	case Working:
		return "working"
	case Idle:
		return "idle"
	}
	return ""
}

// Place is where a session lives.
type Place string

// The places.
const (
	Terminal   Place = "terminal"   // a process outside aimux holds it
	Background Place = "background" // aimux's own (ACP) run
	None       Place = "none"
)

// Screen is what a read of the terminal screen says.
type Screen int

// The screen readings.
const (
	ScreenUnknown Screen = iota // not read
	ScreenIdle
	ScreenWorking
)

// Wait is what a session waits for, from a marker: the remote API's
// `waiting` object.
type Wait struct {
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
	Since   time.Time `json:"since"`
}

// Facts are the inputs about one session.
type Facts struct {
	Held           bool
	RegistryStatus string
	Background     bool
	Approval       bool
	Waiting        *Wait
	Screen         Screen
	Fallback       Activity
}

// State is the answer for one session, with the facts it came from.
type State struct {
	Activity Activity
	Place    Place
	Facts
}

// Decide applies the precedence rule (package doc) to f.
func Decide(f Facts) State {
	return State{Activity: activity(f), Place: place(f), Facts: f}
}

func activity(f Facts) Activity {
	switch {
	case f.Waiting != nil || f.Approval:
		return NeedsYou
	case f.Background || (f.Held && f.RegistryStatus == "busy") || f.Screen == ScreenWorking:
		return Working
	case f.Held && f.RegistryStatus == "" && f.Screen == ScreenUnknown && (f.Fallback == NeedsYou || f.Fallback == Working):
		return f.Fallback
	case f.Held:
		return Idle
	}
	return Gone
}

func place(f Facts) Place {
	switch {
	case f.Background:
		return Background
	case f.Held:
		return Terminal
	}
	return None
}

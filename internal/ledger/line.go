// Package ledger holds the ledger's line type and the rule a reader uses to
// count each action once. See docs/ledger-identity.md.
package ledger

import "time"

// Kind is what a ledger line records.
type Kind string

const (
	KindModelCall Kind = "model_call"
	KindToolCall  Kind = "tool_call"
	KindVerdict   Kind = "verdict"
	KindSwitch    Kind = "switch"
)

// Line is one ledger line: one JSON object per line in a writer's file.
type Line struct {
	At        time.Time `json:"at"`
	Kind      Kind      `json:"kind"`
	Writer    string    `json:"writer"`             // "model-proxy", "mcp-proxy", "tool-gateway", "collector"
	Seq       uint64    `json:"seq"`                // per writer, only goes up
	EventID   string    `json:"event_id,omitempty"` // hook events: call id + hook event name
	Run       string    `json:"run,omitempty"`
	Session   string    `json:"session,omitempty"`
	ToSession string    `json:"to_session,omitempty"` // a switch that started a new session: the new one
	Turn      string    `json:"turn,omitempty"`
	Call      string    `json:"call,omitempty"`
	Secondary bool      `json:"secondary,omitempty"` // a cross-check, not the record
}

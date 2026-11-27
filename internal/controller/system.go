package controller

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// EngineRow is one engine as every frontend lists it: what the config file states beside what the running proxy
// answers with.
type EngineRow struct {
	Name    string `json:"name"`
	On      bool   `json:"on"`
	Type    string `json:"type"`
	Auth    string `json:"auth,omitempty"`
	URL     string `json:"url,omitempty"`
	KeyAt   string `json:"key_at,omitempty"`
	Running *bool  `json:"running,omitempty"` // what the running proxy says; absent when it does not say
	Stated  bool   `json:"stated"`            // the file says something about this engine
}

// EngineNotInFile is the Type of an engine the proxy runs and the file does not list.
const EngineNotInFile = "not in the file"

// Words says what the row states, in the words every frontend shows: the file's side (on, off, not stated, or -
// for an engine that is not in the file), the running side (yes, no, or - when the proxy did not say), and
// whether the two differ, which `aimux setup --apply` settles.
func (r EngineRow) Words() (file, running string, differs bool) {
	file = "off"
	switch {
	case r.Type == EngineNotInFile:
		file = "-"
	case !r.Stated:
		file = "not stated"
	case r.On:
		file = "on"
	}
	if r.Running == nil {
		return file, "-", false
	}
	running = "no"
	if *r.Running {
		running = "yes"
	}
	return file, running, r.Stated && *r.Running != r.On
}

// ProxyHeadline is the diagnosis in one line: how many things are failing and how many are worth a look.
func ProxyHeadline(findings []ProxyFinding) string {
	var fails, warns int
	for _, f := range findings {
		switch f.Level {
		case FindingFail:
			fails++
		case FindingWarn:
			warns++
		}
	}
	switch {
	case fails > 0 && warns > 0:
		return fmt.Sprintf("%d %s failing, %d %s to look at.", fails, pluralWord(fails, "thing is", "things are"), warns, pluralWord(warns, "thing", "things"))
	case fails > 0:
		return fmt.Sprintf("%d %s failing.", fails, pluralWord(fails, "thing is", "things are"))
	case warns > 0:
		return fmt.Sprintf("%d %s to look at; nothing is failing.", warns, pluralWord(warns, "thing", "things"))
	}
	return "Nothing to fix."
}

// SystemSnapshot is one reading of this machine, for a frontend that shows it on one screen: the background
// services, the model proxy's diagnosis, the engines and roles, and the model calls of the last day by role.
// Each part that could not be read carries why in its Err and leaves the rest standing.
type SystemSnapshot struct {
	At time.Time

	Services StatusOverview

	Doctor    ProxyReport
	DoctorErr string

	EnginesPort int
	Engines     []EngineRow
	Roles       []RoleRow
	Subagents   []SubagentRow
	EnginesErr  string

	Meter    MeterReport
	MeterErr string
}

// MeterNotes says what a meter report's figures leave out or cannot vouch for, a sentence each; none when there
// is nothing to say. A frontend that marks estimated tokens with ~ shows them under its table.
func MeterNotes(r MeterReport) []string {
	var notes []string
	if r.Incomplete {
		notes = append(notes, "The ledger has lines that could not be counted, so these figures are low.")
	}
	var outside []string
	for _, part := range []struct {
		n    int
		what string
	}{{r.Unmatched, "with no session"}, {r.Skipped, "of another schema"}, {r.Torn, "torn"}} {
		if part.n > 0 {
			outside = append(outside, fmt.Sprintf("%d %s", part.n, part.what))
		}
	}
	if len(outside) > 0 {
		notes = append(notes, "Not in the table: "+strings.Join(outside, ", ")+".")
	}
	if r.NoUsage > 0 {
		notes = append(notes, fmt.Sprintf("%d of the calls counted carried no token figures.", r.NoUsage))
	}
	if slices.ContainsFunc(r.Rows, func(row MeterRow) bool { return row.Estimated }) {
		notes = append(notes, "~ marks tokens that are partly estimated.")
	}
	return notes
}

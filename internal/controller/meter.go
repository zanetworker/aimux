package controller

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/ledger"
)

// MeterBy is what a meter row stands for.
type MeterBy string

const (
	MeterBySession MeterBy = "session"
	MeterByModel   MeterBy = "model"
	MeterByDay     MeterBy = "day"
)

// MeterOptions narrows and groups what the meter counts.
type MeterOptions struct {
	Since time.Time // zero: everything
	By    MeterBy   // default MeterBySession
	// Session keeps one session: its id in full, or the start of it, as tables show it.
	Session string
	Zone    *time.Location // for days; nil is time.Local
}

// MeterRow is the model calls of one session, one served model or one day.
type MeterRow struct {
	Key          string   `json:"key"` // the session id, the served model, or a day as 2006-01-02
	Requests     int      `json:"requests"`
	Failed       int      `json:"failed"`              // outcome other than ok
	InputTokens  int64    `json:"input_tokens"`        // uncached: the line's input tokens minus its cache read
	CachedTokens int64    `json:"cached_input_tokens"` // read from a cache
	OutputTokens int64    `json:"output_tokens"`
	Estimated    bool     `json:"estimated,omitempty"` // a call in the row had estimated usage
	Models       []string `json:"models,omitempty"`    // served models, sorted; left out when the key is the model
	Asked        []string `json:"asked,omitempty"`     // models the client named, sorted
}

// MeterReport is the ledger's model calls, summed, with what could not be counted said beside them.
type MeterReport struct {
	Rows      []MeterRow `json:"rows"` // most tokens first; days newest first
	Total     MeterRow   `json:"total"`
	Unmatched int        `json:"unmatched"` // model calls with no session: in no row and not in Total
	Skipped   int        `json:"skipped"`   // lines of another ledger version
	Torn      int        `json:"torn"`      // lines that are not whole
	// NoUsage is how many counted calls carried no token figures. They are in Requests, and add nothing to the
	// token sums, which are short by whatever those calls used.
	NoUsage    int                     `json:"no_usage"`
	Gaps       map[string][]ledger.Gap `json:"gaps,omitempty"`
	Incomplete bool                    `json:"incomplete"` // Skipped, Torn or Gaps is not empty
	// Files is how many ledger files were read. Zero means nothing has written a ledger here yet, which is not
	// the same as a quiet day.
	Files int `json:"files"`
}

// ErrAmbiguousSession is a session filter that names more than one session.
var ErrAmbiguousSession = errors.New("more than one session starts with that")

// Meter reads the ledger folder and sums its model calls. It is the one reader every front calls, and it never
// writes. A missing folder is an empty report.
func Meter(dir string, opts MeterOptions) (MeterReport, error) {
	report := MeterReport{Rows: []MeterRow{}}
	lines, read, err := ledger.ReadDir(dir)
	if err != nil {
		return report, err
	}
	counted := ledger.Count(lines)
	report.Skipped, report.Torn, report.Gaps, report.Files = read.Skipped, read.Torn, counted.Gaps, read.Files
	report.Incomplete = read.Skipped > 0 || read.Torn > 0 || len(counted.Gaps) > 0
	zone := cmp.Or(opts.Zone, time.Local)
	wanted := func(l ledger.Line) bool {
		return l.Kind == ledger.KindModelCall && !l.At.Before(opts.Since)
	}
	wantedSession := func(id string) bool { return strings.HasPrefix(id, opts.Session) }
	for _, l := range counted.Unmatched {
		if wanted(l) && opts.Session == "" {
			report.Unmatched++
		}
	}

	// A shortened id must name one session: summing two under one name would be a wrong number, not a wide one.
	if opts.Session != "" {
		matches := map[string]bool{}
		for _, l := range counted.Lines {
			if l.Kind == ledger.KindModelCall && strings.HasPrefix(l.Session, opts.Session) {
				matches[l.Session] = true
			}
		}
		if len(matches) > 1 && !matches[opts.Session] {
			return report, fmt.Errorf("%w: %q matches %d sessions; give more of the id", ErrAmbiguousSession, opts.Session, len(matches))
		}
		if matches[opts.Session] {
			exact := opts.Session
			wantedSession = func(id string) bool { return id == exact }
		}
	}
	rows := map[string]*MeterRow{}
	served, asked := map[string]map[string]bool{}, map[string]map[string]bool{}
	note := func(set map[string]map[string]bool, key, model string) {
		if model == "" {
			return
		}
		if set[key] == nil {
			set[key] = map[string]bool{}
		}
		set[key][model] = true
	}
	for _, l := range counted.Lines {
		if !wanted(l) || !wantedSession(l.Session) {
			continue
		}
		key := l.Session
		switch opts.By {
		case MeterByModel:
			key = l.ResponseModel
		case MeterByDay:
			key = l.At.In(zone).Format("2006-01-02")
		}
		row := rows[key]
		if row == nil {
			row = &MeterRow{Key: key}
			rows[key] = row
		}
		if l.UsageUnknown {
			report.NoUsage++
		}
		for _, sum := range []*MeterRow{row, &report.Total} {
			sum.Requests++
			if l.Outcome != ledger.OutcomeOK {
				sum.Failed++
			}
			sum.InputTokens += max(l.InputTokens-l.CacheRead, 0)
			sum.CachedTokens += l.CacheRead
			sum.OutputTokens += l.OutputTokens
			sum.Estimated = sum.Estimated || l.Estimated
		}
		note(served, key, l.ResponseModel)
		note(asked, key, l.RequestModel)
	}
	names := func(set map[string]bool) []string {
		out := make([]string, 0, len(set))
		for name := range set {
			out = append(out, name)
		}
		slices.Sort(out)
		return out
	}
	for key, row := range rows {
		if opts.By != MeterByModel {
			row.Models = names(served[key])
		}
		row.Asked = names(asked[key])
		report.Rows = append(report.Rows, *row)
	}
	slices.SortFunc(report.Rows, func(a, b MeterRow) int {
		if opts.By == MeterByDay {
			return cmp.Compare(b.Key, a.Key)
		}
		tokens := func(r MeterRow) int64 { return r.InputTokens + r.CachedTokens + r.OutputTokens }
		return cmp.Or(cmp.Compare(tokens(b), tokens(a)), cmp.Compare(a.Key, b.Key))
	})
	return report, nil
}

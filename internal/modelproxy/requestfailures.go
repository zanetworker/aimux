package modelproxy

import (
	"slices"
	"time"
)

// RequestFailure is one kind of failed request in the proxy's history: which engine was asked, what came back,
// and how often.
type RequestFailure struct {
	Engine string // "codex", "anthropic", or a named upstream
	Status int    // the HTTP status the client was given; 0 when there was none
	Error  string // the proxy's word for it
	Count  int
	LastAt time.Time
}

// ReadRequestFailures counts the requests in the history at path over the window ending at now, and groups the
// failed ones by engine, status and error, most frequent first. It reads files only.
// incomplete says some of the history could not be read (damaged lines, or a file not read to its end), so the
// counts cover less than the window.
func ReadRequestFailures(path string, window time.Duration, now time.Time) (total int, failures []RequestFailure, incomplete bool, err error) {
	records, damaged, partial, err := readRequestRecords(path, now.Add(-window))
	if err != nil {
		return 0, nil, false, err
	}
	incomplete = damaged > 0 || partial
	type kind struct {
		engine, word string
		status       int
	}
	byKind := map[kind]*RequestFailure{}
	for _, record := range records {
		total++
		if record.Error == "" && record.Status < 400 {
			continue
		}
		// A forwarded request names its upstream in Detail; everything else is Codex's.
		engine := "codex"
		if record.Outcome == "forwarded" && record.Detail != "" {
			engine = record.Detail
		}
		k := kind{engine: engine, word: record.Error, status: record.Status}
		f := byKind[k]
		if f == nil {
			f = &RequestFailure{Engine: engine, Status: record.Status, Error: record.Error}
			byKind[k] = f
		}
		f.Count++
		if at, perr := time.Parse(time.RFC3339, record.At); perr == nil && at.After(f.LastAt) {
			f.LastAt = at
		}
	}
	for _, f := range byKind {
		failures = append(failures, *f)
	}
	slices.SortFunc(failures, func(a, b RequestFailure) int {
		if a.Count != b.Count {
			return b.Count - a.Count
		}
		return b.LastAt.Compare(a.LastAt)
	})
	return total, failures, incomplete, nil
}

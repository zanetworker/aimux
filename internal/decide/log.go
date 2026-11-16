package decide

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// ErrUnknownDecision means Feedback named an id the log has no decision for.
var ErrUnknownDecision = errors.New("unknown decision id")

// DefaultMaxLogBytes is the size at which the decision log rotates when
// Log.MaxBytes is zero.
const DefaultMaxLogBytes = 10 << 20

// Log is the decision log: one JSON line per Ask and one per Feedback in
// Path (created 0600, folders 0700). Lines carry a SHA-256 of the state,
// never the state itself. When a write would take Path past MaxBytes (zero
// means DefaultMaxLogBytes), Path moves to Path.1 (replacing an older .1)
// and a new Path starts, so the log holds at most about twice MaxBytes.
// Feedback looks decisions up in an in-memory index of both files, built
// on first use (and after a rotation) and kept current by later writes. Ask
// never waits on that build: while Feedback holds the index, Ask leaves its
// entry on a short pending list that Feedback adds in. Safe for concurrent
// use within one process.
type Log struct {
	Path     string
	MaxBytes int64

	mu        sync.Mutex    // serializes appends and rotation
	rotations atomic.Uint64 // bumped on each rotation; a stale index is rebuilt

	idxMu  sync.Mutex
	idx    map[string]*indexed // nil until built
	idxGen uint64              // rotations when idx was built
	inUse  atomic.Bool         // set before the first build starts; record keeps idx current from then on

	pendMu  sync.Mutex
	pending []pendingDecision // decisions recorded while Feedback held idxMu
}

// pendingDecision is a decision line Feedback has yet to add to the index.
type pendingDecision struct{ id, choice string }

// indexed is what the log holds for one decision id: its choice (once the
// decision line is seen) and the modes it already has feedback for.
type indexed struct {
	choice  string
	decided bool   // a decision line was seen
	last    string // the latest feedback final ("" until one is seen)
	fed     bool   // a feedback line was seen
}

// DefaultLogPath is ~/.aimux/logs/decisions.jsonl ("" when there is no home).
func DefaultLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".aimux", "logs", "decisions.jsonl")
}

// decisionLine is the shape of one Ask's log line.
type decisionLine struct {
	Time          time.Time          `json:"time"`
	ID            string             `json:"id"`
	Decider       string             `json:"decider"`
	StateSHA256   string             `json:"state_sha256"`
	Choice        string             `json:"choice"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
	Decided       bool               `json:"decided"`
	Undecided     string             `json:"undecided"`
	Threshold     float64            `json:"threshold"`
	Backend       string             `json:"backend"`
	Model         string             `json:"model"`
	Ms            int64              `json:"ms"`
	Test          bool               `json:"test,omitempty"`
	Hedged        bool               `json:"hedged,omitempty"`
}

func (l *Log) record(decider, stateSHA string, a Answer, test bool) error {
	err := l.append(decisionLine{Time: time.Now().UTC(), ID: a.ID, Decider: decider, StateSHA256: stateSHA,
		Choice: a.Choice, Confidence: a.Confidence, Probabilities: a.Probabilities, Decided: a.Decided,
		Undecided: a.Undecided, Threshold: a.Threshold, Backend: a.Backend, Model: a.Model, Ms: a.Ms, Test: test, Hedged: a.Hedged})
	if err != nil {
		return err
	}
	if test {
		return nil // a test decision is not one to give feedback on
	}
	// Before any index exists the file is the record: the line was written
	// before inUse is read, and the build sets inUse before it scans.
	if !l.inUse.Load() {
		return nil
	}
	if l.idxMu.TryLock() {
		if l.idx != nil {
			e := entryOf(l.idx, a.ID)
			e.choice, e.decided = a.Choice, true
		}
		l.idxMu.Unlock()
		return nil
	}
	l.pendMu.Lock()
	l.pending = append(l.pending, pendingDecision{a.ID, a.Choice})
	l.pendMu.Unlock()
	return nil
}

// Feedback records what the caller finally did with decision id: final is
// the option it went with, and kept says whether that is the logged choice.
// extra adds fields to the line (time, id, feedback and kept cannot be
// overridden); it must hold no state text. An id the log does not hold
// (never logged, or rotated out of both files) returns ErrUnknownDecision
// and writes nothing. A final equal to the id's latest feedback (one
// decision reused by several sessions in the same mode) returns nil and
// writes nothing; any other final (the caller changed the pick, or changed
// back) writes another line, so the latest line per id is the final mode.
func (l *Log) Feedback(id, final string, extra map[string]any) error {
	l.idxMu.Lock()
	defer l.idxMu.Unlock()
	var e *indexed
	for attempt := 0; attempt < 2; attempt++ {
		gen := l.rotations.Load()
		if l.idx == nil || l.idxGen != gen {
			l.inUse.Store(true)
			idx, err := l.loadIndex()
			if err != nil {
				return err
			}
			l.idx, l.idxGen = idx, gen
		}
		l.drainPending()
		if e = l.idx[id]; e != nil && e.decided {
			break
		}
		if l.rotations.Load() == gen {
			break // no rotation raced the build: the id is truly unknown
		}
		l.idx = nil // a rotation moved files mid-build: rebuild once
	}
	if e == nil || !e.decided {
		return fmt.Errorf("decision %s: %w", id, ErrUnknownDecision)
	}
	if e.fed && e.last == final {
		return nil
	}
	line := map[string]any{}
	for k, v := range extra {
		line[k] = v
	}
	line["time"] = time.Now().UTC()
	line["id"] = id
	line["feedback"] = final
	line["kept"] = final == e.choice
	if err := l.append(line); err != nil {
		return err
	}
	e.last, e.fed = final, true
	return nil
}

// drainPending adds the decisions recorded while idxMu was held. Callers
// hold idxMu.
func (l *Log) drainPending() {
	l.pendMu.Lock()
	p := l.pending
	l.pending = nil
	l.pendMu.Unlock()
	for _, d := range p {
		e := entryOf(l.idx, d.id)
		e.choice, e.decided = d.choice, true
	}
}

// betweenScans runs between reading Path.1 and Path (a test seam for a
// rotation racing the build).
var betweenScans = func() {}

// entryOf is idx's record for id, created when absent.
func entryOf(idx map[string]*indexed, id string) *indexed {
	e := idx[id]
	if e == nil {
		e = &indexed{}
		idx[id] = e
	}
	return e
}

// loadIndex reads Path.1 then Path into a fresh index. Feedback calls it
// holding idxMu, never mu, so Ask keeps writing meanwhile; a line written
// during the scan is either read here or added by record afterwards.
func (l *Log) loadIndex() (map[string]*indexed, error) {
	idx := map[string]*indexed{}
	if err := scanInto(idx, l.Path+".1"); err != nil {
		return nil, err
	}
	betweenScans()
	if err := scanInto(idx, l.Path); err != nil {
		return nil, err
	}
	return idx, nil
}

// scanInto adds the lines of the log file p to idx; a missing file adds none.
func scanInto(idx map[string]*indexed, p string) error {
	f, err := os.Open(p) // #nosec G304 -- aimux's own log file
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read decision log %s: %w", p, err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var d struct {
			ID       string  `json:"id"`
			Decider  string  `json:"decider"`
			Choice   string  `json:"choice"`
			Feedback *string `json:"feedback"`
			Test     bool    `json:"test"`
		}
		if json.Unmarshal(sc.Bytes(), &d) != nil || d.ID == "" || d.Test {
			continue
		}
		switch {
		case d.Feedback != nil:
			e := entryOf(idx, d.ID)
			e.last, e.fed = *d.Feedback, true
		case d.Decider != "":
			e := entryOf(idx, d.ID)
			e.choice, e.decided = d.Choice, true
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read decision log %s: %w", p, err)
	}
	return nil
}

func (l *Log) append(v any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.appendLocked(v)
}

func (l *Log) appendLocked(v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("decision log: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o700); err != nil {
		return fmt.Errorf("decision log %s: %w", l.Path, err)
	}
	if err := l.rotateFor(int64(len(line) + 1)); err != nil {
		return err
	}
	f, err := os.OpenFile(l.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- aimux's own log file
	if err != nil {
		return fmt.Errorf("decision log %s: %w", l.Path, err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("decision log %s: %w", l.Path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("decision log %s: %w", l.Path, err)
	}
	return nil
}

// rotateFor moves Path to Path.1 when writing n more bytes would take it
// past the size limit. A file already empty is never rotated, so one line
// larger than the limit still gets written. Callers hold mu.
func (l *Log) rotateFor(n int64) error {
	limit := l.MaxBytes
	if limit <= 0 {
		limit = DefaultMaxLogBytes
	}
	st, err := os.Stat(l.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("decision log %s: %w", l.Path, err)
	}
	if st.Size() == 0 || st.Size()+n <= limit {
		return nil
	}
	if err := os.Rename(l.Path, l.Path+".1"); err != nil {
		return fmt.Errorf("rotate decision log %s: %w", l.Path, err)
	}
	l.rotations.Add(1)
	return nil
}

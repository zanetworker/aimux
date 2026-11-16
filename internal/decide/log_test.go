package decide

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/cost"
)

func readLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- test temp file
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad line %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func TestLog_NoStateText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "decisions.jsonl")
	const secret = "UNIQUE-QUESTION-TEXT-7f3a"
	b := &fakeBackend{a: ans("web", 0.9, map[string]float64{"web": 0.9, "page": 0.1})}
	d := &Decider{Name: "picker", Backends: []Backend{b}, Threshold: 0.25, Timeout: time.Second, Usage: noUsage, Log: &Log{Path: path}}
	a := d.Ask(context.Background(), Request{State: map[string]string{"question": secret}, Question: testQuestion})
	data, err := os.ReadFile(path) // #nosec G304 -- test temp file
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("decision log contains state text: %s", data)
	}
	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("lines %d", len(lines))
	}
	l := lines[0]
	for _, k := range []string{"time", "id", "decider", "state_sha256", "choice", "confidence", "probabilities", "decided", "undecided", "backend", "model", "ms"} {
		if _, ok := l[k]; !ok {
			t.Errorf("missing %q in %v", k, l)
		}
	}
	if l["id"] != a.ID || l["decider"] != "picker" || l["choice"] != "web" || len(l["state_sha256"].(string)) != 64 {
		t.Fatalf("line %v", l)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v", st.Mode().Perm())
	}
	if st, _ := os.Stat(filepath.Dir(path)); st.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", st.Mode().Perm())
	}
}

func TestLog_Feedback_Kept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	l := &Log{Path: path}
	b := &fakeBackend{a: ans("web", 0.9, map[string]float64{"web": 1})}
	d := &Decider{Name: "t", Backends: []Backend{b}, Threshold: 0.25, Timeout: time.Second, Usage: noUsage, Log: l}
	a1 := d.Ask(context.Background(), Request{State: "s", Question: testQuestion})
	a2 := d.Ask(context.Background(), Request{State: "s", Question: testQuestion})
	if err := l.Feedback(a1.ID, "web", map[string]any{"client": "wingman", "kept": "ignored"}); err != nil {
		t.Fatal(err)
	}
	if err := l.Feedback(a2.ID, "page", nil); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, path)
	if len(lines) != 4 {
		t.Fatalf("lines %d", len(lines))
	}
	kept, changed := lines[2], lines[3]
	if kept["id"] != a1.ID || kept["feedback"] != "web" || kept["kept"] != true || kept["client"] != "wingman" || kept["time"] == nil {
		t.Fatalf("kept line %v", kept)
	}
	if changed["id"] != a2.ID || changed["feedback"] != "page" || changed["kept"] != false {
		t.Fatalf("changed line %v", changed)
	}
	if err := l.Feedback("dec_unknown", "web", nil); !errors.Is(err, ErrUnknownDecision) {
		t.Fatalf("want ErrUnknownDecision, got %v", err)
	}
	if n := len(readLines(t, path)); n != 4 {
		t.Fatalf("unknown id wrote a line: %d", n)
	}
}

func TestDefaultLogPath(t *testing.T) {
	if p := DefaultLogPath(); !strings.HasSuffix(p, filepath.Join(".aimux", "logs", "decisions.jsonl")) {
		t.Fatalf("path %q", p)
	}
}

func TestLog_Feedback_OncePerDecisionAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	b := &fakeBackend{a: ans("web", 0.9, map[string]float64{"web": 1})}
	d := &Decider{Name: "t", Backends: []Backend{b}, Threshold: 0.25, Timeout: time.Second, Usage: noUsage, Log: &Log{Path: path}}
	a := d.Ask(context.Background(), Request{State: "s", Question: testQuestion})
	if err := d.Log.Feedback(a.ID, "web", nil); err != nil {
		t.Fatal(err)
	}
	if err := d.Log.Feedback(a.ID, "web", nil); err != nil {
		t.Fatalf("a repeat of the same mode must be ignored, not an error: %v", err)
	}
	// a Change after the decided pick: same id, another mode
	if err := d.Log.Feedback(a.ID, "page", nil); err != nil {
		t.Fatal(err)
	}
	// a fresh Log on the same file (a restart) also sees both earlier feedbacks
	if err := (&Log{Path: path}).Feedback(a.ID, "page", nil); err != nil {
		t.Fatal(err)
	}
	if err := (&Log{Path: path}).Feedback(a.ID, "web", nil); err != nil {
		t.Fatal(err)
	}
	// web, web (repeat: nothing), page (changed), page again after a restart
	// (repeat: nothing), back to web (a line, so the last line is the final)
	lines := readLines(t, path)
	if len(lines) != 4 {
		t.Fatalf("want the decision and three feedback lines, got %v", lines)
	}
	if lines[1]["feedback"] != "web" || lines[1]["kept"] != true || lines[2]["feedback"] != "page" || lines[2]["kept"] != false ||
		lines[3]["feedback"] != "web" || lines[3]["kept"] != true {
		t.Fatalf("want web kept, page changed, web kept, got %v", lines[1:])
	}
}

func TestLog_RotatesAndIndexesBothFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	l := &Log{Path: path, MaxBytes: 2000}
	b := &fakeBackend{a: ans("web", 0.9, map[string]float64{"web": 1})}
	d := &Decider{Name: "t", Backends: []Backend{b}, Threshold: 0.25, Timeout: time.Second, Usage: noUsage, Log: l}
	var ids []string
	for i := 0; i < 30; i++ {
		ids = append(ids, d.Ask(context.Background(), Request{State: "s", Question: testQuestion}).ID)
	}
	for _, p := range []string{path, path + ".1"} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("want %s after rotation: %v", p, err)
		}
		if st.Size() > 2000 {
			t.Errorf("%s is %d bytes, over the 2000 limit", p, st.Size())
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", p, st.Mode().Perm())
		}
	}
	// the newest id is in the live file, an older one only in .1
	inFile := func(p, id string) bool {
		data, _ := os.ReadFile(p) // #nosec G304 -- test temp file
		return strings.Contains(string(data), id)
	}
	newest := ids[len(ids)-1]
	var older string
	for _, id := range ids {
		if inFile(path+".1", id) && !inFile(path, id) {
			older = id
			break
		}
	}
	if older == "" || !inFile(path, newest) {
		t.Fatal("want ids spread over the live file and .1")
	}
	for _, id := range []string{newest, older} {
		if err := (&Log{Path: path, MaxBytes: 2000}).Feedback(id, "web", nil); err != nil {
			t.Fatalf("feedback for %s (index from both files): %v", id, err)
		}
	}
	if err := l.Feedback(ids[0], "web", nil); !errors.Is(err, ErrUnknownDecision) {
		t.Fatalf("an id rotated out of both files: want ErrUnknownDecision, got %v", err)
	}
	// the in-memory index notices a rotation made by this Log
	for i := 0; i < 30; i++ {
		d.Ask(context.Background(), Request{State: "s", Question: testQuestion})
	}
	if err := l.Feedback(older, "page", nil); !errors.Is(err, ErrUnknownDecision) {
		t.Fatalf("after more rotations the old id is gone: want ErrUnknownDecision, got %v", err)
	}
}

func TestLog_FeedbackDoesNotRescanPerCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	l := &Log{Path: path}
	b := &fakeBackend{a: ans("web", 0.9, map[string]float64{"web": 1})}
	d := &Decider{Name: "t", Backends: []Backend{b}, Threshold: 0.25, Timeout: time.Second, Usage: noUsage, Log: l}
	a1 := d.Ask(context.Background(), Request{State: "s", Question: testQuestion})
	if err := l.Feedback(a1.ID, "web", nil); err != nil { // builds the index
		t.Fatal(err)
	}
	// a decision recorded after the index was built is found from memory:
	// hide the file, and feedback still knows the id
	a2 := d.Ask(context.Background(), Request{State: "s", Question: testQuestion})
	if err := os.Rename(path, path+".hidden"); err != nil {
		t.Fatal(err)
	}
	if err := l.Feedback(a2.ID, "page", nil); err != nil {
		t.Fatalf("want the id from the in-memory index, got %v", err)
	}
	lines := readLines(t, path)
	if len(lines) != 1 || lines[0]["id"] != a2.ID || lines[0]["kept"] != false {
		t.Fatalf("lines %v", lines)
	}
}

func TestLog_ConcurrentAskAndFeedback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	l := &Log{Path: path, MaxBytes: 4000}
	done := make(chan error, 40)
	for i := 0; i < 20; i++ {
		// one fake per goroutine: fakeBackend counts calls without a lock
		d := &Decider{Name: "t", Backends: []Backend{&fakeBackend{a: ans("web", 0.9, map[string]float64{"web": 1})}},
			Threshold: 0.25, Timeout: time.Second, Usage: noUsage, Log: l}
		go func() {
			a := d.Ask(context.Background(), Request{State: "s", Question: testQuestion})
			done <- nil
			err := l.Feedback(a.ID, "page", nil)
			if errors.Is(err, ErrUnknownDecision) {
				err = nil // rotated out by the other writers: allowed
			}
			done <- err
		}()
	}
	for i := 0; i < 40; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

// A rotation between reading .1 and the live file must not lose the
// decision that moved: Feedback rebuilds once and finds it.
func TestLog_RotationDuringIndexBuild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	l := &Log{Path: path, MaxBytes: 1 << 20}
	d := &Decider{Name: "t", Backends: []Backend{&fakeBackend{a: ans("web", 0.9, map[string]float64{"web": 1})}},
		Threshold: 0.25, Timeout: time.Second, Usage: noUsage, Log: l}
	a := d.Ask(context.Background(), Request{State: "s", Question: testQuestion}) // in the live file
	once := false
	betweenScans = func() {
		if once {
			return
		}
		once = true
		l.MaxBytes = 1 // the next write rotates the live file (holding a) to .1
		d.Ask(context.Background(), Request{State: "s", Question: testQuestion})
	}
	t.Cleanup(func() { betweenScans = func() {} })
	if err := l.Feedback(a.ID, "web", nil); err != nil {
		t.Fatalf("decision moved to .1 mid-build: want it found, got %v", err)
	}
}

// A Decider marked Test writes "test": true on its decision lines, so shadow
// evaluation can leave them out; an unmarked one writes no such field.
func TestLog_TestMarker(t *testing.T) {
	for _, test := range []bool{true, false} {
		path := filepath.Join(t.TempDir(), "decisions.jsonl")
		b := &fakeBackend{a: ans("web", 0.9, map[string]float64{"web": 0.9, "page": 0.1})}
		d := &Decider{Name: "p", Backends: []Backend{b}, Threshold: 0.25, Timeout: time.Second, Usage: noUsage, Log: &Log{Path: path}, Test: test}
		d.Ask(context.Background(), Request{State: "s", Question: testQuestion})
		v, present := readLines(t, path)[0]["test"]
		if present != test || (test && v != true) {
			t.Errorf("Test=%v: line test field = %v (present %v)", test, v, present)
		}
	}
}

// A test decision is not a real one: feedback for its id is unknown, both when
// the index is built from the file and when it was already built.
func TestLog_TestDecisionsNotInFeedbackIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	l := &Log{Path: path}
	b := &fakeBackend{a: ans("web", 0.9, map[string]float64{"web": 0.9, "page": 0.1})}
	real := &Decider{Name: "p", Backends: []Backend{b}, Threshold: 0.25, Timeout: time.Second, Usage: noUsage, Log: l}
	test := &Decider{Name: "p", Backends: []Backend{b}, Threshold: 0.25, Timeout: time.Second, Usage: noUsage, Log: l, Test: true}
	r1 := real.Ask(context.Background(), Request{State: "s", Question: testQuestion})
	if err := l.Feedback(r1.ID, "web", nil); err != nil { // builds the index
		t.Fatal(err)
	}
	t1 := test.Ask(context.Background(), Request{State: "s", Question: testQuestion})
	if err := l.Feedback(t1.ID, "web", nil); !errors.Is(err, ErrUnknownDecision) {
		t.Errorf("feedback for a test decision (live index) = %v", err)
	}
	if err := (&Log{Path: path}).Feedback(t1.ID, "web", nil); !errors.Is(err, ErrUnknownDecision) {
		t.Errorf("feedback for a test decision (rebuilt index) = %v", err)
	}
}

// The usage line of a test decision says so in its label.
func TestDecider_TestUsageLabel(t *testing.T) {
	var got []cost.Usage
	rec := func(u cost.Usage) error { got = append(got, u); return nil }
	for _, test := range []bool{false, true} {
		b := &fakeBackend{a: ans("web", 0.9, map[string]float64{"web": 0.9})}
		d := &Decider{Name: "p", Backends: []Backend{b}, Threshold: 0.25, Timeout: time.Second, Usage: rec, Test: test}
		d.Ask(context.Background(), Request{State: "s", Question: testQuestion})
	}
	if len(got) != 2 || got[0].Label != "decide:p" || got[1].Label != "decide:p:test" {
		t.Errorf("labels = %+v", got)
	}
}

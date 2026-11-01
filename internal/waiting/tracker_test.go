package waiting

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTracker(t *testing.T, live map[string]int, now *time.Time) *Tracker {
	t.Helper()
	return &Tracker{Dir: t.TempDir(), Live: func() map[string]int { return live }, Now: func() time.Time { return *now }}
}

func TestCurrentShowsOnlyOpenSessions(t *testing.T) {
	now := t0.Add(time.Minute)
	tr := newTracker(t, map[string]int{"open": 10}, &now)
	_ = Write(tr.Dir, "open", "m", t0)
	_ = Write(tr.Dir, "closed", "m", t0)
	got := tr.Current()
	if len(got) != 1 || got["open"].Message != "m" {
		t.Fatalf("Current = %+v", got)
	}
	now = now.Add(MissFor)
	tr.Current()
	if _, ok := Read(tr.Dir, "closed"); ok {
		t.Fatal("an old marker of a closed session was kept")
	}
}

func TestCurrentKeepsAFreshMarkerOfAnUnseenSession(t *testing.T) {
	now := t0.Add(2 * time.Second)
	tr := newTracker(t, map[string]int{}, &now)
	_ = Write(tr.Dir, "new", "m", t0)
	if len(tr.Current()) != 0 {
		t.Fatal("an unseen session was shown")
	}
	if _, ok := Read(tr.Dir, "new"); !ok {
		t.Fatal("a fresh marker was removed before the registry could show its session")
	}
}

func TestCurrentWithoutLiveShowsNothing(t *testing.T) {
	tr := &Tracker{Dir: t.TempDir()}
	_ = Write(tr.Dir, "s", "m", t0)
	if len(tr.Current()) != 0 {
		t.Fatal("no liveness source should show nothing")
	}
}

func TestPollReportsAppearedAndGone(t *testing.T) {
	now := t0
	live := map[string]int{"a": 1, "b": 2}
	tr := newTracker(t, live, &now)
	_ = Write(tr.Dir, "a", "ma", t0)

	app, gone := tr.Poll()
	if len(app) != 1 || app[0].SessionID != "a" || app[0].Message != "ma" || len(gone) != 0 {
		t.Fatalf("first poll = %+v, %+v", app, gone)
	}
	if app, gone = tr.Poll(); len(app)+len(gone) != 0 {
		t.Fatalf("unchanged poll = %+v, %+v", app, gone)
	}

	// A new prompt in the same session: the old alert goes, a new one comes.
	_ = Write(tr.Dir, "a", "ma2", t0.Add(time.Minute))
	app, gone = tr.Poll()
	if len(app) != 1 || app[0].Message != "ma2" || len(gone) != 1 || !gone[0].Since.Equal(t0) {
		t.Fatalf("replaced poll = %+v, %+v", app, gone)
	}

	_ = Remove(tr.Dir, "a")
	app, gone = tr.Poll()
	if len(app) != 0 || len(gone) != 1 || gone[0].SessionID != "a" {
		t.Fatalf("removed poll = %+v, %+v", app, gone)
	}
}

func TestPollReportsGoneWhenTheSessionCloses(t *testing.T) {
	now := t0
	live := map[string]int{"a": 1}
	tr := newTracker(t, live, &now)
	_ = Write(tr.Dir, "a", "m", t0)
	_, _ = tr.Poll()
	delete(live, "a")
	_, gone := tr.Poll()
	if len(gone) != 1 {
		t.Fatalf("gone = %+v", gone)
	}
}

func transcript(t *testing.T, dir string, mtime time.Time) string {
	t.Helper()
	p := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCurrentDropsAMarkerOnceTheTranscriptMoves(t *testing.T) {
	now := t0.Add(time.Minute)
	tr := newTracker(t, map[string]int{"a": 1, "b": 2, "c": 3}, &now)
	quiet := transcript(t, t.TempDir(), t0.Add(2*time.Second)) // within the grace: still waiting
	moved := transcript(t, t.TempDir(), t0.Add(3*time.Second)) // written after: answered or interrupted
	_ = writeMarker(tr.Dir, marker{SessionID: "a", TranscriptPath: quiet, Waiting: Waiting{Kind: KindPermission, Since: t0}})
	_ = writeMarker(tr.Dir, marker{SessionID: "b", TranscriptPath: moved, Waiting: Waiting{Kind: KindPermission, Since: t0}})
	_ = writeMarker(tr.Dir, marker{SessionID: "c", TranscriptPath: filepath.Join(t.TempDir(), "gone.jsonl"), Waiting: Waiting{Kind: KindPermission, Since: t0}})
	got := tr.Current()
	if _, ok := got["a"]; !ok {
		t.Fatal("a quiet transcript dropped the marker")
	}
	if _, ok := got["b"]; ok {
		t.Fatal("a transcript written after the prompt kept the marker")
	}
	if _, ok := Read(tr.Dir, "b"); ok {
		t.Fatal("the moved marker's file was kept")
	}
	if _, ok := got["c"]; !ok {
		t.Fatal("an unreadable transcript should not drop the marker")
	}
}

func TestFirstPollSkipsOldMarkers(t *testing.T) {
	now := t0.Add(10 * time.Minute)
	tr := newTracker(t, map[string]int{"old": 1, "new": 2}, &now)
	_ = Write(tr.Dir, "old", "m", now.Add(-3*time.Minute))
	_ = Write(tr.Dir, "new", "m", now.Add(-time.Minute))
	app, _ := tr.Poll()
	if len(app) != 1 || app[0].SessionID != "new" {
		t.Fatalf("first poll appeared = %+v, want only the recent marker", app)
	}
	_ = Remove(tr.Dir, "old")
	if _, gone := tr.Poll(); len(gone) != 1 || gone[0].SessionID != "old" {
		t.Fatalf("gone = %+v: a skipped marker still retracts", gone)
	}
	_ = Write(tr.Dir, "later", "m", now.Add(-5*time.Minute))
	tr.Live = func() map[string]int { return map[string]int{"later": 3} }
	if app, _ := tr.Poll(); len(app) != 1 {
		t.Fatal("after the first poll every new marker is pushed, however old")
	}
}

func TestCurrentKeepsAMarkerWhileItsProcessLives(t *testing.T) {
	now := t0
	live := map[string]int{"a": 42}
	alive := true
	tr := newTracker(t, live, &now)
	tr.Alive = func(pid int) bool { return pid == 42 && alive }
	_ = Write(tr.Dir, "a", "m", t0)
	tr.Current() // seen live with pid 42
	delete(live, "a")
	now = t0.Add(time.Hour) // one missed registry read, long after
	if len(tr.Current()) != 0 {
		t.Fatal("a session missing from the registry was shown")
	}
	if _, ok := Read(tr.Dir, "a"); !ok {
		t.Fatal("one missed registry read removed a marker whose process lives")
	}
	alive = false
	tr.Current()
	if _, ok := Read(tr.Dir, "a"); ok {
		t.Fatal("a marker whose process is gone was kept")
	}
}

func TestCurrentNeedsTwoMissesForAnUnknownProcess(t *testing.T) {
	now := t0.Add(time.Hour)
	tr := newTracker(t, map[string]int{}, &now)
	tr.Alive = func(int) bool { return true }
	_ = Write(tr.Dir, "a", "m", t0)
	tr.Current()
	if _, ok := Read(tr.Dir, "a"); !ok {
		t.Fatal("removed on the first miss")
	}
	now = now.Add(MissFor)
	tr.Current()
	if _, ok := Read(tr.Dir, "a"); ok {
		t.Fatal("kept after misses spanning MissFor")
	}
}

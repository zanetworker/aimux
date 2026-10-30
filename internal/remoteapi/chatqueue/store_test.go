package chatqueue

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 29, 9, 30, 0, 0, time.UTC)

func clock() time.Time { return t0 }

func openAt(t *testing.T, path string, allowSlash bool) *Store {
	t.Helper()
	s, err := Open(path, clock, allowSlash)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	t.Cleanup(s.Close)
	return s
}

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "remote-queue.json")
	return openAt(t, path, false), path
}

func mustAdd(t *testing.T, s *Store, session, text, key string) Item {
	t.Helper()
	it, err := s.Add(session, text, key)
	if err != nil {
		t.Fatalf("Add(%s, %q): %v", session, text, err)
	}
	return it
}

func texts(q Queue) string {
	var out []string
	for _, it := range q.Items {
		out = append(out, it.Text)
	}
	return strings.Join(out, ",")
}

func wantErr(t *testing.T, err, want error, what string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: err = %v, want %v", what, err, want)
	}
}

func TestAddFillsItem(t *testing.T) {
	s, _ := newStore(t)
	a := mustAdd(t, s, "s1", "run the tests", "k1")
	b := mustAdd(t, s, "s1", "then lint", "")
	if a.Text != "run the tests" || a.Key != "k1" || a.State != StateWaiting || !a.Created.Equal(t0) {
		t.Fatalf("item = %+v", a)
	}
	if a.ID == "" || a.ID == b.ID {
		t.Fatalf("ids %q and %q, want distinct and non-empty", a.ID, b.ID)
	}
	if _, err := base64.RawURLEncoding.DecodeString(a.ID); err != nil || len(a.ID) > 16 {
		t.Fatalf("id %q, want short URL-safe base64: %v", a.ID, err)
	}
	q := s.Get("s1")
	if q.State != QueueRunning || q.Reason != "" || texts(q) != "run the tests,then lint" {
		t.Fatalf("queue = %+v", q)
	}
}

func TestGetUnknownSessionIsEmptyRunning(t *testing.T) {
	s, _ := newStore(t)
	q := s.Get("nope")
	if q.State != QueueRunning || q.Items == nil || len(q.Items) != 0 {
		t.Fatalf("queue = %#v, want running with an empty, non-nil list", q)
	}
}

func TestGetReturnsACopy(t *testing.T) {
	s, _ := newStore(t)
	mustAdd(t, s, "s1", "a", "")
	q := s.Get("s1")
	q.Items[0].Text = "changed"
	if got := s.Get("s1").Items[0].Text; got != "a" {
		t.Fatalf("store text = %q after changing a copy", got)
	}
}

func TestAddLimit(t *testing.T) {
	s, _ := newStore(t)
	for i := range MaxItems {
		mustAdd(t, s, "s1", fmt.Sprint("m", i), fmt.Sprint("k", i))
	}
	_, err := s.Add("s1", "one more", "k-extra")
	wantErr(t, err, ErrFull, "21st item")
	if n := len(s.Get("s1").Items); n != MaxItems {
		t.Fatalf("%d items, want %d", n, MaxItems)
	}
	// A retry of an item already queued is not a new item.
	if it, err := s.Add("s1", "m3", "k3"); err != nil || it.Text != "m3" {
		t.Fatalf("retry at the limit = %+v, %v", it, err)
	}
	// Another session has its own limit.
	mustAdd(t, s, "s2", "x", "")
}

func TestAddTextRules(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		allowSlash bool
		ok         bool
	}{
		{"plain", "fix the bug", false, true},
		{"newlines and tabs", "a\n\tb", false, true},
		{"bang", "!rm -rf /", false, false},
		{"bang after space", "  !ls", true, false},
		{"slash refused", "/clear", false, false},
		{"slash after space refused", " \n/clear", false, false},
		{"slash allowed", "/clear", true, true},
		{"escape", "x\x1b[201~y", true, false},
		{"carriage return", "x\ry", true, false},
		{"C1 control", "x\u009b", true, false},
		{"DEL", "x\x7f", true, false},
		{"empty", "", true, false},
		{"only white space", " \n\t ", true, false},
		{"at the size limit", strings.Repeat("a", MaxTextBytes), false, true},
		{"over the size limit", strings.Repeat("a", MaxTextBytes+1), false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := openAt(t, filepath.Join(t.TempDir(), "q.json"), c.allowSlash)
			_, err := s.Add("s1", c.text, "")
			if c.ok {
				if err != nil {
					t.Fatalf("Add: %v", err)
				}
				return
			}
			wantErr(t, err, ErrBadText, "Add")
			if n := len(s.Get("s1").Items); n != 0 {
				t.Fatalf("%d items after a refused add", n)
			}
		})
	}
}

func TestAddIdempotentByKey(t *testing.T) {
	s, _ := newStore(t)
	a := mustAdd(t, s, "s1", "a", "k1")
	again := mustAdd(t, s, "s1", "a edited by retry", "k1")
	if again.ID != a.ID || again.Text != "a" {
		t.Fatalf("retry = %+v, want the first item %+v", again, a)
	}
	mustAdd(t, s, "s1", "b", "")
	mustAdd(t, s, "s1", "b", "") // no key: always a new item
	if other := mustAdd(t, s, "s2", "a", "k1"); other.ID == a.ID {
		t.Fatal("keys are per session")
	}
	if got := texts(s.Get("s1")); got != "a,b,b" {
		t.Fatalf("items %q", got)
	}
}

func TestAddRetryAfterDoneOrDeleteIsNotRequeued(t *testing.T) {
	s, _ := newStore(t)
	a := mustAdd(t, s, "s1", "sent", "k-sent")
	b := mustAdd(t, s, "s1", "dropped", "k-dropped")
	if _, err := s.MarkSending("s1", a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Done("s1", a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("s1", b.ID); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"k-sent", "k-dropped"} {
		it, err := s.Add("s1", "retry", k)
		if err != nil || it.State != StateDone {
			t.Fatalf("retry of %s = %+v, %v; want state done", k, it, err)
		}
	}
	if n := len(s.Get("s1").Items); n != 0 {
		t.Fatalf("%d items after retries of finished items", n)
	}
}

func TestRetiredKeysAreBounded(t *testing.T) {
	s, _ := newStore(t)
	for i := range maxRetired + 1 {
		it := mustAdd(t, s, "s1", "x", fmt.Sprint("k", i))
		if err := s.Delete("s1", it.ID); err != nil {
			t.Fatal(err)
		}
	}
	// The oldest key was forgotten; the newest is still remembered.
	if it := mustAdd(t, s, "s1", "x", "k0"); it.State != StateWaiting {
		t.Fatalf("oldest key: state %q, want a new waiting item", it.State)
	}
	if it := mustAdd(t, s, "s1", "x", fmt.Sprint("k", maxRetired)); it.State != StateDone {
		t.Fatalf("newest key: state %q, want done", it.State)
	}
}

func TestMoveClampsToTheList(t *testing.T) {
	s, _ := newStore(t)
	a := mustAdd(t, s, "s1", "a", "")
	mustAdd(t, s, "s1", "b", "")
	c := mustAdd(t, s, "s1", "c", "")
	steps := []struct {
		id   string
		pos  int
		want string
	}{
		{c.ID, 0, "c,a,b"},
		{c.ID, 99, "a,b,c"},
		{c.ID, -5, "c,a,b"},
		{a.ID, 2, "c,b,a"},
		{a.ID, 2, "c,b,a"}, // same place
	}
	for _, st := range steps {
		if err := s.Move("s1", st.id, st.pos); err != nil {
			t.Fatal(err)
		}
		if got := texts(s.Get("s1")); got != st.want {
			t.Fatalf("after move to %d: %q, want %q", st.pos, got, st.want)
		}
	}
	wantErr(t, s.Move("s1", "missing", 0), ErrNotFound, "Move unknown item")
	wantErr(t, s.Move("other", a.ID, 0), ErrNotFound, "Move in another session")
}

func TestMoveNeverPassesTheItemInFlight(t *testing.T) {
	s, _ := newStore(t)
	a := mustAdd(t, s, "s1", "a", "")
	b := mustAdd(t, s, "s1", "b", "")
	c := mustAdd(t, s, "s1", "c", "")
	if _, err := s.MarkSending("s1", a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Move("s1", c.ID, 0); err != nil {
		t.Fatal(err)
	}
	if got := texts(s.Get("s1")); got != "a,c,b" {
		t.Fatalf("items %q, want the sending item kept first", got)
	}
	// Positions count the items not in flight: 1 is the last of c, b.
	if err := s.Move("s1", c.ID, 1); err != nil {
		t.Fatal(err)
	}
	if got := texts(s.Get("s1")); got != "a,b,c" {
		t.Fatalf("after move to 1: %q, want a,b,c (position among the waiting items)", got)
	}
	// The largest position goes last, never wraps around to the front.
	if err := s.Move("s1", b.ID, math.MaxInt); err != nil {
		t.Fatal(err)
	}
	if got := texts(s.Get("s1")); got != "a,c,b" {
		t.Fatalf("after move to MaxInt: %q, want a,c,b", got)
	}
	wantErr(t, s.Move("s1", a.ID, 2), ErrItemSending, "Move the sending item")
}

func TestEdit(t *testing.T) {
	s, _ := newStore(t)
	a := mustAdd(t, s, "s1", "a", "")
	if err := s.Edit("s1", a.ID, "a, but better"); err != nil {
		t.Fatal(err)
	}
	wantErr(t, s.Edit("s1", a.ID, "!ls"), ErrBadText, "Edit to bad text")
	wantErr(t, s.Edit("s1", a.ID, "/clear"), ErrBadText, "Edit to a slash command")
	wantErr(t, s.Edit("s1", a.ID, "  "), ErrBadText, "Edit to empty")
	wantErr(t, s.Edit("s1", "missing", "x"), ErrNotFound, "Edit unknown item")
	if got := texts(s.Get("s1")); got != "a, but better" {
		t.Fatalf("text %q", got)
	}
}

func TestInFlightItemCannotChange(t *testing.T) {
	for _, answering := range []bool{false, true} {
		s, _ := newStore(t)
		a := mustAdd(t, s, "s1", "a", "")
		if _, err := s.MarkSending("s1", a.ID); err != nil {
			t.Fatal(err)
		}
		if answering {
			if err := s.MarkAnswering("s1", a.ID); err != nil {
				t.Fatal(err)
			}
		}
		wantErr(t, s.Edit("s1", a.ID, "x"), ErrItemSending, "Edit in flight")
		wantErr(t, s.Delete("s1", a.ID), ErrItemSending, "Delete in flight")
		if q := s.Get("s1"); texts(q) != "a" {
			t.Fatalf("items %q", texts(q))
		}
	}
}

func TestPausedQueueLetsTheOwnerDeleteTheItemInFlight(t *testing.T) {
	s, _ := newStore(t)
	a := mustAdd(t, s, "s1", "a", "")
	mustAdd(t, s, "s1", "b", "")
	if _, err := s.MarkSending("s1", a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Pause("s1", "Your message is in the input box on your Mac"); err != nil {
		t.Fatal(err)
	}
	if got := s.Get("s1").Items[0].State; got != StateSending {
		t.Fatalf("state %q, want sending kept while paused", got)
	}
	wantErr(t, s.Edit("s1", a.ID, "x"), ErrItemSending, "Edit in flight while paused")
	if err := s.Delete("s1", a.ID); err != nil {
		t.Fatalf("Delete while paused: %v", err)
	}
	if q := s.Get("s1"); texts(q) != "b" || q.State != QueuePaused {
		t.Fatalf("queue %+v", q)
	}
}

func TestDelete(t *testing.T) {
	s, _ := newStore(t)
	a := mustAdd(t, s, "s1", "a", "")
	mustAdd(t, s, "s1", "b", "")
	if err := s.Delete("s1", a.ID); err != nil {
		t.Fatal(err)
	}
	wantErr(t, s.Delete("s1", a.ID), ErrNotFound, "Delete twice")
	if got := texts(s.Get("s1")); got != "b" {
		t.Fatalf("items %q", got)
	}
}

func TestOnlyResumeResumes(t *testing.T) {
	s, _ := newStore(t)
	a := mustAdd(t, s, "s1", "a", "")
	b := mustAdd(t, s, "s1", "b", "")
	const reason = "You used this session on your Mac"
	if err := s.Pause("s1", reason); err != nil {
		t.Fatal(err)
	}
	ops := []func() error{
		func() error { _, err := s.Add("s1", "c", ""); return err },
		func() error { return s.Edit("s1", a.ID, "a2") },
		func() error { return s.Move("s1", b.ID, 0) },
		func() error { return s.Delete("s1", a.ID) },
	}
	for i, op := range ops {
		if err := op(); err != nil {
			t.Fatalf("op %d: %v", i, err)
		}
		if q := s.Get("s1"); q.State != QueuePaused || q.Reason != reason {
			t.Fatalf("after op %d: %s %q, want paused with its reason", i, q.State, q.Reason)
		}
	}
	if err := s.Resume("s1"); err != nil {
		t.Fatal(err)
	}
	if q := s.Get("s1"); q.State != QueueRunning || q.Reason != "" {
		t.Fatalf("after Resume: %s %q", q.State, q.Reason)
	}
}

func TestPauseKeepsTheLatestReason(t *testing.T) {
	s, _ := newStore(t)
	mustAdd(t, s, "s1", "one", "")
	if err := s.Pause("s1", "first"); err != nil {
		t.Fatal(err)
	}
	if err := s.Pause("s1", "This session closed on your Mac"); err != nil {
		t.Fatal(err)
	}
	if q := s.Get("s1"); q.State != QueuePaused || q.Reason != "This session closed on your Mac" {
		t.Fatalf("queue %+v", q)
	}
	if err := s.Resume("never-paused"); err != nil {
		t.Fatalf("Resume of a running queue: %v", err)
	}
	// A queue with no items is always running (TestEmptyQueueIsRunning).
}

func TestNextSendsOneAtATime(t *testing.T) {
	s, _ := newStore(t)
	if _, ok := s.Next("s1"); ok {
		t.Fatal("Next on an empty queue")
	}
	a := mustAdd(t, s, "s1", "a", "")
	b := mustAdd(t, s, "s1", "b", "")
	if it, ok := s.Next("s1"); !ok || it.ID != a.ID {
		t.Fatalf("Next = %+v, %v; want a", it, ok)
	}
	wantErr(t, func() error { _, err := s.MarkSending("s1", b.ID); return err }(), ErrWrongState, "MarkSending not first")
	if err := s.Edit("s1", a.ID, "a, edited before the send"); err != nil {
		t.Fatal(err)
	}
	sent, err := s.MarkSending("s1", a.ID)
	if err != nil || sent.Text != "a, edited before the send" || sent.State != StateSending {
		t.Fatalf("MarkSending = %+v, %v; want the current text", sent, err)
	}
	if _, ok := s.Next("s1"); ok {
		t.Fatal("Next while an item is sending")
	}
	wantErr(t, func() error { _, err := s.MarkSending("s1", a.ID); return err }(), ErrWrongState, "MarkSending twice")
	if err := s.MarkAnswering("s1", a.ID); err != nil {
		t.Fatal(err)
	}
	wantErr(t, s.MarkAnswering("s1", a.ID), ErrWrongState, "MarkAnswering twice")
	if _, ok := s.Next("s1"); ok {
		t.Fatal("Next while an item is answering")
	}
	if err := s.Done("s1", a.ID); err != nil {
		t.Fatal(err)
	}
	wantErr(t, s.Done("s1", a.ID), ErrNotFound, "Done twice")
	if it, ok := s.Next("s1"); !ok || it.ID != b.ID {
		t.Fatalf("Next after Done = %+v, %v; want b", it, ok)
	}
	wantErr(t, s.MarkAnswering("s1", b.ID), ErrWrongState, "MarkAnswering a waiting item")
	wantErr(t, s.Done("s1", b.ID), ErrWrongState, "Done of a waiting item")
}

func TestNextWaitsWhilePaused(t *testing.T) {
	s, _ := newStore(t)
	a := mustAdd(t, s, "s1", "a", "")
	if err := s.Pause("s1", "No answer seen yet"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Next("s1"); ok {
		t.Fatal("Next on a paused queue")
	}
	wantErr(t, func() error { _, err := s.MarkSending("s1", a.ID); return err }(), ErrWrongState, "MarkSending while paused")
	if err := s.Resume("s1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Next("s1"); !ok {
		t.Fatal("Next after Resume")
	}
}

func TestDoneAfterResumeOfAnAnsweringItem(t *testing.T) {
	s, _ := newStore(t)
	a := mustAdd(t, s, "s1", "a", "")
	mustAdd(t, s, "s1", "b", "")
	if _, err := s.MarkSending("s1", a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkAnswering("s1", a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Pause("s1", "No answer seen yet"); err != nil {
		t.Fatal(err)
	}
	if err := s.Resume("s1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Next("s1"); ok {
		t.Fatal("Next must not offer b while a is still answering")
	}
	if err := s.Done("s1", a.ID); err != nil {
		t.Fatal(err)
	}
	if it, ok := s.Next("s1"); !ok || it.Text != "b" {
		t.Fatalf("Next = %+v, %v", it, ok)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	s, path := newStore(t)
	a := mustAdd(t, s, "s1", "line one\nline two", "k1")
	mustAdd(t, s, "s1", "b", "")
	mustAdd(t, s, "s2", "c", "k3")
	if err := s.Pause("s2", "This session closed on your Mac"); err != nil {
		t.Fatal(err)
	}
	d := mustAdd(t, s, "s1", "d", "k-d")
	if err := s.Delete("s1", d.ID); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("dir holds %d entries, want only the queue file (no temp files left)", len(entries))
	}
	want1, want2 := s.Get("s1"), s.Get("s2")
	s.Close()

	r := openAt(t, path, false)
	got1, got2 := r.Get("s1"), r.Get("s2")
	if fmt.Sprint(got1) != fmt.Sprint(want1) || fmt.Sprint(got2) != fmt.Sprint(want2) {
		t.Fatalf("reloaded\n%+v\n%+v\nwant\n%+v\n%+v", got1, got2, want1, want2)
	}
	if !got1.Items[0].Created.Equal(a.Created) || got1.Items[0].Key != "k1" {
		t.Fatalf("item %+v", got1.Items[0])
	}
	if it := mustAdd(t, r, "s1", "d", "k-d"); it.State != StateDone {
		t.Fatalf("retired key forgotten across a restart: %+v", it)
	}
}

func TestMissingFileStartsEmptyAndWritesNothing(t *testing.T) {
	s, path := newStore(t)
	if q := s.Get("s1"); len(q.Items) != 0 {
		t.Fatalf("queue %+v", q)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file exists before any change: %v", err)
	}
}

func TestLoadPausesQueuesWithAnItemInFlight(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote-queue.json")
	file := `{
	  "future": true,
	  "sessions": {
	    "sending":   {"state": "running", "items": [{"id": "a", "text": "x", "key": "ka", "created": "2026-10-29T09:00:00Z", "state": "sending", "extra": 1}, {"id": "b", "text": "y", "created": "2026-10-29T09:00:00Z", "state": "waiting"}]},
	    "answering": {"state": "running", "items": [{"id": "c", "text": "x", "created": "2026-10-29T09:00:00Z", "state": "answering"}]},
	    "waiting":   {"state": "running", "items": [{"id": "d", "text": "x", "created": "2026-10-29T09:00:00Z", "state": "waiting"}]}
	  }
	}`
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	s := openAt(t, path, false)
	for _, id := range []string{"sending", "answering"} {
		q := s.Get(id)
		if q.State != QueuePaused || q.Reason != ReasonMaybeSent {
			t.Fatalf("%s: %s %q, want paused with %q", id, q.State, q.Reason, ReasonMaybeSent)
		}
		if q.Items[0].State != StateWaiting {
			t.Fatalf("%s: first item %s, want waiting so the owner can resume or delete it", id, q.Items[0].State)
		}
		if _, ok := s.Next(id); ok {
			t.Fatalf("%s: Next offers an item before Resume", id)
		}
	}
	if err := s.Delete("sending", "a"); err != nil {
		t.Fatalf("Delete after restart: %v", err)
	}
	if q := s.Get("waiting"); q.State != QueueRunning {
		t.Fatalf("waiting: %s, want running", q.State)
	}
	if it, _ := s.Add("sending", "again", "ka"); it.State != StateDone {
		t.Fatalf("key of the deleted item: %+v", it)
	}
}

func TestLoadRefusesABadFile(t *testing.T) {
	cases := map[string]string{
		"not JSON":            `{"sessions": `,
		"unknown item state":  `{"sessions": {"s": {"state": "running", "items": [{"id": "a", "text": "x", "state": "lost"}]}}}`,
		"unknown queue state": `{"sessions": {"s": {"state": "stopped", "items": []}}}`,
		"item without id":     `{"sessions": {"s": {"state": "running", "items": [{"text": "x", "state": "waiting"}]}}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "remote-queue.json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			s, err := Open(path, clock, false)
			if err == nil {
				s.Close()
				t.Fatal("Open accepted a bad file")
			}
			if !strings.Contains(err.Error(), path) {
				t.Fatalf("error %q does not name %s", err, path)
			}
			if strings.Contains(err.Error(), `"x"`) {
				t.Fatalf("error %q carries message text", err)
			}
		})
	}
}

func TestFailedWriteLeavesTheQueueUnchanged(t *testing.T) {
	dir := t.TempDir()
	s := openAt(t, filepath.Join(dir, "remote-queue.json"), false)
	mustAdd(t, s, "s1", "a", "")
	if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // a read-only test dir makes the save fail
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // restore so TempDir cleanup can remove it
	_, err := s.Add("s1", "secret text", "")
	if err == nil {
		t.Fatal("Add succeeded with a read-only directory")
	}
	if !strings.Contains(err.Error(), dir) || strings.Contains(err.Error(), "secret text") {
		t.Fatalf("error %q: want the path and no message text", err)
	}
	if got := texts(s.Get("s1")); got != "a" {
		t.Fatalf("items %q after a failed write", got)
	}
}

func TestChangesNeverBlocksAndCoalesces(t *testing.T) {
	s, _ := newStore(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 60 {
			session := fmt.Sprint("s", i%2)
			it, err := s.Add(session, "x", "")
			if err != nil {
				t.Errorf("Add: %v", err)
				return
			}
			if err := s.Delete(session, it.ID); err != nil {
				t.Errorf("Delete: %v", err)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("changes with nobody reading Changes() blocked the store")
	}
	seen := map[string]bool{}
	timeout := time.After(2 * time.Second)
	for !seen["s0"] || !seen["s1"] {
		select {
		case id := <-s.Changes():
			seen[id] = true
		case <-timeout:
			t.Fatalf("saw %v, want both sessions", seen)
		}
	}
}

func TestChangesNamesTheSessionAndSkipsRefusals(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Add("s1", "!no", ""); err == nil {
		t.Fatal("bad text accepted")
	}
	select {
	case id := <-s.Changes():
		t.Fatalf("change %q for a refused add", id)
	case <-time.After(50 * time.Millisecond):
	}
	mustAdd(t, s, "s1", "ok", "")
	select {
	case id := <-s.Changes():
		if id != "s1" {
			t.Fatalf("change for %q, want s1", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no change after Add")
	}
}

func TestConcurrentUse(t *testing.T) {
	s, _ := newStore(t)
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-s.Changes():
			case <-stop:
				return
			}
		}
	}()
	watched := make(chan struct{})
	defer func() { close(stop); <-watched }()
	go func() { // every state any reader sees keeps the in-flight rule
		defer close(watched)
		for {
			select {
			case <-stop:
				return
			default:
			}
			q := s.Get("s1")
			n := 0
			for i, it := range q.Items {
				if inFlight(it) {
					n++
					if i != 0 {
						t.Errorf("item %d in flight, want only the first", i)
						return
					}
				}
			}
			if n > 1 {
				t.Errorf("%d items in flight", n)
				return
			}
		}
	}()
	var wg sync.WaitGroup
	for w := range 4 {
		wg.Add(4)
		go func() { // client adds, with retries
			defer wg.Done()
			for i := range 50 {
				_, _ = s.Add("s1", "x", fmt.Sprint("w", w, "-", i%10))
			}
		}()
		go func() { // client reorders and edits
			defer wg.Done()
			for i := range 50 {
				if q := s.Get("s1"); len(q.Items) > 0 {
					it := q.Items[i%len(q.Items)]
					_ = s.Move("s1", it.ID, len(q.Items)-1-i%len(q.Items))
					_ = s.Edit("s1", it.ID, "edited")
				}
			}
		}()
		go func() { // client deletes
			defer wg.Done()
			for range 50 {
				if q := s.Get("s1"); len(q.Items) > 2 {
					_ = s.Delete("s1", q.Items[len(q.Items)-1].ID)
				}
			}
		}()
		go func() { // dispatcher
			defer wg.Done()
			for range 50 {
				it, ok := s.Next("s1")
				if !ok {
					continue
				}
				if _, err := s.MarkSending("s1", it.ID); err != nil {
					continue
				}
				_ = s.MarkAnswering("s1", it.ID)
				time.Sleep(time.Millisecond) // let clients act while it is in flight
				_ = s.Done("s1", it.ID)
			}
		}()
	}
	wg.Wait()
	q := s.Get("s1")
	if len(q.Items) > MaxItems {
		t.Fatalf("%d items, over the limit", len(q.Items))
	}
	for i, it := range q.Items {
		if it.State != StateWaiting {
			t.Fatalf("item %d left %s with every send finished", i, it.State)
		}
	}
}

func TestConcurrentClaimsSendOnce(t *testing.T) {
	s, _ := newStore(t)
	mustAdd(t, s, "s1", "a", "")
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if it, ok := s.Next("s1"); ok {
				if _, err := s.MarkSending("s1", it.ID); err == nil {
					mu.Lock()
					wins++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d goroutines marked the item sending, want exactly 1", wins)
	}
}

func TestMarkSendingRefusesTextTheRulesNoLongerAllow(t *testing.T) {
	t.Run("hand-edited file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "remote-queue.json")
		file := `{"sessions": {"s1": {"state": "running", "items": [{"id": "a", "text": "!ls", "created": "2026-10-29T09:00:00Z", "state": "waiting"}]}}}`
		if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
			t.Fatal(err)
		}
		s := openAt(t, path, true)
		checkRefusedSend(t, s, "a")
	})
	t.Run("slash commands turned off later", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "remote-queue.json")
		before := openAt(t, path, true)
		it := mustAdd(t, before, "s1", "/clear", "")
		before.Close()
		checkRefusedSend(t, openAt(t, path, false), it.ID)
	})
}

func checkRefusedSend(t *testing.T, s *Store, id string) {
	t.Helper()
	if it, ok := s.Next("s1"); !ok || it.ID != id {
		t.Fatalf("Next = %+v, %v", it, ok)
	}
	_, err := s.MarkSending("s1", id)
	wantErr(t, err, ErrBadText, "MarkSending of text the rules refuse")
	q := s.Get("s1")
	if q.State != QueuePaused || q.Reason != ReasonNotAllowed {
		t.Fatalf("queue %s %q, want paused with %q", q.State, q.Reason, ReasonNotAllowed)
	}
	if q.Items[0].State != StateWaiting {
		t.Fatalf("item %s, want waiting", q.Items[0].State)
	}
	if err := s.Delete("s1", id); err != nil {
		t.Fatalf("Delete of the refused item: %v", err)
	}
}

func TestAddRetryFindsTheItemBeforeCheckingText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote-queue.json")
	before := openAt(t, path, true)
	first := mustAdd(t, before, "s1", "/clear", "k1")
	before.Close()
	s := openAt(t, path, false)
	it, err := s.Add("s1", "/clear", "k1")
	if err != nil || it.ID != first.ID {
		t.Fatalf("retry = %+v, %v; want the queued item", it, err)
	}
	_, err = s.Add("s1", "/clear", "k2")
	wantErr(t, err, ErrBadText, "new slash item with slash commands off")
}

func TestCloseEndsChanges(t *testing.T) {
	s, _ := newStore(t)
	mustAdd(t, s, "s1", "a", "")
	ended := make(chan struct{})
	go func() {
		for range s.Changes() {
		}
		close(ended)
	}()
	s.Close()
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("range over Changes() did not end after Close")
	}
	mustAdd(t, s, "s1", "b", "") // the store keeps working
}

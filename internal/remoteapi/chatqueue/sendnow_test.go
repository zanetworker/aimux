package chatqueue

import (
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/terminput"
)

// A claimed item stays at its place, waiting, and nothing else may take or
// change it until the claim ends.
func TestClaimNowHoldsTheItemInPlace(t *testing.T) {
	s, _ := newStore(t)
	a := mustAdd(t, s, "s1", "a", "")
	b := mustAdd(t, s, "s1", "b", "")
	got, err := s.ClaimNow("s1", a.ID)
	if err != nil || got.Text != "a" {
		t.Fatalf("ClaimNow = %+v, %v", got, err)
	}
	if q := s.Get("s1"); texts(q) != "a,b" || q.Items[0].State != StateWaiting {
		t.Fatalf("queue %+v", q)
	}
	if _, ok := s.Next("s1"); ok {
		t.Fatal("Next offered the claimed item")
	}
	_, err = s.MarkSending("s1", a.ID)
	wantErr(t, err, ErrWrongState, "MarkSending a claimed item")
	wantErr(t, s.Edit("s1", a.ID, "x"), ErrItemSending, "Edit claimed")
	wantErr(t, s.Move("s1", a.ID, 1), ErrItemSending, "Move claimed")
	wantErr(t, s.Delete("s1", a.ID), ErrItemSending, "Delete claimed")
	_, err = s.ClaimNow("s1", b.ID)
	wantErr(t, err, ErrBusy, "a second claim in the session")
	_, err = s.ClaimNow("s1", a.ID)
	wantErr(t, err, ErrItemSending, "the same claim twice")
}

// A refused send releases the claim: the item is where it was, and the
// dispatcher may take it again.
func TestReleaseNowKeepsTheItem(t *testing.T) {
	s, _ := newStore(t)
	a := mustAdd(t, s, "s1", "a", "")
	mustAdd(t, s, "s1", "b", "")
	if _, err := s.ClaimNow("s1", a.ID); err != nil {
		t.Fatal(err)
	}
	s.ReleaseNow("s1", a.ID)
	if q := s.Get("s1"); texts(q) != "a,b" {
		t.Fatalf("items %q", texts(q))
	}
	if it, ok := s.Next("s1"); !ok || it.ID != a.ID {
		t.Fatalf("Next = %+v, %v", it, ok)
	}
	if err := s.Edit("s1", a.ID, "x"); err != nil {
		t.Fatalf("Edit after release: %v", err)
	}
}

// A sent item leaves the queue and its key is retired, so a retried Add
// with that key does not queue it again.
func TestDoneNowRemovesTheItem(t *testing.T) {
	s, _ := newStore(t)
	mustAdd(t, s, "s1", "a", "")
	b := mustAdd(t, s, "s1", "b", "kb")
	mustAdd(t, s, "s1", "c", "")
	if _, err := s.ClaimNow("s1", b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DoneNow("s1", b.ID); err != nil {
		t.Fatal(err)
	}
	if q := s.Get("s1"); texts(q) != "a,c" {
		t.Fatalf("items %q", texts(q))
	}
	if it := mustAdd(t, s, "s1", "b", "kb"); it.State != StateDone {
		t.Fatalf("retry = %+v, want done", it)
	}
	if _, err := s.ClaimNow("s1", "nope"); err == nil {
		t.Fatal("claimed an unknown item")
	}
	wantErr(t, s.DoneNow("s1", b.ID), ErrWrongState, "DoneNow without a claim")
}

// Only waiting items can be sent now: never the one in flight.
func TestClaimNowRefusesTheItemInFlight(t *testing.T) {
	s, _ := newStore(t)
	a := mustAdd(t, s, "s1", "a", "")
	if _, err := s.MarkSending("s1", a.ID); err != nil {
		t.Fatal(err)
	}
	_, err := s.ClaimNow("s1", a.ID)
	wantErr(t, err, ErrItemSending, "ClaimNow in flight")
	_, err = s.ClaimNow("s1", "missing")
	wantErr(t, err, ErrNotFound, "ClaimNow unknown")
}

// A waiting item sent now while another one's answer runs: its turn (with
// Claude Code's "while you were working" line) is aimux's own, so the
// queue keeps running and the next item goes as usual.
func TestSentNowTurnIsNotTheOwners(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	r.add("s", "first")
	r.add("s", "second")
	third := r.add("s", "third")
	r.settle()
	r.wantSends("first")
	r.now = r.now.Add(time.Second)
	r.turn("first")
	r.term.status = terminput.Status{CanType: true, Working: true}
	r.step()

	// Send now of "third", as the API does it.
	if _, err := r.store.ClaimNow("s", third.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.store.DoneNow("s", third.ID); err != nil {
		t.Fatal(err)
	}
	r.d.NoteSentNow("s", "third", r.now)
	r.now = r.now.Add(time.Second)
	r.turn(workingPrefix + "\nthird")
	r.step()
	r.wantQueue("s", QueueRunning, "")
	r.wantFirst("s", "first", StateAnswering)

	r.term.status = free
	for range 2 {
		r.now = r.now.Add(2 * time.Second)
		r.step()
	}
	r.settle()
	r.wantSends("first", "second")
	r.wantQueue("s", QueueRunning, "")
}

// A note serves once, and only for 10 minutes: an older or a second turn
// with that text is the owner's.
func TestSentNowNoteExpiresAndServesOnce(t *testing.T) {
	for name, tc := range map[string]struct {
		later time.Duration
		twice bool
	}{"expired": {later: 11 * time.Minute}, "a second turn": {later: time.Second, twice: true}} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.openTerm("s")
			r.add("s", "first")
			r.add("s", "second")
			r.settle()
			r.now = r.now.Add(time.Second)
			r.turn("first")
			r.term.status = terminput.Status{CanType: true, Working: true}
			r.step()
			r.d.NoteSentNow("s", "fix it", r.now)
			r.now = r.now.Add(tc.later)
			r.turn(workingPrefix + "\nfix it")
			if tc.twice {
				r.now = r.now.Add(time.Millisecond)
				r.turn(workingPrefix + "\nfix it")
			}
			r.step()
			r.wantQueue("s", QueuePaused, ReasonUsedOnMac)
		})
	}
}

// Another message being typed (an item sending, or another claim) is
// ErrBusy for every other item; the item itself in flight is ErrItemSending.
func TestClaimNowWhileAnotherIsTyped(t *testing.T) {
	s, _ := newStore(t)
	a := mustAdd(t, s, "s1", "a", "")
	b := mustAdd(t, s, "s1", "b", "")
	if _, err := s.MarkSending("s1", a.ID); err != nil {
		t.Fatal(err)
	}
	_, err := s.ClaimNow("s1", b.ID)
	wantErr(t, err, ErrBusy, "claim while another is sending")
	_, err = s.ClaimNow("s1", a.ID)
	wantErr(t, err, ErrItemSending, "claim the sending item")
	if !s.Typing("s1") {
		t.Fatal("Typing false with an item sending")
	}
	if err := s.Release("s1", a.ID); err != nil {
		t.Fatal(err)
	}
	if s.Typing("s1") {
		t.Fatal("Typing true with nothing typed")
	}
	if _, err := s.ClaimNow("s1", a.ID); err != nil {
		t.Fatal(err)
	}
	if !s.Typing("s1") {
		t.Fatal("Typing false with a claim held")
	}
}

// A claim is saved before anything is typed: a restart between Enter and
// DoneNow pauses the queue as "may have been sent", never typing it again.
// A finished or released claim leaves nothing behind.
func TestClaimNowSurvivesARestartAsMaybeSent(t *testing.T) {
	for name, end := range map[string]func(s *Store, id string){
		"open":     func(*Store, string) {},
		"done":     func(s *Store, id string) { _ = s.DoneNow("s1", id) },
		"released": func(s *Store, id string) { s.ReleaseNow("s1", id) },
	} {
		t.Run(name, func(t *testing.T) {
			s, path := newStore(t)
			mustAdd(t, s, "s1", "a", "")
			b := mustAdd(t, s, "s1", "b", "")
			if _, err := s.ClaimNow("s1", b.ID); err != nil {
				t.Fatal(err)
			}
			end(s, b.ID)
			q := openAt(t, path, false).Get("s1")
			switch name {
			case "open":
				if q.State != QueuePaused || q.Reason != ReasonMaybeSent || texts(q) != "a,b" {
					t.Fatalf("queue %+v, want paused maybe sent with b kept", q)
				}
			case "done":
				if q.State != QueueRunning || texts(q) != "a" {
					t.Fatalf("queue %+v", q)
				}
			case "released":
				if q.State != QueueRunning || texts(q) != "a,b" {
					t.Fatalf("queue %+v", q)
				}
			}
		})
	}
}

// A note is made before typing; forget drops it when the send fails, so a
// later turn with that text is the owner's again.
func TestSentNowNoteForgottenAfterAFailedSend(t *testing.T) {
	r := newRig(t)
	r.openTerm("s")
	r.add("s", "first")
	r.add("s", "second")
	r.settle()
	r.now = r.now.Add(time.Second)
	r.turn("first")
	r.term.status = terminput.Status{CanType: true, Working: true}
	r.step()
	forget := r.d.NoteSentNow("s", "fix it", r.now)
	forget()
	forget() // twice is harmless
	r.now = r.now.Add(time.Second)
	r.turn(workingPrefix + "\nfix it")
	r.step()
	r.wantQueue("s", QueuePaused, ReasonUsedOnMac)
}

// Notes are bounded: each new note drops every session's expired ones, so
// composer notes for sessions without a queue never pile up.
func TestSentNowNotesArePrunedOnEachNote(t *testing.T) {
	r := newRig(t)
	r.d.NoteSentNow("x", "one", r.now)
	r.d.NoteSentNow("x", "two", r.now)
	r.now = r.now.Add(sentNowTTL + time.Second)
	r.d.NoteSentNow("y", "three", r.now)
	r.d.sentNowMu.Lock()
	defer r.d.sentNowMu.Unlock()
	if _, ok := r.d.sentNow["x"]; ok || len(r.d.sentNow["y"]) != 1 {
		t.Fatalf("notes %v, want only y's", r.d.sentNow)
	}
}

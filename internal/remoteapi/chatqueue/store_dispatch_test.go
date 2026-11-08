package chatqueue

import (
	"slices"
	"testing"
)

func TestSessionsListsQueuesWithItems(t *testing.T) {
	s, _ := newStore(t)
	mustAdd(t, s, "b", "one", "")
	mustAdd(t, s, "a", "two", "")
	it := mustAdd(t, s, "c", "three", "")
	if err := s.Delete("c", it.ID); err != nil {
		t.Fatal(err)
	}
	_ = s.Pause("d", "why") // a paused queue without items
	if got := s.Sessions(); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("Sessions = %v, want [a b]", got)
	}
}

func TestReleaseReturnsASendingItemToWaiting(t *testing.T) {
	s, _ := newStore(t)
	it := mustAdd(t, s, "s", "one", "")
	if _, err := s.MarkSending("s", it.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Release("s", it.ID); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Next("s"); !ok || got.ID != it.ID || got.State != StateWaiting {
		t.Fatalf("Next = %+v %v, want the item waiting", got, ok)
	}
	// Only a sending item: an answering one was sent.
	if _, err := s.MarkSending("s", it.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkAnswering("s", it.ID); err != nil {
		t.Fatal(err)
	}
	wantErr(t, s.Release("s", it.ID), ErrWrongState, "Release answering")
	wantErr(t, s.Release("s", "nope"), ErrNotFound, "Release unknown")
}

func TestTerminalBindingPersistsThroughRestartAndResume(t *testing.T) {
	s, path := newStore(t)
	mustAdd(t, s, "s", "one", "")
	if s.Terminal("s") {
		t.Fatal("a new queue is bound to the terminal")
	}
	if err := s.BindTerminal("s"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2 := openAt(t, path, false)
	if !s2.Terminal("s") {
		t.Fatal("the binding did not survive a restart")
	}
	if s2.Get("s").State != QueueRunning {
		t.Fatal("binding changed the queue state")
	}
	if err := s2.Pause("s", "closed"); err != nil {
		t.Fatal(err)
	}
	if err := s2.Resume("s"); err != nil {
		t.Fatal(err)
	}
	if !s2.Terminal("s") {
		t.Fatal("Resume cleared the binding: a terminal queue never moves to ACP")
	}
}

func TestBindingGoesWithTheLastKeyedItem(t *testing.T) {
	s, _ := newStore(t)
	it := mustAdd(t, s, "s", "one", "key-1")
	if err := s.BindTerminal("s"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("s", it.ID); err != nil {
		t.Fatal(err)
	}
	if s.Terminal("s") {
		t.Fatal("binding kept by an empty queue with retired keys")
	}
	if got, _ := s.Add("s", "one", "key-1"); got.State != StateDone {
		t.Fatalf("retired key forgotten: %+v", got)
	}
}

func TestEmptyQueueIsRunning(t *testing.T) {
	s, _ := newStore(t)
	it := mustAdd(t, s, "s", "one", "key-1")
	if err := s.Pause("s", "why"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("s", it.ID); err != nil {
		t.Fatal(err)
	}
	if q := s.Get("s"); q.State != QueueRunning || q.Reason != "" {
		t.Fatalf("emptied queue = %s %q, want running", q.State, q.Reason)
	}
	if err := s.Pause("s", "why"); err != nil {
		t.Fatal(err)
	}
	if q := s.Get("s"); q.State != QueueRunning {
		t.Fatalf("paused with nothing in it: %s %q", q.State, q.Reason)
	}
}

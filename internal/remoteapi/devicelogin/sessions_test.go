package devicelogin

import (
	"encoding/base64"
	"sync"
	"testing"
	"time"
)

// clock is a settable time for tests.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)} }

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func closed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestSessions_CreateAndCheck(t *testing.T) {
	c := newClock()
	s := NewSessions(c.now)
	tok := s.Create("dev1", "me@example.com")
	if b, err := base64.RawURLEncoding.Strict().DecodeString(tok); err != nil || len(b) != 32 {
		t.Fatalf("token %q is not 32 bytes of base64url", tok)
	}
	if tok2 := s.Create("dev1", "me@example.com"); tok2 == tok {
		t.Fatal("two sessions got the same token")
	}
	got, ok := s.Check(tok, "me@example.com", false)
	if !ok || got.DeviceID != "dev1" || got.Login != "me@example.com" || !got.Created.Equal(c.now()) {
		t.Fatalf("Check = %+v, %v", got, ok)
	}
	if _, ok := s.Check("nope", "me@example.com", false); ok {
		t.Fatal("unknown token accepted")
	}
	if _, ok := s.Check("", "me@example.com", false); ok {
		t.Fatal("empty token accepted")
	}
}

func TestSessions_OnlyTheLoginThatMadeIt(t *testing.T) {
	s := NewSessions(newClock().now)
	tok := s.Create("dev1", "me@example.com")
	if _, ok := s.Check(tok, "other@example.com", true); ok {
		t.Fatal("a session worked for another Tailscale login")
	}
	if _, ok := s.Check(tok, "me@example.com", false); !ok {
		t.Fatal("the wrong login ended the session")
	}
}

func TestSessions_IdleLimit(t *testing.T) {
	c := newClock()
	s := NewSessions(c.now)
	tok := s.Create("dev1", "me")
	c.add(50 * time.Minute)
	if _, ok := s.Check(tok, "me", false); !ok { // a poll: alive, but does not count as use
		t.Fatal("ended before the idle limit")
	}
	c.add(10*time.Minute - time.Second)
	if _, ok := s.Check(tok, "me", true); !ok { // 59m59s: use moves the clock
		t.Fatal("ended a second before the idle limit")
	}
	c.add(time.Hour - time.Second)
	if _, ok := s.Check(tok, "me", false); !ok {
		t.Fatal("use did not move the idle clock")
	}
	c.add(time.Second)
	if _, ok := s.Check(tok, "me", true); ok {
		t.Fatal("alive one hour after its last use")
	}
}

func TestSessions_AbsoluteLimit(t *testing.T) {
	c := newClock()
	s := NewSessions(c.now)
	tok := s.Create("dev1", "me")
	for range 14 { // 14 x 50 min = 11h40m, always in use
		c.add(50 * time.Minute)
		if _, ok := s.Check(tok, "me", true); !ok {
			t.Fatalf("ended at %v while in use", c.now())
		}
	}
	c.add(20*time.Minute - time.Second)
	if _, ok := s.Check(tok, "me", true); !ok {
		t.Fatal("ended a second before 12 hours")
	}
	c.add(time.Second)
	if _, ok := s.Check(tok, "me", true); ok {
		t.Fatal("alive after 12 hours")
	}
}

func TestSessions_EndClosesDone(t *testing.T) {
	s := NewSessions(newClock().now)
	tok := s.Create("dev1", "me")
	sess, _ := s.Check(tok, "me", false)
	if closed(sess.Done()) || closed(s.Done(tok)) {
		t.Fatal("done before the end")
	}
	s.End(tok)
	if !closed(sess.Done()) || !closed(s.Done(tok)) {
		t.Fatal("End did not close Done")
	}
	if _, ok := s.Check(tok, "me", true); ok {
		t.Fatal("ended session still checks")
	}
	s.End(tok) // twice is fine
	if !closed(s.Done("never issued")) {
		t.Fatal("Done of an unknown token must be closed")
	}
}

func TestSessions_EndDevice(t *testing.T) {
	s := NewSessions(newClock().now)
	a1, a2, b := s.Create("a", "me"), s.Create("a", "me"), s.Create("b", "me")
	if n := s.EndDevice("a"); n != 2 {
		t.Fatalf("EndDevice ended %d sessions, want 2", n)
	}
	if !closed(s.Done(a1)) || !closed(s.Done(a2)) || closed(s.Done(b)) {
		t.Fatal("EndDevice ended the wrong sessions")
	}
	if _, ok := s.Check(b, "me", false); !ok {
		t.Fatal("another device's session ended")
	}
}

func TestSessions_SweepClosesExpired(t *testing.T) {
	c := newClock()
	s := NewSessions(c.now)
	old := s.Create("a", "me")
	c.add(30 * time.Minute)
	fresh := s.Create("b", "me")
	c.add(30 * time.Minute)
	s.Sweep()
	if !closed(s.Done(old)) {
		t.Fatal("Sweep left an idle session open")
	}
	if closed(s.Done(fresh)) {
		t.Fatal("Sweep ended a live session")
	}
}

func TestSessions_ConcurrentUse(t *testing.T) {
	s := NewSessions(newClock().now)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok := s.Create("d", "me")
			s.Check(tok, "me", i%2 == 0)
			s.Sweep()
			if i%3 == 0 {
				s.EndDevice("d")
			}
			s.End(tok)
		}()
	}
	wg.Wait()
}

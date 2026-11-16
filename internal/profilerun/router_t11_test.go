package profilerun

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// checkingEngine is a fakeEngine that knows only some sessions.
type checkingEngine struct {
	fakeEngine
	known map[string]bool
}

func (c *checkingEngine) Check(session string) error {
	if !c.known[session] {
		return errors.New("session " + session + " is not known to the sandbox engine")
	}
	return nil
}

// A profiled session its engine cannot serve (lost on a restart) fails Check
// with the engine's own clear error, never silently passing.
func TestRouter_CheckAsksTheEngine(t *testing.T) {
	r, _, _, _ := newRouter(t)
	eng := &checkingEngine{known: map[string]bool{"known": true}}
	r.Engines["sandbox"] = eng
	for _, id := range []string{"known", "lost"} {
		if err := r.Store.Put(id, Meta{Profile: "work-readonly", Client: "wingman", Created: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Check("known"); err != nil {
		t.Fatalf("Check(known) = %v", err)
	}
	if err := r.Check("lost"); err == nil || !strings.Contains(err.Error(), "not known to the sandbox engine") {
		t.Fatalf("Check(lost) = %v, want the engine's error", err)
	}
}

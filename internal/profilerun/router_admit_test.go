package profilerun

import (
	"context"
	"errors"
	"testing"

	"github.com/zanetworker/aimux/internal/acprun"
)

// Admit refuses a new turn (a direct or queued message) for a profile that
// no longer checks, while Check and Cancel of the session still work.
func TestRouter_AdmitRefusesNewTurnsNotCancel(t *testing.T) {
	r, _, sandbox, _ := newRouter(t)
	if err := r.Store.Put("w1", Meta{Profile: "work-readonly", Client: "cli"}); err != nil {
		t.Fatal(err)
	}
	broken := errors.New("profile work-readonly: role planner: profile nope not found")
	r.Admit = func(p string) error {
		if p == "work-readonly" {
			return broken
		}
		return nil
	}
	if _, err := r.Send(context.Background(), "w1", "q", func(acprun.Event) {}); !errors.Is(err, broken) {
		t.Fatalf("Send = %v, want the admission error", err)
	}
	if len(sandbox.sends) != 0 {
		t.Fatal("a turn started for a profile that does not check")
	}
	if err := r.Cancel("w1"); err != nil {
		t.Fatalf("Cancel = %v, want it to reach the engine", err)
	}
	if len(sandbox.cancels) != 1 {
		t.Fatal("Cancel did not reach the engine")
	}
	r.Admit = nil
	if _, err := r.Send(context.Background(), "w1", "q", func(acprun.Event) {}); err != nil {
		t.Fatalf("Send without Admit = %v", err)
	}
}

// Send admits the same profile it routed by: a store change between the
// routing read and admission must not let a turn route under one profile
// and be admitted under another.
func TestRouter_AdmitChecksTheRoutedProfile(t *testing.T) {
	r, _, sandbox, _ := newRouter(t)
	if err := r.Store.Put("w1", Meta{Profile: "work-readonly", Client: "cli"}); err != nil {
		t.Fatal(err)
	}
	runs := r.Runs
	r.Runs = func(p string) (string, error) {
		// The session's Meta changes after routing read it.
		if err := r.Store.Put("w1", Meta{Profile: "page", Client: "cli"}); err != nil {
			t.Fatal(err)
		}
		return runs(p)
	}
	var admitted []string
	r.Admit = func(p string) error {
		admitted = append(admitted, p)
		if p == "work-readonly" {
			return errors.New("work-readonly roles broken")
		}
		return nil
	}
	if _, err := r.Send(context.Background(), "w1", "q", func(acprun.Event) {}); err == nil {
		t.Fatalf("Send admitted %v; want the routed profile work-readonly refused", admitted)
	}
	if len(admitted) != 1 || admitted[0] != "work-readonly" {
		t.Fatalf("Admit saw %v, want [work-readonly]", admitted)
	}
	if len(sandbox.sends) != 0 {
		t.Fatal("a turn started for a profile that does not check")
	}
}

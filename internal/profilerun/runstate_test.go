package profilerun

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/zanetworker/aimux/internal/runcheck"
)

// A run session's stage state is saved with its Meta, so a session adopted
// after a restart still reports its last stage.
func TestStore_RunStateRoundTrip(t *testing.T) {
	s := newStore(t)
	if err := s.Put("r1", Meta{Profile: "research-with-work", Client: "wingman"}); err != nil {
		t.Fatal(err)
	}
	want := RunState{
		Stage:            StageReading,
		Children:         map[string]string{"planner": "p1", "reader": "rd1"},
		Accepted:         []string{"openshell egress release notes"},
		Rejected:         []runcheck.Rejection{{Index: 1, Reason: runcheck.ReasonIssueKey}},
		OutsideAttempted: []string{"gh"},
		OutsideSucceeded: []string{"gh"},
		Links:            []string{"https://github.com/NVIDIA/OpenShell/releases"},
	}
	if err := s.SetRun("r1", want); err != nil {
		t.Fatal(err)
	}
	m, ok := s.Get("r1")
	if !ok || m.Run == nil || !reflect.DeepEqual(*m.Run, want) {
		t.Fatalf("Get = %+v %v, want Run %+v", m.Run, ok, want)
	}
	if m.Profile != "research-with-work" || m.Client != "wingman" {
		t.Fatalf("SetRun dropped the rest of Meta: %+v", m)
	}
	if err := s.SetRun("missing", want); err == nil {
		t.Fatal("SetRun on a session not in the store succeeded")
	}
}

// Files written before RunState existed still load, with no run state.
func TestStore_OldFilesWithoutRunLoad(t *testing.T) {
	s := newStore(t)
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path, []byte(`{"s1":{"Profile":"page","Client":"cli"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m, ok := s.Get("s1")
	if !ok || m.Profile != "page" || m.Run != nil {
		t.Fatalf("Get = %+v %v", m, ok)
	}
}

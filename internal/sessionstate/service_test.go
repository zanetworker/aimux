package sessionstate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/harness"
)

type busySet map[string]bool

func (b busySet) Busy(id string) bool             { return b[id] }
func (b busySet) AwaitingApproval(id string) bool { return b[id] }

func TestService_CombinesInputs(t *testing.T) {
	reads := 0
	s := &Service{
		Holders: func() map[string]harness.Holder {
			reads++
			return map[string]harness.Holder{
				"held-idle": {PID: 1, SessionID: "held-idle", Status: "idle"},
				"held-busy": {PID: 2, SessionID: "held-busy", Status: "busy"},
				"held-wait": {PID: 3, SessionID: "held-wait", Status: "busy"},
				"guessed":   {PID: 4, SessionID: "guessed"},
			}
		},
		Busy:      busySet{"run": true, "run-ask": true},
		Approvals: busySet{"run-ask": true},
		Markers:   func() map[string]Wait { return map[string]Wait{"held-wait": *marker} },
		Fallback:  func() map[string]Activity { return map[string]Activity{"guessed": Working} },
	}
	want := map[string]Activity{"held-idle": Idle, "held-busy": Working, "held-wait": NeedsYou, "guessed": Working,
		"run": Working, "run-ask": NeedsYou, "nobody": Gone}
	snap := s.Read()
	if reads != 1 {
		t.Fatalf("Read read the holders %d times, want 1", reads)
	}
	for id, w := range want {
		if got := snap.Of(id).Activity; got != w {
			t.Errorf("snapshot Of(%s) = %s, want %s", id, got, w)
		}
		if got := s.Of(id).Activity; got != w {
			t.Errorf("Of(%s) = %s, want %s", id, got, w)
		}
	}
	if w := snap.Of("held-wait").Waiting; w == nil || w.Message != marker.Message {
		t.Errorf("held-wait carries waiting %v", w)
	}
	if got := snap.Of("held-busy").RegistryStatus; got != "busy" {
		t.Errorf("held-busy RegistryStatus = %q", got)
	}
	all := s.All()
	for _, id := range []string{"held-idle", "held-busy", "held-wait", "guessed"} {
		if all[id].Activity != want[id] {
			t.Errorf("All()[%s] = %s, want %s", id, all[id].Activity, want[id])
		}
	}
	if _, ok := all["nobody"]; ok {
		t.Error("All lists a session no input knows")
	}
}

func TestService_NilInputs(t *testing.T) {
	var s Service
	if got := s.Of("x"); got.Activity != Gone || got.Place != None {
		t.Fatalf("zero Service Of = %+v, want gone/none", got)
	}
	if len(s.All()) != 0 {
		t.Fatal("zero Service All is not empty")
	}
}

// A marker for a session no input holds counts as needs_you only when the
// marker source reports it: the markers source decides which markers are
// live (waiting.Tracker drops closed sessions' markers).
func TestService_MarkerOnly(t *testing.T) {
	s := &Service{Markers: func() map[string]Wait { return map[string]Wait{"m": *marker} }}
	if got := s.All()["m"].Activity; got != NeedsYou {
		t.Fatalf("All()[m] = %s, want needs_you", got)
	}
}

// TestNoHarnessNames: the package names no harness (sessionstate's rule 5).
func TestNoHarnessNames(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f) //nolint:gosec // package files
		if err != nil {
			t.Fatal(err)
		}
		low := strings.ToLower(string(data))
		for _, name := range []string{"claude", "codex", "gemini"} {
			if strings.Contains(low, name) {
				t.Errorf("%s names the harness %q", f, name)
			}
		}
	}
}

// Holders is the snapshot's own read, for a caller that lists sessions
// from the same read (the remote list's state and working).
func TestSnapshot_Holders(t *testing.T) {
	reads := 0
	s := &Service{Holders: func() map[string]harness.Holder {
		reads++
		return map[string]harness.Holder{"a": {PID: 1, SessionID: "a", Status: "busy"}}
	}}
	snap := s.Read()
	got := snap.Holders()
	if reads != 1 || len(got) != 1 || got["a"].Status != "busy" {
		t.Fatalf("Holders = %v after %d reads", got, reads)
	}
	delete(got, "a")
	if snap.Of("a").Activity != Working {
		t.Fatal("a caller's change reached the snapshot")
	}
	if len((&Service{}).Read().Holders()) != 0 {
		t.Fatal("no Holders input gives holders")
	}
}

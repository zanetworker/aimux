package harness

import "testing"

func TestOpenSessions_ExcludesOwnedAndKeysByID(t *testing.T) {
	hs := []Holder{
		{PID: 10, SessionID: "a", Status: "busy"},
		{PID: 11, SessionID: "b", Status: "idle"},
		{PID: 12, SessionID: "c"},
	}
	got := OpenSessions(hs, func(pid int) bool { return pid == 11 })
	if len(got) != 2 || got["a"].PID != 10 || got["a"].Status != "busy" || got["c"].PID != 12 {
		t.Fatalf("OpenSessions = %+v, want a and c, b left out as owned", got)
	}
	if _, ok := got["b"]; ok {
		t.Fatal("an owned holder's session counted as open")
	}
}

func TestOpenSessions_OwnedHolderDoesNotHideTerminalHolder(t *testing.T) {
	hs := []Holder{{PID: 1, SessionID: "s", Status: "busy"}, {PID: 2, SessionID: "s", Status: "idle"}}
	got := OpenSessions(hs, func(pid int) bool { return pid == 1 })
	if got["s"].PID != 2 || got["s"].Status != "idle" {
		t.Fatalf("OpenSessions = %+v, want the terminal holder (pid 2)", got)
	}
}

func TestOpenSessions_NilExcludeAndEmpty(t *testing.T) {
	if got := OpenSessions(nil, nil); got == nil || len(got) != 0 {
		t.Fatalf("OpenSessions(nil, nil) = %v, want an empty non-nil map", got)
	}
	got := OpenSessions([]Holder{{PID: 3, SessionID: "x"}}, nil)
	if got["x"].PID != 3 {
		t.Fatalf("nil exclude: got %+v", got)
	}
}

func TestOpenSessions_TwoHoldersKeepTheHighestPID(t *testing.T) {
	for _, hs := range [][]Holder{
		{{PID: 5, SessionID: "s", Status: "idle"}, {PID: 9, SessionID: "s", Status: "busy"}},
		{{PID: 9, SessionID: "s", Status: "busy"}, {PID: 5, SessionID: "s", Status: "idle"}},
	} {
		if got := OpenSessions(hs, nil)["s"]; got.PID != 9 || got.Status != "busy" {
			t.Errorf("OpenSessions(%v)[s] = %+v, want pid 9 whatever the order", hs, got)
		}
	}
}

package controller

import (
	"errors"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
)

func liveAgents() []agent.Agent {
	return []agent.Agent{
		{PID: 101, SessionID: "19c0ce61-aaaa-4000-8000-000000000001"},
		{PID: 202, SessionID: "19c0ff00-bbbb-4000-8000-000000000002"},
		{PID: 303, SessionID: "7d2e0000-cccc-4000-8000-000000000003", TMuxSession: "aimux-codex"},
		{PID: 404}, // no session ID yet
	}
}

func TestFindLiveAgent_ExactSessionID(t *testing.T) {
	a, err := FindLiveAgent(liveAgents(), "19c0ff00-bbbb-4000-8000-000000000002")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.PID != 202 {
		t.Errorf("PID = %d, want 202", a.PID)
	}
}

func TestFindLiveAgent_UniquePrefix(t *testing.T) {
	a, err := FindLiveAgent(liveAgents(), "19c0ce")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.PID != 101 {
		t.Errorf("PID = %d, want 101", a.PID)
	}
}

func TestFindLiveAgent_PID(t *testing.T) {
	a, err := FindLiveAgent(liveAgents(), "404")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.PID != 404 {
		t.Errorf("PID = %d, want 404", a.PID)
	}
}

func TestFindLiveAgent_AmbiguousPrefix(t *testing.T) {
	_, err := FindLiveAgent(liveAgents(), "19c0")
	if !errors.Is(err, ErrAmbiguousAgent) {
		t.Fatalf("err = %v, want ErrAmbiguousAgent", err)
	}
	if !strings.Contains(err.Error(), "19c0ce61") || !strings.Contains(err.Error(), "19c0ff00") {
		t.Errorf("error should name both candidates, got %q", err)
	}
}

func TestFindLiveAgent_NotFound(t *testing.T) {
	_, err := FindLiveAgent(liveAgents(), "deadbeef")
	if !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("err = %v, want ErrAgentNotFound", err)
	}
}

func TestFindLiveAgent_EmptyRef(t *testing.T) {
	_, err := FindLiveAgent(liveAgents(), "  ")
	if !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("err = %v, want ErrAgentNotFound", err)
	}
}

func TestFindLiveAgent_ExactBeatsPrefix(t *testing.T) {
	// A full ID that is also a prefix of nothing else must not be ambiguous,
	// and an exact PID match wins even if some session ID starts with it.
	agents := []agent.Agent{{PID: 19, SessionID: "aaaa"}, {PID: 7, SessionID: "19ab"}}
	a, err := FindLiveAgent(agents, "19")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.PID != 19 {
		t.Errorf("PID = %d, want 19 (exact PID match)", a.PID)
	}
}

func TestFocusAgent_FocusesITermPane(t *testing.T) {
	var gotTTY string
	f := Focuser{
		TTYForPID: func(pid int) string { return "/dev/ttys031" },
		FocusTTY: func(tty string) (string, bool, error) {
			gotTTY = tty
			return "tab 3 · pane 1 of 2", true, nil
		},
	}
	res, err := FocusAgent(agent.Agent{PID: 101, SessionID: "s1"}, f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTTY != "/dev/ttys031" {
		t.Errorf("focused tty %q, want /dev/ttys031", gotTTY)
	}
	if !res.Focused || res.Where != "tab 3 · pane 1 of 2" || res.TTY != "/dev/ttys031" || res.PID != 101 || res.SessionID != "s1" {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestFocusAgent_NoTTY_TmuxHint(t *testing.T) {
	f := Focuser{
		TTYForPID: func(int) string { return "" },
		FocusTTY: func(string) (string, bool, error) {
			t.Fatal("FocusTTY must not be called without a tty")
			return "", false, nil
		},
	}
	res, err := FocusAgent(agent.Agent{PID: 303, TMuxSession: "aimux-codex"}, f)
	if !errors.Is(err, ErrNoPane) {
		t.Fatalf("err = %v, want ErrNoPane", err)
	}
	if res.Focused {
		t.Error("Focused should be false")
	}
	if !strings.Contains(res.Hint, "aimux-codex") {
		t.Errorf("Hint should name the tmux session, got %q", res.Hint)
	}
}

func TestFocusAgent_PaneNotInITerm(t *testing.T) {
	f := Focuser{
		TTYForPID: func(int) string { return "/dev/ttys009" },
		FocusTTY:  func(string) (string, bool, error) { return "", false, nil },
	}
	res, err := FocusAgent(agent.Agent{PID: 101}, f)
	if !errors.Is(err, ErrNoPane) {
		t.Fatalf("err = %v, want ErrNoPane", err)
	}
	if res.TTY != "/dev/ttys009" || res.Hint != "" {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestFocusAgent_OsascriptError(t *testing.T) {
	boom := errors.New("osascript: not authorized")
	f := Focuser{
		TTYForPID: func(int) string { return "/dev/ttys009" },
		FocusTTY:  func(string) (string, bool, error) { return "", false, boom },
	}
	_, err := FocusAgent(agent.Agent{PID: 101}, f)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped osascript error", err)
	}
}

func TestFocusAgent_OsascriptError_KeepsTmuxHint(t *testing.T) {
	f := Focuser{
		TTYForPID: func(int) string { return "/dev/pts/3" },
		FocusTTY:  func(string) (string, bool, error) { return "", false, errors.New("osascript: not found") },
	}
	res, err := FocusAgent(agent.Agent{PID: 303, TMuxSession: "aimux-codex"}, f)
	if err == nil {
		t.Fatal("want an error")
	}
	if res.Hint != "tmux attach-session -t aimux-codex" {
		t.Errorf("Hint = %q, want the tmux attach command", res.Hint)
	}
}

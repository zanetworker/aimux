package controller

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/harness"
	"github.com/zanetworker/aimux/internal/waiting"
)

type fixedLive []harness.Holder

func (f fixedLive) Holders(harness.Mode) ([]harness.Holder, error) { return f, nil }

func byID(agents []agent.Agent) map[string]agent.Agent {
	out := map[string]agent.Agent{}
	for _, a := range agents {
		out[a.SessionID] = a
	}
	return out
}

// The TUI and the dashboard show the state service's activity: registry
// status beats the transcript guess, a hook marker reads needs you, and a
// harness without a registry falls back to its guess.
func TestLocalActivity_Apply(t *testing.T) {
	dir := t.TempDir()
	me := os.Getpid() // a live pid, so the marker's session counts as open
	live := fixedLive{
		{PID: me, SessionID: "asking", Status: "busy"},
		{PID: 101, SessionID: "busy", Status: "busy"},
		{PID: 102, SessionID: "quiet", Status: "idle"},
	}
	if err := waiting.Write(dir, "asking", "Claude needs your permission to use Bash", time.Now()); err != nil {
		t.Fatal(err)
	}
	agents := []agent.Agent{
		{PID: me, SessionID: "asking", Status: agent.StatusIdle},
		{PID: 101, SessionID: "busy", Status: agent.StatusIdle},               // transcript says idle, registry busy
		{PID: 102, SessionID: "quiet", Status: agent.StatusWaitingPermission}, // the old guess: a running tool read as a prompt
		{PID: 103, SessionID: "codex-run", Status: agent.StatusActive},        // no registry: the guess decides
		{PID: 104, SessionID: "codex-idle", Status: agent.StatusIdle},
		{PID: 105, SessionID: "crashed", Status: agent.StatusError}, // idle with an error keeps the error
		{PID: 0, SessionID: "", Status: agent.StatusIdle},           // no session: untouched
	}
	NewLocalActivity([]harness.LiveSessions{live}, dir).Apply(agents)
	got := byID(agents)
	want := map[string]struct {
		activity string
		status   agent.Status
	}{
		"asking":     {"needs_you", agent.StatusWaitingPermission},
		"busy":       {"working", agent.StatusActive},
		"quiet":      {"idle", agent.StatusIdle},
		"codex-run":  {"working", agent.StatusActive},
		"codex-idle": {"idle", agent.StatusIdle},
		"crashed":    {"idle", agent.StatusError},
		"":           {"", agent.StatusIdle},
	}
	for id, w := range want {
		if got[id].Activity != w.activity || got[id].Status != w.status {
			t.Errorf("%q: activity %q status %v, want %q %v", id, got[id].Activity, got[id].Status, w.activity, w.status)
		}
	}
}

// A marker of a session nobody holds is not shown (waiting.Tracker's rule,
// the same in every process).
func TestLocalActivity_ClosedMarkerIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := waiting.Write(dir, "closed", "Claude needs your permission to use Bash", time.Now()); err != nil {
		t.Fatal(err)
	}
	agents := []agent.Agent{{PID: 0, SessionID: "closed", Status: agent.StatusIdle}}
	NewLocalActivity(nil, dir).Apply(agents)
	if agents[0].Activity != "gone" || agents[0].Status != agent.StatusIdle {
		t.Fatalf("closed: activity %q status %v, want gone and untouched", agents[0].Activity, agents[0].Status)
	}
}

func TestLocalActivity_NilIsANoOp(t *testing.T) {
	agents := []agent.Agent{{PID: 1, SessionID: "s", Status: agent.StatusActive}}
	var l *LocalActivity
	l.Apply(agents)
	if agents[0].Activity != "" || agents[0].Status != agent.StatusActive {
		t.Fatalf("nil LocalActivity changed the agent: %+v", agents[0])
	}
}

// ActivityBySession is what the TUI's sessions view reads: each agent's
// activity by session id; agents without either are left out.
func TestActivityBySession(t *testing.T) {
	got := ActivityBySession([]agent.Agent{
		{SessionID: "a", Activity: "needs_you"},
		{SessionID: "b", Activity: "working"},
		{SessionID: "", Activity: "idle"},
		{SessionID: "c"},
	})
	if len(got) != 2 || got["a"] != "needs_you" || got["b"] != "working" {
		t.Fatalf("ActivityBySession = %v", got)
	}
	if len(ActivityBySession(nil)) != 0 {
		t.Fatal("nil agents give activity")
	}
}

// HooksInstalled reads Claude Code's settings once, then again only after
// HooksRecheck: the TUI and dashboard hint at `aimux remote hooks install`
// while the hooks that write permission markers are missing.
func TestLocalActivity_HooksInstalled(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	clock := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	l := NewLocalActivity(nil, t.TempDir())
	l.HooksSettings, l.Now = settings, func() time.Time { return clock }
	if l.HooksInstalled() {
		t.Fatal("no settings file: hooks installed")
	}
	if _, err := waiting.Install(settings, "/usr/local/bin/aimux remote hooks notify || true", clock); err != nil {
		t.Fatal(err)
	}
	if l.HooksInstalled() {
		t.Fatal("re-read before HooksRecheck: the answer must be the cached one")
	}
	clock = clock.Add(HooksRecheck)
	if !l.HooksInstalled() {
		t.Fatal("after HooksRecheck: hooks not seen installed")
	}
}

// Without a settings path (or a LocalActivity) nothing is known, and no
// hint shows.
func TestLocalActivity_HooksUnknownIsInstalled(t *testing.T) {
	if !NewLocalActivity(nil, t.TempDir()).HooksInstalled() {
		t.Fatal("no settings path: want installed (no hint)")
	}
	var l *LocalActivity
	if !l.HooksInstalled() {
		t.Fatal("nil LocalActivity: want installed (no hint)")
	}
}

// The TUI with Claude disabled has no Claude liveness, yet the markers dir
// is shared with remote serve: a marker of a Claude session still waiting
// (its pid alive, outside this frontend's providers) must survive, so the
// phone's alert stays. Frontend trackers only read.
func TestLocalActivity_KeepsMarkersOutsideItsProviders(t *testing.T) {
	dir := t.TempDir()
	start := time.Now().Add(-time.Hour)
	if err := waiting.Write(dir, "claude-waiting", "Claude needs your permission to use Bash", start); err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	l := NewLocalActivity(nil, dir) // no live source: Claude disabled here
	l.Now = func() time.Time { return clock }
	agents := []agent.Agent{{PID: 0, SessionID: "codex-1", Status: agent.StatusIdle}}
	l.Apply(agents)
	clock = clock.Add(waiting.StaleAfter + waiting.MissFor + time.Second)
	l.Apply(agents)
	clock = clock.Add(time.Hour)
	l.Apply(agents)
	if _, ok := waiting.Read(dir, "claude-waiting"); !ok {
		t.Fatal("the frontend's tracker deleted a marker it cannot judge")
	}
}

// An error the provider found (a context overflow, a crash) stays visible:
// registry busy or a permission marker sets the activity but never hides
// StatusError, as the idle case already did.
func TestLocalActivity_ErrorSurvivesWorkingAndNeedsYou(t *testing.T) {
	dir := t.TempDir()
	me := os.Getpid()
	live := fixedLive{
		{PID: me, SessionID: "err-asking", Status: "busy"},
		{PID: 201, SessionID: "err-busy", Status: "busy"},
	}
	if err := waiting.Write(dir, "err-asking", "Claude needs your permission to use Bash", time.Now()); err != nil {
		t.Fatal(err)
	}
	agents := []agent.Agent{
		{PID: me, SessionID: "err-asking", Status: agent.StatusError},
		{PID: 201, SessionID: "err-busy", Status: agent.StatusError},
	}
	NewLocalActivity([]harness.LiveSessions{live}, dir).Apply(agents)
	got := byID(agents)
	if a := got["err-asking"]; a.Activity != "needs_you" || a.Status != agent.StatusError {
		t.Errorf("err-asking: activity %q status %v, want needs_you and StatusError", a.Activity, a.Status)
	}
	if a := got["err-busy"]; a.Activity != "working" || a.Status != agent.StatusError {
		t.Errorf("err-busy: activity %q status %v, want working and StatusError", a.Activity, a.Status)
	}
}

// Wrap gives a plain discover func (the CLI's `aimux agents`, kill, focus)
// the same activity the TUI and dashboard apply; a discover error passes
// through untouched; a nil LocalActivity returns discover as it is.
func TestLocalActivity_Wrap(t *testing.T) {
	dir := t.TempDir()
	live := fixedLive{{PID: 301, SessionID: "busy", Status: "busy"}}
	discover := func() ([]agent.Agent, error) {
		return []agent.Agent{{PID: 301, SessionID: "busy", Status: agent.StatusIdle}}, nil
	}
	got, err := NewLocalActivity([]harness.LiveSessions{live}, dir).Wrap(discover)()
	if err != nil || len(got) != 1 || got[0].Activity != "working" || got[0].Status != agent.StatusActive {
		t.Fatalf("wrapped: %+v %v, want one working agent", got, err)
	}
	boom := errors.New("boom")
	if _, err := NewLocalActivity(nil, dir).Wrap(func() ([]agent.Agent, error) { return nil, boom })(); !errors.Is(err, boom) {
		t.Fatalf("error not passed through: %v", err)
	}
	var none *LocalActivity
	if got, _ := none.Wrap(discover)(); got[0].Activity != "" || got[0].Status != agent.StatusIdle {
		t.Fatalf("nil LocalActivity changed agents: %+v", got)
	}
}

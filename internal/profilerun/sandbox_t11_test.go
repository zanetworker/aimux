package profilerun

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/profile"
)

// A sandbox deleted out of band makes Exec fail to start: the next turn
// must Ensure again rather than trust the cached digest.
func TestSandboxEngine_ExecStartFailureForgetsEnsured(t *testing.T) {
	e, b := newSandbox(t)
	sid, err := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	if err != nil {
		t.Fatal(err)
	}
	b.script = func(string) (string, error, error) { return "", nil, errors.New("sandbox not found") }
	if _, _, err := sendCollect(t, e, sid, "q"); err == nil {
		t.Fatal("a failed exec reported success")
	}
	b.script = func(s string) (string, error, error) { return okStream(s), nil, nil }
	if _, _, err := sendCollect(t, e, sid, "q"); err != nil {
		t.Fatal(err)
	}
	if got := b.ensures["aimux-work-readonly"]; got != 2 {
		t.Fatalf("ensures %d after a failed exec, want 2 (ensure again)", got)
	}
}

// A caller waiting for another caller's Ensure gives up when its context ends.
func TestSandboxEngine_EnsureWaiterRespectsContext(t *testing.T) {
	e, _ := newSandbox(t)
	c := compiledFor("work-readonly")
	l := e.sandboxLock(c.Sandbox)
	l <- struct{}{} // another caller holds the sandbox
	defer func() { <-l }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.ensure(ctx, c) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ensure = %v, want the context's error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ensure waited past its context")
	}
}

const adoptedID = "11111111-2222-4333-8444-555555555555"

// After a restart, a session whose first turn's process had started resumes
// (--resume, no page); Check passes.
func TestAdopt_StartedSessionResumes(t *testing.T) {
	e, b := newSandbox(t)
	if err := e.Check(adoptedID); err == nil {
		t.Fatal("Check accepted a session the engine does not know")
	}
	e.Adopt(adoptedID, Meta{Profile: "work-readonly", Started: true}, nil)
	if err := e.Check(adoptedID); err != nil {
		t.Fatalf("Check after Adopt: %v", err)
	}
	if _, _, err := sendCollect(t, e, adoptedID, "again"); err != nil {
		t.Fatal(err)
	}
	if args := b.execs[0].args; argAfter(args, "--resume") != adoptedID || argAfter(args, "--session-id") != "" {
		t.Fatalf("adopted turn args %q, want --resume %s", args, adoptedID)
	}
	if strings.Contains(b.execs[0].prompt, "untrusted data") {
		t.Fatalf("a resumed turn carried a page: %q", b.execs[0].prompt)
	}
}

// A session whose first turn never started stays first-turn: claude never
// created the id, so --resume would fail every turn. With its page still
// held, the next turn creates it (--session-id) and carries the page.
func TestAdopt_UnstartedSessionUsesSessionID(t *testing.T) {
	e, b := newSandbox(t)
	page := "PAGE BODY"
	e.Adopt(adoptedID, Meta{Profile: "work-readonly", URL: "https://a", Title: "T"}, &page)
	if _, _, err := sendCollect(t, e, adoptedID, "first"); err != nil {
		t.Fatal(err)
	}
	args, prompt := b.execs[0].args, b.execs[0].prompt
	if argAfter(args, "--session-id") != adoptedID || argAfter(args, "--resume") != "" {
		t.Fatalf("unstarted adopted turn args %q, want --session-id %s", args, adoptedID)
	}
	if !strings.Contains(prompt, "PAGE BODY") || !strings.Contains(prompt, "URL: https://a") {
		t.Fatalf("first prompt lost the page: %q", prompt)
	}
}

// Without its page (the store keeps none), an unstarted session cannot be
// answered as asked: Check and Send fail with a clear error, nothing runs.
func TestAdopt_UnstartedWithoutPageIsDropped(t *testing.T) {
	e, b := newSandbox(t)
	e.Adopt(adoptedID, Meta{Profile: "work-readonly"}, nil)
	for name, err := range map[string]error{"Check": e.Check(adoptedID), "Send": func() error { _, _, err := sendCollect(t, e, adoptedID, "q"); return err }()} {
		if err == nil || !strings.Contains(err.Error(), "never started before aimux restarted") {
			t.Errorf("%s = %v, want the restart error", name, err)
		}
	}
	if len(b.execs) != 0 {
		t.Fatalf("%d execs for a dropped session", len(b.execs))
	}
}

// The moment the first turn's process starts is reported once (to the
// store, by the serve wiring); later turns and failed starts report nothing.
func TestSandboxEngine_FirstTurnReportsStarted(t *testing.T) {
	e, b := newSandbox(t)
	var started []string
	e.OnStarted = func(s string) error { started = append(started, s); return nil }
	sid, err := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	if err != nil {
		t.Fatal(err)
	}
	b.script = func(string) (string, error, error) { return "", nil, errors.New("sandbox not found") }
	_, _, _ = sendCollect(t, e, sid, "q")
	if len(started) != 0 {
		t.Fatalf("a failed start was reported: %v", started)
	}
	b.script = func(s string) (string, error, error) { return okStream(s), nil, nil }
	for i := 0; i < 2; i++ {
		if _, _, err := sendCollect(t, e, sid, "q"); err != nil {
			t.Fatal(err)
		}
	}
	if len(started) != 1 || started[0] != sid {
		t.Fatalf("started reports %v, want one for %s", started, sid)
	}
}

// Running is true only while a turn of the session runs.
func TestSandboxEngine_Running(t *testing.T) {
	e, b := newSandbox(t)
	sid, err := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if e.Running(sid) || e.Running("nope") {
		t.Fatal("running before any turn")
	}
	b.block, b.closed = make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() { _, err := e.Send(context.Background(), sid, "q", func(acprun.Event) {}); done <- err }()
	deadline := time.Now().Add(2 * time.Second)
	for !e.Running(sid) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !e.Running(sid) {
		t.Fatal("not running during a turn")
	}
	close(b.block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if e.Running(sid) {
		t.Fatal("still running after the turn")
	}
}

// A re-pin changes the policy digest but not the manifest digest; the
// sandbox must be ensured again so its MCP rules follow the pins.
func TestSandboxEngine_EnsureKeyedOnPolicyDigest(t *testing.T) {
	e, b := newSandbox(t)
	policy := "p1"
	compile := e.Compile
	e.Compile = func(p string) (profile.Compiled, error) {
		c, err := compile(p)
		c.PolicyDigest = policy
		return c, err
	}
	if _, err := e.New(context.Background(), Meta{Profile: "work-readonly"}, ""); err != nil {
		t.Fatal(err)
	}
	policy = "p2"
	if _, err := e.New(context.Background(), Meta{Profile: "work-readonly"}, ""); err != nil {
		t.Fatal(err)
	}
	if b.ensures["aimux-work-readonly"] != 2 {
		t.Fatalf("changed policy digest with the same manifest digest not ensured: %v", b.ensures)
	}
}

// A first turn whose openshell process started but whose stream never
// showed claude's session (a connect or startup failure) did not create
// the session: it is not reported started and the retry creates it again
// with --session-id, never --resume on an id claude never made.
func TestSandboxEngine_FirstTurnFailedBeforeClaudeStartedRetriesWithSessionID(t *testing.T) {
	e, b := newSandbox(t)
	var started []string
	e.OnStarted = func(s string) error { started = append(started, s); return nil }
	sid, err := e.New(context.Background(), Meta{Profile: "work-readonly"}, "PAGE BODY")
	if err != nil {
		t.Fatal(err)
	}
	b.script = func(string) (string, error, error) { return "", errRemote, nil } // ssh: connect refused
	if _, _, err := sendCollect(t, e, sid, "q1"); err == nil {
		t.Fatal("a turn with no claude output succeeded")
	}
	if len(started) != 0 {
		t.Fatalf("a turn claude never ran was reported started: %v", started)
	}
	b.script = func(s string) (string, error, error) { return okStream(s), nil, nil }
	if _, _, err := sendCollect(t, e, sid, "q2"); err != nil {
		t.Fatal(err)
	}
	retry := b.execs[1]
	if argAfter(retry.args, "--session-id") != sid || argAfter(retry.args, "--resume") != "" {
		t.Fatalf("retry args %q, want --session-id %s", retry.args, sid)
	}
	if !strings.Contains(retry.prompt, "PAGE BODY") {
		t.Fatal("the retry lost the page")
	}
	if len(started) != 1 || started[0] != sid {
		t.Fatalf("started reports %v, want one for %s", started, sid)
	}
}

// A stream showing another session is no evidence this one exists: it is
// not reported started and the retry still uses --session-id.
func TestSandboxEngine_OtherSessionInStreamIsNotStarted(t *testing.T) {
	e, b := newSandbox(t)
	var started []string
	e.OnStarted = func(s string) error { started = append(started, s); return nil }
	sid, _ := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	b.script = func(string) (string, error, error) { return okStream("4bcd525c-3970-43f1-8420-b5e0d81d30b6"), nil, nil }
	if _, _, err := sendCollect(t, e, sid, "q1"); err == nil {
		t.Fatal("a turn that ran another session succeeded")
	}
	b.script = func(s string) (string, error, error) { return okStream(s), nil, nil }
	if _, _, err := sendCollect(t, e, sid, "q2"); err != nil {
		t.Fatal(err)
	}
	if retry := b.execs[1]; argAfter(retry.args, "--session-id") != sid {
		t.Fatalf("retry args %q, want --session-id %s", retry.args, sid)
	}
	if len(started) != 1 {
		t.Fatalf("started reports %v, want one (the retry)", started)
	}
}

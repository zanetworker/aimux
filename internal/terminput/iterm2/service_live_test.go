//go:build iterm2

package iterm2

// End-to-end check of the real Service, System probe, iTerm2 driver and
// Claude's session registry against ONE scratch Claude Code session that the
// tester opened by hand in its own iTerm2 window, in manual permission mode,
// with a scratch working directory:
//
//	AIMUX_LIVE_SESSION_ID=<id> go test -tags iterm2 ./internal/terminput/iterm2/ -run ServiceLive -v -timeout 300s
//
// It types into that session only (the Service checks its foreground is that
// session's claude), never opens, focuses or closes windows, and leaves the
// session idle. It asks Claude to write live.txt (approved) and no.txt
// (denied) in the session's working directory.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/discovery"
	"github.com/zanetworker/aimux/internal/terminput"
)

// registrySessions is terminput.Sessions over Claude's registry, the same
// rule the server uses (live holder), minus the aimux-owned check: aimux
// does not own the scratch session.
type registrySessions struct{ dir string }

func (r registrySessions) entry(id string) (discovery.ClaudeRegistryEntry, bool) {
	for _, e := range discovery.ReadClaudeRegistry(r.dir) {
		if e.SessionID == id && discovery.IsLiveHolder(e) {
			return e, true
		}
	}
	return discovery.ClaudeRegistryEntry{}, false
}

func (r registrySessions) TerminalPID(id string) (int, bool) {
	e, ok := r.entry(id)
	return e.PID, ok
}

func tail(screen string, n int) string {
	lines := strings.Split(strings.TrimRight(screen, "\n "), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func TestServiceLive(t *testing.T) {
	id := os.Getenv("AIMUX_LIVE_SESSION_ID")
	if id == "" {
		t.Skip("AIMUX_LIVE_SESSION_ID is not set")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	reg := registrySessions{dir: filepath.Join(home, ".claude", "sessions")}
	entry, ok := reg.entry(id)
	if !ok {
		t.Fatalf("session %s has no live registry entry", id)
	}
	cwd := entry.CWD
	t.Logf("session pid %d cwd %s", entry.PID, cwd)

	sys := terminput.SystemProbe{}
	d := New(Options{})
	defer func() { _ = d.Close() }()
	svc := terminput.NewService(config.TerminalInputConfig{Enabled: true, AllowWhileInUse: true}, reg, sys, nil, d)
	ctx := context.Background()

	pane := func() terminput.Pane {
		tty, err := sys.TTY(entry.PID)
		if err != nil {
			t.Fatalf("tty: %v", err)
		}
		p, found, err := d.Locate(ctx, terminput.Target{PID: entry.PID, TTY: tty})
		if err != nil || !found {
			t.Fatalf("locate %s: found=%v err=%v", tty, found, err)
		}
		return p
	}()
	screen := func() string {
		s, err := d.Screen(ctx, pane)
		if err != nil {
			t.Fatalf("screen: %v", err)
		}
		return s
	}
	regStatus := func() string { e, _ := reg.entry(id); return e.Status }
	status := func() terminput.Status { return svc.Status(ctx, id, regStatus()) }
	// waitFor polls cond every 500 ms up to limit and reports how long it took.
	waitFor := func(what string, limit time.Duration, cond func() bool) bool {
		t.Helper()
		start := time.Now()
		for time.Since(start) < limit {
			if cond() {
				t.Logf("  %s after %v", what, time.Since(start).Round(100*time.Millisecond))
				return true
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Errorf("  %s: not within %v; screen tail:\n%s", what, limit, tail(screen(), 25))
		return false
	}
	// answered reports a line with word that is not the line we typed.
	answered := func(word string) func() bool {
		return func() bool {
			for _, l := range strings.Split(screen(), "\n") {
				if strings.Contains(l, word) && !strings.Contains(l, "single word") {
					return true
				}
			}
			return false
		}
	}
	idleAgain := func(what string) bool {
		return waitFor(what+": idle and can type", 90*time.Second, func() bool {
			st := status()
			return st.CanType && st.State == "idle"
		})
	}
	timed := func(what string, f func() error) error {
		start := time.Now()
		err := f()
		t.Logf("%s: %s in %v", what, terminput.Code(err), time.Since(start).Round(time.Millisecond))
		return err
	}
	waitPrompt := func(what string) *terminput.Prompt {
		var pr *terminput.Prompt
		var sawFooter bool
		ok := waitFor(what+": prompt in Status", 60*time.Second, func() bool {
			st := status()
			if st.Prompt != nil {
				pr = st.Prompt
				return true
			}
			if strings.Contains(screen(), "Esc to cancel") {
				sawFooter = true
			}
			return false
		})
		if !ok {
			t.Logf("  footer seen on screen while Status had no prompt: %v", sawFooter)
			return nil
		}
		t.Logf("  prompt id %s question %q", pr.ID, pr.Question)
		for _, o := range pr.Options {
			t.Logf("  option %s %q", o.Key, o.Label)
		}
		t.Logf("  screen tail at prompt:\n%s", tail(screen(), 14))
		return pr
	}

	// 1. Status while idle.
	start := time.Now()
	st := status()
	t.Logf("1 status: state=%s canType=%v reason=%q prompt=%v in %v", st.State, st.CanType, st.Reason, st.Prompt != nil, time.Since(start).Round(time.Millisecond))
	if st.State != "idle" || !st.CanType {
		t.Fatalf("1: want idle and canType, got %+v; screen tail:\n%s", st, tail(screen(), 20))
	}

	// 2. Send while idle.
	if err := timed("2 send", func() error { return svc.Send(ctx, id, "Reply with the single word LIVEONE", "live-1") }); err != nil {
		t.Fatalf("2: %v", err)
	}
	waitFor("2 LIVEONE answered", 60*time.Second, answered("LIVEONE"))
	idleAgain("2")

	// 3. Permission prompt.
	if err := timed("3 send", func() error {
		return svc.Send(ctx, id, "Create a file named live.txt containing ok, using the Write tool", "live-3")
	}); err != nil {
		t.Fatalf("3: %v", err)
	}
	pr := waitPrompt("3")
	if pr == nil {
		t.Fatal("3: no prompt parsed; stopping (the session may be at a prompt: answer it by hand)")
	}

	// 4. Send refused while the prompt shows.
	before := screen()
	err = timed("4 send at prompt", func() error { return svc.Send(ctx, id, "this must not be typed", "live-4") })
	if !errors.Is(err, terminput.ErrNotAtInput) {
		t.Errorf("4: want not_at_input, got %v", err)
	}
	if strings.Contains(screen(), "this must not be typed") || !strings.Contains(before, "Esc to cancel") {
		t.Errorf("4: screen changed")
	}

	// 5. Approve with 1.
	if err := timed("5 press 1", func() error { return svc.Press(ctx, id, pr.ID, "1", "live-5") }); err != nil {
		t.Fatalf("5: %v", err)
	}
	livePath := filepath.Join(cwd, "live.txt")
	waitFor("5 live.txt written", 60*time.Second, func() bool {
		b, err := os.ReadFile(livePath) // #nosec G304 -- scratch session's cwd
		return err == nil && strings.TrimSpace(string(b)) == "ok"
	})
	idleAgain("5")

	// 6. Deny with the No option.
	if err := timed("6 send", func() error {
		return svc.Send(ctx, id, "Create a file named no.txt containing no, using the Write tool", "live-6")
	}); err != nil {
		t.Fatalf("6: %v", err)
	}
	pr2 := waitPrompt("6")
	if pr2 == nil {
		t.Fatal("6: no prompt parsed; stopping")
	}
	var no terminput.Key
	for _, o := range pr2.Options {
		if strings.HasPrefix(strings.ToLower(o.Label), "no") {
			no = o.Key
		}
	}
	if no == "" {
		t.Fatalf("6: no option labelled No in %+v", pr2.Options)
	}
	if err := timed("6 press "+string(no), func() error { return svc.Press(ctx, id, pr2.ID, no, "live-6b") }); err != nil {
		t.Fatalf("6: %v", err)
	}
	idleAgain("6")
	if _, err := os.Stat(filepath.Join(cwd, "no.txt")); !os.IsNotExist(err) {
		t.Errorf("6: no.txt exists or stat failed: %v", err)
	} else {
		t.Logf("6: no.txt absent")
	}

	// 7. Old prompt id.
	err = timed("7 press old id", func() error { return svc.Press(ctx, id, pr2.ID, "1", "live-7") })
	if !errors.Is(err, terminput.ErrNoPrompt) && !errors.Is(err, terminput.ErrPromptChanged) {
		t.Errorf("7: want no_prompt or prompt_changed, got %v", err)
	}

	// 8. Idle guard on.
	guarded := terminput.NewService(config.TerminalInputConfig{Enabled: true}, reg, sys, nil, d)
	idle, err := sys.IdleSeconds()
	t.Logf("8 idle seconds %.1f (err %v)", idle, err)
	err = timed("8 send with guard", func() error { return guarded.Send(ctx, id, "Reply with the single word LIVEIDLE", "live-8") })
	switch {
	case errors.Is(err, terminput.ErrMacInUse):
		if idle >= 120 {
			t.Errorf("8: mac_in_use with idle %.1f s", idle)
		}
	case err == nil:
		if idle < 120 {
			t.Errorf("8: sent with idle %.1f s", idle)
		}
		waitFor("8 LIVEIDLE answered", 60*time.Second, answered("LIVEIDLE"))
		idleAgain("8")
	default:
		t.Errorf("8: %v", err)
	}

	// 9. Long and two-line text (both go in as a paste).
	long := "Reply with the single word LIVELONG. " + strings.Repeat("padding ", 40)
	long = long[:300]
	if err := timed("9 send 300 chars", func() error { return svc.Send(ctx, id, long, "live-9a") }); err != nil {
		t.Errorf("9a: %v", err)
	} else {
		waitFor("9a LIVELONG answered", 60*time.Second, answered("LIVELONG"))
		idleAgain("9a")
	}
	if err := timed("9 send two lines", func() error {
		return svc.Send(ctx, id, "Reply with the single word LIVETWO.\nThis is the second line.", "live-9b")
	}); err != nil {
		t.Errorf("9b: %v", err)
	} else {
		waitFor("9b LIVETWO answered", 60*time.Second, answered("LIVETWO"))
		idleAgain("9b")
	}

	// 10. Control character refused before anything is typed.
	before = screen()
	err = timed("10 send with ESC", func() error { return svc.Send(ctx, id, "LIVEBAD\x1b[201~", "live-10") })
	if !errors.Is(err, terminput.ErrBadText) {
		t.Errorf("10: want bad_text, got %v", err)
	}
	time.Sleep(time.Second)
	if after := screen(); strings.Contains(after, "LIVEBAD") || !terminput.InputBoxEmpty(after) {
		t.Errorf("10: something was typed; tail:\n%s", tail(after, 10))
	}
	_ = before
	t.Logf("done; final status %+v", status())
}

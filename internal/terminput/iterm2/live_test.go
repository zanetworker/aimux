//go:build iterm2

package iterm2

// Run by hand against a pane you opened yourself, which swallows input:
//
//	stty -echo; cat > /dev/null        (in a scratch iTerm2 window)
//	AIMUX_ITERM2_TEST_TTY=/dev/ttysNNN go test -tags iterm2 ./internal/terminput/iterm2/ -run Live -v
//
// Set AIMUX_ITERM2_TEST_FOREGROUND when cat is aliased (e.g. to bat). The
// test never opens, focuses or closes windows, and touches only that tty.

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/terminput"
)

func TestLiveITerm2(t *testing.T) {
	tty := os.Getenv("AIMUX_ITERM2_TEST_TTY")
	if tty == "" {
		t.Skip("AIMUX_ITERM2_TEST_TTY is not set")
	}
	want := os.Getenv("AIMUX_ITERM2_TEST_FOREGROUND")
	if want == "" {
		want = "cat"
	}
	out, err := exec.Command("ps", "-o", "stat=,comm=", "-t", strings.TrimPrefix(tty, "/dev/")).Output() // #nosec G204 G702 -- tester's tty
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	fg := ""
	for _, l := range strings.Split(string(out), "\n") {
		if f := strings.Fields(l); len(f) == 2 && strings.Contains(f[0], "+") {
			fg = f[1]
		}
	}
	if fg != want {
		t.Fatalf("foreground of %s is %q, want %q: refusing to type", tty, fg, want)
	}

	d := New(Options{})
	defer func() { _ = d.Close() }()
	ctx := context.Background()
	timed := func(what string, f func() error) {
		t.Helper()
		start := time.Now()
		if err := f(); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		t.Logf("%-28s %v", what, time.Since(start).Round(time.Millisecond))
	}

	var p terminput.Pane
	timed("locate (helper start)", func() error {
		var ok bool
		var err error
		p, ok, err = d.Locate(ctx, terminput.Target{TTY: tty})
		if err == nil && !ok {
			t.Fatalf("no iTerm2 session has tty %s", tty)
		}
		return err
	})
	timed("locate (cached)", func() error { _, _, err := d.Locate(ctx, terminput.Target{TTY: tty}); return err })
	timed("probe (bulk listing, warm)", func() error { return d.Probe(ctx) })
	timed("focus (read only)", func() error {
		front, focused, err := d.Focused(ctx)
		t.Logf("iTerm2 frontmost=%v, focused session is the test tty: %v", front, focused == tty)
		return err
	})
	timed("type", func() error { return d.Type(ctx, p, "# aimux live test \"quotes\" سلام") })
	timed("key enter", func() error { return d.Key(ctx, p, terminput.KeyEnter) })
	timed("type paste (multi-line)", func() error { return d.Type(ctx, p, "# line one\n# line two") })
	timed("key enter", func() error { return d.Key(ctx, p, terminput.KeyEnter) })
	timed("key 1", func() error { return d.Key(ctx, p, "1") })
	timed("key escape", func() error { return d.Key(ctx, p, terminput.KeyEscape) })
	timed("screen", func() error {
		s, err := d.Screen(ctx, p)
		if err == nil && strings.TrimSpace(s) == "" {
			t.Fatal("screen is empty")
		}
		return err // the text itself is not logged
	})
	start := time.Now()
	_ = d.Close()
	if el := time.Since(start); el > 900*time.Millisecond {
		t.Fatalf("close took %v: the helper did not exit on EOF", el)
	} else {
		t.Logf("%-28s %v", "close (EOF exit)", el.Round(time.Millisecond))
	}
}

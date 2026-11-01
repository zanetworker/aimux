//go:build iterm2

package iterm2

// Live check of Send now against ONE scratch Claude Code session the tester
// opened in its own iTerm2 window, idle, in a scratch working directory:
//
//	AIMUX_LIVE_SENDNOW_SESSION_ID=<id> go test -tags iterm2 ./internal/terminput/iterm2/ -run SendNowLive -v -timeout 300s
//
// It asks Claude for a long reply with no tools, sends a second message with
// SendNow while Claude is still working, and checks that Claude saw it during
// the same turn (its reply ends with the marker word). It types into that
// session only, never opens, focuses or closes windows.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/terminput"
)

func TestSendNowLive(t *testing.T) {
	id := os.Getenv("AIMUX_LIVE_SENDNOW_SESSION_ID")
	if id == "" {
		t.Skip("AIMUX_LIVE_SENDNOW_SESSION_ID is not set")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	reg := registrySessions{dir: filepath.Join(home, ".claude", "sessions")}
	if _, ok := reg.entry(id); !ok {
		t.Fatalf("session %s has no live registry entry", id)
	}
	d := New(Options{})
	defer func() { _ = d.Close() }()
	svc := terminput.NewService(config.TerminalInputConfig{Enabled: true, AllowWhileInUse: true}, reg, terminput.SystemProbe{}, nil, d)
	ctx := context.Background()
	regStatus := func() string { e, _ := reg.entry(id); return e.Status }
	status := func() terminput.Status { return svc.Status(ctx, id, regStatus()) }
	waitFor := func(what string, limit time.Duration, cond func() bool) bool {
		t.Helper()
		start := time.Now()
		for time.Since(start) < limit {
			if cond() {
				t.Logf("%s after %v", what, time.Since(start).Round(100*time.Millisecond))
				return true
			}
			time.Sleep(300 * time.Millisecond)
		}
		t.Errorf("%s: not within %v", what, limit)
		return false
	}

	if !waitFor("idle at start", 30*time.Second, func() bool { st := status(); return st.CanType && st.State == "idle" }) {
		t.FailNow()
	}
	const marker = "PINEAPPLE"
	first := "Write a 700 word story about a lighthouse keeper. Do not use any tools. Write it as plain prose."
	if err := svc.Send(ctx, id, first, "live-first"); err != nil {
		t.Fatalf("first send: %s (%v)", terminput.Code(err), err)
	}
	if !waitFor("working", 30*time.Second, func() bool { return status().Working }) {
		t.FailNow()
	}
	time.Sleep(2 * time.Second) // well inside the turn
	if !status().Working {
		t.Fatal("the turn ended before Send now; make the first request longer")
	}
	second := "Also: end the story with the single word " + marker + "."
	start := time.Now()
	err = svc.SendNow(ctx, id, second, "live-now")
	t.Logf("SendNow while working: %s in %v", terminput.Code(err), time.Since(start).Round(time.Millisecond))
	if err != nil {
		t.Fatalf("SendNow refused: %v", err)
	}
	if !waitFor("idle again", 180*time.Second, func() bool { st := status(); return st.CanType && st.State == "idle" && !st.Working }) {
		t.FailNow()
	}
	// The marker must appear in Claude's reply, not only in the line typed.
	scr, err := d.Screen(ctx, mustPane(t, ctx, d, reg, id))
	if err != nil {
		t.Fatalf("screen: %v", err)
	}
	seen := false
	for _, l := range strings.Split(scr, "\n") {
		if strings.Contains(l, marker) && !strings.Contains(l, "single word") {
			seen = true
		}
	}
	t.Logf("Claude acted on the Send now message in the same turn: %v", seen)
	if !seen {
		t.Error("the reply does not end with the marker: the mid-turn message was not delivered or not acted on")
	}
}

func mustPane(t *testing.T, ctx context.Context, d *Driver, reg registrySessions, id string) terminput.Pane {
	t.Helper()
	e, _ := reg.entry(id)
	tty, err := terminput.SystemProbe{}.TTY(e.PID)
	if err != nil {
		t.Fatalf("tty: %v", err)
	}
	p, found, err := d.Locate(ctx, terminput.Target{PID: e.PID, TTY: tty})
	if err != nil || !found {
		t.Fatalf("locate: found=%v err=%v", found, err)
	}
	return p
}

package remoteapp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/push"
	"github.com/zanetworker/aimux/internal/waiting"
)

func TestWaitingAlertsPushAndRetract(t *testing.T) {
	rec := &retractingNotifier{recordingNotifier{got: make(chan push.Message, 2)}, make(chan push.Message, 2)}
	var errOut strings.Builder
	since := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	alerts := waitingAlerts(pushSetup{notifiers: []push.Notifier{rec}, clickURL: "https://mac.ts.net:8443/app/"},
		func(context.Context, string) string { return "Fix the cache" }, &errOut)
	e := waiting.Entry{SessionID: "s1", Waiting: waiting.Waiting{Kind: waiting.KindPermission, Message: "Claude needs your permission to use Bash", Since: since}}
	alerts([]waiting.Entry{e}, nil)
	m := <-rec.got
	if m.Title != "Approval needed" || m.Body != "Fix the cache: Claude needs your permission to use Bash" || m.SessionID != "s1" ||
		m.Link != "https://mac.ts.net:8443/app/#/s/s1" || m.ApprovalID == "" || len(m.ApprovalID) > 64 {
		t.Fatalf("message %+v", m)
	}
	alerts(nil, []waiting.Entry{e})
	if r := <-rec.retracted; r.ApprovalID != m.ApprovalID || r.SessionID != "s1" {
		t.Fatalf("retracted %+v, want id %q", r, m.ApprovalID)
	}
	if !strings.Contains(errOut.String(), "old ntfy server") {
		t.Fatalf("a failed retraction must be logged: %q", errOut.String())
	}
	if waitingAlerts(pushSetup{}, nil, &errOut) != nil {
		t.Fatal("alerts with nothing to send to")
	}
}

func TestHooksSummary(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	if got := hooksSummary(p); !strings.Contains(got, "aimux remote hooks install") {
		t.Fatalf("not installed: %q", got)
	}
	_, _ = waiting.Install(p, "/x/aimux remote hooks notify", time.Now())
	if got := hooksSummary(p); !strings.Contains(got, "on") || strings.Contains(got, "install") {
		t.Fatalf("installed: %q", got)
	}
	_ = os.WriteFile(p, []byte("{broken"), 0o600)
	if got := hooksSummary(p); !strings.Contains(got, "cannot read") {
		t.Fatalf("broken: %q", got)
	}
}

// The loop hands each new marker to the alerts once and stops with ctx;
// with no alerts it returns at once (the list still shows waiting).
func TestRunWaitingAlerts_HandsNewMarkersOnceAndStops(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "waiting")
	if err := waiting.Write(dir, "s1", "Claude needs your permission to use Bash", time.Now()); err != nil {
		t.Fatal(err)
	}
	tracker := &waiting.Tracker{Dir: dir, Live: func() map[string]int { return map[string]int{"s1": os.Getpid()} }}
	got := make(chan []waiting.Entry, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runWaitingAlerts(ctx, tracker, func(appeared, _ []waiting.Entry) { got <- appeared })
		close(done)
	}()
	select {
	case a := <-got:
		if len(a) != 1 || a[0].SessionID != "s1" {
			t.Fatalf("appeared %+v", a)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the marker never reached the alerts")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the loop did not stop on cancel")
	}
	if len(got) != 0 {
		t.Fatalf("the same marker was handed over again: %v", <-got)
	}
	stopped := make(chan struct{})
	go func() { runWaitingAlerts(context.Background(), tracker, nil); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("nil alerts: want an immediate return")
	}
}

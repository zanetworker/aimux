package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/toolgateway"
)

// A failed server's state says how long ago it failed, so a failure from
// yesterday is not read as live.
func TestPrintProxyStatus_FailedStateShowsItsAge(t *testing.T) {
	rep := fixtureReport()
	rep.Servers[1].LastErrorAt = statusNow.Add(-12 * time.Minute)
	if got := renderRep(rep, false, false); !strings.Contains(got, "● failed 12m ago") {
		t.Fatalf("output lacks the failure's age:\n%s", got)
	}
	// A failure with no time (an older daemon) shows no age.
	if got := renderRep(fixtureReport(), false, false); strings.Contains(got, "failed never") || strings.Contains(got, "failed just") {
		t.Fatalf("a failure without a time got an age:\n%s", got)
	}
}

// The gateway line says when the worker started and when each down
// backend was last checked; one with no time (an older worker) shows none.
func TestGatewayStatusLine_SaysHowOldEachStateIs(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	st := toolgateway.ControlStatus{Leases: 2, Backends: []string{"jira", "slack", "wiki"}, Down: []string{"slack", "wiki"},
		StartedAt: now.Add(-3 * time.Hour), Checked: map[string]time.Time{"wiki": now.Add(-40 * time.Second)}}
	got := gatewayStatusLineAt(context.Background(), fakeGatewayStatus{st: st}, fixedClock(now))
	want := "tool gateway: running (started 3h ago), 2 leases, 3 backends, down: slack, wiki (checked 40s ago)"
	if got != want {
		t.Fatalf("line\n got %q\nwant %q", got, want)
	}
}

func TestStateAge(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		at   time.Time
		want string
	}{
		{time.Time{}, ""},
		{now.Add(-12 * time.Minute), " 12m ago"},
		{now.Add(-40 * time.Second), " 40s ago"},
		{now.Add(time.Minute), " just now"}, // a clock a little ahead is not "in the future"
	} {
		if got := stateAge(now, c.at); got != c.want {
			t.Errorf("stateAge(%v) = %q, want %q", c.at, got, c.want)
		}
	}

}

// fixedClock is a clock that always reads at.
func fixedClock(at time.Time) func() time.Time { return func() time.Time { return at } }

// The probe's failure is stamped while the probe runs, after the command
// started; its age is measured once the answer is in, not before.
func TestProxyStatus_AgesMeasuredAfterTheAnswer(t *testing.T) {
	rep := fixtureReport()
	rep.Servers[1].LastErrorAt = statusNow.Add(-40 * time.Second)
	addr, got := fakeDaemon(t, rep)
	var out bytes.Buffer
	clock := func() time.Time {
		if len(*got) == 0 { // read before the daemon answered: a stale clock
			return statusNow.Add(-time.Hour)
		}
		return statusNow
	}
	if err := proxyStatus(context.Background(), proxyStatusOpts{addr: addr, probe: true, timeout: time.Second}, &out, clock); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "failed 40s ago") {
		t.Fatalf("output:\n%s", out.String())
	}
}

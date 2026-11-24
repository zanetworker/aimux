package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/zanetworker/aimux/internal/controller"
)

const wantStatusTable = `aimux v0.14.0-184    4 services, 2 need attention

SERVICE       STATE     BUILD  UP   ADDRESS         LAST ACTIVITY
model proxy   ok        184    38m  127.0.0.1:7385  turn 2m ago
mcp proxy     ok        128 !  2d   127.0.0.1:7381  call 12h ago
tool gateway  degraded  -      2d   127.0.0.1:7390  3 leases
collector     ok        184    2d   127.0.0.1:4318  span 4s ago

NEEDS ATTENTION                                   FIX
tool gateway: 1 of 5 backends down (slack)        aimux mcp proxy status
mcp proxy: build 128 is older than the CLI (184)  aimux mcp proxy restart
`

func statusTestOverview() (controller.StatusOverview, time.Time) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	before := func(age time.Duration) *time.Time {
		at := now.Add(-age)
		return &at
	}
	const cliBuild = "v0.14.0-184-g398934a7"
	return controller.StatusOverview{
		CLIBuild: cliBuild,
		Services: []controller.ServiceStatus{
			{
				Name: "model proxy", State: controller.ServiceOK, Build: cliBuild,
				StartedAt: before(38 * time.Minute), Address: "127.0.0.1:7385",
				LastActivity: before(2 * time.Minute), ActivityNote: "turn",
			},
			{
				Name: "mcp proxy", State: controller.ServiceOK, Build: "v0.14.0-128-gd7cf3658",
				StartedAt: before(48 * time.Hour), Address: "127.0.0.1:7381",
				LastActivity: before(12 * time.Hour), ActivityNote: "call",
				Attention: []controller.Attention{{
					Problem: "build 128 is older than the CLI (184)", Fix: "aimux mcp proxy restart",
				}},
			},
			{
				Name: "tool gateway", State: controller.ServiceDegraded,
				StartedAt: before(48 * time.Hour), Address: "127.0.0.1:7390", ActivityNote: "3 leases",
				Attention: []controller.Attention{{
					Problem: "1 of 5 backends down (slack)", Fix: "aimux mcp proxy status",
				}},
			},
			{
				Name: "collector", State: controller.ServiceOK, Build: cliBuild,
				StartedAt: before(48 * time.Hour), Address: "127.0.0.1:4318",
				LastActivity: before(4 * time.Second), ActivityNote: "span",
			},
		},
	}, now
}

func TestStatus_Table(t *testing.T) {
	o, now := statusTestOverview()
	var out bytes.Buffer
	_ = runStatus(context.Background(), o, &out, now, false)
	if got := out.String(); got != wantStatusTable {
		t.Fatalf("table differs:\ngot:\n%s\nwant:\n%s", got, wantStatusTable)
	}
}

func TestStatus_AllOK(t *testing.T) {
	o, now := statusTestOverview()
	for i := range o.Services {
		o.Services[i].State = controller.ServiceOK
		o.Services[i].Attention = nil
	}
	o.Services[1].BuildNote = "restart pending"
	var out bytes.Buffer
	if err := runStatus(context.Background(), o, &out, now, false); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.HasPrefix(got, "aimux v0.14.0-184    4 services, all ok\n") ||
		strings.Contains(got, "NEEDS ATTENTION") || !strings.Contains(got, "128 (restart pending)") {
		t.Fatalf("unexpected status output:\n%s", got)
	}
}

func TestStatus_ReturnsErrorWhenAttention(t *testing.T) {
	o, now := statusTestOverview()
	o.Services[1].BuildNote = "restart pending"
	o.Services[2].Attention = append(o.Services[2].Attention,
		controller.Attention{Problem: "another backend failed", Fix: "aimux mcp proxy status"})
	var out bytes.Buffer
	if err := runStatus(context.Background(), o, &out, now, false); err == nil || err.Error() != "2 services need attention" {
		t.Fatalf("error = %v, want 2 services need attention", err)
	}
	if got := out.String(); !strings.Contains(got, "128 (restart pending)") || strings.Contains(got, "128 !") {
		t.Fatalf("build note must replace the marker:\n%s", got)
	}

	t.Run("down", func(t *testing.T) {
		o = statusOverviewOf(t, o.CLIBuild, controller.ServiceStatus{
			Name: "collector", State: controller.ServiceDown, Reason: "wrong process",
			Address: "127.0.0.1:4318", ActivityNote: "span", LastActivity: &now,
			RestartFix: "aimux doctor",
		})
		out.Reset()
		if err := runStatus(context.Background(), o, &out, now, false); err == nil || err.Error() != "1 service needs attention" {
			t.Fatalf("error = %v, want 1 service needs attention", err)
		}
		lines := strings.Split(out.String(), "\n")
		if len(lines) < 4 || !reflect.DeepEqual(strings.Fields(lines[3]),
			[]string{"collector", "down", "-", "-", "127.0.0.1:4318", "wrong", "process"}) {
			t.Fatalf("down row must show the reason in LAST ACTIVITY:\n%s", out.String())
		}
	})
}

func TestStatus_NotInstalledAloneIsOK(t *testing.T) {
	o, now := statusTestOverview()
	o.Services = o.Services[:1]
	o.Services[0].State = controller.ServiceNotInstalled
	var out bytes.Buffer
	if err := runStatus(context.Background(), o, &out, now, false); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.HasPrefix(got, "aimux v0.14.0-184    1 service, all ok\n") || strings.Contains(got, "NEEDS ATTENTION") {
		t.Fatalf("unexpected status output:\n%s", got)
	}
	lines := strings.Split(got, "\n")
	if len(lines) < 4 || !reflect.DeepEqual(strings.Fields(lines[3]),
		[]string{"model", "proxy", "not", "installed", "-", "-", "-", "-"}) {
		t.Fatalf("not installed row must show four placeholders:\n%s", got)
	}
}

func TestStatus_JSON(t *testing.T) {
	want, now := statusTestOverview()
	const restartFix = "private restart hint"
	want.Services[0].RestartFix = restartFix
	var out bytes.Buffer
	if err := runStatus(context.Background(), want, &out, now, true); err == nil || err.Error() != "2 services need attention" {
		t.Fatalf("error = %v, want 2 services need attention", err)
	}
	var got controller.StatusOverview
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), restartFix) || strings.Contains(strings.ToLower(out.String()), "restartfix") {
		t.Fatal("JSON exposes RestartFix")
	}
	want.Services[0].RestartFix = ""
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON differs from the overview:\n%s", out.String())
	}
}

func TestStatus_Under100Columns(t *testing.T) {
	o, now := statusTestOverview()
	o.Services[0].Build = strings.Repeat("build", 25)
	o.Services[2].Attention[0].Problem += strings.Repeat(" backend", 20)
	var out bytes.Buffer
	_ = runStatus(context.Background(), o, &out, now, false)
	for _, line := range strings.Split(out.String(), "\n") {
		if width := runewidth.StringWidth(line); width > 99 {
			t.Errorf("status line has %d columns, want at most 99: %q", width, line)
		}
	}
	if got := out.String(); !strings.Contains(got, "LAST ACTIVITY") || !strings.Contains(got, "aimux mcp proxy status") {
		t.Fatalf("wide fields hid the activity column or fix command:\n%s", got)
	}
}

func TestStatus_DegradedRowShowsItsReason(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	o := statusOverviewOf(t, "v0.14.0-184-g398934a7", controller.ServiceStatus{
		Name: "collector", State: controller.ServiceDegraded, Reason: "no health answer",
		Address: "127.0.0.1:4318", RestartFix: "aimux collect restart",
	})
	var out bytes.Buffer
	if err := runStatus(context.Background(), o, &out, now, false); err == nil {
		t.Fatal("a degraded service must make status fail")
	}
	got := out.String()
	for _, want := range []string{
		"collector  degraded  -      -   127.0.0.1:4318  no health answer\n",
		"collector: no health answer  aimux collect restart\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
}

func TestStatus_JSONCarriesAttentionAndUTC(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	started := time.Date(2026, 11, 24, 11, 0, 0, 0, time.FixedZone("CET", 3600))
	o := statusOverviewOf(t, "v0.14.0-184-g398934a7", controller.ServiceStatus{
		Name: "collector", State: controller.ServiceDegraded, Reason: "no health answer",
		Address: "127.0.0.1:4318", RestartFix: "aimux collect restart", StartedAt: &started,
	})
	var out bytes.Buffer
	if err := runStatus(context.Background(), o, &out, now, true); err == nil {
		t.Fatal("a degraded service must make status fail")
	}
	var got struct {
		Attention []controller.Attention `json:"attention"`
		Services  []struct {
			StartedAt string `json:"started_at"`
		} `json:"services"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []controller.Attention{{Problem: "collector: no health answer", Fix: "aimux collect restart"}}
	if !reflect.DeepEqual(got.Attention, want) {
		t.Errorf("attention = %+v, want %+v", got.Attention, want)
	}
	if got.Services[0].StartedAt != "2026-11-24T10:00:00Z" {
		t.Errorf("started_at = %q, want UTC 2026-11-24T10:00:00Z", got.Services[0].StartedAt)
	}
}

func TestStatus_DegradedReasonBesideOtherProblems(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	lastTurn := now.Add(-2 * time.Minute)
	o := statusOverviewOf(t, "v0.14.0-184-g398934a7", controller.ServiceStatus{
		Name: "collector", State: controller.ServiceDegraded, Reason: "no health answer", Build: "v0.14.0-128-gd7cf3658",
		Address: "127.0.0.1:4318", RestartFix: "aimux collect restart", LastActivity: &lastTurn, ActivityNote: "span",
	})
	var out bytes.Buffer
	if err := runStatus(context.Background(), o, &out, now, false); err == nil || err.Error() != "1 service needs attention" {
		t.Fatalf("error = %v, want 1 service needs attention", err)
	}
	got := out.String()
	for _, want := range []string{
		"127.0.0.1:4318  no health answer\n", // the reason wins over the last activity
		"collector: no health answer                       aimux collect restart\n",
		"collector: build 128 is older than the CLI (184)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if reason, older := strings.Index(got, "collector: no health answer"), strings.Index(got, "collector: build 128"); reason > older {
		t.Errorf("the reason must come before the other problem:\n%s", got)
	}
	if strings.Contains(got, "span 2m ago") {
		t.Errorf("a degraded row with a reason must not show its last activity:\n%s", got)
	}
}

// statusOverviewOf runs rows through the controller, as the command does, so attention items are filled in.
func statusOverviewOf(t *testing.T, cliBuild string, rows ...controller.ServiceStatus) controller.StatusOverview {
	t.Helper()
	var probes []controller.StatusProbe
	for _, row := range rows {
		probes = append(probes, controller.StatusProbe{Name: row.Name, Run: func(context.Context) controller.ServiceStatus { return row }})
	}
	return controller.ServicesOverview(t.Context(), cliBuild, probes, time.Second)
}

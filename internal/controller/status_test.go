package controller

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestServicesOverview_KeepsProbeOrderAndRunsInParallel(t *testing.T) {
	names := []string{"model proxy", "mcp proxy", "tool gateway", "collector"}
	var probes []StatusProbe
	var want []ServiceStatus
	for _, name := range names {
		row := ServiceStatus{Name: name, State: ServiceOK}
		want = append(want, row)
		probes = append(probes, StatusProbe{Name: name, Run: func(context.Context) ServiceStatus {
			time.Sleep(300 * time.Millisecond)
			return row
		}})
	}

	start := time.Now()
	got := ServicesOverview(t.Context(), "dev", probes, 2*time.Second)
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Errorf("overview took %s, want less than 1s", elapsed)
	}
	if got.CLIBuild != "dev" || !reflect.DeepEqual(got.Services, want) {
		t.Errorf("overview = %+v, want CLI build dev and services %+v", got, want)
	}
}

func TestServicesOverview_SlowProbeIsDown(t *testing.T) {
	for _, waitForContext := range []bool{true, false} {
		name := "ignores cancellation"
		if waitForContext {
			name = "returns on cancellation"
		}
		t.Run(name, func(t *testing.T) {
			release := make(chan struct{})
			defer close(release)
			model := ServiceStatus{Name: "model proxy", State: ServiceOK, ActivityNote: "turn"}
			gateway := ServiceStatus{Name: "tool gateway", State: ServiceDegraded, Reason: "backend down"}
			collector := ServiceStatus{Name: "collector", State: ServiceNotInstalled}
			probes := []StatusProbe{
				{Name: model.Name, Run: func(context.Context) ServiceStatus { return model }},
				{Name: "mcp proxy", Address: "127.0.0.1:7381", RestartFix: "aimux mcp proxy restart", Run: func(ctx context.Context) ServiceStatus {
					if waitForContext {
						<-ctx.Done()
					} else {
						<-release
					}
					return ServiceStatus{Name: "mcp proxy", State: ServiceOK}
				}},
				{Name: gateway.Name, Run: func(context.Context) ServiceStatus { return gateway }},
				{Name: collector.Name, Run: func(context.Context) ServiceStatus { return collector }},
			}
			gatewayWant := gateway
			gatewayWant.Attention = []Attention{{Problem: "backend down"}}
			want := []ServiceStatus{
				model,
				{
					Name: "mcp proxy", State: ServiceDown, Reason: "no answer in 100ms",
					Address: "127.0.0.1:7381", RestartFix: "aimux mcp proxy restart",
					Attention: []Attention{{Problem: "no answer in 100ms", Fix: "aimux mcp proxy restart"}},
				},
				gatewayWant,
				collector,
			}

			result := make(chan StatusOverview, 1)
			go func() {
				result <- ServicesOverview(t.Context(), "dev", probes, 100*time.Millisecond)
			}()
			select {
			case got := <-result:
				if !reflect.DeepEqual(got.Services, want) {
					t.Errorf("services = %+v, want %+v", got.Services, want)
				}
			case <-time.After(time.Second):
				t.Fatal("overview took more than 1s with a 100ms probe limit")
			}
		})
	}
}

func TestServicesOverview_FlagsOlderBuild(t *testing.T) {
	const cliBuild = "v0.14.0-184-g398934a7"
	for _, tt := range []struct {
		build string
		want  []Attention
	}{
		{"v0.14.0-128-gd7cf3658", []Attention{{
			Problem: "build 128 is older than the CLI (184)",
			Fix:     "aimux mcp proxy restart",
		}}},
		{"v0.14.0-184-gd7cf3658", nil},
		{"v0.14.0-1000-gd7cf3658", nil},
	} {
		t.Run(tt.build, func(t *testing.T) {
			probes := []StatusProbe{{Name: "mcp proxy", Run: func(context.Context) ServiceStatus {
				return ServiceStatus{Name: "mcp proxy", State: ServiceOK, Build: tt.build, RestartFix: "aimux mcp proxy restart"}
			}}}
			got := ServicesOverview(t.Context(), cliBuild, probes, time.Second)
			if len(got.Services) != 1 {
				t.Fatalf("services = %+v, want one row", got.Services)
			}
			row := got.Services[0]
			if row.State != ServiceOK || !slices.Equal(row.Attention, tt.want) {
				t.Errorf("state = %q, attention = %+v, want %q and %+v", row.State, row.Attention, ServiceOK, tt.want)
			}
		})
	}
}

func TestServicesOverview_NoDriftLineWhenNoted(t *testing.T) {
	for _, note := range []string{"pinned", "restart pending"} {
		t.Run(note, func(t *testing.T) {
			probes := []StatusProbe{{Name: "mcp proxy", Run: func(context.Context) ServiceStatus {
				return ServiceStatus{
					Name: "mcp proxy", State: ServiceOK, Build: "v0.14.0-128-gd7cf3658",
					BuildNote: note, RestartFix: "aimux mcp proxy restart",
				}
			}}}
			got := ServicesOverview(t.Context(), "v0.14.0-184-g398934a7", probes, time.Second)
			if len(got.Services) != 1 || got.Services[0].BuildNote != note || len(got.Services[0].Attention) != 0 {
				t.Errorf("services = %+v, want one row with build note %q and no attention", got.Services, note)
			}
		})
	}
}

func TestServicesOverview_NoDriftWithoutCount(t *testing.T) {
	for _, tt := range []struct{ cli, service string }{
		{"dev", "v0.14.0-128-gd7cf3658"},
		{"v0.15.0", "v0.14.0-128-gd7cf3658"},
		{"v0.14.0-184-g398934a7", "dev"},
		{"v0.14.0-184-g398934a7", "v0.15.0"},
	} {
		t.Run(tt.cli+"/"+tt.service, func(t *testing.T) {
			probes := []StatusProbe{{Name: "mcp proxy", Run: func(context.Context) ServiceStatus {
				return ServiceStatus{Name: "mcp proxy", State: ServiceOK, Build: tt.service, RestartFix: "aimux mcp proxy restart"}
			}}}
			got := ServicesOverview(t.Context(), tt.cli, probes, time.Second)
			if len(got.Services) != 1 || got.Services[0].Build != tt.service || len(got.Services[0].Attention) != 0 {
				t.Errorf("services = %+v, want one row with build %q and no attention", got.Services, tt.service)
			}
		})
	}
}

func TestBuildNumber(t *testing.T) {
	for _, tt := range []struct{ version, want string }{
		{"v0.14.0-184-g398934a7", "184"},
		{"dev", "dev"},
		{"v0.15.0", "v0.15.0"},
	} {
		if got := BuildNumber(tt.version); got != tt.want {
			t.Errorf("BuildNumber(%q) = %q, want %q", tt.version, got, tt.want)
		}
	}
}

func TestStatusOverview_AttentionPrefixesName(t *testing.T) {
	issue := Attention{Problem: "1 of 5 backends down (slack)", Fix: "aimux mcp proxy status"}
	overview := StatusOverview{Services: []ServiceStatus{
		{Name: "tool gateway", Attention: []Attention{issue, {Problem: "no leases"}}},
		{Name: "collector"},
		{Name: "mcp proxy", Attention: []Attention{{Problem: "no answer"}}},
	}}
	want := []Attention{
		{Problem: "tool gateway: 1 of 5 backends down (slack)", Fix: "aimux mcp proxy status"},
		{Problem: "tool gateway: no leases"},
		{Problem: "mcp proxy: no answer"},
	}
	if got := overview.Attention(); !slices.Equal(got, want) {
		t.Errorf("attention = %+v, want %+v", got, want)
	}
	if got := overview.Services[0].Attention[0]; got != issue {
		t.Errorf("service attention changed to %+v, want %+v", got, issue)
	}
}

func TestServicesOverview_UnhealthyReasonIsAnAttentionItem(t *testing.T) {
	const cliBuild = "v0.14.0-184-g398934a7"
	const fix = "aimux mcp proxy restart"
	older := Attention{Problem: "build 128 is older than the CLI (184)", Fix: fix}
	backends := Attention{Problem: "1 of 5 backends down (slack)", Fix: "aimux mcp proxy status"}
	for _, tt := range []struct {
		name string
		row  ServiceStatus
		want []Attention
	}{
		{"down with a reason", ServiceStatus{State: ServiceDown, Reason: "not running"},
			[]Attention{{Problem: "not running", Fix: fix}}},
		{"degraded and older: reason first", ServiceStatus{State: ServiceDegraded, Reason: "no health answer", Build: "v0.14.0-128-gd7cf3658"},
			[]Attention{{Problem: "no health answer", Fix: fix}, older}},
		{"reason added before the probe's own item", ServiceStatus{State: ServiceDegraded, Reason: "slow", Attention: []Attention{backends}},
			[]Attention{{Problem: "slow", Fix: fix}, backends}},
		{"same problem text is not repeated", ServiceStatus{State: ServiceDegraded, Reason: "degraded",
			Attention: []Attention{{Problem: "degraded", Fix: "aimux model proxy status"}}},
			[]Attention{{Problem: "degraded", Fix: "aimux model proxy status"}}},
		{"ok row gets no reason item", ServiceStatus{State: ServiceOK, Reason: "ignored"}, nil},
		{"not installed gets no item", ServiceStatus{State: ServiceNotInstalled}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			row := tt.row
			row.Name, row.RestartFix = "mcp proxy", fix
			probes := []StatusProbe{{Name: row.Name, Run: func(context.Context) ServiceStatus { return row }}}
			got := ServicesOverview(t.Context(), cliBuild, probes, time.Second)
			if !slices.Equal(got.Services[0].Attention, tt.want) {
				t.Errorf("attention = %+v, want %+v", got.Services[0].Attention, tt.want)
			}
		})
	}
}

func TestServicesOverview_UnhealthyItemDoesNotChangeTheProbeSlice(t *testing.T) {
	// Spare capacity would let an in-place insert write into the probe's backing array.
	own := append(make([]Attention, 0, 2), Attention{Problem: "1 of 5 backends down (slack)"})
	row := ServiceStatus{Name: "tool gateway", State: ServiceDegraded, Reason: "slow", Attention: own}
	probes := []StatusProbe{{Name: row.Name, Run: func(context.Context) ServiceStatus { return row }}}
	_ = ServicesOverview(t.Context(), "dev", probes, time.Second)
	if backing := own[:2]; backing[0].Problem != "1 of 5 backends down (slack)" || backing[1] != (Attention{}) {
		t.Errorf("the probe's attention array was changed: %+v", backing)
	}
}

func TestServicesOverview_ComparesCountsOnlyUnderTheSameTag(t *testing.T) {
	for _, tt := range []struct {
		cli, service string
		flagged      bool
	}{
		// A newer tag restarts the count: 2 commits after v0.15.0 is newer than 184 after v0.14.0.
		{"v0.14.0-184-g398934a7", "v0.15.0-2-gabc1234", false},
		// And an older tag with a bigger count is not judged newer or older by the count either.
		{"v0.15.0-2-gabc1234", "v0.14.0-184-g398934a7", false},
		{"v0.15.0-184-g398934a7", "v0.15.0-2-gabc1234", true},
		{"v0.15.0-2-gabc1234", "v0.15.0-184-g398934a7", false},
	} {
		t.Run(tt.cli+"/"+tt.service, func(t *testing.T) {
			probes := []StatusProbe{{Name: "mcp proxy", Run: func(context.Context) ServiceStatus {
				return ServiceStatus{Name: "mcp proxy", State: ServiceOK, Build: tt.service, RestartFix: "aimux mcp proxy restart"}
			}}}
			got := ServicesOverview(t.Context(), tt.cli, probes, time.Second)
			if flagged := len(got.Services[0].Attention) > 0; flagged != tt.flagged {
				t.Errorf("flagged = %v (attention %+v), want %v", flagged, got.Services[0].Attention, tt.flagged)
			}
		})
	}
}

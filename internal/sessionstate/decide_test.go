package sessionstate

import (
	"testing"
	"time"
)

var marker = &Wait{Kind: "permission", Message: "needs permission to use Bash", Since: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}

// TestDecide is the precedence table: needs_you > working > idle > gone.
func TestDecide(t *testing.T) {
	cases := []struct {
		name  string
		f     Facts
		want  Activity
		where Place
	}{
		{"nothing at all", Facts{}, Gone, None},
		{"held, registry idle", Facts{Held: true, RegistryStatus: "idle"}, Idle, Terminal},
		{"held, nothing known", Facts{Held: true}, Idle, Terminal},
		{"held, registry busy", Facts{Held: true, RegistryStatus: "busy"}, Working, Terminal},
		{"held, screen working, registry idle", Facts{Held: true, RegistryStatus: "idle", Screen: ScreenWorking}, Working, Terminal},
		{"held, screen idle, registry busy", Facts{Held: true, RegistryStatus: "busy", Screen: ScreenIdle}, Working, Terminal},
		{"held, marker", Facts{Held: true, Waiting: marker}, NeedsYou, Terminal},
		{"held, marker beats registry busy", Facts{Held: true, RegistryStatus: "busy", Waiting: marker}, NeedsYou, Terminal},
		{"held, marker beats screen working", Facts{Held: true, Screen: ScreenWorking, Waiting: marker}, NeedsYou, Terminal},
		{"aimux turn", Facts{Background: true}, Working, Background},
		{"aimux turn with a pending approval", Facts{Background: true, Approval: true}, NeedsYou, Background},
		{"approval beats everything", Facts{Held: true, Background: true, Approval: true, RegistryStatus: "busy"}, NeedsYou, Background},
		{"aimux turn and a terminal: background", Facts{Held: true, Background: true}, Working, Background},
		{"marker without a holder still needs you", Facts{Waiting: marker}, NeedsYou, None},
		{"registry busy without a holder is gone", Facts{RegistryStatus: "busy"}, Gone, None},
		// The transcript heuristic: only for a held session nothing else describes.
		{"fallback working, nothing else known", Facts{Held: true, Fallback: Working}, Working, Terminal},
		{"fallback needs you, nothing else known", Facts{Held: true, Fallback: NeedsYou}, NeedsYou, Terminal},
		{"fallback idle", Facts{Held: true, Fallback: Idle}, Idle, Terminal},
		{"fallback ignored when the registry knows", Facts{Held: true, RegistryStatus: "idle", Fallback: Working}, Idle, Terminal},
		{"fallback ignored when the screen knows", Facts{Held: true, Screen: ScreenIdle, Fallback: NeedsYou}, Idle, Terminal},
		{"fallback ignored for a gone session", Facts{Fallback: Working}, Gone, None},
		{"fallback ignored during an aimux turn", Facts{Background: true, Fallback: NeedsYou}, Working, Background},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Decide(c.f)
			if got.Activity != c.want || got.Place != c.where {
				t.Fatalf("Decide(%+v) = %s/%s, want %s/%s", c.f, got.Activity, got.Place, c.want, c.where)
			}
			if got.Facts != c.f {
				t.Fatalf("State.Facts = %+v, want the inputs %+v", got.Facts, c.f)
			}
		})
	}
}

func TestActivityLabel(t *testing.T) {
	for a, want := range map[Activity]string{NeedsYou: "needs you", Working: "working", Idle: "idle", Gone: "", "": ""} {
		if got := a.Label(); got != want {
			t.Errorf("%q.Label() = %q, want %q", a, got, want)
		}
	}
}

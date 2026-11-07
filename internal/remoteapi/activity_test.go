package remoteapi

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/zanetworker/aimux/internal/harness"
	"github.com/zanetworker/aimux/internal/sessionstate"
	"github.com/zanetworker/aimux/internal/waiting"
)

type idSet map[string]bool

func (s idSet) Busy(id string) bool             { return s[id] }
func (s idSet) AwaitingApproval(id string) bool { return s[id] }

// activityAPI lists five sessions; the state service holds busy (registry
// busy), open (registry idle) and asking (a permission marker), aimux runs
// run with a pending approval, and gone has nothing.
func activityAPI(screenWorking bool) *API {
	list := []Session{
		{ID: "busy", State: StateOpenInTerminal, Working: true},
		{ID: "open", State: StateOpenInTerminal},
		{ID: "asking", State: StateOpenInTerminal, Working: true},
		{ID: "run", State: StateBusy},
		{ID: "gone", State: StateIdle},
	}
	return &API{
		Sessions: func(context.Context, int, bool) ([]Session, error) { return list, nil },
		Session: func(_ context.Context, id string, _ int) (SessionDetail, error) {
			for _, s := range list {
				if s.ID == id {
					d := SessionDetail{Session: s, Turns: []Turn{}}
					d.Working = false // the detail never carries working
					if s.State == StateOpenInTerminal {
						d.Terminal = &TerminalStatus{Status: "idle", Working: screenWorking}
					}
					return d, nil
				}
			}
			return SessionDetail{}, nil
		},
		States: &sessionstate.Service{
			Holders: func() map[string]harness.Holder {
				return map[string]harness.Holder{"busy": {PID: 1, Status: "busy"}, "open": {PID: 2, Status: "idle"}, "asking": {PID: 3, Status: "busy"}}
			},
			Busy:      idSet{"run": true},
			Approvals: idSet{"run": true},
			Markers: func() map[string]waiting.Waiting {
				return map[string]waiting.Waiting{"asking": {Kind: waiting.KindPermission, Message: "Claude needs your permission to use Bash", Since: waitSince}}
			},
		},
	}
}

func TestListSessions_Activity(t *testing.T) {
	want := map[string]string{"busy": "working", "open": "idle", "asking": "needs_you", "run": "needs_you", "gone": "gone"}
	for _, s := range listJSON(t, activityAPI(false)) {
		id := s["id"].(string)
		if s["activity"] != want[id] {
			t.Errorf("%s activity = %v, want %s", id, s["activity"], want[id])
		}
		_, hasWorking := s["working"]
		if wantWorking := id == "busy" || id == "asking"; hasWorking != wantWorking {
			t.Errorf("%s working present = %v, want %v", id, hasWorking, wantWorking)
		}
		_, hasWaiting := s["waiting"]
		if hasWaiting != (id == "asking") {
			t.Errorf("%s waiting present = %v", id, hasWaiting)
		}
	}
}

func TestGetSession_Activity(t *testing.T) {
	for _, c := range []struct {
		screen bool
		id     string
		want   string
	}{
		{false, "open", "idle"},
		{true, "open", "working"}, // the screen says working: known, so it counts
		{false, "asking", "needs_you"},
		{false, "run", "needs_you"},
		{false, "gone", "gone"},
	} {
		srv := httptest.NewServer(activityAPI(c.screen).Handler([]string{testLogin}, []string{"127.0.0.1"}))
		_, body := do(t, srv, "GET", "/v1/sessions/"+c.id, "")
		srv.Close()
		if body["activity"] != c.want {
			t.Errorf("screen %v: %s activity = %v, want %s", c.screen, c.id, body["activity"], c.want)
		}
		if _, ok := body["working"]; ok {
			t.Errorf("%s detail carries working", c.id)
		}
		if _, ok := body["waiting"]; ok != (c.id == "asking") {
			t.Errorf("%s detail waiting present = %v", c.id, ok)
		}
	}
}

func TestListSessions_NoStateService(t *testing.T) {
	api := activityAPI(false)
	api.States = nil
	for _, s := range listJSON(t, api) {
		if _, ok := s["activity"]; ok {
			t.Fatalf("activity present without a state service: %v", s)
		}
	}
}

// With SessionsAt the list gets the state service's own open-sessions read,
// so state, working and activity come from one read and cannot disagree.
func TestListSessions_OneOpenRead(t *testing.T) {
	reads := 0
	var given map[string]harness.Holder
	api := &API{
		SessionsAt: func(_ context.Context, _ int, _ bool, open map[string]harness.Holder) ([]Session, error) {
			given = open
			out := []Session{}
			for _, id := range []string{"a", "b"} {
				s := Session{ID: id, State: StateIdle}
				if h, ok := open[id]; ok {
					s.State, s.Working = StateOpenInTerminal, h.Status == "busy"
				}
				out = append(out, s)
			}
			return out, nil
		},
		States: &sessionstate.Service{Holders: func() map[string]harness.Holder {
			reads++
			if reads == 1 {
				return map[string]harness.Holder{"a": {PID: 1, SessionID: "a", Status: "busy"}}
			}
			return map[string]harness.Holder{"b": {PID: 2, SessionID: "b", Status: "busy"}} // a later read differs
		}},
	}
	got := listJSON(t, api)
	if reads != 1 {
		t.Fatalf("the list read the open sessions %d times, want 1", reads)
	}
	if _, ok := given["a"]; !ok || len(given) != 1 {
		t.Fatalf("SessionsAt got %v, want the state service's read", given)
	}
	for _, s := range got {
		switch s["id"] {
		case "a":
			if s["state"] != StateOpenInTerminal || s["working"] != true || s["activity"] != "working" {
				t.Errorf("a = %v", s)
			}
		case "b":
			if s["state"] != StateIdle || s["working"] != nil || s["activity"] != "gone" {
				t.Errorf("b = %v", s)
			}
		}
	}
}

// Broker.Busy counts a session held for an Open on Mac handoff, so the
// state service reads it working, in the background, until the handoff
// ends (intended: the session is being moved, not idle).
func TestActivity_HandoffHoldReadsWorking(t *testing.T) {
	b := NewBroker(BrokerDeps{}, BrokerOptions{})
	svc := &sessionstate.Service{Busy: b, Approvals: b}
	release, err := b.Hold("s1")
	if err != nil {
		t.Fatal(err)
	}
	if st := svc.Of("s1"); st.Activity != sessionstate.Working || st.Place != sessionstate.Background {
		t.Fatalf("held: %s/%s, want working/background", st.Activity, st.Place)
	}
	release()
	if st := svc.Of("s1"); st.Activity != sessionstate.Gone {
		t.Fatalf("released: %s, want gone", st.Activity)
	}
}

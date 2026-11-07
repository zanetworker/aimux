package remoteapi

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/harness"
	"github.com/zanetworker/aimux/internal/sessionstate"
	"github.com/zanetworker/aimux/internal/waiting"
)

var waitSince = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

func waitingAPI() *API {
	return &API{
		Sessions: func(context.Context, int, bool) ([]Session, error) {
			return []Session{{ID: "s1", State: StateOpenInTerminal}, {ID: "s2", State: StateOpenInTerminal}}, nil
		},
		Session: func(_ context.Context, id string, _ int) (SessionDetail, error) {
			return SessionDetail{Session: Session{ID: id, State: StateOpenInTerminal}, Turns: []Turn{}}, nil
		},
		States: &sessionstate.Service{
			Holders: func() map[string]harness.Holder {
				return map[string]harness.Holder{"s1": {PID: 1, SessionID: "s1"}, "s2": {PID: 2, SessionID: "s2"}}
			},
			Markers: func() map[string]waiting.Waiting {
				return map[string]waiting.Waiting{"s1": {Kind: waiting.KindPermission, Message: "Claude needs your permission to use Bash", Since: waitSince}}
			},
		},
	}
}

func TestListSessions_Waiting(t *testing.T) {
	got := listJSON(t, waitingAPI())
	w, ok := got[0]["waiting"].(map[string]any)
	if !ok || w["kind"] != "permission" || w["message"] != "Claude needs your permission to use Bash" || w["since"] != "2026-03-04T05:06:07Z" {
		t.Fatalf("s1 waiting = %v", got[0]["waiting"])
	}
	if _, ok := got[1]["waiting"]; ok {
		t.Fatalf("s2 has no marker but has waiting: %v", got[1]["waiting"])
	}
}

func TestListSessions_NoWaitingSource(t *testing.T) {
	api := waitingAPI()
	api.States = nil
	for _, s := range listJSON(t, api) {
		if _, ok := s["waiting"]; ok {
			t.Fatalf("waiting present with no source: %v", s)
		}
	}
}

func TestGetSession_Waiting(t *testing.T) {
	srv := httptest.NewServer(waitingAPI().Handler([]string{testLogin}, []string{"127.0.0.1"}))
	defer srv.Close()
	_, body := do(t, srv, "GET", "/v1/sessions/s1", "")
	if w, ok := body["waiting"].(map[string]any); !ok || w["kind"] != "permission" {
		t.Fatalf("detail waiting = %v", body["waiting"])
	}
	_, body = do(t, srv, "GET", "/v1/sessions/s2", "")
	if _, ok := body["waiting"]; ok {
		t.Fatalf("s2 detail has waiting: %v", body["waiting"])
	}
}

package web

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/controller"
)

func postFocus(t *testing.T, s *Server, id string) (int, map[string]any) {
	t.Helper()
	go func() { _ = s.Start() }()
	t.Cleanup(s.Stop)
	time.Sleep(100 * time.Millisecond)
	resp, err := http.Post(s.URL()+"/api/agents/"+id+"/focus", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	return resp.StatusCode, body
}

func TestHandleFocus_Focused(t *testing.T) {
	var gotTTY string
	s := newServerWithAgents([]agent.Agent{{PID: 4242, SessionID: "19c0ce61-aaaa"}})
	s.SetFocuser(controller.Focuser{
		TTYForPID: func(int) string { return "/dev/ttys031" },
		FocusTTY: func(tty string) (string, bool, error) {
			gotTTY = tty
			return "tab 2 · pane 1 of 1", true, nil
		},
	})
	status, body := postFocus(t, s, "19c0ce")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", status, body)
	}
	if gotTTY != "/dev/ttys031" || body["focused"] != true || body["where"] != "tab 2 · pane 1 of 1" {
		t.Errorf("unexpected: tty=%q body=%v", gotTTY, body)
	}
}

func TestHandleFocus_NotFound(t *testing.T) {
	s := newServerWithAgents(nil)
	status, body := postFocus(t, s, "nope")
	if status != http.StatusNotFound || body["error"] == nil {
		t.Errorf("status = %d body = %v, want 404 with error", status, body)
	}
}

func TestHandleFocus_Ambiguous(t *testing.T) {
	s := newServerWithAgents([]agent.Agent{{PID: 1, SessionID: "abc1"}, {PID: 2, SessionID: "abc2"}})
	status, _ := postFocus(t, s, "abc")
	if status != http.StatusConflict {
		t.Errorf("status = %d, want 409", status)
	}
}

func TestHandleFocus_NoPane_ReturnsHint(t *testing.T) {
	s := newServerWithAgents([]agent.Agent{{PID: 7, SessionID: "tm", TMuxSession: "aimux-codex"}})
	s.SetFocuser(controller.Focuser{
		TTYForPID: func(int) string { return "" },
		FocusTTY:  func(string) (string, bool, error) { return "", false, nil },
	})
	status, body := postFocus(t, s, "tm")
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", status)
	}
	if body["hint"] != "tmux attach-session -t aimux-codex" || body["focused"] != false {
		t.Errorf("unexpected body: %v", body)
	}
}

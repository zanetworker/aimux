package web

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/harness"
	"github.com/zanetworker/aimux/internal/waiting"
)

type fixedLive []harness.Holder

func (f fixedLive) Holders(harness.Mode) ([]harness.Holder, error) { return f, nil }

// activityServer discovers two agents: one whose hook marker says it is at
// a permission prompt (the transcript guess says idle) and one the
// registry calls busy.
func activityServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	me := os.Getpid()
	if err := waiting.Write(dir, "asks", "Claude needs your permission to use Bash", time.Now()); err != nil {
		t.Fatal(err)
	}
	s := NewServer(0)
	s.SetDiscoverFunc(func() ([]agent.Agent, error) {
		return []agent.Agent{
			{PID: me, SessionID: "asks", ProviderName: "claude", Status: agent.StatusIdle},
			{PID: 7, SessionID: "runs", ProviderName: "claude", Status: agent.StatusIdle},
		}, nil
	})
	s.SetActivity(controller.NewLocalActivity([]harness.LiveSessions{fixedLive{
		{PID: me, SessionID: "asks", Status: "busy"}, {PID: 7, SessionID: "runs", Status: "busy"},
	}}, dir))
	return s
}

// The agents stream carries the state service's activity, and Status
// follows it, as the TUI shows it.
func TestSSE_CarriesActivity(t *testing.T) {
	s := activityServer(t)
	rec := httptest.NewRecorder()
	s.sendAgentEvent(rec, rec)
	data, ok := strings.CutPrefix(strings.Split(rec.Body.String(), "\n")[1], "data: ")
	if !ok {
		t.Fatalf("no data line in %q", rec.Body.String())
	}
	var payload struct {
		Agents []agent.Agent `json:"agents"`
	}
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		t.Fatal(err)
	}
	got := map[string]agent.Agent{}
	for _, a := range payload.Agents {
		got[a.SessionID] = a
	}
	if got["asks"].Activity != "needs_you" || got["asks"].Status != agent.StatusWaitingPermission {
		t.Errorf("asks: %q %v, want needs_you, waiting", got["asks"].Activity, got["asks"].Status)
	}
	if got["runs"].Activity != "working" || got["runs"].Status != agent.StatusActive {
		t.Errorf("runs: %q %v, want working, active", got["runs"].Activity, got["runs"].Status)
	}
}

// GET /api/agents carries it too.
func TestAgentsAPI_CarriesActivity(t *testing.T) {
	s := activityServer(t)
	rec := httptest.NewRecorder()
	s.handleAgents(rec, httptest.NewRequest("GET", "/api/agents", nil))
	var body struct {
		Agents []map[string]any `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	got := map[any]any{}
	for _, a := range body.Agents {
		got[a["sessionId"]] = a["activity"]
	}
	if got["asks"] != "needs_you" || got["runs"] != "working" {
		t.Fatalf("activity by session = %v", got)
	}
}

// The agents stream carries hooksHint while the permission hooks are
// missing, and an empty one once they are installed.
func TestSSE_HooksHint(t *testing.T) {
	hint := func(s *Server) any {
		rec := httptest.NewRecorder()
		s.sendAgentEvent(rec, rec)
		data, _ := strings.CutPrefix(strings.Split(rec.Body.String(), "\n")[1], "data: ")
		var payload map[string]any
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			t.Fatal(err)
		}
		return payload["hooksHint"]
	}
	s := activityServer(t)
	s.activity.HooksSettings = filepath.Join(t.TempDir(), "settings.json") // none: missing
	if got := hint(s); got != controller.HooksHint {
		t.Fatalf("hooks missing: hooksHint = %v", got)
	}
	s = activityServer(t) // no settings checked: installed
	if got := hint(s); got != "" {
		t.Fatalf("hooks known installed: hooksHint = %v", got)
	}
}

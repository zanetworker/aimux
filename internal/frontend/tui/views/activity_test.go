package views

import (
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/history"
)

// The agents view, its preview, the header and the sessions view use the
// state service's words: needs you, working, idle.

func TestAgentsView_NeedsYouWordInRow(t *testing.T) {
	v := NewAgentsView()
	v.SetSize(200, 30)
	v.SetAgents([]agent.Agent{
		{PID: 1, Name: "asks", ProviderName: "claude", SessionID: "s1", Status: agent.StatusWaitingPermission, Activity: "needs_you", LastAction: "Bash"},
		{PID: 2, Name: "runs", ProviderName: "claude", SessionID: "s2", Status: agent.StatusActive, Activity: "working", LastAction: "Edit"},
	})
	out := v.View()
	if !strings.Contains(out, "needs you") {
		t.Errorf("agents view has no 'needs you':\n%s", out)
	}
	if !strings.Contains(out, "Edit") {
		t.Errorf("a working agent's row lost its last action:\n%s", out)
	}
}

func TestPreview_ActivityWord(t *testing.T) {
	for activity, want := range map[string]string{"needs_you": "needs you", "working": "working", "idle": "idle"} {
		p := NewPreviewPane()
		p.SetSize(120, 30)
		a := agent.Agent{PID: 1, Name: "x", SessionID: "s", Status: agent.StatusIdle, Activity: activity}
		p.SetAgent(&a)
		if out := p.renderHeader(); !strings.Contains(out, want) {
			t.Errorf("activity %s: preview header has no %q:\n%s", activity, want, out)
		}
	}
	// Without an activity (cached agents, before the first refresh) the status stays.
	p := NewPreviewPane()
	p.SetSize(120, 30)
	a := agent.Agent{PID: 1, Name: "x", Status: agent.StatusError}
	p.SetAgent(&a)
	if out := p.renderHeader(); !strings.Contains(out, "Error") {
		t.Errorf("no activity: preview header has no status:\n%s", out)
	}
}

func TestHeader_ActivityWords(t *testing.T) {
	h := NewHeaderView()
	h.SetWidth(200)
	h.SetAgents([]agent.Agent{
		{PID: 1, Status: agent.StatusWaitingPermission, Activity: "needs_you"},
		{PID: 2, Status: agent.StatusActive, Activity: "working"},
		{PID: 3, Status: agent.StatusIdle, Activity: "idle"},
	})
	out := h.View()
	for _, w := range []string{"1 working", "1 needs you", "1 idle"} {
		if !strings.Contains(out, w) {
			t.Errorf("header has no %q:\n%s", w, out)
		}
	}
}

func TestSessionsView_ActivityWord(t *testing.T) {
	now := time.Now()
	v := NewSessionsView()
	v.SetSize(200, 20)
	v.SetSessions([]history.Session{
		{ID: "asks", Project: "/w", FirstPrompt: "fix the cache", TurnCount: 5, CostUSD: 1, StartTime: now, LastActive: now},
		{ID: "runs", Project: "/w", FirstPrompt: "write the docs", TurnCount: 5, CostUSD: 1, StartTime: now, LastActive: now},
		{ID: "closed", Project: "/w", FirstPrompt: "old work", TurnCount: 5, CostUSD: 1, StartTime: now, LastActive: now},
	})
	v.SetActivity(map[string]string{"asks": "needs_you", "runs": "working", "closed": "gone"})
	out := v.View()
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "fix the cache") && !strings.Contains(line, "needs you"):
			t.Errorf("asks row has no 'needs you': %q", line)
		case strings.Contains(line, "write the docs") && !strings.Contains(line, "working"):
			t.Errorf("runs row has no 'working': %q", line)
		case strings.Contains(line, "old work") && (strings.Contains(line, "gone") || strings.Contains(line, "idle")):
			t.Errorf("a closed session shows an activity: %q", line)
		}
	}
}

const hooksHint = "Permission alerts need `aimux remote hooks install`"

// Without the hooks a permission prompt cannot read "needs you": the header
// and the help say so, muted; with them nothing shows.
func TestHeader_HooksHint(t *testing.T) {
	h := NewHeaderView()
	h.SetWidth(200)
	before := h.Height()
	h.SetHooksMissing(true)
	if !strings.Contains(h.View(), hooksHint) {
		t.Fatalf("header without hooks has no hint:\n%s", h.View())
	}
	if h.Height() != before || strings.Count(h.View(), "\n") != strings.Count(func() string { h.SetHooksMissing(false); v := h.View(); h.SetHooksMissing(true); return v }(), "\n") {
		t.Fatal("the hint changed the header's height")
	}
	h.SetHooksMissing(false)
	if strings.Contains(h.View(), hooksHint) {
		t.Fatal("header with hooks still hints")
	}
}

func TestHelp_HooksHint(t *testing.T) {
	v := NewHelpView()
	v.SetSize(120, 80)
	if strings.Contains(v.View(), hooksHint) {
		t.Fatal("help hints before hooks are known missing")
	}
	v.SetHooksMissing(true)
	if !strings.Contains(v.View(), hooksHint) {
		t.Fatalf("help without hooks has no hint:\n%s", v.View())
	}
}

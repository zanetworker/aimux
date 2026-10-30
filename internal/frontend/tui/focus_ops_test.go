package tui

import (
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/frontend/tui/views"
)

func focusTestApp(agents []agent.Agent, f controller.Focuser) App {
	av := views.NewAgentsView()
	av.SetAgents(agents)
	return App{currentView: viewAgents, agentsView: av, headerView: views.NewHeaderView(), focuser: f}
}

// pressF presses F and delivers the async focus result back to the app.
func pressF(t *testing.T, app App) App {
	t.Helper()
	result, cmd := app.handleKey(keyMsg("F"))
	a := result.(App)
	if cmd == nil {
		return a
	}
	next, _ := a.Update(cmd())
	return next.(App)
}

func TestFocusKey_DoesNotBlockKeyHandling(t *testing.T) {
	called := false
	app := focusTestApp([]agent.Agent{{PID: 4242, SessionID: "s1"}}, controller.Focuser{
		TTYForPID: func(int) string { called = true; return "/dev/ttys031" },
		FocusTTY:  func(string) (string, bool, error) { return "x", true, nil },
	})
	_, cmd := app.handleKey(keyMsg("F"))
	if called {
		t.Fatal("focus ran inside handleKey; it must run in the returned tea.Cmd")
	}
	if cmd == nil {
		t.Fatal("want a tea.Cmd that does the focusing")
	}
}

func TestFocusKey_RaisesSelectedAgentPane(t *testing.T) {
	var gotPID int
	app := focusTestApp([]agent.Agent{{PID: 4242, SessionID: "s1", Name: "oce"}}, controller.Focuser{
		TTYForPID: func(pid int) string { gotPID = pid; return "/dev/ttys031" },
		FocusTTY:  func(string) (string, bool, error) { return "tab 2 · pane 1 of 1", true, nil },
	})
	a := pressF(t, app)
	if gotPID != 4242 {
		t.Errorf("focused pid %d, want 4242", gotPID)
	}
	if !strings.Contains(a.statusHint, "tab 2 · pane 1 of 1") {
		t.Errorf("statusHint = %q, want the pane location", a.statusHint)
	}
}

func TestFocusKey_NoPane_ShowsTmuxHint(t *testing.T) {
	app := focusTestApp([]agent.Agent{{PID: 7, SessionID: "s1", TMuxSession: "aimux-codex"}}, controller.Focuser{
		TTYForPID: func(int) string { return "" },
		FocusTTY:  func(string) (string, bool, error) { return "", false, nil },
	})
	if hint := pressF(t, app).statusHint; !strings.Contains(hint, "tmux attach-session -t aimux-codex") {
		t.Errorf("statusHint = %q, want tmux attach hint", hint)
	}
}

func TestFocusKey_NoSelection(t *testing.T) {
	app := focusTestApp(nil, controller.Focuser{
		TTYForPID: func(int) string { t.Fatal("must not look up a tty"); return "" },
	})
	if hint := pressF(t, app).statusHint; hint != "No agent selected" {
		t.Errorf("statusHint = %q, want %q", hint, "No agent selected")
	}
}

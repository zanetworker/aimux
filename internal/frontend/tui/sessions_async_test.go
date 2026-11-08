package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/frontend/tui/views"
)

// sessionsHome makes a HOME with one Claude session in /work/proj.
func sessionsHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "projects", "-work-proj")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	id := "6d0c1f3e-1111-4222-8333-944445555666"
	line := fmt.Sprintf(`{"type":"user","sessionId":%q,"cwd":"/work/proj","timestamp":"2026-11-01T10:00:00Z","message":{"role":"user","content":"hello there"}}`+"\n", id) +
		fmt.Sprintf(`{"type":"assistant","sessionId":%q,"cwd":"/work/proj","timestamp":"2026-11-01T10:00:05Z","message":{"role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":10,"output_tokens":5}}}`+"\n", id)
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

// sessionLoads runs cmd (following batches) and returns its sessions loads.
func sessionLoads(cmd tea.Cmd) []sessionsLoadedMsg {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case sessionsLoadedMsg:
		return []sessionsLoadedMsg{msg}
	case tea.BatchMsg:
		var out []sessionsLoadedMsg
		for _, c := range msg {
			out = append(out, sessionLoads(c)...)
		}
		return out
	}
	return nil
}

func TestOpenSessions_LoadsOffTheUIGoroutine(t *testing.T) {
	sessionsHome(t)
	app := newTestAppForPending(map[string]agent.Agent{})
	app.sessionsView.SetShowAll(true)
	app.width, app.height = 200, 50

	m, cmd := app.Update(keyMsg("S"))
	app = m.(App)
	if app.currentView != viewSessions {
		t.Fatalf("S did not open the sessions view (view %v)", app.currentView)
	}
	if app.sessionsView.SelectedSession() != nil {
		t.Fatal("sessions were listed before the load message: Discover ran inside Update")
	}
	loads := sessionLoads(cmd)
	if len(loads) != 1 {
		t.Fatalf("S returned %d session loads, want 1", len(loads))
	}
	if len(loads[0].sessions) != 1 {
		t.Fatalf("load found %d sessions, want 1", len(loads[0].sessions))
	}
	m, _ = app.Update(loads[0])
	app = m.(App)
	if app.sessionsView.SelectedSession() == nil {
		t.Error("sessions not listed after the load message")
	}
}

func TestOpenSessions_OlderLoadDropped(t *testing.T) {
	sessionsHome(t)
	app := newTestAppForPending(map[string]agent.Agent{})
	app.sessionsView.SetShowAll(true)

	m, first := app.Update(keyMsg("S"))
	app = m.(App)
	m, _ = app.Update(tea.KeyMsg{Type: tea.KeyEsc})
	app = m.(App)
	m, second := app.Update(keyMsg("S"))
	app = m.(App)

	old, fresh := sessionLoads(first), sessionLoads(second)
	if len(old) != 1 || len(fresh) != 1 {
		t.Fatalf("loads: %d and %d, want 1 each", len(old), len(fresh))
	}
	old[0].sessions = nil // tell the two apart
	m, _ = app.Update(fresh[0])
	app = m.(App)
	m, _ = app.Update(old[0])
	app = m.(App)
	if app.sessionsView.SelectedSession() == nil {
		t.Error("an older load replaced the newer one's sessions")
	}
}

// A scan that read the sessions before a delete or a star must not undo it
// when its result arrives; the mutation starts a fresh scan instead.
func TestSessionsMutationDuringScan_NotUndone(t *testing.T) {
	sessionsHome(t)
	app := newTestAppForPending(map[string]agent.Agent{})
	app.sessionsView.SetShowAll(true)

	m, open := app.Update(keyMsg("S"))
	app = m.(App)
	stale := sessionLoads(open) // the scan read the list now
	if len(stale) != 1 || len(stale[0].sessions) != 1 {
		t.Fatalf("setup: loads %+v", stale)
	}
	session := stale[0].sessions[0]

	// Star it while that scan is still on its way.
	m, starCmd := app.Update(views.SessionStarMsg{Session: session, Starred: true})
	app = m.(App)
	rescans := sessionLoads(starCmd)
	if len(rescans) != 1 {
		t.Fatalf("star during a pending scan started %d scans, want 1", len(rescans))
	}
	m, _ = app.Update(stale[0])
	app = m.(App)
	if sel := app.sessionsView.SelectedSession(); sel != nil && !sel.Starred {
		t.Fatal("the older scan's result undid the star")
	}
	m, _ = app.Update(rescans[0])
	app = m.(App)
	if sel := app.sessionsView.SelectedSession(); sel == nil || !sel.Starred {
		t.Fatalf("after the fresh scan: %+v, want the session starred", sel)
	}

	// Delete it while a scan that still lists it is on its way.
	m, toggle := app.Update(views.SessionToggleScopeMsg{ShowAll: true})
	app = m.(App)
	stale = sessionLoads(toggle)
	if len(stale) != 1 || len(stale[0].sessions) != 1 {
		t.Fatalf("setup: second scan %+v", stale)
	}
	m, delCmd := app.Update(views.SessionDeleteMsg{Session: session})
	app = m.(App)
	rescans = sessionLoads(delCmd)
	if len(rescans) != 1 {
		t.Fatalf("delete during a pending scan started %d scans, want 1", len(rescans))
	}
	m, _ = app.Update(stale[0])
	app = m.(App)
	m, _ = app.Update(rescans[0])
	app = m.(App)
	if sel := app.sessionsView.SelectedSession(); sel != nil {
		t.Error("a deleted session came back from a scan that started before the delete")
	}

	// With no scan pending, a mutation starts none.
	m, cmd := app.Update(views.SessionNoteMsg{Session: session, Note: "x"})
	app = m.(App)
	if n := len(sessionLoads(cmd)); n != 0 {
		t.Errorf("a note with no scan pending started %d scans", n)
	}
	_ = app
}

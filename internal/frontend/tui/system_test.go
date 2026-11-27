package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/zanetworker/aimux/internal/controller"
)

// :system (and M from the agents list) opens the System view and reads the machine off the UI thread; the
// reading arrives as a message and is shown; r reads again; j and k scroll.
func TestSystemView_OpensReadsAndRefreshes(t *testing.T) {
	if resolveCommand("system") != "system" {
		t.Fatal(":system is not a command")
	}
	reads := 0
	app := NewApp()
	app.width, app.height = 140, 60
	app.SetSystemReader(func(context.Context) controller.SystemSnapshot {
		reads++
		return controller.SystemSnapshot{At: time.Now(), Services: controller.StatusOverview{CLIBuild: "v0.14.0-332-gabc",
			Services: []controller.ServiceStatus{{Name: "model proxy", State: controller.ServiceOK}}}}
	})

	model, cmd := app.executeCommand("system")
	a := model.(App)
	if a.currentView != viewSystem || cmd == nil {
		t.Fatalf("view %d, a read started: %v", a.currentView, cmd != nil)
	}
	if out := a.systemView.View(); !strings.Contains(out, "Reading this machine") || reads != 0 {
		t.Fatalf("before the reading arrives (reads %d):\n%s", reads, out)
	}
	model, _ = a.Update(cmd())
	a = model.(App)
	if out := a.systemView.View(); reads != 1 || !strings.Contains(out, "model proxy") || !strings.Contains(out, "SERVICES") {
		t.Fatalf("after the reading (reads %d):\n%s", reads, out)
	}

	// r reads again; the screen keeps the last reading until the new one arrives.
	model, cmd = a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	a = model.(App)
	if cmd == nil || !strings.Contains(a.systemView.View(), "model proxy") {
		t.Fatal("r did not start a read, or cleared the screen")
	}
	if _, _ = a.Update(cmd()); reads != 2 {
		t.Fatalf("reads after r: %d", reads)
	}

	// M opens it from the agents list.
	fresh := NewApp()
	fresh.SetSystemReader(func(context.Context) controller.SystemSnapshot { return controller.SystemSnapshot{} })
	model, cmd = fresh.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("M")})
	if model.(App).currentView != viewSystem || cmd == nil {
		t.Fatal("M did not open the System view")
	}
}

// Without a reader (a build that wires none), the command says so and stays where it is.
func TestSystemView_NoReader(t *testing.T) {
	model, cmd := NewApp().executeCommand("system")
	if a := model.(App); a.currentView == viewSystem || cmd != nil || a.statusHint == "" {
		t.Fatalf("view %d, hint %q", a.currentView, a.statusHint)
	}
}

// A reading that arrives after a newer one is dropped: two reads can be in flight after r is pressed twice, and
// the slower one must not put older figures back on the screen.
func TestSystemView_AnOlderReadingDoesNotReplaceANewerOne(t *testing.T) {
	app := NewApp()
	app.width, app.height = 140, 60
	app.SetSystemReader(func(context.Context) controller.SystemSnapshot { return controller.SystemSnapshot{} })
	reading := func(at time.Time, name string) systemMsg {
		return systemMsg(controller.SystemSnapshot{At: at, Services: controller.StatusOverview{Services: []controller.ServiceStatus{{Name: name, State: controller.ServiceOK}}}})
	}
	now := time.Now()
	model, _ := app.Update(reading(now, "the newer reading"))
	model, _ = model.(App).Update(reading(now.Add(-time.Minute), "the older reading"))
	if out := model.(App).systemView.View(); !strings.Contains(out, "the newer reading") || strings.Contains(out, "the older reading") {
		t.Fatalf("an older reading replaced a newer one:\n%s", out)
	}
}

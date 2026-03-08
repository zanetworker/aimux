package controller

import (
	"testing"
)

func TestNavigator_InitialState(t *testing.T) {
	n := NewNavigator()
	if n.CurrentView != ViewAgents {
		t.Errorf("initial view = %d, want ViewAgents", n.CurrentView)
	}
	if len(n.Breadcrumbs) != 1 || n.Breadcrumbs[0] != "Agents" {
		t.Errorf("initial breadcrumbs = %v, want [Agents]", n.Breadcrumbs)
	}
	if n.Zoomed || n.SplitMode {
		t.Error("should not be zoomed or split initially")
	}
	if n.SplitFocus != "" {
		t.Errorf("initial split focus = %q, want empty", n.SplitFocus)
	}
}

func TestNavigator_NavigateTo(t *testing.T) {
	n := NewNavigator()
	n.NavigateTo(ViewSessions, "Sessions")
	if n.CurrentView != ViewSessions {
		t.Errorf("view = %d, want ViewSessions", n.CurrentView)
	}
	if len(n.Breadcrumbs) != 2 || n.Breadcrumbs[1] != "Sessions" {
		t.Errorf("breadcrumbs = %v, want [Agents Sessions]", n.Breadcrumbs)
	}
}

func TestNavigator_NavigateToAgents_ResetsBreadcrumbs(t *testing.T) {
	n := NewNavigator()
	n.NavigateTo(ViewSessions, "Sessions")
	n.NavigateTo(ViewAgents, "")
	if len(n.Breadcrumbs) != 1 {
		t.Errorf("breadcrumbs = %v, want [Agents]", n.Breadcrumbs)
	}
	if n.Breadcrumbs[0] != "Agents" {
		t.Errorf("breadcrumbs[0] = %q, want Agents", n.Breadcrumbs[0])
	}
}

func TestNavigator_NavigateBack(t *testing.T) {
	n := NewNavigator()
	n.NavigateTo(ViewLogs, "Logs [PID 123]")
	n.NavigateBack()
	if n.CurrentView != ViewAgents {
		t.Errorf("view = %d, want ViewAgents after back", n.CurrentView)
	}
	if len(n.Breadcrumbs) != 1 {
		t.Errorf("breadcrumbs = %v, want [Agents]", n.Breadcrumbs)
	}
}

func TestNavigator_NavigateBack_AlreadyAtAgents(t *testing.T) {
	n := NewNavigator()
	n.NavigateBack() // should be no-op
	if n.CurrentView != ViewAgents {
		t.Errorf("view = %d, want ViewAgents", n.CurrentView)
	}
	if len(n.Breadcrumbs) != 1 || n.Breadcrumbs[0] != "Agents" {
		t.Errorf("breadcrumbs = %v, want [Agents]", n.Breadcrumbs)
	}
}

func TestNavigator_EnterAndExitZoom_Hierarchical(t *testing.T) {
	n := NewNavigator()
	n.EnterZoom()
	if !n.Zoomed || !n.SplitMode {
		t.Error("expected zoomed + split after EnterZoom")
	}
	if n.SplitFocus != "session" {
		t.Errorf("focus after EnterZoom = %q, want session", n.SplitFocus)
	}

	// Toggle to full-screen (SplitMode off)
	n.ToggleSplit()
	if n.SplitMode {
		t.Error("expected split off after toggle")
	}

	// First exit: should return to split (not main)
	exited := n.ExitZoom()
	if exited {
		t.Error("first ExitZoom should return to split, not fully exit")
	}
	if !n.SplitMode {
		t.Error("should be back in split mode")
	}
	if n.SplitFocus != "session" {
		t.Errorf("focus after first exit = %q, want session", n.SplitFocus)
	}

	// Second exit: should fully exit
	exited = n.ExitZoom()
	if !exited {
		t.Error("second ExitZoom should fully exit")
	}
	if n.Zoomed || n.SplitMode {
		t.Error("should not be zoomed or split after full exit")
	}
	if n.SplitFocus != "" {
		t.Errorf("focus after full exit = %q, want empty", n.SplitFocus)
	}
}

func TestNavigator_ToggleSplitFocus(t *testing.T) {
	n := NewNavigator()
	n.EnterZoom()
	if n.SplitFocus != "session" {
		t.Errorf("initial focus = %q, want session", n.SplitFocus)
	}
	n.ToggleSplitFocus()
	if n.SplitFocus != "trace" {
		t.Errorf("after toggle = %q, want trace", n.SplitFocus)
	}
	n.ToggleSplitFocus()
	if n.SplitFocus != "session" {
		t.Errorf("after second toggle = %q, want session", n.SplitFocus)
	}
}

func TestNavigator_ExitZoom_FromSplitMode(t *testing.T) {
	// When already in split mode, ExitZoom should fully exit immediately.
	n := NewNavigator()
	n.EnterZoom() // zoomed=true, splitMode=true
	exited := n.ExitZoom()
	if !exited {
		t.Error("ExitZoom from split mode should fully exit")
	}
	if n.Zoomed || n.SplitMode {
		t.Error("should not be zoomed or split after exit from split mode")
	}
}

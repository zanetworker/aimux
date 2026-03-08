package controller

// ViewType identifies which view is active.
type ViewType int

const (
	ViewAgents ViewType = iota
	ViewLogs
	ViewCosts
	ViewTeams
	ViewSessions
	ViewHelp
)

// Navigator manages the view navigation state machine.
// It tracks which view is active, breadcrumbs, and zoom/split state.
type Navigator struct {
	CurrentView ViewType
	Breadcrumbs []string
	Zoomed      bool
	SplitMode   bool
	SplitFocus  string // "trace" or "session"
}

// NewNavigator creates a Navigator in the default state (agents view).
func NewNavigator() *Navigator {
	return &Navigator{
		CurrentView: ViewAgents,
		Breadcrumbs: []string{"Agents"},
	}
}

// NavigateTo switches to a new view with a breadcrumb label.
func (n *Navigator) NavigateTo(v ViewType, label string) {
	n.CurrentView = v
	if v == ViewAgents {
		n.Breadcrumbs = []string{"Agents"}
	} else {
		n.Breadcrumbs = []string{"Agents", label}
	}
}

// NavigateBack returns to the agents view.
func (n *Navigator) NavigateBack() {
	if n.CurrentView != ViewAgents {
		n.CurrentView = ViewAgents
		n.Breadcrumbs = []string{"Agents"}
	}
}

// EnterZoom enters zoomed session mode with split view.
func (n *Navigator) EnterZoom() {
	n.Zoomed = true
	n.SplitMode = true
	n.SplitFocus = "session"
}

// ExitZoom implements hierarchical back navigation:
// fullscreen -> split view -> main view.
// Returns true if fully exited zoom (back to main), false if returned to split.
func (n *Navigator) ExitZoom() (exitedFully bool) {
	// If in full-screen mode (not split), return to split
	if !n.SplitMode && n.Zoomed {
		n.SplitMode = true
		n.SplitFocus = "session"
		return false
	}
	// Otherwise exit to main view
	n.Zoomed = false
	n.SplitMode = false
	n.SplitFocus = ""
	return true
}

// ToggleSplit toggles between split and full-screen mode.
func (n *Navigator) ToggleSplit() {
	n.SplitMode = !n.SplitMode
}

// ToggleSplitFocus switches focus between "trace" and "session" panes.
func (n *Navigator) ToggleSplitFocus() {
	if n.SplitFocus == "trace" {
		n.SplitFocus = "session"
	} else {
		n.SplitFocus = "trace"
	}
}

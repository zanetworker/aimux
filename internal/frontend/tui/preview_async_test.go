package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/frontend/tui/views"
	"github.com/zanetworker/aimux/internal/provider"
)

// writeClaudeTranscript writes a Claude Code transcript of turns turns, each
// padded to about pad bytes, and returns its path.
func writeClaudeTranscript(tb testing.TB, dir, name string, turns, pad int) string {
	tb.Helper()
	filler := strings.Repeat("x", pad)
	var b strings.Builder
	for i := 0; i < turns; i++ {
		ts := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		fmt.Fprintf(&b, `{"type":"user","timestamp":%q,"message":{"role":"user","content":"question %d %s"}}`+"\n", ts, i, filler)
		fmt.Fprintf(&b, `{"type":"assistant","timestamp":%q,"message":{"role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"answer %d"}],"usage":{"input_tokens":10,"output_tokens":5}}}`+"\n", ts, i)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		tb.Fatal(err)
	}
	return path
}

// newPreviewTestApp is an App in the Agents view with two Claude agents: a
// small transcript first and a large one second.
func newPreviewTestApp(tb testing.TB, largeTurns int) (App, string) {
	tb.Helper()
	dir := tb.TempDir()
	small := writeClaudeTranscript(tb, dir, "small.jsonl", 2, 10)
	large := writeClaudeTranscript(tb, dir, "large.jsonl", largeTurns, 4000)
	app := newTestAppForPending(map[string]agent.Agent{})
	app.providers = []provider.Provider{&provider.Claude{}}
	app.currentView = viewAgents
	app.layout = NewLayout(200, 50)
	app.width, app.height = 200, 50
	app.instances = []agent.Agent{
		{PID: 101, Name: "a-small", ProviderName: "claude", SessionFile: small, Status: agent.StatusIdle},
		{PID: 102, Name: "b-large", ProviderName: "claude", SessionFile: large, Status: agent.StatusIdle},
	}
	app.agentsView.SetAgents(app.instances)
	return app, large
}

// previewLoads runs cmd (following batches) and returns the preview loads
// it produced.
func previewLoads(cmd tea.Cmd) []views.PreviewLoadedMsg {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case views.PreviewLoadedMsg:
		return []views.PreviewLoadedMsg{msg}
	case tea.BatchMsg:
		var out []views.PreviewLoadedMsg
		for _, c := range msg {
			out = append(out, previewLoads(c)...)
		}
		return out
	}
	return nil
}

func TestAgentsKeyDown_PreviewArrivesAsMessage(t *testing.T) {
	app, large := newPreviewTestApp(t, 400) // ~1.6 MB
	if sel := app.agentsView.Selected(); sel == nil || sel.PID != 101 {
		t.Fatalf("setup: selected = %+v, want PID 101", sel)
	}

	m, cmd := app.Update(tea.KeyMsg{Type: tea.KeyDown})
	app = m.(App)
	if sel := app.agentsView.Selected(); sel == nil || sel.PID != 102 {
		t.Fatalf("KeyDown did not move to the large agent: %+v", sel)
	}
	if out := app.View(); !strings.Contains(out, "Loading conversation") {
		t.Errorf("before the load message the preview should be loading, got no loading line")
	}

	loads := previewLoads(cmd)
	if len(loads) != 1 {
		t.Fatalf("KeyDown returned %d preview loads, want 1", len(loads))
	}
	if loads[0].PID != 102 || loads[0].Path != large {
		t.Fatalf("load is for pid %d, want 102", loads[0].PID)
	}
	if len(loads[0].Turns) == 0 || len(loads[0].Turns) >= 400 {
		t.Errorf("preview parsed %d turns; want the tail only (some, fewer than the session's 400)", len(loads[0].Turns))
	}

	m, _ = app.Update(loads[0])
	app = m.(App)
	out := app.View()
	if strings.Contains(out, "Loading conversation") {
		t.Error("preview still loading after its message arrived")
	}
	if !strings.Contains(out, "question 399") {
		t.Error("preview does not show the session's last turn")
	}
}

func TestAgentsKeyDown_StaleLoadDropped(t *testing.T) {
	app, _ := newPreviewTestApp(t, 50)
	m, down := app.Update(tea.KeyMsg{Type: tea.KeyDown})
	app = m.(App)
	m, up := app.Update(tea.KeyMsg{Type: tea.KeyUp})
	app = m.(App)

	stale := previewLoads(down)
	fresh := previewLoads(up)
	if len(stale) != 1 || len(fresh) != 1 {
		t.Fatalf("loads: down=%d up=%d, want 1 each", len(stale), len(fresh))
	}
	m, _ = app.Update(stale[0])
	app = m.(App)
	if strings.Contains(app.View(), "question 49") {
		t.Fatal("the large agent's turns were shown while the small one is selected")
	}
	m, _ = app.Update(fresh[0])
	app = m.(App)
	if !strings.Contains(app.View(), "question 1") {
		t.Error("the selected agent's turns were not shown")
	}
}

func TestInstancesMsg_LoadsFirstSelectedAgentsPreview(t *testing.T) {
	// The first selected agent used to stay "No conversation data" until the
	// cursor moved: only key presses set the preview's parser.
	app, _ := newPreviewTestApp(t, 5)
	m, cmd := app.Update(instancesMsg(append([]agent.Agent(nil), app.instances...)))
	app = m.(App)
	loads := previewLoads(cmd)
	if len(loads) != 1 || loads[0].PID != 101 {
		t.Fatalf("refresh returned loads %+v, want one for PID 101", loads)
	}
	m, _ = app.Update(loads[0])
	app = m.(App)
	if !strings.Contains(app.View(), "question 1") {
		t.Error("first agent's preview is empty after the first refresh")
	}

	// A second refresh with nothing changed loads nothing.
	m, cmd = app.Update(instancesMsg(append([]agent.Agent(nil), app.instances...)))
	app = m.(App)
	if n := len(previewLoads(cmd)); n != 0 {
		t.Errorf("unchanged refresh started %d preview loads, want 0", n)
	}
}

func TestView_DoesNotChangePreviewAgent(t *testing.T) {
	// View must not change the preview's agent (it used to call SetAgent
	// and could parse a transcript inside View); Update keeps it in sync.
	app, _ := newPreviewTestApp(t, 5)
	_ = app.View()
	if out := app.View(); !strings.Contains(out, "No agent selected") {
		t.Error("View set the preview's agent by itself")
	}
	m, cmd := app.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	app = m.(App)
	if n := len(previewLoads(cmd)); n != 1 {
		t.Fatalf("Update returned %d preview loads for the selection, want 1", n)
	}
	if out := app.View(); strings.Contains(out, "No agent selected") {
		t.Error("Update did not sync the preview with the selection")
	}
}

func TestSortKey_PreviewFollowsSelection(t *testing.T) {
	// Keys handled before the view's own Update (sort, filter, kill) change
	// the selection too; the preview must follow without View's help.
	app, _ := newPreviewTestApp(t, 5)
	m, _ := app.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	app = m.(App)
	before := app.agentsView.Selected().PID
	for i := 0; i < 4 && app.agentsView.Selected().PID == before; i++ {
		m, _ = app.Update(keyMsg("s"))
		app = m.(App)
	}
	sel := app.agentsView.Selected()
	if sel.PID == before {
		t.Skip("sorting never moved the selection with these agents")
	}
	if out := app.View(); !strings.Contains(out, sel.Name) {
		t.Errorf("preview does not show the newly selected agent %q", sel.Name)
	}
}

// BenchmarkAgentsKeyDown is one cursor move between a small and a ~20 MB
// transcript, Update and View, as the UI goroutine sees it.
func BenchmarkAgentsKeyDown(b *testing.B) {
	app, _ := newPreviewTestApp(b, 5000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := tea.KeyMsg{Type: tea.KeyDown}
		if i%2 == 1 {
			key = tea.KeyMsg{Type: tea.KeyUp}
		}
		m, _ := app.Update(key)
		app = m.(App)
		_ = app.View()
	}
}

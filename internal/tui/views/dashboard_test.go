package views

import (
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
)

func TestDashboardView_AutoSize(t *testing.T) {
	d := NewDashboardView()
	d.SetSize(80, 30)
	agents := []agent.Agent{
		{PID: 1, Name: "app1", Status: agent.StatusActive, EstCostUSD: 5.0},
		{PID: 2, Name: "app2", Status: agent.StatusIdle, EstCostUSD: 3.0},
		{PID: 3, Name: "app3", Status: agent.StatusWaitingPermission, EstCostUSD: 10.0},
	}
	d.SetAgents(agents)
	output := d.View()
	if !strings.Contains(output, "app1") {
		t.Error("dashboard should contain app1")
	}
	if !strings.Contains(output, "app2") {
		t.Error("dashboard should contain app2")
	}
	if !strings.Contains(output, "app3") {
		t.Error("dashboard should contain app3")
	}
}

func TestDashboardView_PrioritySort(t *testing.T) {
	d := NewDashboardView()
	d.SetSize(80, 30)
	agents := []agent.Agent{
		{PID: 1, Name: "idle-agent", Status: agent.StatusIdle},
		{PID: 2, Name: "waiting-agent", Status: agent.StatusWaitingPermission},
		{PID: 3, Name: "active-agent", Status: agent.StatusActive},
		{PID: 4, Name: "error-agent", Status: agent.StatusError},
	}
	d.SetAgents(agents)
	output := d.View()

	waitIdx := strings.Index(output, "waiting-agent")
	errorIdx := strings.Index(output, "error-agent")
	activeIdx := strings.Index(output, "active-agent")
	idleIdx := strings.Index(output, "idle-agent")

	if waitIdx > activeIdx {
		t.Error("waiting-agent should appear before active-agent")
	}
	if errorIdx > activeIdx {
		t.Error("error-agent should appear before active-agent")
	}
	if activeIdx > idleIdx {
		t.Error("active-agent should appear before idle-agent")
	}
}

func TestDashboardView_SelectedHighlight(t *testing.T) {
	d := NewDashboardView()
	d.SetSize(80, 20)
	agents := []agent.Agent{
		{PID: 1, Name: "app1", Status: agent.StatusActive},
		{PID: 2, Name: "app2", Status: agent.StatusIdle},
	}
	d.SetAgents(agents)
	d.SetSelected(2)
	output := d.View()
	if output == "" {
		t.Error("dashboard should render non-empty output")
	}
}

func TestDashboardView_Overflow(t *testing.T) {
	d := NewDashboardView()
	d.SetSize(80, 12)
	agents := make([]agent.Agent, 8)
	for i := range agents {
		agents[i] = agent.Agent{PID: i + 1, Name: "agent", Status: agent.StatusActive}
	}
	d.SetAgents(agents)
	output := d.View()
	if !strings.Contains(output, "more") {
		t.Error("dashboard should show overflow indicator when agents exceed space")
	}
}

func TestDashboardView_Empty(t *testing.T) {
	d := NewDashboardView()
	d.SetSize(80, 20)
	d.SetAgents(nil)
	output := d.View()
	if output == "" {
		t.Error("empty dashboard should render placeholder, not empty string")
	}
}

func TestDashboardView_TmuxContent(t *testing.T) {
	d := NewDashboardView()
	d.SetSize(80, 20)
	agents := []agent.Agent{
		{PID: 1, Name: "app1", Status: agent.StatusActive, TMuxSession: "tmux-app1"},
	}
	d.SetAgents(agents)
	d.SetTmuxCaptures(map[string]string{
		"tmux-app1": "$ go test ./...\nPASS\nok  github.com/test  1.2s",
	})
	output := d.View()
	if !strings.Contains(output, "PASS") {
		t.Error("dashboard should show tmux capture content")
	}
}

func TestDashboardView_TraceContent(t *testing.T) {
	d := NewDashboardView()
	d.SetSize(80, 20)
	agents := []agent.Agent{
		{PID: 1, Name: "app1", Status: agent.StatusActive},
	}
	d.SetAgents(agents)
	d.SetTraceSnippets(map[int]string{
		1: "TOOL ✓ Read config.go\nTOOL ✓ Edit config.go:45",
	})
	output := d.View()
	if !strings.Contains(output, "Read config.go") {
		t.Error("dashboard should show trace snippet content")
	}
}

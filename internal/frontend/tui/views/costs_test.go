package views

import (
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
)

func TestCostsView_Empty(t *testing.T) {
	v := NewCostsView()
	if !strings.Contains(v.View(), "No cost data available.") {
		t.Errorf("empty view = %q", v.View())
	}
}

// Pins the TUI cost table: rows grouped by project, most expensive first,
// an empty provider shown as "unknown", no project as "(unknown)", totals.
func TestCostsView_GroupsSortsAndLabelsUnknown(t *testing.T) {
	v := NewCostsView()
	v.SetAgents([]agent.Agent{
		{WorkingDir: "/p/cheap", ProviderName: "codex", Model: "o4-mini", TokensIn: 1000, EstCostUSD: 0.10},
		{WorkingDir: "/p/dear", ProviderName: "claude", Model: "claude-opus-4-6", TokensIn: 2000, EstCostUSD: 1.00},
		{WorkingDir: "/p/dear", ProviderName: "claude", Model: "claude-opus-4-6", TokensIn: 3000, EstCostUSD: 0.50},
		{EstCostUSD: 0.20},
	})
	out := v.View()
	lines := strings.Split(out, "\n")
	var rows []string
	for _, l := range lines[1:] {
		if strings.HasPrefix(l, "─") || strings.Contains(l, "─") {
			break
		}
		rows = append(rows, l)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %q", rows)
	}
	if !strings.HasPrefix(rows[0], "dear") || !strings.Contains(rows[0], "$1.50") || !strings.Contains(rows[0], "5.0K") {
		t.Errorf("row 0 = %q", rows[0])
	}
	if !strings.HasPrefix(rows[1], "(unknown)") || !strings.Contains(rows[1], " unknown ") {
		t.Errorf("row 1 = %q", rows[1])
	}
	if !strings.HasPrefix(rows[2], "cheap") {
		t.Errorf("row 2 = %q", rows[2])
	}
	if !strings.Contains(out, "$1.80") {
		t.Errorf("total missing in %q", out)
	}
}

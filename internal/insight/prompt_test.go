package insight

import (
	"strings"
	"testing"
)

func TestDashboardPrompt_CustomPromptWins(t *testing.T) {
	if got := DashboardPrompt("my prompt", []byte(`{"a":1}`)); got != "my prompt" {
		t.Errorf("got %q", got)
	}
}

// Pins the text the web dashboard sent before the prompt moved here.
func TestDashboardPrompt_DefaultWrapsData(t *testing.T) {
	want := "You are analyzing a skill analytics dashboard for an AI coding agent system. " +
		"The data below shows skill invocations, correction rates, learning funnel metrics, and failure patterns. " +
		"For each section with data, provide 1-2 sentences of actionable insight. " +
		"Focus on: which skills need attention, what patterns indicate, and specific next steps. " +
		"Format as a JSON object where keys are section names and values are insight strings.\n\n" +
		"Dashboard data:\n" + `{"a":1}`
	if got := DashboardPrompt("", []byte(`{"a":1}`)); got != want {
		t.Errorf("got %q", got)
	}
}

func TestDashboardPrompt_NoData(t *testing.T) {
	if got := DashboardPrompt("", nil); !strings.HasSuffix(got, "Dashboard data:\n") {
		t.Errorf("got %q", got)
	}
}

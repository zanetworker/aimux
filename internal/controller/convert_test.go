package controller

import (
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/trace"
)

func TestTurnsToInputs_RoundTrip(t *testing.T) {
	ts := time.Date(2026, 3, 8, 10, 0, 0, 0, time.UTC)
	original := []trace.Turn{
		{
			Number:      1,
			Timestamp:   ts,
			EndTime:     ts.Add(5 * time.Second),
			UserLines:   []string{"fix the bug"},
			OutputLines: []string{"I'll fix that.", "Done."},
			TokensIn:    1000,
			TokensOut:   500,
			CostUSD:     0.05,
			Model:       "claude-sonnet-4-5",
			Actions: []trace.ToolSpan{
				{Name: "Read", Snippet: "main.go", Success: true},
				{Name: "Edit", Snippet: "main.go", Success: false, ErrorMsg: "conflict"},
			},
		},
		{
			Number:    2,
			Timestamp: ts.Add(10 * time.Second),
			UserLines: []string{"thanks"},
			TokensIn:  200,
			TokensOut: 50,
			CostUSD:   0.01,
			Model:     "claude-sonnet-4-5",
		},
	}

	// Convert to inputs
	inputs := TurnsToInputs(original)
	if len(inputs) != 2 {
		t.Fatalf("expected 2 inputs, got %d", len(inputs))
	}

	// Verify first input
	in := inputs[0]
	if in.Number != 1 {
		t.Errorf("Number = %d, want 1", in.Number)
	}
	if in.UserText != "fix the bug" {
		t.Errorf("UserText = %q, want 'fix the bug'", in.UserText)
	}
	if !strings.Contains(in.OutputText, "Done.") {
		t.Errorf("OutputText should contain 'Done.', got %q", in.OutputText)
	}
	if in.TokensIn != 1000 {
		t.Errorf("TokensIn = %d, want 1000", in.TokensIn)
	}
	if in.Model != "claude-sonnet-4-5" {
		t.Errorf("Model = %q, want claude-sonnet-4-5", in.Model)
	}
	if in.DurationMs != 5000 {
		t.Errorf("DurationMs = %d, want 5000", in.DurationMs)
	}
	if len(in.Actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(in.Actions))
	}
	if in.Actions[1].Error != "conflict" {
		t.Errorf("Actions[1].Error = %q, want conflict", in.Actions[1].Error)
	}

	// Convert back to trace.Turn
	roundTripped := inputsToTraceTurns(inputs)
	if len(roundTripped) != 2 {
		t.Fatalf("expected 2 round-tripped turns, got %d", len(roundTripped))
	}

	rt := roundTripped[0]
	if rt.Number != 1 {
		t.Errorf("round-trip Number = %d, want 1", rt.Number)
	}
	if len(rt.UserLines) != 1 || rt.UserLines[0] != "fix the bug" {
		t.Errorf("round-trip UserLines = %v, want [fix the bug]", rt.UserLines)
	}
	if rt.TokensIn != 1000 {
		t.Errorf("round-trip TokensIn = %d, want 1000", rt.TokensIn)
	}
	if len(rt.Actions) != 2 {
		t.Fatalf("round-trip expected 2 actions, got %d", len(rt.Actions))
	}
	if rt.Actions[0].Name != "Read" {
		t.Errorf("round-trip Actions[0].Name = %q, want Read", rt.Actions[0].Name)
	}
}

func TestTurnsToInputs_Empty(t *testing.T) {
	inputs := TurnsToInputs(nil)
	if len(inputs) != 0 {
		t.Errorf("expected 0 inputs for nil, got %d", len(inputs))
	}

	turns := inputsToTraceTurns(nil)
	if len(turns) != 0 {
		t.Errorf("expected 0 turns for nil, got %d", len(turns))
	}
}

func TestTurnsToInputs_NoOutput(t *testing.T) {
	ts := time.Date(2026, 3, 8, 10, 0, 0, 0, time.UTC)
	turns := []trace.Turn{
		{
			Number:    1,
			Timestamp: ts,
			UserLines: []string{"hello"},
			TokensIn:  100,
		},
	}

	inputs := TurnsToInputs(turns)
	if inputs[0].OutputText != "" {
		t.Errorf("expected empty OutputText, got %q", inputs[0].OutputText)
	}
	if inputs[0].DurationMs != 0 {
		t.Errorf("expected 0 DurationMs, got %d", inputs[0].DurationMs)
	}
}

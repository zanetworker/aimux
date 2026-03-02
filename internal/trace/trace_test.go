package trace

import (
	"testing"
	"time"
)

func TestTurnDuration_Valid(t *testing.T) {
	turn := Turn{
		Timestamp: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 1, 1, 10, 0, 30, 0, time.UTC),
	}
	got := turn.Duration()
	want := 30 * time.Second
	if got != want {
		t.Errorf("Duration() = %v, want %v", got, want)
	}
}

func TestTurnDuration_ZeroTimestamp(t *testing.T) {
	turn := Turn{
		EndTime: time.Date(2026, 1, 1, 10, 0, 30, 0, time.UTC),
	}
	if got := turn.Duration(); got != 0 {
		t.Errorf("Duration() with zero Timestamp = %v, want 0", got)
	}
}

func TestTurnDuration_ZeroEndTime(t *testing.T) {
	turn := Turn{
		Timestamp: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
	}
	if got := turn.Duration(); got != 0 {
		t.Errorf("Duration() with zero EndTime = %v, want 0", got)
	}
}

func TestTurnDuration_BothZero(t *testing.T) {
	turn := Turn{}
	if got := turn.Duration(); got != 0 {
		t.Errorf("Duration() with both zero = %v, want 0", got)
	}
}

func TestTurnDuration_NegativeDuration(t *testing.T) {
	turn := Turn{
		Timestamp: time.Date(2026, 1, 1, 10, 0, 30, 0, time.UTC),
		EndTime:   time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
	}
	if got := turn.Duration(); got != 0 {
		t.Errorf("Duration() with negative result = %v, want 0", got)
	}
}

func TestTurnErrorCount_Mixed(t *testing.T) {
	turn := Turn{
		Actions: []ToolSpan{
			{Name: "Read", Success: true},
			{Name: "Bash", Success: false, ErrorMsg: "command failed"},
			{Name: "Edit", Success: true},
			{Name: "Bash", Success: false, ErrorMsg: "syntax error"},
		},
	}
	if got := turn.ErrorCount(); got != 2 {
		t.Errorf("ErrorCount() = %d, want 2", got)
	}
}

func TestTurnErrorCount_AllSuccess(t *testing.T) {
	turn := Turn{
		Actions: []ToolSpan{
			{Name: "Read", Success: true},
			{Name: "Edit", Success: true},
		},
	}
	if got := turn.ErrorCount(); got != 0 {
		t.Errorf("ErrorCount() = %d, want 0", got)
	}
}

func TestTurnErrorCount_NoActions(t *testing.T) {
	turn := Turn{}
	if got := turn.ErrorCount(); got != 0 {
		t.Errorf("ErrorCount() with no actions = %d, want 0", got)
	}
}

func TestTurnErrorCount_AllFailed(t *testing.T) {
	turn := Turn{
		Actions: []ToolSpan{
			{Name: "Bash", Success: false, ErrorMsg: "error 1"},
			{Name: "Edit", Success: false, ErrorMsg: "error 2"},
		},
	}
	if got := turn.ErrorCount(); got != 2 {
		t.Errorf("ErrorCount() = %d, want 2", got)
	}
}

func TestEstimateTurnCost_KnownModels(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		tokIn   int64
		tokOut  int64
		wantMin float64
		wantMax float64
	}{
		{
			name:    "opus model",
			model:   "claude-opus-4-6",
			tokIn:   1000,
			tokOut:  500,
			wantMin: 0.05,
			wantMax: 0.06,
		},
		{
			name:    "sonnet model",
			model:   "claude-sonnet-4-5",
			tokIn:   1000,
			tokOut:  500,
			wantMin: 0.01,
			wantMax: 0.012,
		},
		{
			name:    "haiku model",
			model:   "claude-haiku-3-5",
			tokIn:   1000,
			tokOut:  500,
			wantMin: 0.002,
			wantMax: 0.004,
		},
		{
			name:    "zero tokens",
			model:   "claude-opus-4-6",
			tokIn:   0,
			tokOut:  0,
			wantMin: 0,
			wantMax: 0.0001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateTurnCost(tt.model, tt.tokIn, tt.tokOut)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("EstimateTurnCost(%q, %d, %d) = %f, want between %f and %f",
					tt.model, tt.tokIn, tt.tokOut, got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestEstimateTurnCost_UnknownModelReturnsZero(t *testing.T) {
	got := EstimateTurnCost("unknown-model", 1000, 500)
	if got != 0 {
		t.Errorf("EstimateTurnCost for unknown model = %f, want 0 (unknown model returns 0 from cost.Calculate)", got)
	}
}

func TestEstimateTurnCostLegacy_DefaultsToSonnetRates(t *testing.T) {
	got := EstimateTurnCostLegacy("unknown-model", 1000, 500)
	// Default rates: inRate=3.0, outRate=15.0
	// (1000*3.0 + 500*15.0) / 1M = 0.0105
	if got < 0.01 || got > 0.012 {
		t.Errorf("EstimateTurnCostLegacy for unknown model = %f, want ~0.0105", got)
	}
}

func TestToolSpanFields(t *testing.T) {
	span := ToolSpan{
		Name:      "Edit",
		Snippet:   "/path/to/file.go",
		Success:   true,
		ErrorMsg:  "",
		OldString: "old code",
		NewString: "new code",
		ToolUseID: "tu-123",
	}

	if span.Name != "Edit" {
		t.Errorf("Name = %q, want %q", span.Name, "Edit")
	}
	if span.Snippet != "/path/to/file.go" {
		t.Errorf("Snippet = %q, want %q", span.Snippet, "/path/to/file.go")
	}
	if !span.Success {
		t.Error("Success = false, want true")
	}
	if span.OldString != "old code" {
		t.Errorf("OldString = %q, want %q", span.OldString, "old code")
	}
	if span.NewString != "new code" {
		t.Errorf("NewString = %q, want %q", span.NewString, "new code")
	}
	if span.ToolUseID != "tu-123" {
		t.Errorf("ToolUseID = %q, want %q", span.ToolUseID, "tu-123")
	}
}

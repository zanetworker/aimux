package views

import (
	"strings"
	"testing"
	"time"
)

// --- parseJSONLToTurns tests ---

func TestParseJSONLToTurnsClaudeFormat(t *testing.T) {
	claudeData := `{"type":"user","timestamp":"2026-01-01T10:00:00Z","message":{"role":"user","content":"fix the bug"}}
{"type":"assistant","timestamp":"2026-01-01T10:00:05Z","message":{"role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"I'll fix that."},{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"main.go"}}],"usage":{"input_tokens":100,"output_tokens":50}}}`

	turns := parseJSONLToTurns(claudeData)

	if len(turns) != 1 {
		t.Fatalf("parseJSONLToTurns() returned %d turns, want 1", len(turns))
	}

	turn := turns[0]
	if turn.Number != 1 {
		t.Errorf("Number = %d, want 1", turn.Number)
	}
	if len(turn.UserLines) == 0 {
		t.Fatal("UserLines is empty")
	}
	if turn.UserLines[0] != "fix the bug" {
		t.Errorf("UserLines[0] = %q, want %q", turn.UserLines[0], "fix the bug")
	}
	if len(turn.Actions) != 1 {
		t.Fatalf("Actions has %d entries, want 1", len(turn.Actions))
	}
	if turn.Actions[0].Name != "Read" {
		t.Errorf("Actions[0].Name = %q, want %q", turn.Actions[0].Name, "Read")
	}
	if turn.Actions[0].Snippet != "main.go" {
		t.Errorf("Actions[0].Snippet = %q, want %q", turn.Actions[0].Snippet, "main.go")
	}
	if len(turn.OutputLines) == 0 {
		t.Fatal("OutputLines is empty")
	}
	if turn.OutputLines[0] != "I'll fix that." {
		t.Errorf("OutputLines[0] = %q, want %q", turn.OutputLines[0], "I'll fix that.")
	}
	if turn.TokensIn != 100 {
		t.Errorf("TokensIn = %d, want 100", turn.TokensIn)
	}
	if turn.TokensOut != 50 {
		t.Errorf("TokensOut = %d, want 50", turn.TokensOut)
	}
	if turn.Model != "claude-opus-4-6" {
		t.Errorf("Model = %q, want %q", turn.Model, "claude-opus-4-6")
	}
}

func TestParseJSONLToTurnsCodexFormat(t *testing.T) {
	codexData := `{"timestamp":"2026-01-01T10:00:00Z","type":"session_meta","payload":{"id":"abc-123","cwd":"/tmp/test"}}
{"timestamp":"2026-01-01T10:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"fix the bug"}}
{"timestamp":"2026-01-01T10:00:02Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"I'll look at it."}]}}
{"timestamp":"2026-01-01T10:00:03Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"c1","arguments":"{\"cmd\":\"cat main.go\"}"}}
{"timestamp":"2026-01-01T10:00:04Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"file contents here"}}`

	turns := parseJSONLToTurns(codexData)

	if len(turns) != 1 {
		t.Fatalf("parseJSONLToTurns() returned %d turns, want 1", len(turns))
	}

	turn := turns[0]
	if turn.Number != 1 {
		t.Errorf("Number = %d, want 1", turn.Number)
	}
	if len(turn.UserLines) == 0 {
		t.Fatal("UserLines is empty")
	}
	if turn.UserLines[0] != "fix the bug" {
		t.Errorf("UserLines[0] = %q, want %q", turn.UserLines[0], "fix the bug")
	}
	if len(turn.Actions) != 1 {
		t.Fatalf("Actions has %d entries, want 1", len(turn.Actions))
	}
	if turn.Actions[0].Name != "Bash" {
		t.Errorf("Actions[0].Name = %q, want %q (mapped from exec_command)", turn.Actions[0].Name, "Bash")
	}
	if !strings.Contains(turn.Actions[0].Snippet, "cat main.go") {
		t.Errorf("Actions[0].Snippet = %q, should contain %q", turn.Actions[0].Snippet, "cat main.go")
	}
	if len(turn.OutputLines) == 0 {
		t.Fatal("OutputLines is empty")
	}
	if turn.OutputLines[0] != "I'll look at it." {
		t.Errorf("OutputLines[0] = %q, want %q", turn.OutputLines[0], "I'll look at it.")
	}
}

func TestParseJSONLToTurnsAutoDetectClaude(t *testing.T) {
	// The first non-empty line does not contain session_meta/response_item/event_msg,
	// so it should be detected as Claude format.
	claudeData := `{"type":"user","timestamp":"2026-01-01T10:00:00Z","message":{"role":"user","content":"hello"}}`
	turns := parseJSONLToTurns(claudeData)

	if len(turns) != 1 {
		t.Fatalf("expected 1 turn for Claude format, got %d", len(turns))
	}
	if turns[0].UserLines[0] != "hello" {
		t.Errorf("UserLines[0] = %q, want %q", turns[0].UserLines[0], "hello")
	}
}

func TestParseJSONLToTurnsAutoDetectCodex(t *testing.T) {
	// The first non-empty line contains "session_meta", so it should be detected as Codex.
	codexData := `{"timestamp":"2026-01-01T10:00:00Z","type":"session_meta","payload":{"id":"abc"}}
{"timestamp":"2026-01-01T10:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"hello codex"}}`

	turns := parseJSONLToTurns(codexData)

	if len(turns) != 1 {
		t.Fatalf("expected 1 turn for Codex format, got %d", len(turns))
	}
	if turns[0].UserLines[0] != "hello codex" {
		t.Errorf("UserLines[0] = %q, want %q", turns[0].UserLines[0], "hello codex")
	}
}

func TestParseJSONLToTurnsEmptyData(t *testing.T) {
	turns := parseJSONLToTurns("")
	if len(turns) != 0 {
		t.Errorf("expected 0 turns for empty data, got %d", len(turns))
	}
}

func TestParseJSONLToTurnsMultipleTurns(t *testing.T) {
	data := `{"type":"user","timestamp":"2026-01-01T10:00:00Z","message":{"role":"user","content":"first question"}}
{"type":"assistant","timestamp":"2026-01-01T10:00:05Z","message":{"role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"first answer"}],"usage":{"input_tokens":50,"output_tokens":25}}}
{"type":"user","timestamp":"2026-01-01T10:01:00Z","message":{"role":"user","content":"second question"}}
{"type":"assistant","timestamp":"2026-01-01T10:01:05Z","message":{"role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"second answer"}],"usage":{"input_tokens":80,"output_tokens":40}}}`

	turns := parseJSONLToTurns(data)

	if len(turns) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(turns))
	}
	if turns[0].UserLines[0] != "first question" {
		t.Errorf("turn 1 UserLines[0] = %q, want %q", turns[0].UserLines[0], "first question")
	}
	if turns[1].UserLines[0] != "second question" {
		t.Errorf("turn 2 UserLines[0] = %q, want %q", turns[1].UserLines[0], "second question")
	}
}

// --- TraceTurn.Duration() tests ---

func TestTraceTurnDurationValid(t *testing.T) {
	turn := TraceTurn{
		Timestamp: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 1, 1, 10, 0, 30, 0, time.UTC),
	}

	got := turn.Duration()
	want := 30 * time.Second
	if got != want {
		t.Errorf("Duration() = %v, want %v", got, want)
	}
}

func TestTraceTurnDurationZeroTimestamp(t *testing.T) {
	turn := TraceTurn{
		EndTime: time.Date(2026, 1, 1, 10, 0, 30, 0, time.UTC),
	}

	got := turn.Duration()
	if got != 0 {
		t.Errorf("Duration() with zero Timestamp = %v, want 0", got)
	}
}

func TestTraceTurnDurationZeroEndTime(t *testing.T) {
	turn := TraceTurn{
		Timestamp: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
	}

	got := turn.Duration()
	if got != 0 {
		t.Errorf("Duration() with zero EndTime = %v, want 0", got)
	}
}

func TestTraceTurnDurationBothZero(t *testing.T) {
	turn := TraceTurn{}
	got := turn.Duration()
	if got != 0 {
		t.Errorf("Duration() with both zero = %v, want 0", got)
	}
}

// --- TraceTurn.ErrorCount() tests ---

func TestTraceTurnErrorCountMixed(t *testing.T) {
	turn := TraceTurn{
		Actions: []ToolSpan{
			{Name: "Read", Success: true},
			{Name: "Bash", Success: false, ErrorMsg: "command failed"},
			{Name: "Edit", Success: true},
			{Name: "Bash", Success: false, ErrorMsg: "syntax error"},
		},
	}

	got := turn.ErrorCount()
	if got != 2 {
		t.Errorf("ErrorCount() = %d, want 2", got)
	}
}

func TestTraceTurnErrorCountAllSuccess(t *testing.T) {
	turn := TraceTurn{
		Actions: []ToolSpan{
			{Name: "Read", Success: true},
			{Name: "Edit", Success: true},
		},
	}

	got := turn.ErrorCount()
	if got != 0 {
		t.Errorf("ErrorCount() = %d, want 0", got)
	}
}

func TestTraceTurnErrorCountNoActions(t *testing.T) {
	turn := TraceTurn{}
	got := turn.ErrorCount()
	if got != 0 {
		t.Errorf("ErrorCount() with no actions = %d, want 0", got)
	}
}

// --- estimateTurnCost tests ---

func TestEstimateTurnCost(t *testing.T) {
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
			wantMin: 0.05,  // (1000*15 + 500*75) / 1M = 0.0525
			wantMax: 0.06,
		},
		{
			name:    "sonnet model",
			model:   "claude-sonnet-4-5",
			tokIn:   1000,
			tokOut:  500,
			wantMin: 0.01,  // (1000*3 + 500*15) / 1M = 0.0105
			wantMax: 0.012,
		},
		{
			name:    "haiku model",
			model:   "claude-haiku-3-5",
			tokIn:   1000,
			tokOut:  500,
			wantMin: 0.0008, // (1000*0.25 + 500*1.25) / 1M = 0.000875
			wantMax: 0.001,
		},
		{
			name:    "unknown model defaults to sonnet rates",
			model:   "gpt-4o",
			tokIn:   1000,
			tokOut:  500,
			wantMin: 0.01,
			wantMax: 0.012,
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
			got := estimateTurnCost(tt.model, tt.tokIn, tt.tokOut)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("estimateTurnCost(%q, %d, %d) = %f, want between %f and %f",
					tt.model, tt.tokIn, tt.tokOut, got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

// --- formatTokenCount tests ---

func TestFormatTokenCount(t *testing.T) {
	tests := []struct {
		name string
		n    int64
		want string
	}{
		{"zero", 0, "0"},
		{"small", 500, "500"},
		{"one thousand", 1000, "1.0k"},
		{"fifteen hundred", 1500, "1.5k"},
		{"large k", 99500, "99.5k"},
		{"one million", 1000000, "1.0M"},
		{"one and a half million", 1500000, "1.5M"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTokenCount(tt.n)
			if got != tt.want {
				t.Errorf("formatTokenCount(%d) = %q, want %q", tt.n, got, tt.want)
			}
		})
	}
}

// --- codexToolName tests ---

func TestCodexToolName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"exec_command", "Bash"},
		{"shell", "Bash"},
		{"read_file", "Read"},
		{"write_file", "Write"},
		{"apply_patch", "Edit"},
		{"edit_file", "Edit"},
		{"search_files", "Grep"},
		{"grep", "Grep"},
		{"list_directory", "Ls"},
		{"ls", "Ls"},
		{"unknown_short", "unknown_shor"},  // truncated to 12
		{"tiny", "tiny"},                    // short enough, no truncation
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := codexToolName(tt.input)
			if got != tt.want {
				t.Errorf("codexToolName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// --- shortToolName tests ---

func TestShortToolName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Read", "Rd"},
		{"Write", "Wr"},
		{"Edit", "Ed"},
		{"Bash", "Sh"},
		{"Grep", "Gr"},
		{"Glob", "Gl"},
		{"Task", "Tk"},
		{"WebSearch", "Ws"},
		{"WebFetch", "Wf"},
		{"SomeOtherTool", "Som"}, // truncated to 3
		{"AB", "AB"},             // short enough, returned as-is
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := shortToolName(tt.input)
			if got != tt.want {
				t.Errorf("shortToolName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// --- turnMatchesFilter tests ---

func TestTurnMatchesFilter(t *testing.T) {
	turn := TraceTurn{
		UserLines:   []string{"Fix the login bug"},
		Actions:     []ToolSpan{{Name: "Read", Snippet: "auth.go"}},
		OutputLines: []string{"I fixed the authentication issue."},
	}

	tests := []struct {
		name   string
		needle string
		want   bool
	}{
		{"matches user input", "login", true},
		{"matches action name", "read", true},
		{"matches action snippet", "auth.go", true},
		{"matches output", "authentication", true},
		{"case insensitive", "LOGIN", true},
		{"no match", "kubernetes", false},
		{"empty needle matches everything", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := turnMatchesFilter(turn, strings.ToLower(tt.needle))
			if got != tt.want {
				t.Errorf("turnMatchesFilter(turn, %q) = %v, want %v", tt.needle, got, tt.want)
			}
		})
	}
}

// --- padRight tests ---

func TestPadRightPlainString(t *testing.T) {
	got := padRight("abc", 8)
	// lipgloss.Width("abc") = 3, so we expect 5 trailing spaces
	if len(got) != 8 {
		t.Errorf("padRight(%q, 8) length = %d, want 8", "abc", len(got))
	}
	if !strings.HasPrefix(got, "abc") {
		t.Errorf("padRight(%q, 8) = %q, should start with %q", "abc", got, "abc")
	}
}

func TestPadRightAlreadyWide(t *testing.T) {
	got := padRight("longstring", 5)
	if got != "longstring" {
		t.Errorf("padRight(%q, 5) = %q, want unchanged", "longstring", got)
	}
}

func TestPadRightExactWidth(t *testing.T) {
	got := padRight("abc", 3)
	if got != "abc" {
		t.Errorf("padRight(%q, 3) = %q, want unchanged", "abc", got)
	}
}

func TestPadRightEmptyString(t *testing.T) {
	got := padRight("", 4)
	if len(got) != 4 {
		t.Errorf("padRight(%q, 4) length = %d, want 4", "", len(got))
	}
}

// --- truncate tests ---

func TestTruncate(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"short string", "abc", 10, "abc"},
		{"exact length", "abc", 3, "abc"},
		{"needs truncation", "abcdefghij", 7, "abcd..."},
		{"very short max", "abcdef", 2, "ab"},
		{"max equals 3", "abcdef", 3, "abc"},
		{"empty string", "", 5, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

// --- HeaderView.Height() test ---

func TestHeaderViewHeight(t *testing.T) {
	h := NewHeaderView()
	got := h.Height()
	if got != 7 {
		t.Errorf("HeaderView.Height() = %d, want 7", got)
	}
}

// --- Claude format with tool_result (error matching) ---

func TestParseClaudeJSONLToolResultError(t *testing.T) {
	data := `{"type":"user","timestamp":"2026-01-01T10:00:00Z","message":{"role":"user","content":"run the build"}}
{"type":"assistant","timestamp":"2026-01-01T10:00:01Z","message":{"role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"go build ./..."}}],"usage":{"input_tokens":50,"output_tokens":20}}}
{"type":"user","timestamp":"2026-01-01T10:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","is_error":true,"content":"compilation failed: undefined variable"}]}}`

	turns := parseJSONLToTurns(data)

	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}
	if len(turns[0].Actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(turns[0].Actions))
	}

	action := turns[0].Actions[0]
	if action.Success {
		t.Error("expected action.Success = false for errored tool result")
	}
	if !strings.Contains(action.ErrorMsg, "compilation failed") {
		t.Errorf("ErrorMsg = %q, should contain %q", action.ErrorMsg, "compilation failed")
	}
}

// --- Codex format function_call_output with error ---

func TestParseCodexJSONLFunctionCallError(t *testing.T) {
	data := `{"timestamp":"2026-01-01T10:00:00Z","type":"session_meta","payload":{"id":"test"}}
{"timestamp":"2026-01-01T10:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"run tests"}}
{"timestamp":"2026-01-01T10:00:02Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"c1","arguments":"{\"cmd\":\"go test\"}"}}
{"timestamp":"2026-01-01T10:00:03Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"Process exited with code 1\nerror in test"}}`

	turns := parseJSONLToTurns(data)

	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}
	if len(turns[0].Actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(turns[0].Actions))
	}

	action := turns[0].Actions[0]
	if action.Success {
		t.Error("expected action.Success = false for error output")
	}
	if action.ErrorMsg == "" {
		t.Error("expected non-empty ErrorMsg for error output")
	}
}

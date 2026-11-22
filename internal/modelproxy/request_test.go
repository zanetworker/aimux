package modelproxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMapRequest(t *testing.T) {
	tests := []struct {
		name, body, model, effort, system, items, input, wantErr string
	}{
		{
			name: "history and mixed final message",
			body: `{"model":"claude-haiku","system":[{"type":"text","text":"one"},{"type":"text","text":"two"}],
				"tools":[{"name":"Read","description":"Read it","input_schema":{"type":"object"}}],
				"messages":[{"role":"user","content":"read it"},
				{"role":"assistant","content":[{"type":"thinking","thinking":"private"},
				{"type":"text","text":"On it"},{"type":"tool_use","id":"tool-1","name":"Read","input":{"line":9}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","is_error":true,
				"content":[{"type":"text","text":"missing"},{"type":"text","text":"try again"}]},
				{"type":"text","text":"read another"},{"type":"text","text":"please"}]}]}`,
			model: "gpt-6.2-sol", effort: "low", system: "one\ntwo",
			items: `[{"type":"message","role":"user","content":[{"type":"input_text","text":"read it"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"On it"}]},{"type":"function_call","name":"Read","call_id":"tool-1","arguments":"{\"line\":9}"},{"type":"function_call_output","call_id":"tool-1","output":"Error: missing\ntry again"}]`,
			input: `[{"type":"text","text":"read another"},{"type":"text","text":"please"}]`,
		},
		{
			name:  "tool result only",
			body:  `{"model":"codex:gpt-6.2-astra","system":"base","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":"fruit"}]}]}`,
			model: "gpt-6.2-astra", system: "base",
			items: `[{"type":"function_call_output","call_id":"x","output":"fruit"}]`, input: `[]`,
		},
		{
			name: "system messages become developer history, also after the final user message",
			body: `{"model":"codex:gpt-6.2-sol","messages":[{"role":"user","content":"first"},
				{"role":"system","content":"reminder one"},
				{"role":"assistant","content":"ok"},
				{"role":"user","content":"second"},
				{"role":"system","content":[{"type":"text","text":"reminder two"}]}]}`,
			model: "gpt-6.2-sol",
			// A reminder after the final user message must reach Codex after it, so the final user text is
			// injected too and the turn starts with empty input.
			items: `[{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]},{"type":"message","role":"developer","content":[{"type":"input_text","text":"reminder one"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"second"}]},{"type":"message","role":"developer","content":[{"type":"input_text","text":"reminder two"}]}]`,
			input: `[]`,
		},
		{
			name:  "system message before the final user message keeps the user text as turn input",
			body:  `{"messages":[{"role":"system","content":"note"},{"role":"user","content":"go"}]}`,
			model: "gpt-6.2-sol", effort: "low",
			items: `[{"type":"message","role":"developer","content":[{"type":"input_text","text":"note"}]}]`,
			input: `[{"type":"text","text":"go"}]`,
		},
		{name: "only system messages", body: `{"messages":[{"role":"system","content":"x"}]}`, wantErr: "messages must contain a user message"},
		{name: "unknown role", body: `{"messages":[{"role":"tool","content":"x"},{"role":"user","content":"y"}]}`, wantErr: `unsupported message role "tool"`},
		{name: "image", body: `{"messages":[{"role":"user","content":[{"type":"image"}]}]}`, wantErr: "images are not supported by the codex backend"},
		{name: "nested image", body: `{"messages":[{"role":"user","content":[{"type":"tool_result","content":[{"type":"image"}]}]}]}`, wantErr: "images are not supported by the codex backend"},
		{name: "empty messages", body: `{"messages":[]}`, wantErr: "messages must contain a user message"},
		{name: "empty model prefix", body: `{"model":"codex:","messages":[{"role":"user","content":"test"}]}`, wantErr: "codex model ID is empty"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var req messageRequest
			if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
				t.Fatal(err)
			}
			turn, err := mapRequest(req)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if turn.Model != tc.model || turn.Effort != tc.effort || turn.System != tc.system {
				t.Fatalf("unexpected model, effort or system: %+v", turn)
			}
			items, err := json.Marshal(turn.History)
			if err != nil || string(items) != tc.items {
				t.Fatalf("history = %s, %v; want %s", items, err, tc.items)
			}
			input, err := json.Marshal(turn.Input)
			if err != nil || string(input) != tc.input {
				t.Fatalf("input = %s, %v; want %s", input, err, tc.input)
			}
			if len(req.Tools) != 0 {
				tools, err := json.Marshal(turn.Tools)
				if err != nil || string(tools) != `[{"type":"function","name":"Read","description":"Read it","inputSchema":{"type":"object"}}]` {
					t.Fatalf("dynamic tools = %s, %v", tools, err)
				}
			}
		})
	}
}

func TestDecodeContent(t *testing.T) {
	for _, tc := range []struct {
		raw, text string
		length    int
		invalid   bool
	}{
		{`"hello"`, "hello", 1, false},
		{`[{"type":"text","text":"hello"}]`, "hello", 1, false},
		{`[]`, "", 0, false},
		{`22`, "", 0, true},
	} {
		blocks, err := decodeContent(json.RawMessage(tc.raw))
		if (err != nil) != tc.invalid || len(blocks) != tc.length {
			t.Fatalf("%s: blocks = %+v, err = %v", tc.raw, blocks, err)
		}
		if len(blocks) != 0 && (blocks[0].Type != "text" || blocks[0].Text != tc.text) {
			t.Fatalf("%s: block = %+v", tc.raw, blocks[0])
		}
	}
}

func TestJoinText(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`"plain"`, "plain"},
		{`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`, "a\nb"},
		{`null`, ""},
	} {
		got, err := joinText(json.RawMessage(tc.raw))
		if err != nil || got != tc.want {
			t.Fatalf("%s: text = %q, err = %v", tc.raw, got, err)
		}
	}
}

func TestMapRequestRenamesReservedToolNames(t *testing.T) {
	var req messageRequest
	body := `{"tools":[{"name":"mcp__granola__get_meeting_transcript","description":"d","input_schema":{"type":"object"}},
		{"name":"Read","description":"r","input_schema":{"type":"object"}}],
		"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"mcp__granola__get_meeting_transcript","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"ok"},{"type":"text","text":"go"}]}]}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	turn, err := mapRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{turn.Tools[0].Name, turn.Tools[1].Name}; got[0] != "mcp_granola__get_meeting_transcript" || got[1] != "Read" {
		t.Fatalf("codex tool names = %v", got)
	}
	if turn.ToolNames["mcp_granola__get_meeting_transcript"] != "mcp__granola__get_meeting_transcript" || turn.ToolNames["Read"] != "Read" {
		t.Fatalf("reverse map = %v", turn.ToolNames)
	}
	history, _ := json.Marshal(turn.History[0])
	if string(history) != `{"type":"function_call","name":"mcp_granola__get_meeting_transcript","call_id":"c1","arguments":"{}"}` {
		t.Fatalf("history call = %s", history)
	}
}

func TestMapRequestRejectsRenamedToolCollision(t *testing.T) {
	var req messageRequest
	body := `{"tools":[{"name":"mcp__x__y","input_schema":{}},{"name":"mcp_x__y","input_schema":{}}],"messages":[{"role":"user","content":"hi"}]}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	if _, err := mapRequest(req); err == nil || !strings.Contains(err.Error(), "collide") {
		t.Fatalf("got %v, want a collision error", err)
	}
}

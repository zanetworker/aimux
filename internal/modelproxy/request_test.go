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
			model: "gpt-6.2-astra", effort: "high", system: "base",
			items: `[{"type":"function_call_output","call_id":"x","output":"fruit"}]`, input: `[]`,
		},
		{
			name: "system messages become developer history, also after the final user message",
			body: `{"model":"codex:gpt-6.2-sol","messages":[{"role":"user","content":"first"},
				{"role":"system","content":"reminder one"},
				{"role":"assistant","content":"ok"},
				{"role":"user","content":"second"},
				{"role":"system","content":[{"type":"text","text":"reminder two"}]}]}`,
			model: "gpt-6.2-sol", effort: "high",
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
		{
			name:  "image in the latest user message becomes a turn input",
			body:  `{"messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}`,
			model: "gpt-6.2-sol", effort: "low", items: `null`,
			input: `[{"type":"text","text":"look"},{"type":"image","url":"data:image/png;base64,AAAA"}]`,
		},
		{
			name:  "image in an earlier user message is injected as input_image",
			body:  `{"messages":[{"role":"user","content":[{"type":"text","text":"this one"},{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}]},{"role":"assistant","content":"ok"},{"role":"user","content":"color?"}]}`,
			model: "gpt-6.2-sol", effort: "low",
			items: `[{"type":"message","role":"user","content":[{"type":"input_text","text":"this one"},{"type":"input_image","image_url":"https://example.com/a.png"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]`,
			input: `[{"type":"text","text":"color?"}]`,
		},
		{
			name:  "image in a tool result makes the output a list of parts",
			body:  `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","is_error":true,"content":[{"type":"text","text":"shot"},{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"BBBB"}}]}]}]}`,
			model: "gpt-6.2-sol", effort: "low",
			items: `[{"type":"function_call_output","call_id":"t","output":[{"type":"input_text","text":"Error: shot"},{"type":"input_image","image_url":"data:image/jpeg;base64,BBBB"}]}]`, input: `[]`,
		},
		{
			// An image without a usable source must not fail every later request of the session: it becomes a note.
			name: "image without a usable source becomes a note", body: `{"messages":[{"role":"user","content":[{"type":"image"}]}]}`,
			model: "gpt-6.2-sol", effort: "low", items: `null`,
			input: `[{"type":"text","text":"[image omitted: the Codex backend did not receive this image]"}]`,
		},
		{
			name: "codex model takes Claude Code's effort", body: `{"model":"codex:gpt-6.2-sol","output_config":{"effort":"xhigh"},"messages":[{"role":"user","content":"hi"}]}`,
			model: "gpt-6.2-sol", effort: "xhigh", items: `null`, input: `[{"type":"text","text":"hi"}]`,
		},
		{
			name: "codex model without effort defaults to high", body: `{"model":"codex:gpt-6.2-sol","messages":[{"role":"user","content":"hi"}]}`,
			model: "gpt-6.2-sol", effort: "high", items: `null`, input: `[{"type":"text","text":"hi"}]`,
		},
		{
			name: "unknown effort becomes high", body: `{"model":"codex:gpt-6.2-sol","output_config":{"effort":"turbo"},"messages":[{"role":"user","content":"hi"}]}`,
			model: "gpt-6.2-sol", effort: "high", items: `null`, input: `[{"type":"text","text":"hi"}]`,
		},
		{
			name: "background calls stay low whatever the effort", body: `{"model":"claude-haiku-5-5","output_config":{"effort":"max"},"messages":[{"role":"user","content":"title"}]}`,
			model: "gpt-6.2-sol", effort: "low", items: `null`, input: `[{"type":"text","text":"title"}]`,
		},
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

// A conversation that ran on Anthropic can hold blocks only Anthropic produces: a web search it ran itself, say.
// Claude Code resends the whole history, so refusing one would end the session on Codex for good.
func TestMapRequestKeepsGoingPastBlocksOnlyAnthropicMakes(t *testing.T) {
	request := messageRequest{Model: "codex:gpt-6.2-sol", Messages: []conversationMessage{
		conversation("user", `"what is new?"`),
		conversation("assistant", `[
			{"type":"text","text":"Searching."},
			{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"SECRET-QUERY"}},
			{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"web_search_result","title":"SECRET-TITLE"}]},
			{"type":"text","text":"Here it is."}]`),
		conversation("user", `"and then?"`),
	}}
	turn, err := mapRequest(request)
	if err != nil {
		t.Fatalf("mapRequest: %v", err)
	}
	history, _ := json.Marshal(turn.History)
	for _, want := range []string{"Searching.", "Here it is.", "[server_tool_use omitted: the Codex backend did not receive this block]", "[web_search_tool_result omitted: the Codex backend did not receive this block]"} {
		if !strings.Contains(string(history), want) {
			t.Errorf("history lacks %q: %s", want, history)
		}
	}
	for _, leak := range []string{"SECRET-QUERY", "SECRET-TITLE"} {
		if strings.Contains(string(history), leak) {
			t.Errorf("history holds %q from a block that was left out", leak)
		}
	}
}

func TestMapRequestStillRefusesAnUnknownBlockFromTheUser(t *testing.T) {
	for name, messages := range map[string][]conversationMessage{
		"in the prompt":  {conversation("user", `[{"type":"mystery","data":"x"}]`)},
		"in the history": {conversation("user", `[{"type":"mystery","data":"x"}]`), conversation("assistant", `"ok"`), conversation("user", `"next"`)},
	} {
		if _, err := mapRequest(messageRequest{Messages: messages}); err == nil || !strings.Contains(err.Error(), "unsupported content block") {
			t.Errorf("%s: err = %v; what the user sends is never dropped silently", name, err)
		}
	}
	// A type that is not a plain name is not repeated in the note.
	turn, err := mapRequest(messageRequest{Messages: []conversationMessage{
		conversation("user", `"hi"`), conversation("assistant", `[{"type":"odd type <b>"}]`), conversation("user", `"next"`),
	}})
	history, _ := json.Marshal(turn.History)
	if err != nil || strings.Contains(string(history), "odd type") || !strings.Contains(string(history), "[block omitted:") {
		t.Errorf("err %v, history %s", err, history)
	}
}

// A tool the provider runs itself is declared with a type and no input schema. Codex cannot be given one.
func TestMapRequestLeavesOutToolsOnlyTheProviderRuns(t *testing.T) {
	var request messageRequest
	if err := json.Unmarshal([]byte(`{"model":"codex:gpt-6.2-sol",
		"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":5},
		         {"name":"Bash","description":"run","input_schema":{"type":"object"}},
		         {"name":"nothing","input_schema":null}],
		"messages":[{"role":"user","content":"hi"}]}`), &request); err != nil {
		t.Fatal(err)
	}
	turn, err := mapRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(turn.Tools) != 1 || turn.Tools[0].Name != "Bash" || len(turn.ToolNames) != 1 {
		t.Fatalf("tools = %+v, names = %v; want only the one with an input schema", turn.Tools, turn.ToolNames)
	}
}

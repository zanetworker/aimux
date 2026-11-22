// fakecodex is a subprocess fixture for the app-server's stdio protocol.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type request struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
}

type thread struct {
	system string
	model  string
	tools  []struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	history []json.RawMessage
}

type heldTurn struct {
	pending               int
	text, image, reminder string
}

func main() {
	if slices.Equal(os.Args[1:], []string{"--version"}) {
		version := os.Getenv("AIMUX_FAKE_CODEX_VERSION")
		if version == "" {
			version = "0.170.0"
		}
		fmt.Println("codex-cli " + version)
		return
	}
	if slices.Equal(os.Args[1:], []string{"debug", "models"}) {
		if os.Getenv("AIMUX_FAKE_CODEX_CATALOG_FAIL") != "" {
			fmt.Fprintln(os.Stderr, "fakecodex: catalog refresh failed")
			os.Exit(4)
		}
		// Shaped like Codex 0.170's catalog for GPT-6.2: code mode, multi-agent v2, apply_patch, extra tools.
		fmt.Println(`{"models":[{"slug":"gpt-6.2-sol","tool_mode":"code_mode_only","multi_agent_version":"v2",` +
			`"apply_patch_tool_type":"freeform","experimental_supported_tools":["clock"]},` +
			`{"slug":"gpt-6.2-astra","tool_mode":"code_mode_only","multi_agent_version":"v2","apply_patch_tool_type":"freeform"}]}`)
		return
	}
	if len(os.Args) > 2 && os.Args[1] == "mcp" && os.Args[2] == "list" {
		if os.Getenv("AIMUX_FAKE_CODEX_MCP_FAIL") != "" {
			fmt.Fprintln(os.Stderr, "fakecodex: cannot read config")
			os.Exit(5)
		}
		servers := `{"name":"github","enabled":true},{"name":"odd.name","enabled":true},{"name":"off","enabled":false}`
		if _, err := os.Stat(filepath.Join(".codex", "config.toml")); err == nil {
			// Like real Codex, a project config in the working directory adds its servers to the list.
			servers += `,{"name":"project_only","enabled":true}`
		}
		if !slices.Contains(os.Args, "plugins") {
			// Like real Codex, servers from plugins are listed unless plugins are disabled.
			servers += `,{"name":"codex_app","enabled":true}`
		}
		fmt.Println("[" + servers + "]")
		return
	}
	require(len(os.Args) > 1 && os.Args[1] == "app-server", "expected app-server")
	requireLeanCatalog(os.Args[2:])
	// Like real Codex 0.170: a -c entry for a server the config does not define (a plugin's) makes it reject the
	// whole config ("invalid transport").
	require(!strings.Contains(strings.Join(os.Args[2:], " "), "mcp_servers.codex_app."), "invalid transport in mcp_servers.codex_app")
	// app-server runs in the proxy's empty working directory, where a project's servers are not defined either.
	require(!strings.Contains(strings.Join(os.Args[2:], " "), "mcp_servers.project_only."), "invalid transport in mcp_servers.project_only")
	// Like real Codex, configured MCP servers would start and offer their tools unless each is switched off.
	for _, flag := range []string{"-c mcp_servers.github.enabled=false", `-c mcp_servers."odd.name".enabled=false`} {
		require(strings.Contains(" "+strings.Join(os.Args[2:], " ")+" ", " "+flag+" "), "MCP server left enabled: missing "+flag)
	}
	for _, flag := range []string{"--disable hooks", "--disable shell_tool", "--disable skill_search",
		`-c web_search="disabled"`, "-c tools.update_plan.enabled=false", "-c tools.experimental_request_user_input.enabled=false",
		// Claude Code supplies the real cwd and permissions; Codex's own context names the proxy's empty
		// read-only cwd, which made the model read the wrong path and refuse edits (end-to-end run).
		"-c include_environment_context=false", "-c include_permissions_instructions=false",
		"-c include_apps_instructions=false"} {
		require(strings.Contains(" "+strings.Join(os.Args[2:], " ")+" ", " "+flag+" "), "missing "+flag)
	}

	in := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	log, err := os.OpenFile(os.Getenv("AIMUX_FAKE_CODEX_LOG"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		fail(err.Error())
	}
	defer log.Close()
	logEvent := func(event string) { _, _ = fmt.Fprintln(log, event) }
	var sendMu sync.Mutex
	var interruptedMu sync.Mutex
	interrupted := map[string]bool{}
	send := func(msg map[string]any) {
		sendMu.Lock()
		defer sendMu.Unlock()
		msg["jsonrpc"] = "2.0"
		if err := out.Encode(msg); err != nil {
			fail(err.Error())
		}
	}
	respond := func(id json.RawMessage, result any) { send(map[string]any{"id": id, "result": result}) }
	notify := func(method, tid string, params map[string]any) {
		params["threadId"] = tid
		send(map[string]any{"method": method, "params": params})
	}
	delta := func(tid, text string) {
		notify("item/agentMessage/delta", tid, map[string]any{"turnId": tid + "-turn", "delta": text})
	}
	usage := func(tid string, input int) {
		notify("thread/tokenUsage/updated", tid, map[string]any{"turnId": tid + "-turn",
			"tokenUsage": map[string]any{
				"total": map[string]int{"inputTokens": input, "outputTokens": 3},
				"last":  map[string]int{"inputTokens": input, "outputTokens": 3},
			}})
	}
	completed := func(tid, status string) {
		notify("turn/completed", tid, map[string]any{"turn": map[string]string{"id": tid + "-turn", "status": status}})
	}
	toolCallNamed := func(tid, id, name string) {
		send(map[string]any{"id": id, "method": "item/tool/call", "params": map[string]any{
			"threadId": tid, "turnId": tid + "-turn", "callId": "call-" + tid, "tool": name,
			"arguments": map[string]string{"path": "a.txt"},
		}})
	}
	toolCall := func(tid, id string) { toolCallNamed(tid, id, "Read") }

	threads := make(map[string]*thread)
	held := make(map[string]*heldTurn)
	type pendingCall struct {
		threadID string
		image    bool
	}
	pending := make(map[string]pendingCall)
	sequence := 0
	initialized := false
	var firstConcurrent request
	var firstID string
	for in.Scan() {
		var msg request
		decode(in.Bytes(), &msg)
		switch msg.Method {
		case "initialize":
			var p struct {
				ClientInfo   struct{ Name, Version string }
				Capabilities struct{ ExperimentalAPI bool }
			}
			decode(msg.Params, &p)
			require(p.ClientInfo.Name != "" && p.ClientInfo.Version != "" && p.Capabilities.ExperimentalAPI, "experimental initialize required")
			logEvent(fmt.Sprintf("initialize %d", os.Getpid()))
			respond(msg.ID, map[string]string{"userAgent": "fakecodex"})
		case "initialized":
			initialized = true
		case "thread/start":
			var p struct {
				Cwd, BaseInstructions, Sandbox, ApprovalPolicy string
				Model                                          string
				DynamicTools                                   []struct {
					Type, Name, Description string
					InputSchema             json.RawMessage
				}
				Ephemeral bool
			}
			decode(msg.Params, &p)
			require(initialized && p.Ephemeral && p.Sandbox == "read-only" && p.ApprovalPolicy == "never", "unsafe thread parameters")
			files, err := os.ReadDir(p.Cwd)
			require(err == nil && filepath.IsAbs(p.Cwd) && len(files) == 0, "expected an empty absolute cwd")
			// Codex 0.170 rejects these names (thread_processor.rs: name == "mcp" || starts_with("mcp__")).
			reserved := ""
			for _, tool := range p.DynamicTools {
				if tool.Name == "mcp" || strings.HasPrefix(tool.Name, "mcp__") {
					reserved = tool.Name
				}
			}
			if reserved != "" {
				send(map[string]any{"id": msg.ID, "error": map[string]any{"code": -32600, "message": "dynamic tool name is reserved: " + reserved}})
				continue
			}
			sequence++
			id := fmt.Sprintf("t%d", sequence)
			th := &thread{system: p.BaseInstructions, model: p.Model}
			for _, tool := range p.DynamicTools {
				require(tool.Type == "function" && len(tool.InputSchema) != 0, "invalid tool schema")
				th.tools = append(th.tools, struct {
					Type string `json:"type"`
					Name string `json:"name"`
				}{tool.Type, tool.Name})
			}
			threads[id] = th
			logEvent("thread " + id)
			respond(msg.ID, map[string]any{"thread": map[string]string{"id": id}})
		case "thread/inject_items":
			var p struct {
				ThreadID string
				Items    []json.RawMessage
			}
			decode(msg.Params, &p)
			require(threads[p.ThreadID] != nil && len(p.Items) != 0, "expected non-empty history on a known thread")
			threads[p.ThreadID].history = append(threads[p.ThreadID].history, p.Items...)
			logEvent(fmt.Sprintf("inject %s %d", p.ThreadID, len(p.Items)))
			for _, item := range p.Items {
				logEvent(fmt.Sprintf("inject_item %s %s", p.ThreadID, item))
			}
			respond(msg.ID, struct{}{})
		case "turn/start":
			var p struct {
				ThreadID, Effort string
				Input            []struct{ Type, Text, URL string }
			}
			decode(msg.Params, &p)
			tid := p.ThreadID
			th := threads[tid]
			if th == nil {
				send(map[string]any{"id": msg.ID, "error": map[string]any{"code": -32602, "message": "thread not found"}})
				continue
			}
			require(p.Input != nil, "expected input array")
			logEvent("turn " + tid)
			text, images := "", 0
			if len(p.Input) > 0 {
				// Turn input holds text parts and image parts ({"type":"image","url":...}), in any order.
				for _, part := range p.Input {
					switch part.Type {
					case "text":
						if text == "" {
							text = part.Text
						}
					case "image":
						require(strings.HasPrefix(part.URL, "data:image/") || strings.HasPrefix(part.URL, "https://"), "image input without a data: or https: url")
						images++
					default:
						fail("unexpected turn input type: " + part.Type)
					}
				}
			}
			if text == "crash" {
				os.Exit(3)
			}
			if text == "crash-once" {
				marker := log.Name() + ".crashed"
				if _, err := os.Stat(marker); err != nil {
					require(os.WriteFile(marker, nil, 0o600) == nil, "write crash marker")
					os.Exit(3)
				}
			}
			if text == "cancel-before-ack" {
				awaitRelease(log.Name())
			}
			result := func(id json.RawMessage, tid string) {
				respond(id, map[string]any{"turn": map[string]string{"id": tid + "-turn", "status": "inProgress"}})
			}
			if text == "concurrent one" {
				firstConcurrent, firstID = msg, tid
				continue
			}
			if text == "concurrent two" {
				require(firstID != "", "first turn must be pending")
				delta(firstID, "concurrent ")
				result(msg.ID, tid)
				delta(tid, text)
				result(firstConcurrent.ID, firstID)
				delta(firstID, "one")
				usage(tid, 29)
				usage(firstID, 17)
				completed(tid, "completed")
				completed(firstID, "completed")
				continue
			}
			result(msg.ID, tid)
			switch text {
			case "tool":
				require(th.model == "gpt-6.2-sol" && p.Effort == "low" && strings.HasSuffix(th.system, "\n\nbase instructions") &&
					strings.HasPrefix(th.system, "You are the model behind Claude Code."), "expected default model, low effort, preamble and system")
				require(len(th.tools) == 1 && th.tools[0].Name == "Read" && len(th.history) == 1, "expected dynamic Read and injected history")
				delta(tid, "before ")
				delta(tid, "tool")
				usage(tid, 17)
				toolCall(tid, "tool-request")
				toolCall(tid, "second-tool-request")
			case "session-parallel", "session-parallel-late", "session-tool", "session-tool-with-usage":
				require(len(th.tools) == 1 && th.tools[0].Name == "Read", "expected dynamic Read")
				held[tid] = &heldTurn{pending: 1}
				delta(tid, "before tools")
				if text == "session-tool-with-usage" {
					usage(tid, 17)
				}
				first := fmt.Sprintf("%q", tid+"-request")
				pending[first] = pendingCall{threadID: tid}
				toolCall(tid, tid+"-request")
				if text == "session-parallel" || text == "session-parallel-late" {
					held[tid].pending++
					id := 100 + sequence
					pending[strconv.Itoa(id)] = pendingCall{threadID: tid, image: true}
					second := func() {
						send(map[string]any{"id": id, "method": "item/tool/call", "params": map[string]any{
							"threadId": tid, "turnId": tid + "-turn", "callId": "call-" + tid + "-second",
							"tool": "Read", "arguments": map[string]string{"path": "b.txt"},
						}})
						logEvent("sent " + tid + " second")
					}
					if text == "session-parallel-late" {
						time.AfterFunc(450*time.Millisecond, second)
					} else {
						second()
					}
				}
			case "tool-late-usage":
				// Like real Codex: the tool call can be dispatched before the response's usage is reported, and an
				// interrupted response reports none.
				toolCall(tid, "late-usage-request")
				time.AfterFunc(300*time.Millisecond, func() {
					interruptedMu.Lock()
					done := interrupted[tid]
					interruptedMu.Unlock()
					if !done {
						usage(tid, 23)
					}
				})
			case "partial-cache", "full-cache", "excess-cache":
				input, cached := 23, 21
				switch text {
				case "full-cache":
					input, cached = 17, 17
				case "excess-cache":
					input, cached = 17, 18
				}
				delta(tid, text)
				notify("thread/tokenUsage/updated", tid, map[string]any{"turnId": tid + "-turn",
					"tokenUsage": map[string]any{
						"total": map[string]int{"inputTokens": input + 19, "cachedInputTokens": cached + 7, "outputTokens": 5},
						"last":  map[string]int{"inputTokens": input, "cachedInputTokens": cached, "outputTokens": 3},
					}})
				completed(tid, "completed")
			case "expire-thread":
				delta(tid, "expired")
				usage(tid, 17)
				completed(tid, "completed")
				delete(threads, tid)
			case "crash-once":
				delta(tid, "recovered")
				usage(tid, 17)
				completed(tid, "completed")
			case "call-first-tool":
				require(len(th.tools) > 0, "expected at least one dynamic tool")
				usage(tid, 17)
				toolCallNamed(tid, "first-tool-request", th.tools[0].Name)
			case "wait":
				delta(tid, "waiting")
			case "crash-after-delta":
				delta(tid, "before crash")
				awaitRelease(log.Name())
				os.Exit(3)
			case "cancel-before-ack":
			case "interrupted":
				completed(tid, "interrupted")
			case "":
				if len(p.Input) != 0 {
					if images > 0 {
						delta(tid, fmt.Sprintf("+%d image", images))
					}
					usage(tid, 17)
					completed(tid, "completed")
					continue
				}
				if len(th.history) != 0 {
					var last struct {
						Role    string
						Content []struct{ Text string }
					}
					decode(th.history[len(th.history)-1], &last)
					if last.Role == "developer" && len(last.Content) != 0 {
						logEvent("empty_input " + tid)
						delta(tid, last.Content[0].Text)
						usage(tid, 17)
						completed(tid, "completed")
						continue
					}
				}
				require(th.model == "gpt-6.2-astra" && p.Effort == "high" && len(th.history) == 3, "expected explicit model, high effort and three history items")
				require(string(th.history[1]) == `{"type":"function_call","name":"Read","call_id":"call-t1","arguments":"{\"path\":\"a.txt\"}"}`, "expected injected Read call")
				require(string(th.history[2]) == `{"type":"function_call_output","call_id":"call-t1","output":"PAPAYA"}`, "expected injected result")
				delta(tid, "PAPAYA")
				usage(tid, 17)
				completed(tid, "completed")
			default:
				if images > 0 {
					text = fmt.Sprintf("%s+%d image", text, images)
				}
				delta(tid, text)
				usage(tid, 17)
				completed(tid, "completed")
			}
		case "turn/steer":
			var p struct {
				ThreadID, ExpectedTurnID string
				Input                    []struct{ Type, Text string }
			}
			decode(msg.Params, &p)
			turn := held[p.ThreadID]
			require(turn != nil && p.ExpectedTurnID == p.ThreadID+"-turn", "steered the wrong turn")
			for _, part := range p.Input {
				require(part.Type == "text", "expected text steering input")
				turn.reminder += part.Text
			}
			logEvent("steer " + p.ThreadID)
			respond(msg.ID, map[string]string{"turnId": p.ExpectedTurnID})
		case "":
			call, ok := pending[string(msg.ID)]
			require(ok, "answered an unknown tool request")
			turn := held[call.threadID]
			require(turn != nil, "answered an inactive turn")
			var result struct {
				ContentItems []struct{ Type, Text, ImageURL string }
				Success      *bool
			}
			decode(msg.Result, &result)
			require(result.Success != nil && result.ContentItems != nil, "expected dynamic tool response")
			var content strings.Builder
			hasImage := false
			for _, part := range result.ContentItems {
				switch part.Type {
				case "inputImage":
					require(strings.HasPrefix(part.ImageURL, "data:image/png;base64,"), "expected a PNG")
					hasImage = true
					content.WriteString("<image>")
				case "inputText":
					content.WriteString(part.Text)
				default:
					fail("unexpected dynamic tool content type: " + part.Type)
				}
			}
			value := content.String()
			if !*result.Success {
				value = "failed(" + value + ")"
			}
			if call.image {
				require(!*result.Success && hasImage, "expected an image error")
				turn.image = value
			} else {
				turn.text = value
			}
			delete(pending, string(msg.ID))
			turn.pending--
			logEvent("answer " + call.threadID)
			if turn.pending == 0 {
				// Codex reports the held model request's usage only after the client answers.
				notify("thread/tokenUsage/updated", call.threadID, map[string]any{"turnId": call.threadID + "-turn",
					"tokenUsage": map[string]any{
						"total": map[string]int{"inputTokens": 19, "cachedInputTokens": 7, "outputTokens": 2},
						"last":  map[string]int{"inputTokens": 19, "cachedInputTokens": 7, "outputTokens": 2},
					}})
				delta(call.threadID, turn.text+turn.image+"|"+turn.reminder)
				notify("thread/tokenUsage/updated", call.threadID, map[string]any{"turnId": call.threadID + "-turn",
					"tokenUsage": map[string]any{
						"total": map[string]int{"inputTokens": 42, "cachedInputTokens": 28, "outputTokens": 6},
						"last":  map[string]int{"inputTokens": 23, "cachedInputTokens": 21, "outputTokens": 4},
					}})
				completed(call.threadID, "completed")
				delete(held, call.threadID)
			}
		case "account/rateLimits/read":
			respond(msg.ID, json.RawMessage(`{"ordinaryUsageAllowed":true,"rateLimits":{"limitId":"codex",`+
				`"primary":{"usedPercent":19,"windowDurationMins":10080,"resetsAt":1795606397},"secondary":null,`+
				`"planType":"prolite"},"rateLimitResetCredits":{"availableCount":3,"credits":[]}}`))
		case "turn/interrupt":
			var p struct{ ThreadID, TurnID string }
			decode(msg.Params, &p)
			interruptedMu.Lock()
			interrupted[p.ThreadID] = true
			interruptedMu.Unlock()
			require(threads[p.ThreadID] != nil && p.TurnID == p.ThreadID+"-turn", "interrupted the wrong turn")
			logEvent("interrupt " + p.ThreadID)
			respond(msg.ID, struct{}{})
			completed(p.ThreadID, "interrupted")
		case "thread/unsubscribe":
			var p struct{ ThreadID string }
			decode(msg.Params, &p)
			logEvent("unsubscribe " + p.ThreadID)
			delete(threads, p.ThreadID)
			delete(held, p.ThreadID)
			for id, call := range pending {
				if call.threadID == p.ThreadID {
					delete(pending, id)
				}
			}
			respond(msg.ID, map[string]string{"status": "unsubscribed"})
		default:
			fail("unexpected method: " + msg.Method)
		}
	}
	if err := in.Err(); err != nil {
		fail(err.Error())
	}
}

func awaitRelease(path string) {
	for range 500 {
		if _, err := os.Stat(path + ".release"); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	fail("no release signal from test")
}

func decode(data []byte, value any) {
	if err := json.Unmarshal(data, value); err != nil {
		fail(err.Error())
	}
}

func require(ok bool, message string) {
	if !ok {
		fail(message)
	}
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, "fakecodex:", message)
	os.Exit(2)
}

// requireLeanCatalog fails unless app-server was started the way the proxy must start it: skill descriptions
// omitted and a private catalog in which every model is in direct tool mode with no multi-agent version,
// no apply_patch and no experimental tools.
func requireLeanCatalog(args []string) {
	require(strings.Contains(" "+strings.Join(args, " ")+" ", " --enable omit_skill_descriptions "), "missing --enable omit_skill_descriptions")
	path := ""
	for i, arg := range args {
		if rest, ok := strings.CutPrefix(arg, "model_catalog_json="); ok && i > 0 && args[i-1] == "-c" {
			unquoted, err := strconv.Unquote(rest)
			require(err == nil, "model_catalog_json must be a quoted TOML string")
			path = unquoted
		}
	}
	require(path != "", "missing -c model_catalog_json=...")
	info, err := os.Stat(path)
	require(err == nil && info.Mode().Perm() == 0o600, "catalog file must exist with mode 0600")
	data, err := os.ReadFile(path)
	require(err == nil, "read catalog")
	var catalog struct{ Models []map[string]any }
	decode(data, &catalog)
	require(len(catalog.Models) > 0, "catalog has no models")
	for _, m := range catalog.Models {
		_, multi := m["multi_agent_version"]
		patch, hasPatch := m["apply_patch_tool_type"]
		extra, _ := m["experimental_supported_tools"].([]any)
		require(m["tool_mode"] == "direct" && !multi && hasPatch && patch == nil && extra != nil && len(extra) == 0,
			fmt.Sprintf("catalog model %v is not lean", m["slug"]))
	}
}

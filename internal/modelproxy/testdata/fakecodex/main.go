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
	require(len(os.Args) > 1 && os.Args[1] == "app-server", "expected app-server")
	requireLeanCatalog(os.Args[2:])
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
			"tokenUsage": map[string]any{"total": map[string]int{"inputTokens": input, "outputTokens": 3}}})
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
			threads[p.ThreadID].history = p.Items
			respond(msg.ID, struct{}{})
		case "turn/start":
			var p struct {
				ThreadID, Effort string
				Input            []struct{ Type, Text string }
			}
			decode(msg.Params, &p)
			tid := p.ThreadID
			th := threads[tid]
			require(th != nil && p.Input != nil, "expected known thread and input array")
			logEvent("turn " + tid)
			text := ""
			if len(p.Input) > 0 {
				require(p.Input[0].Type == "text", "expected text input")
				text = p.Input[0].Text
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
					usage(tid, 17)
					completed(tid, "completed")
					continue
				}
				require(th.model == "gpt-6.2-astra" && p.Effort == "" && len(th.history) == 3, "expected explicit model, default effort and three history items")
				require(string(th.history[1]) == `{"type":"function_call","name":"Read","call_id":"call-t1","arguments":"{\"path\":\"a.txt\"}"}`, "expected injected Read call")
				require(string(th.history[2]) == `{"type":"function_call_output","call_id":"call-t1","output":"PAPAYA"}`, "expected injected result")
				delta(tid, "PAPAYA")
				usage(tid, 17)
				completed(tid, "completed")
			default:
				delta(tid, text)
				usage(tid, 17)
				completed(tid, "completed")
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

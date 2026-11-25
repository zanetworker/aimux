package modelproxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func sessionPost(t *testing.T, h http.Handler, model, id string, messages ...conversationMessage) messageResponse {
	t.Helper()
	return sessionPostWithTools(t, h, model, id, `[{"name":"Read","input_schema":{"type":"object"}}]`, messages...)
}

func sessionPostWithTools(t *testing.T, h http.Handler, model, id, tools string, messages ...conversationMessage) messageResponse {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model": model, "messages": messages,
		"metadata": map[string]string{"user_id": id},
		"tools":    json.RawMessage(tools),
	})
	if err != nil {
		t.Fatal(err)
	}
	res := request(h, http.MethodPost, "/v1/messages", string(body))
	var message messageResponse
	if res.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", res.Code, res.Body)
	}
	if err := json.Unmarshal(res.Body.Bytes(), &message); err != nil {
		t.Fatal(err)
	}
	return message
}

func conversation(role, content string) conversationMessage {
	return conversationMessage{Role: role, Content: json.RawMessage(content)}
}

func protocolLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- test temp file
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSessionIdleTurnAndStatelessIsolation(t *testing.T) {
	backend, log := newFakeBackend(t)
	h := NewHandler(backend, testKey)
	model, id := "codex:"+defaultModel, `{"session_id":"session-a"}`
	first := conversation("user", `"first"`)
	sessionPost(t, h, model, id, first)
	sessionPost(t, h, "claude-haiku", id, conversation("user", `"title"`))
	sessionPost(t, h, model, "invalid metadata", conversation("user", `"one off"`))
	next := sessionPost(t, h, model, id,
		conversation("user", `[{"type":"text","text":"first","cache_control":{"type":"ephemeral"}}]`),
		conversation("assistant", `"first"`), conversation("system", `"new reminder"`), conversation("user", `"follow-up"`))
	if next.StopReason == nil || *next.StopReason != "end_turn" || len(next.Content) != 1 ||
		next.Content[0].Text == nil || *next.Content[0].Text != "follow-up" {
		t.Fatalf("unexpected follow-up: %+v", next)
	}
	data := protocolLog(t, log)
	if strings.Count(data, "thread ") != 3 || strings.Count(data, "turn t1\n") != 2 ||
		!strings.Contains(data, "inject t1 1") || strings.Contains(data, "unsubscribe t1") || !strings.Contains(data, "unsubscribe t2") ||
		!strings.Contains(data, "unsubscribe t3") {
		t.Fatalf("the session should reuse its idle thread and each other request should release its own thread:\n%s", data)
	}
}

func TestSessionIdleTurnWithTrailingSystemMessages(t *testing.T) {
	backend, log := newFakeBackend(t)
	h := NewHandler(backend, testKey)
	model, id := "codex:"+defaultModel, `{"session_id":"trailing-reminders"}`
	first := conversation("user", `"first"`)
	reply := sessionPost(t, h, model, id, first)
	history := []conversationMessage{first, sessionAssistant(t, reply),
		conversation("system", `"before user"`), conversation("user", `"follow-up"`),
		conversation("system", `"after user"`), conversation("system", `"another reminder"`)}
	next := sessionPost(t, h, model, id, history...)
	if next.StopReason == nil || *next.StopReason != "end_turn" || len(next.Content) != 1 ||
		next.Content[0].Text == nil || *next.Content[0].Text != "another reminder" {
		t.Fatalf("unexpected follow-up: %+v", next)
	}
	sessionPost(t, h, model, id, append(history, sessionAssistant(t, next), conversation("user", `"next"`))...)

	want := `inject t1 4
inject_item t1 {"type":"message","role":"developer","content":[{"type":"input_text","text":"before user"}]}
inject_item t1 {"type":"message","role":"user","content":[{"type":"input_text","text":"follow-up"}]}
inject_item t1 {"type":"message","role":"developer","content":[{"type":"input_text","text":"after user"}]}
inject_item t1 {"type":"message","role":"developer","content":[{"type":"input_text","text":"another reminder"}]}
turn t1
empty_input t1
`
	if data := protocolLog(t, log); !strings.Contains(data, want) ||
		strings.Count(data, "thread ") != 1 || strings.Count(data, "turn t1\n") != 3 {
		t.Fatalf("expected ordered injection and an empty turn input on the reused thread:\n%s", data)
	}
}

func sessionAssistant(t *testing.T, response messageResponse) conversationMessage {
	t.Helper()
	content, err := json.Marshal(response.Content)
	if err != nil {
		t.Fatal(err)
	}
	return conversation("assistant", string(content))
}

func TestSessionContinuesParallelToolCalls(t *testing.T) {
	backend, log := newFakeBackend(t)
	h := NewHandler(backend, testKey)
	model, id := "codex:"+defaultModel, `{"session_id":"session-parallel"}`
	first := conversation("user", `"session-parallel"`)
	start := time.Now()
	paused := sessionPost(t, h, model, id, first)
	elapsed := time.Since(start)
	t.Logf("first tool handoff took %s with usage %+v", elapsed, paused.Usage)
	if elapsed >= usageGrace/2 {
		t.Errorf("tool handoff took %s, want under %s", elapsed, usageGrace/2)
	}
	if paused.Usage != (Usage{}) {
		t.Errorf("first tool handoff usage = %+v, want zero", paused.Usage)
	}
	if paused.StopReason == nil || *paused.StopReason != "tool_use" || len(paused.Content) != 3 ||
		paused.Content[0].Type != "text" || paused.Content[1].ID != "call-t1" || paused.Content[2].ID != "call-t1-second" {
		t.Fatalf("expected text and two tool calls, got %+v", paused)
	}
	if data := protocolLog(t, log); strings.Contains(data, "interrupt ") || strings.Contains(data, "unsubscribe ") {
		t.Fatalf("handoff released an active turn:\n%s", data)
	}
	history := []conversationMessage{first, sessionAssistant(t, paused), conversation("user", `[
		{"type":"tool_result","tool_use_id":"call-t1-second","is_error":true,
		 "content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]},
		{"type":"text","text":"reminder"},
		{"type":"tool_result","tool_use_id":"call-t1","is_error":true,"content":[
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"BBBB"}},
			{"type":"text","text":"missing"},
			{"type":"text","text":"; try again"}
		]}
	]`)}
	finished := sessionPost(t, h, model, id, history...)
	if finished.StopReason == nil || *finished.StopReason != "end_turn" || len(finished.Content) != 1 ||
		finished.Content[0].Text == nil {
		t.Fatalf("the same turn should see both tool outputs and the reminder: %+v", finished)
	}
	if got := *finished.Content[0].Text; got != "failed(<image>Error: missing; try again)failed(Error: <image>)|reminder" {
		t.Errorf("continued tool outputs and reminder = %q", got)
	}
	t.Logf("continuation usage %+v", finished.Usage)
	if want := (Usage{InputTokens: 2, CacheReadInputTokens: 21, OutputTokens: 6}); finished.Usage != want {
		t.Errorf("continuation usage = %+v, want %+v", finished.Usage, want)
	}

	data := protocolLog(t, log)
	if strings.Count(data, "thread ") != 1 || strings.Count(data, "turn ") != 1 ||
		strings.Count(data, "answer ") != 2 || !strings.Contains(data, "steer t1\n") {
		t.Fatalf("expected two answers and steer on one turn:\n%s", data)
	}

	next := conversation("user", `"session-tool"`)
	pausedAgain := sessionPost(t, h, model, id, append(history, sessionAssistant(t, finished), next)...)
	t.Logf("next tool handoff usage %+v", pausedAgain.Usage)
	if want := (Usage{InputTokens: 2, CacheReadInputTokens: 21}); pausedAgain.Usage != want {
		t.Errorf("next tool handoff usage = %+v, want %+v", pausedAgain.Usage, want)
	}
	other := sessionPost(t, h, model, `{"session_id":"independent"}`, next)
	t.Logf("independent thread's tool handoff usage %+v", other.Usage)
	if other.Usage != (Usage{}) {
		t.Errorf("independent thread's tool handoff usage = %+v, want zero", other.Usage)
	}
	if data := protocolLog(t, log); strings.Count(data, "turn t1\n") != 2 || strings.Count(data, "turn t2\n") != 1 {
		t.Fatalf("expected one more turn on the same thread and one on a fresh thread:\n%s", data)
	}
}

// Claude Code splits parallel tool results over separate user messages, and its hooks add messages of their own
// after them. Measured on a live session, two of every three requests took that shape, and each one threw the
// thread away and reprocessed the whole conversation: 36.5 s against 4.8 s for a continuation.
func TestSessionContinuesToolResultsSplitOverMessages(t *testing.T) {
	backend, log := newFakeBackend(t)
	h := NewHandler(backend, testKey)
	model, id := "codex:"+defaultModel, `{"session_id":"session-parallel"}`
	first := conversation("user", `"session-parallel"`)
	paused := sessionPost(t, h, model, id, first)
	if len(paused.Content) != 3 || paused.Content[1].ID != "call-t1" || paused.Content[2].ID != "call-t1-second" {
		t.Fatalf("expected two tool calls, got %+v", paused)
	}
	// The same answers as the one-message case, split the way Claude Code sends them.
	history := []conversationMessage{first, sessionAssistant(t, paused),
		conversation("user", `[{"type":"tool_result","tool_use_id":"call-t1-second","is_error":true,
			"content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]`),
		conversation("user", `[{"type":"tool_result","tool_use_id":"call-t1","is_error":true,
			"content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"BBBB"}}]}]`),
		conversation("user", `[{"type":"text","text":"reminder"}]`),
	}
	finished := sessionPost(t, h, model, id, history...)
	if finished.StopReason == nil || *finished.StopReason != "end_turn" || len(finished.Content) != 1 || finished.Content[0].Text == nil {
		t.Fatalf("the same turn should see both tool outputs: %+v", finished)
	}
	if got := *finished.Content[0].Text; got != "failed(Error: <image>)failed(Error: <image>)|reminder" {
		t.Errorf("continued tool outputs and reminder = %q", got)
	}
	data := protocolLog(t, log)
	if strings.Count(data, "thread ") != 1 || strings.Count(data, "turn ") != 1 || strings.Count(data, "answer ") != 2 {
		t.Fatalf("expected both answers on the one paused turn, not a fresh thread:\n%s", data)
	}
}

// Claude Code follows a tool result with an in-conversation system message (a reminder). The live proxy's
// history named this shape as the one still throwing threads away: pending=1 tail=[user:tool_result system:text].
func TestSessionContinuesAfterASystemReminder(t *testing.T) {
	backend, log := newFakeBackend(t)
	h := NewHandler(backend, testKey)
	model, id := "codex:"+defaultModel, `{"session_id":"session-tool"}`
	first := conversation("user", `"session-tool"`)
	paused := sessionPost(t, h, model, id, first)
	if len(paused.Content) != 2 || paused.Content[1].Type != "tool_use" {
		t.Fatalf("expected a tool call, got %+v", paused)
	}
	finished := sessionPost(t, h, model, id, first, sessionAssistant(t, paused),
		conversation("user", `[{"type":"tool_result","tool_use_id":"call-t1","content":"one"}]`),
		conversation("system", `"a reminder from a hook"`))
	if finished.StopReason == nil || *finished.StopReason != "end_turn" {
		t.Fatalf("the paused turn should have continued: %+v", finished)
	}
	if data := protocolLog(t, log); strings.Count(data, "thread ") != 1 || strings.Count(data, "answer ") != 1 {
		t.Fatalf("expected the answer on the one paused turn, not a fresh thread:\n%s", data)
	}
}

// An assistant message among the new ones is history the proxy did not produce, so the thread cannot be reused.
func TestSessionFallsBackOnAnUnknownAssistantMessage(t *testing.T) {
	backend, log := newFakeBackend(t)
	h := NewHandler(backend, testKey)
	model, id := "codex:"+defaultModel, `{"session_id":"session-parallel"}`
	first := conversation("user", `"session-parallel"`)
	paused := sessionPost(t, h, model, id, first)
	// Answers the paused turn would accept, so the assistant message is the only reason to fall back.
	history := []conversationMessage{first, sessionAssistant(t, paused),
		conversation("user", `[
			{"type":"tool_result","tool_use_id":"call-t1-second","is_error":true,
			 "content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]},
			{"type":"tool_result","tool_use_id":"call-t1","is_error":true,
			 "content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"BBBB"}}]}]`),
		conversation("assistant", `[{"type":"text","text":"not ours"}]`),
		conversation("user", `"session-tool"`),
	}
	sessionPost(t, h, model, id, history...)
	if data := protocolLog(t, log); strings.Count(data, "thread ") != 2 {
		t.Fatalf("expected a fresh thread after an assistant message the proxy did not produce:\n%s", data)
	}
}

func TestSessionFallsBackWithoutAnsweringStaleCalls(t *testing.T) {
	model, id := "codex:"+defaultModel, `{"session_id":"fallback"}`
	first := conversation("user", `"session-parallel"`)
	result := conversation("user", `[
		{"type":"tool_result","tool_use_id":"call-t1","content":"PAPAYA"},
		{"type":"tool_result","tool_use_id":"call-t1-second","content":"PEAR"},
		{"type":"text","text":"replay"}
	]`)
	for _, tc := range []struct {
		name, reason string
		run          func(*testing.T, http.Handler, *CodexBackend, conversationMessage)
	}{
		{"rewind", "rewind", func(t *testing.T, h http.Handler, _ *CodexBackend, _ conversationMessage) {
			sessionPost(t, h, model, id, first)
		}},
		{"compaction", "compaction", func(t *testing.T, h http.Handler, _ *CodexBackend, _ conversationMessage) {
			sessionPost(t, h, model, id, conversation("user", `"summary after compaction"`))
		}},
		{"edited assistant", "prefix_changed", func(t *testing.T, h http.Handler, _ *CodexBackend, _ conversationMessage) {
			sessionPost(t, h, model, id, first, conversation("assistant", `"edited reply"`), result)
		}},
		{"changed tools", "configuration_changed", func(t *testing.T, h http.Handler, _ *CodexBackend, assistant conversationMessage) {
			sessionPostWithTools(t, h, model, id, `[{"name":"Edit","input_schema":{"type":"object"}}]`, first, assistant, result)
		}},
		{"changed model", "model_changed", func(t *testing.T, h http.Handler, _ *CodexBackend, assistant conversationMessage) {
			sessionPost(t, h, "codex:gpt-6.2-astra", id, first, assistant, result)
		}},
		{"unmatched call", "unmatched_tool_result", func(t *testing.T, h http.Handler, _ *CodexBackend, assistant conversationMessage) {
			sessionPost(t, h, model, id, first, assistant, conversation("user", `[
				{"type":"tool_result","tool_use_id":"call-t1","content":"only one result"},{"type":"text","text":"replay"}
			]`))
		}},
		{"app-server restart", "app_server_restart", func(t *testing.T, h http.Handler, b *CodexBackend, assistant conversationMessage) {
			b.server.close()
			sessionPost(t, h, model, id, first, assistant, result)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend, log := newFakeBackend(t)
			stderr, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stderr.Close() }()
			backend.stderr = stderr
			h := NewHandler(backend, testKey)
			paused := sessionPost(t, h, model, id, first)
			tc.run(t, h, backend, sessionAssistant(t, paused))
			data := protocolLog(t, log)
			if strings.Count(data, "thread ") != 2 || strings.Contains(data, "answer ") {
				t.Fatalf("expected a fresh thread and no stale answers:\n%s", data)
			}
			if tc.reason != "app_server_restart" && (!strings.Contains(data, "interrupt t1\n") || !strings.Contains(data, "unsubscribe t1\n")) {
				t.Fatalf("displaced paused turn was not released:\n%s", data)
			}
			stats := protocolLog(t, stderr.Name())
			if !strings.Contains(stats, "session="+tc.reason+" ") {
				t.Fatalf("missing fallback reason %q:\n%s", tc.reason, stats)
			}
		})
	}
}

func TestSessionExpiresPausedAndIdleThreads(t *testing.T) {
	backend, log := newFakeBackend(t)
	backend.sessions.pauseTimeout = 50 * time.Millisecond
	backend.sessions.idleTimeout = 150 * time.Millisecond
	h := NewHandler(backend, testKey)
	model, id := "codex:"+defaultModel, `{"session_id":"expiry"}`
	first := conversation("user", `"session-tool"`)
	paused := sessionPost(t, h, model, id, first)
	waitForLog(t, log, "interrupt t1\n")
	waitForLog(t, log, "unsubscribe t1\n")
	finished := sessionPost(t, h, model, id, first, sessionAssistant(t, paused), conversation("user", `[
		{"type":"tool_result","tool_use_id":"call-t1","content":"PAPAYA"},{"type":"text","text":"replay"}
	]`))
	if len(finished.Content) != 1 || finished.Content[0].Text == nil || *finished.Content[0].Text != "replay" {
		t.Fatalf("timed-out tool request should replay history: %+v", finished)
	}
	waitForLog(t, log, "unsubscribe t2\n")
	if data := protocolLog(t, log); strings.Contains(data, "answer ") || strings.Contains(data, "interrupt t2\n") {
		t.Fatalf("expected idle unsubscribe and no stale answer:\n%s", data)
	}
}

func TestSessionLogsOnceWithoutPromptOrSessionID(t *testing.T) {
	backend, _ := newFakeBackend(t)
	stderr, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stderr.Close() }()
	backend.stderr = stderr
	h := NewHandler(backend, testKey)
	id := `{"session_id":"private-id"}`
	sessionPost(t, h, "codex:"+defaultModel, id, conversation("user", `"private prompt"`))
	sessionPost(t, h, "claude-haiku", id, conversation("user", `"private background prompt"`))
	lines := strings.Split(strings.TrimSpace(protocolLog(t, stderr.Name())), "\n")
	want := regexp.MustCompile(`^model proxy: session=(new_session|stateless) input_tokens=17 cached_input_tokens=0 ttfb_ms=[0-9]+$`)
	started := regexp.MustCompile(`^model proxy: Codex started in [0-9]+ms$`)
	if len(lines) != 3 || !started.MatchString(lines[0]) || !want.MatchString(lines[1]) || !want.MatchString(lines[2]) || lines[1] == lines[2] {
		t.Fatalf("expected the start line, then one content-free timing line for each request: %q", lines)
	}
}

func TestSessionLogsPausedUsageAsEstimated(t *testing.T) {
	backend, _ := newFakeBackend(t)
	stderr, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stderr.Close() }()
	backend.stderr = stderr
	h := NewHandler(backend, testKey)
	model, id := "codex:"+defaultModel, `{"session_id":"estimated-usage"}`
	first := conversation("user", `"partial-cache"`)
	reply := sessionPost(t, h, model, id, first)
	paused := sessionPost(t, h, model, id, first, sessionAssistant(t, reply), conversation("user", `"session-tool"`))
	if paused.StopReason == nil || *paused.StopReason != "tool_use" ||
		paused.Usage != (Usage{InputTokens: 2, CacheReadInputTokens: 21}) {
		t.Fatalf("expected cached prompt estimate on tool handoff: %+v", paused)
	}
	observed := sessionPost(t, h, model, `{"session_id":"observed-usage"}`, conversation("user", `"session-tool-with-usage"`))
	if observed.StopReason == nil || *observed.StopReason != "tool_use" ||
		observed.Usage != (Usage{InputTokens: 17, OutputTokens: 3}) {
		t.Fatalf("expected observed usage on tool handoff: %+v", observed)
	}
	lines := strings.Split(strings.TrimSpace(protocolLog(t, stderr.Name())), "\n")
	want := []string{
		`^model proxy: Codex started in [0-9]+ms$`,
		`^model proxy: session=new_session input_tokens=2 cached_input_tokens=21 ttfb_ms=[0-9]+$`,
		`^model proxy: session=hit_idle usage=estimated estimated_input_tokens=2 estimated_cached_input_tokens=21 ttfb_ms=[0-9]+$`,
		`^model proxy: session=new_session input_tokens=17 cached_input_tokens=0 ttfb_ms=[0-9]+$`,
	}
	if len(lines) != len(want) {
		t.Fatalf("unexpected usage logs: %q", lines)
	}
	for i, pattern := range want {
		if !regexp.MustCompile(pattern).MatchString(lines[i]) {
			t.Errorf("usage log = %q, want %s", lines[i], pattern)
		}
	}
}

func TestSessionRejectsEditedThinkingHistory(t *testing.T) {
	backend, log := newFakeBackend(t)
	h := NewHandler(backend, testKey)
	model, id := "codex:"+defaultModel, `{"session_id":"edited-thinking"}`
	user := conversation("user", `"first"`)
	first := sessionPost(t, h, model, id,
		conversation("assistant", `[{"type":"thinking","thinking":"original","signature":"a"}]`), user)
	sessionPost(t, h, model, id,
		conversation("assistant", `[{"type":"thinking","thinking":"edited","signature":"a"}]`), user,
		sessionAssistant(t, first), conversation("user", `"next"`))
	if data := protocolLog(t, log); strings.Count(data, "thread ") != 2 || !strings.Contains(data, "unsubscribe t1\n") {
		t.Fatalf("expected edited history to start a fresh thread:\n%s", data)
	}
}

func TestBackgroundRequestIgnoresMetadataShape(t *testing.T) {
	backend, _ := newFakeBackend(t)
	res := request(NewHandler(backend, testKey), http.MethodPost, "/v1/messages",
		`{"model":"claude-haiku","metadata":42,"messages":[{"role":"user","content":"title"}]}`)
	if res.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", res.Code, res.Body)
	}
	checkJSON(t, res.Body.Bytes(), "content", []any{map[string]any{"type": "text", "text": "title"}})
}

func TestSessionReplaysWhenThreadDisappears(t *testing.T) {
	backend, log := newFakeBackend(t)
	h := NewHandler(backend, testKey)
	model, id := "codex:"+defaultModel, `{"session_id":"missing-thread"}`
	first := conversation("user", `"expire-thread"`)
	a := sessionPost(t, h, model, id, first)
	b := sessionPost(t, h, model, id, first, sessionAssistant(t, a), conversation("user", `"next"`))
	if b.StopReason == nil || *b.StopReason != "end_turn" || len(b.Content) != 1 ||
		b.Content[0].Text == nil || *b.Content[0].Text != "next" {
		t.Fatalf("unexpected replay: %+v", b)
	}
	if data := protocolLog(t, log); strings.Count(data, "thread ") != 2 ||
		!strings.Contains(data, "inject t2 2\n") || strings.Count(data, "initialize ") != 1 {
		t.Fatalf("expected full replay on the same app-server:\n%s", data)
	}
}

func TestSessionParallelWindowUsesArrivalTime(t *testing.T) {
	backend, log := newFakeBackend(t)
	var req messageRequest
	if err := json.Unmarshal([]byte(`{"model":"codex:gpt-6.2-sol","metadata":{"user_id":"{\"session_id\":\"late\"}"},
		"tools":[{"name":"Read","input_schema":{"type":"object"}}],
		"messages":[{"role":"user","content":"session-parallel-late"}]}`), &req); err != nil {
		t.Fatal(err)
	}
	turn, err := mapRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var calls []string
	_, err = backend.Run(ctx, turn, func(event Event) error {
		if event.Text != "" {
			// Simulate a slow downstream reader while the app-server sends both calls.
			waitForLog(t, log, "sent t1 second")
		}
		if event.Tool != nil {
			calls = append(calls, event.Tool.ID)
		}
		return nil
	})
	if err != nil || !slices.Equal(calls, []string{"call-t1"}) {
		t.Fatalf("expected only the tool that arrived inside the first 300 ms: %v, %v", calls, err)
	}
}

func TestSessionStreamsParallelToolBlocksInOrder(t *testing.T) {
	backend, _ := newFakeBackend(t)
	res := request(NewHandler(backend, testKey), http.MethodPost, "/v1/messages",
		`{"model":"codex:gpt-6.2-sol","stream":true,"metadata":{"user_id":"{\"session_id\":\"stream\"}"},
		"tools":[{"name":"Read","input_schema":{"type":"object"}}],
		"messages":[{"role":"user","content":"session-parallel"}]}`)
	var blocks []string
	var indexes []int
	for _, event := range readSSE(t, res.Body) {
		switch event.Type {
		case "content_block_start":
			var block responseBlock
			if err := json.Unmarshal(event.ContentBlock, &block); err != nil {
				t.Fatal(err)
			}
			if block.Type == "tool_use" {
				blocks = append(blocks, block.ID)
			}
		case "content_block_stop":
			if event.Index == nil {
				t.Fatal("content block stop is missing its index")
			}
			indexes = append(indexes, *event.Index)
		case "message_delta":
			checkJSON(t, event.Delta, "stop_reason", "tool_use")
		}
	}
	if !slices.Equal(blocks, []string{"call-t1", "call-t1-second"}) || !slices.Equal(indexes, []int{0, 1, 2}) {
		t.Fatalf("expected text then two distinct tool blocks; calls=%v indexes=%v", blocks, indexes)
	}
}

func TestSessionKeepsDifferentSubagentConfiguration(t *testing.T) {
	backend, log := newFakeBackend(t)
	h := NewHandler(backend, testKey)
	model, id := "codex:"+defaultModel, `{"session_id":"subagent"}`
	main := conversation("user", `"session-tool"`)
	paused := sessionPost(t, h, model, id, main)
	sessionPostWithTools(t, h, model, id, `[{"name":"Edit","input_schema":{"type":"object"}}]`,
		conversation("user", `"independent subagent prompt"`))
	res := sessionPost(t, h, model, id, main, sessionAssistant(t, paused), conversation("user", `[
		{"type":"tool_result","tool_use_id":"call-t1","content":"PAPAYA"}
	]`))
	if res.StopReason == nil || *res.StopReason != "end_turn" || len(res.Content) != 1 ||
		res.Content[0].Text == nil || *res.Content[0].Text != "PAPAYA|" {
		t.Fatalf("subagent displaced the main thread: %+v", res)
	}
	data := protocolLog(t, log)
	if strings.Count(data, "thread ") != 2 || !strings.Contains(data, "answer t1\n") || strings.Contains(data, "interrupt t1\n") {
		t.Fatalf("expected the main session to continue independently:\n%s", data)
	}
}

func TestSessionSerializesRequestsForOneThread(t *testing.T) {
	backend, log := newFakeBackend(t)
	var req messageRequest
	if err := json.Unmarshal([]byte(`{"model":"codex:gpt-6.2-sol","metadata":{"user_id":"{\"session_id\":\"busy\"}"},
		"messages":[{"role":"user","content":"wait"}]}`), &req); err != nil {
		t.Fatal(err)
	}
	first, err := mapRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	req.Messages[0].Content = json.RawMessage(`"competing request"`)
	second, err := mapRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := backend.Run(ctx, first, func(Event) error { return nil })
		done <- err
	}()
	waitForLog(t, log, "turn t1\n")
	wait, stopWaiting := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stopWaiting()
	_, err = backend.Run(wait, second, func(Event) error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("concurrent request should time out waiting for the first: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled running request returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not cancel")
	}
	data := protocolLog(t, log)
	if strings.Count(data, "thread ") != 1 || !strings.Contains(data, "interrupt t1\n") ||
		!strings.Contains(data, "unsubscribe t1\n") {
		t.Fatalf("expected a single in-flight thread to be cleaned up:\n%s", data)
	}
}

func TestSessionReusesThreadAfterEmptyAssistantMessage(t *testing.T) {
	backend, log := newFakeBackend(t)
	h := NewHandler(backend, testKey)
	model, id := "codex:"+defaultModel, `{"session_id":"empty-content"}`
	first := conversation("user", `""`)
	empty := sessionPost(t, h, model, id, first)
	if empty.StopReason == nil || *empty.StopReason != "end_turn" || len(empty.Content) != 0 {
		t.Fatalf("expected an empty assistant reply, got %+v", empty)
	}
	sessionPost(t, h, model, id, first, sessionAssistant(t, empty), conversation("user", `"next"`))
	if data := protocolLog(t, log); strings.Count(data, "thread ") != 1 || strings.Count(data, "turn t1\n") != 2 {
		t.Fatalf("expected the empty assistant reply to match the prefix:\n%s", data)
	}
}

func TestSessionToolAliasesUseDifferentThreads(t *testing.T) {
	backend, log := newFakeBackend(t)
	h := NewHandler(backend, testKey)
	model, id := "codex:"+defaultModel, `{"session_id":"tool-alias"}`
	first := conversation("user", `"first"`)
	a := sessionPostWithTools(t, h, model, id, `[{"name":"mcp","input_schema":{"type":"object"}}]`, first)
	sessionPostWithTools(t, h, model, id, `[{"name":"mcp_","input_schema":{"type":"object"}}]`,
		first, sessionAssistant(t, a), conversation("user", `"next"`))
	if data := protocolLog(t, log); strings.Count(data, "thread ") != 2 || !strings.Contains(data, "unsubscribe t1\n") {
		t.Fatalf("expected the changed tool name to start a fresh thread:\n%s", data)
	}
}

// A fallback that says only its name leaves the next investigation to guesswork: the live bug where Claude Code
// split tool results over messages was found by printing these numbers by hand.
func TestSessionFallbackExplainsItselfInTheRequestLog(t *testing.T) {
	backend, _ := newFakeBackend(t)
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	requests, err := OpenRequestLog(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = requests.Close() }()
	h := NewHandler(backend, testKey, WithRequestLog(requests))
	model, id := "codex:"+defaultModel, `{"session_id":"session-parallel"}`
	first := conversation("user", `"session-parallel"`)
	paused := sessionPost(t, h, model, id, first)
	// Two calls are pending; answering one of them cannot continue the turn.
	sessionPost(t, h, model, id, first, sessionAssistant(t, paused),
		conversation("user", `[{"type":"tool_result","tool_use_id":"call-t1","content":"one"},{"type":"text","text":"replay"}]`))
	records := readRecords(t, path)
	if len(records) != 2 {
		t.Fatalf("got %d records, want one per request", len(records))
	}
	last := records[1]
	if last["outcome"] != "unmatched_tool_result" {
		t.Fatalf("outcome = %v, want the fallback", last["outcome"])
	}
	detail, _ := last["detail"].(string)
	for _, want := range []string{"pending=2", "tail=[user:tool_result,text]"} {
		if !strings.Contains(detail, want) {
			t.Errorf("fallback detail %q does not mention %q", detail, want)
		}
	}
	if last["model"] != model || last["effort"] != "high" || last["status"] != float64(200) {
		t.Errorf("record lacks the request's shape: %v", last)
	}
}

// A new session injects the whole conversation before its first turn. However long that takes, turn/start gets
// its own lifecycle timeout.
func TestNewSessionStartsTurnAfterSlowHistoryInjection(t *testing.T) {
	backend, log := newFakeBackend(t)
	backend.lifecycleTimeout = 200 * time.Millisecond
	t.Setenv("AIMUX_FAKE_CODEX_INJECT_DELAY_MS", "500")
	h := NewHandler(backend, testKey)
	model, id := "codex:"+defaultModel, `{"session_id":"session-slow-history"}`
	answer := sessionPost(t, h, model, id,
		conversation("user", `"first"`), conversation("assistant", `"first"`), conversation("user", `"after slow history"`))
	if len(answer.Content) != 1 || answer.Content[0].Text == nil || *answer.Content[0].Text != "after slow history" {
		t.Fatalf("unexpected answer: %+v", answer)
	}
	if data := protocolLog(t, log); !strings.Contains(data, "inject t1 2") || !strings.Contains(data, "turn t1") {
		t.Fatalf("expected the history injected and the turn started:\n%s", data)
	}
}

func TestDescribeTailNamesOnlyPlainKinds(t *testing.T) {
	tail := []inputMessage{{role: "assistant", blocks: []requestBlock{{Type: "text"}, {Type: "server_tool_use"}, {Type: "odd kind\x1b[31m"}, {Type: ""}}}}
	if got, want := describeTail(tail, 1), "pending=1 tail=[assistant:text,server_tool_use,other,other]"; got != want {
		t.Errorf("describeTail = %q, want %q", got, want)
	}
}

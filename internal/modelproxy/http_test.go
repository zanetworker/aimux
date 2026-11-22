package modelproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

const toolRequest = `{"model":"claude-haiku","system":"base instructions","tools":[{"name":"Read","description":"Read it","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"previous"},{"role":"user","content":"tool"}]`

func TestMessagesToolLoop(t *testing.T) {
	backend, _ := newFakeBackend(t)
	handler := NewHandler(backend, testKey)
	first := request(handler, http.MethodPost, "/v1/messages", toolRequest+`,"stream":true}`)
	if first.Code != http.StatusOK || first.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d, headers %v, body %s", first.Code, first.Header(), first.Body)
	}
	events := readSSE(t, first.Body)
	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_delta",
		"content_block_stop", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event.Type)
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("events = %v, want %v", names, want)
	}
	checkJSON(t, events[0].Message, "type", "message", "role", "assistant", "model", "claude-haiku", "content", []any{}, "stop_reason", nil)
	checkJSON(t, events[1].ContentBlock, "type", "text", "text", "")
	checkJSON(t, events[2].Delta, "type", "text_delta", "text", "before ")
	checkJSON(t, events[3].Delta, "type", "text_delta", "text", "tool")
	checkJSON(t, events[5].ContentBlock, "type", "tool_use", "id", "call-t1", "name", "Read", "input", map[string]any{})
	checkJSON(t, events[6].Delta, "type", "input_json_delta", "partial_json", `{"path":"a.txt"}`)
	if events[1].Index == nil || *events[1].Index != 0 || events[7].Index == nil || *events[7].Index != 1 {
		t.Fatalf("unexpected block indexes: %+v", events)
	}
	checkJSON(t, events[8].Delta, "stop_reason", "tool_use")
	checkJSON(t, events[8].Usage, "input_tokens", float64(17), "cache_read_input_tokens", float64(0), "output_tokens", float64(3))

	second := request(handler, http.MethodPost, "/v1/messages", `{"model":"codex:gpt-6.2-astra","messages":[
		{"role":"user","content":"previous"},{"role":"assistant","content":[{"type":"tool_use","id":"call-t1","name":"Read","input":{"path":"a.txt"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-t1","content":"PAPAYA"}]}]}`)
	if second.Code != http.StatusOK {
		t.Fatalf("continuation status %d, body %s", second.Code, second.Body)
	}
	checkJSON(t, second.Body.Bytes(), "role", "assistant", "model", "codex:gpt-6.2-astra", "stop_reason", "end_turn",
		"content", []any{map[string]any{"type": "text", "text": "PAPAYA"}},
		"usage", map[string]any{"input_tokens": float64(17), "cache_read_input_tokens": float64(0), "output_tokens": float64(3)})
}

func TestMessagesJSONToolsAndEmptyText(t *testing.T) {
	backend, _ := newFakeBackend(t)
	handler := NewHandler(backend, testKey)
	res := request(handler, http.MethodPost, "/v1/messages", toolRequest+`}`)
	if res.Code != http.StatusOK || res.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, headers %v, body %s", res.Code, res.Header(), res.Body)
	}
	checkJSON(t, res.Body.Bytes(), "stop_reason", "tool_use", "content", []any{
		map[string]any{"type": "text", "text": "before tool"},
		map[string]any{"type": "tool_use", "id": "call-t1", "name": "Read", "input": map[string]any{"path": "a.txt"}},
	})

	empty := request(handler, http.MethodPost, "/v1/messages", `{"messages":[{"role":"user","content":""}],"stream":true}`)
	events := readSSE(t, empty.Body)
	if empty.Code != http.StatusOK || len(events) != 3 || events[0].Type != "message_start" || events[2].Type != "message_stop" {
		t.Fatalf("empty response: status %d, events %+v", empty.Code, events)
	}
	checkJSON(t, events[1].Delta, "stop_reason", "end_turn")
}

func TestMessagesStatelessCachedUsage(t *testing.T) {
	backend, _ := newFakeBackend(t)
	h := NewHandler(backend, testKey)
	for _, tc := range []struct {
		prompt        string
		input, cached float64
		stream        bool
	}{
		{"partial-cache", 2, 21, true},
		{"full-cache", 0, 17, false},
		{"excess-cache", 0, 18, false},
	} {
		t.Run(tc.prompt, func(t *testing.T) {
			body := `{"messages":[{"role":"user","content":"` + tc.prompt + `"}]`
			if tc.stream {
				body += `,"stream":true`
			}
			res := request(h, http.MethodPost, "/v1/messages", body+`}`)
			if res.Code != http.StatusOK {
				t.Fatalf("HTTP %d: %s", res.Code, res.Body)
			}
			want := map[string]any{"input_tokens": tc.input, "cache_read_input_tokens": tc.cached, "output_tokens": float64(5)}
			if tc.stream {
				events := readSSE(t, res.Body)
				if len(events) < 2 || events[len(events)-2].Type != "message_delta" {
					t.Fatalf("missing message_delta: %+v", events)
				}
				checkJSON(t, events[len(events)-2].Usage, "input_tokens", tc.input,
					"cache_read_input_tokens", tc.cached, "output_tokens", float64(5))
			} else {
				checkJSON(t, res.Body.Bytes(), "usage", want)
			}
		})
	}
}

func TestProxyEndpoints(t *testing.T) {
	backend, _ := newFakeBackend(t)
	handler := NewHandler(backend, testKey)
	body := `{"messages":[{"role":"user","content":"hello"}]}`
	res := request(handler, http.MethodPost, "/v1/messages/count_tokens", body)
	if res.Code != http.StatusOK {
		t.Fatalf("count tokens status %d: %s", res.Code, res.Body)
	}
	checkJSON(t, res.Body.Bytes(), "input_tokens", float64(len(body)/4))

	res = request(handler, http.MethodGet, "/v1/models", "")
	var models struct {
		Data []struct{ ID string }
	}
	err := json.Unmarshal(res.Body.Bytes(), &models)
	if res.Code != http.StatusOK || err != nil || len(models.Data) != 2 ||
		models.Data[0].ID != "codex:gpt-6.2-sol" || models.Data[1].ID != "codex:gpt-6.2-astra" {
		t.Fatalf("models status %d: %s, %v", res.Code, res.Body, err)
	}
	if res := request(handler, http.MethodGet, "/v1/messages", ""); res.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method status %d", res.Code)
	}
}

func TestMessagesErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body, errorType, want string
		status                      int
	}{
		{"bad JSON", `{`, "invalid_request_error", "invalid JSON", 400},
		{"trailing JSON", `{} {}`, "invalid_request_error", "invalid JSON", 400},
		{"interrupted", `{"messages":[{"role":"user","content":"interrupted"}]}`, "api_error", "codex turn interrupted", 502},
		{"crashed", `{"messages":[{"role":"user","content":"crash"}],"stream":true}`, "api_error", "app-server exited", 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend, log := newFakeBackend(t)
			res := request(NewHandler(backend, testKey), http.MethodPost, "/v1/messages", tc.body)
			var envelope struct {
				Type  string
				Error struct{ Type, Message string }
			}
			err := json.Unmarshal(res.Body.Bytes(), &envelope)
			if err != nil || res.Code != tc.status || envelope.Type != "error" ||
				envelope.Error.Type != tc.errorType || !strings.Contains(envelope.Error.Message, tc.want) {
				t.Fatalf("status %d body %s, error %v", res.Code, res.Body, err)
			}
			if tc.status == 400 {
				if _, err := os.Stat(log); !os.IsNotExist(err) {
					t.Fatalf("invalid input started the backend: %v", err)
				}
			}
		})
	}
}

func TestMessagesStreamingFailure(t *testing.T) {
	backend, log := newFakeBackend(t)
	server := httptest.NewServer(NewHandler(backend, testKey))
	defer server.Close()
	client := server.Client()
	client.Timeout = 20 * time.Second
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/messages", strings.NewReader(
		`{"stream":true,"messages":[{"role":"user","content":"crash-after-delta"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-api-key", testKey)
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	reader := bufio.NewReader(res.Body)
	for i := 0; i < 3; i++ {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal("expected flushed events before releasing the subprocess:", err)
			}
			if line == "\n" {
				break
			}
		}
	}
	if err := os.WriteFile(log+".release", nil, 0600); err != nil {
		t.Fatal(err)
	}
	events := readSSE(t, reader)
	if len(events) != 2 || events[0].Type != "content_block_stop" || events[1].Type != "error" ||
		!strings.Contains(string(events[1].Error), "app-server exited") {
		t.Fatalf("unexpected SSE after crash: %+v", events)
	}
}

func TestMessagesRequestCancellation(t *testing.T) {
	backend, log := newFakeBackend(t)
	server := httptest.NewServer(NewHandler(backend, testKey))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/messages",
		strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"wait"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-api-key", testKey)
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	cancel()
	waitForLog(t, log, "interrupt t1")
	waitForLog(t, log, "unsubscribe t1")
}

func request(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	res := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("x-api-key", testKey)
	handler.ServeHTTP(res, req)
	return res
}

type testEvent struct {
	Type         string
	Index        *int
	Message      json.RawMessage
	ContentBlock json.RawMessage `json:"content_block"`
	Delta        json.RawMessage
	Usage        json.RawMessage
	Error        json.RawMessage
}

func readSSE(t *testing.T, reader io.Reader) []testEvent {
	t.Helper()
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var events []testEvent
	for _, frame := range strings.Split(strings.TrimSuffix(string(raw), "\n\n"), "\n\n") {
		name, data, ok := strings.Cut(frame, "\ndata: ")
		var event testEvent
		if !ok || json.Unmarshal([]byte(data), &event) != nil || name != "event: "+event.Type {
			t.Fatalf("bad SSE frame: %q", frame)
		}
		events = append(events, event)
	}
	return events
}

func checkJSON(t *testing.T, raw []byte, fields ...any) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	if len(fields)%2 != 0 {
		t.Fatalf("checkJSON takes name, value pairs; got %d arguments", len(fields))
	}
	for i := 0; i+1 < len(fields); i += 2 {
		name := fields[i].(string)
		if value, ok := got[name]; !ok || !reflect.DeepEqual(value, fields[i+1]) {
			t.Fatalf("%s field %s = %#v, want %#v", raw, name, value, fields[i+1])
		}
	}
}

func TestMessagesMCPToolNameRoundTrip(t *testing.T) {
	backend, _ := newFakeBackend(t)
	handler := NewHandler(backend, testKey)
	res := request(handler, http.MethodPost, "/v1/messages", `{"tools":[{"name":"mcp__granola__get_meeting_transcript",
		"description":"d","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"call-first-tool"}]}`)
	if res.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", res.Code, res.Body)
	}
	checkJSON(t, res.Body.Bytes(), "stop_reason", "tool_use", "content", []any{
		map[string]any{"type": "tool_use", "id": "call-t1", "name": "mcp__granola__get_meeting_transcript", "input": map[string]any{"path": "a.txt"}},
	})
}

const testKey = "test-proxy-key"

func TestHandlerRequiresKey(t *testing.T) {
	backend, _ := newFakeBackend(t)
	call := func(handler http.Handler, header, value string) *httptest.ResponseRecorder {
		res := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		if header != "" {
			req.Header.Set(header, value)
		}
		handler.ServeHTTP(res, req)
		return res
	}
	handler := NewHandler(backend, testKey)
	for name, tc := range map[string]struct {
		header, value string
		want          int
	}{
		"no key":         {"", "", http.StatusUnauthorized},
		"wrong key":      {"x-api-key", "nope", http.StatusUnauthorized},
		"x-api-key":      {"x-api-key", testKey, http.StatusOK},
		"bearer":         {"Authorization", "Bearer " + testKey, http.StatusOK},
		"bearer, wrong":  {"Authorization", "Bearer nope", http.StatusUnauthorized},
		"no bearer word": {"Authorization", testKey, http.StatusUnauthorized},
	} {
		t.Run(name, func(t *testing.T) {
			res := call(handler, tc.header, tc.value)
			if res.Code != tc.want {
				t.Fatalf("status %d, want %d, body %s", res.Code, tc.want, res.Body)
			}
			if tc.want == http.StatusUnauthorized {
				checkJSON(t, res.Body.Bytes(), "type", "error", "error", map[string]any{"type": "authentication_error", "message": "invalid or missing proxy key"})
			}
		})
	}
	if res := call(NewHandler(backend, ""), "x-api-key", ""); res.Code != http.StatusUnauthorized {
		t.Fatalf("an empty configured key must reject every request, got %d", res.Code)
	}
}

func TestMessagesWithImageSucceeds(t *testing.T) {
	backend, _ := newFakeBackend(t)
	res := request(NewHandler(backend, testKey), http.MethodPost, "/v1/messages",
		`{"messages":[{"role":"user","content":[{"type":"text","text":"see"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}`)
	if res.Code != http.StatusOK {
		t.Fatalf("a request with an image must not fail the session: status %d, body %s", res.Code, res.Body)
	}
	// The fixture reports how many images reached Codex's turn input.
	checkJSON(t, res.Body.Bytes(), "stop_reason", "end_turn", "content", []any{map[string]any{"type": "text", "text": "see+1 image"}})

	only := request(NewHandler(backend, testKey), http.MethodPost, "/v1/messages",
		`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}]}]}`)
	if only.Code != http.StatusOK {
		t.Fatalf("image-only message: status %d, body %s", only.Code, only.Body)
	}
	checkJSON(t, only.Body.Bytes(), "content", []any{map[string]any{"type": "text", "text": "+1 image"}})
}

func TestMessagesRejectsOversizedBodies(t *testing.T) {
	saved := maxRequestBytes
	maxRequestBytes = 1024
	t.Cleanup(func() { maxRequestBytes = saved })
	backend, _ := newFakeBackend(t)
	big := `{"messages":[{"role":"user","content":"` + strings.Repeat("x", 2048) + `"}]}`
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
		res := request(NewHandler(backend, testKey), http.MethodPost, path, big)
		if res.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("%s: status %d, want 413; body %s", path, res.Code, res.Body)
		}
		checkJSON(t, res.Body.Bytes(), "type", "error")
		if !strings.Contains(res.Body.String(), "request_too_large") {
			t.Fatalf("%s: body %s, want request_too_large", path, res.Body)
		}
	}
	if res := request(NewHandler(backend, testKey), http.MethodPost, "/v1/messages", `{"messages":[{"role":"user","content":"small"}]}`); res.Code != http.StatusOK {
		t.Fatalf("a small request failed: %d %s", res.Code, res.Body)
	}
}

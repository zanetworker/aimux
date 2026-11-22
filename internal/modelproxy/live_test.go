package modelproxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLiveCodex(t *testing.T) {
	if os.Getenv("AIMUX_LIVE_CODEX") != "1" {
		t.Skip("set AIMUX_LIVE_CODEX=1 to use your Codex login")
	}
	backend := NewCodexBackend("", os.Stderr)
	defer backend.Close()
	server := httptest.NewServer(NewHandler(backend, testKey))
	defer server.Close()
	client := server.Client()
	client.Timeout = 120 * time.Second

	post := func(model string, messages []map[string]any, stream bool) []byte {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"model": model, "stream": stream, "messages": messages,
			"system": "Follow the user's instructions. Use the Lookup tool if asked. When its result is present, answer with the result alone.",
			"tools": []map[string]any{{
				"name": "Lookup", "description": "Look up an item by key.",
				"input_schema": json.RawMessage(`{"type":"object","properties":{"key":{"type":"string"}},"required":["key"],"additionalProperties":false}`),
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/messages", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", testKey)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		output, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != http.StatusOK {
			t.Fatalf("real Codex HTTP response %d: %s, %v", res.StatusCode, output, err)
		}
		return output
	}
	messages := []map[string]any{{"role": "user", "content": "Call the Lookup tool with key fruit."}}
	events := readSSE(t, bytes.NewReader(post("claude-haiku", messages, true)))
	var tool responseBlock
	var args json.RawMessage
	for _, event := range events {
		switch event.Type {
		case "content_block_start":
			var block responseBlock
			if err := json.Unmarshal(event.ContentBlock, &block); err != nil {
				t.Fatal(err)
			}
			if block.Type == "tool_use" {
				tool = block
			}
		case "content_block_delta":
			var delta struct {
				PartialJSON string `json:"partial_json"`
			}
			if err := json.Unmarshal(event.Delta, &delta); err != nil {
				t.Fatal(err)
			}
			if delta.PartialJSON != "" {
				args = json.RawMessage(delta.PartialJSON)
			}
		case "message_delta":
			checkJSON(t, event.Delta, "stop_reason", "tool_use")
			t.Logf("tool turn usage %s", event.Usage)
		}
	}
	if tool.Name != "Lookup" || tool.ID == "" || len(args) == 0 || events[len(events)-1].Type != "message_stop" {
		t.Fatalf("expected completed Lookup handoff, got %+v", events)
	}
	checkJSON(t, args, "key", "fruit")
	tool.Input = args
	messages = append(messages,
		map[string]any{"role": "assistant", "content": []responseBlock{tool}},
		map[string]any{"role": "user", "content": []map[string]string{{
			"type": "tool_result", "tool_use_id": tool.ID, "content": "PAPAYA",
		}}},
	)
	var response messageResponse
	output := post("claude-haiku", messages, false)
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatal(err)
	}
	if response.StopReason == nil || *response.StopReason != "end_turn" ||
		len(response.Content) == 0 || response.Content[0].Text == nil ||
		!strings.Contains(strings.ToUpper(*response.Content[0].Text), "PAPAYA") ||
		response.Usage.InputTokens <= 0 {
		t.Fatalf("Codex did not continue using the injected result: %s", output)
	}
	t.Logf("continuation usage %+v", response.Usage)
}

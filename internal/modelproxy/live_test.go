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

	post := func(messages []map[string]any) messageResponse {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"model": "codex:" + defaultModel, "messages": messages,
			"metadata": map[string]string{"user_id": `{"session_id":"aimux-live-cache-test"}`},
			// Use a stable prefix large enough to be eligible for the provider's prompt cache.
			"system": "Follow the user's instructions. Use the Lookup tool if asked. When its result is present, answer with the result alone. " +
				strings.Repeat("Background reference entry for this test. ", 800),
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
		var response messageResponse
		if err := json.Unmarshal(output, &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	messages := []map[string]any{{"role": "user", "content": "Call the Lookup tool with key fruit."}}
	first := post(messages)
	var tool responseBlock
	for _, block := range first.Content {
		if block.Type == "tool_use" {
			tool = block
		}
	}
	if first.StopReason == nil || *first.StopReason != "tool_use" || tool.Name != "Lookup" || tool.ID == "" {
		t.Fatalf("expected a completed Lookup handoff, got %+v", first)
	}
	checkJSON(t, tool.Input, "key", "fruit")
	t.Logf("tool handoff usage %+v", first.Usage)
	messages = append(messages,
		map[string]any{"role": "assistant", "content": first.Content},
		map[string]any{"role": "user", "content": []map[string]string{{
			"type": "tool_result", "tool_use_id": tool.ID, "content": "PAPAYA",
		}}},
	)
	response := post(messages)
	promptTokens := response.Usage.InputTokens + response.Usage.CacheReadInputTokens
	if response.StopReason == nil || *response.StopReason != "end_turn" ||
		len(response.Content) == 0 || response.Content[0].Text == nil ||
		!strings.Contains(strings.ToUpper(*response.Content[0].Text), "PAPAYA") ||
		promptTokens <= 0 {
		t.Fatalf("Codex did not continue using the tool result: %+v", response)
	}
	t.Logf("continuation usage %+v", response.Usage)
	// The continuation reports the input and cache counts of its last model request.
	if response.Usage.CacheReadInputTokens*5 <= promptTokens*4 {
		t.Fatalf("continuation cache_read_input_tokens = %d, want more than 80%% of the %d prompt tokens",
			response.Usage.CacheReadInputTokens, promptTokens)
	}
}

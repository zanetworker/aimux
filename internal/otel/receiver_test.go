package otel

import (
	"bytes"
	"fmt"
	"net/http"
	"testing"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

func TestReceiver_StartAndStop(t *testing.T) {
	store := NewSpanStore()
	port := 14318 // use non-standard port for testing
	receiver := NewReceiver(store, port)

	if err := receiver.Start(); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	defer receiver.Stop()

	// Give server a moment to start
	time.Sleep(50 * time.Millisecond)

	// Verify it's listening
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/traces", port))
	if err != nil {
		t.Fatalf("GET /v1/traces error: %v", err)
	}
	defer resp.Body.Close()

	// GET should return 405 (we only accept POST)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}

	if receiver.Port() != port {
		t.Errorf("Port() = %d, want %d", receiver.Port(), port)
	}
}

func TestReceiver_InvalidPayload(t *testing.T) {
	store := NewSpanStore()
	port := 14319
	receiver := NewReceiver(store, port)

	if err := receiver.Start(); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	defer receiver.Stop()

	time.Sleep(50 * time.Millisecond)

	// Send invalid protobuf
	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/v1/traces", port),
		"application/x-protobuf",
		nil,
	)
	if err != nil {
		t.Fatalf("POST error: %v", err)
	}
	defer resp.Body.Close()

	// Should return 400 for invalid/empty protobuf
	// (empty body is valid empty protobuf, so this may return 200)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST empty body status = %d", resp.StatusCode)
	}
}

// TestReceiver_LogsEndToEnd sends a real protobuf ExportLogsServiceRequest
// (simulating what Claude Code sends) to the /v1/logs endpoint and verifies
// the full pipeline: HTTP POST → handleLogs → logRecordToSpan → store.Add →
// GetByConversation → SpansToTurns.
func TestReceiver_LogsEndToEnd(t *testing.T) {
	store := NewSpanStore()
	port := 14320
	receiver := NewReceiver(store, port)

	if err := receiver.Start(); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	defer receiver.Stop()
	time.Sleep(50 * time.Millisecond)

	sessionID := "test-session-e2e"
	now := time.Now()

	// Build a realistic ExportLogsServiceRequest like Claude Code sends
	req := &collectorlogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{
			{
				Resource: &resourcepb.Resource{
					Attributes: []*commonpb.KeyValue{
						{Key: "service.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "claude-code"}}},
					},
				},
				ScopeLogs: []*logspb.ScopeLogs{
					{
						LogRecords: []*logspb.LogRecord{
							// User prompt event
							{
								TimeUnixNano: uint64(now.UnixNano()),
								EventName:    "claude_code.user_prompt",
								Attributes: []*commonpb.KeyValue{
									{Key: "session.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: sessionID}}},
									{Key: "prompt.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "prompt-1"}}},
									{Key: "prompt", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "fix the otel bug"}}},
								},
							},
							// API request event (same prompt.id = same turn)
							{
								TimeUnixNano: uint64(now.Add(1 * time.Second).UnixNano()),
								EventName:    "claude_code.api_request",
								Attributes: []*commonpb.KeyValue{
									{Key: "session.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: sessionID}}},
									{Key: "prompt.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "prompt-1"}}},
									{Key: "model", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "claude-opus-4-6"}}},
									{Key: "input_tokens", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 5000}}},
									{Key: "output_tokens", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 1200}}},
								},
							},
							// Tool result event (same prompt.id = same turn)
							{
								TimeUnixNano: uint64(now.Add(2 * time.Second).UnixNano()),
								EventName:    "claude_code.tool_result",
								Attributes: []*commonpb.KeyValue{
									{Key: "session.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: sessionID}}},
									{Key: "prompt.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "prompt-1"}}},
									{Key: "tool_name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "Read"}}},
									{Key: "success", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "true"}}},
								},
							},
						},
					},
				},
			},
		},
	}

	body, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("proto.Marshal error: %v", err)
	}

	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/v1/logs", port),
		"application/x-protobuf",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatalf("POST /v1/logs error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/logs status = %d, want 200", resp.StatusCode)
	}

	// Verify receiver stats
	traces, logs, _ := receiver.Stats()
	if logs != 1 {
		t.Errorf("receiver logs count = %d, want 1", logs)
	}
	if traces != 0 {
		t.Errorf("receiver traces count = %d, want 0", traces)
	}

	// Verify store has data
	if !store.HasData() {
		t.Fatal("store.HasData() = false after receiving logs")
	}

	// Verify data indexed by session/conversation ID
	root := store.GetByConversation(sessionID)
	if root == nil {
		t.Fatal("GetByConversation returned nil for session ID")
	}

	// Root is the first log event (user_prompt), children are subsequent events
	if root.Name != "claude_code.user_prompt" {
		t.Errorf("root.Name = %q, want claude_code.user_prompt", root.Name)
	}
	if len(root.Children) != 2 {
		t.Fatalf("root has %d children, want 2 (api_request + tool_result)", len(root.Children))
	}

	// Verify enrichment happened (logRecordToSpan normalizes Claude event attrs)
	if root.AttrStr("gen_ai.operation.name") != "invoke_agent" {
		t.Errorf("root gen_ai.operation.name = %q, want invoke_agent", root.AttrStr("gen_ai.operation.name"))
	}
	if root.AttrStr("gen_ai.input.messages") != "fix the otel bug" {
		t.Errorf("root gen_ai.input.messages = %q, want 'fix the otel bug'", root.AttrStr("gen_ai.input.messages"))
	}

	// Verify api_request child has model info
	apiChild := root.Children[0]
	if apiChild.AttrStr("gen_ai.request.model") != "claude-opus-4-6" {
		t.Errorf("api child model = %q, want claude-opus-4-6", apiChild.AttrStr("gen_ai.request.model"))
	}

	// Verify tool_result child
	toolChild := root.Children[1]
	if toolChild.AttrStr("gen_ai.tool.name") != "Read" {
		t.Errorf("tool child name = %q, want Read", toolChild.AttrStr("gen_ai.tool.name"))
	}

	// Verify the full pipeline: SpansToTurns groups by prompt.id
	// All 3 events share prompt.id="prompt-1" so they become 1 turn
	turns := SpansToTurns(root)
	if len(turns) != 1 {
		t.Fatalf("SpansToTurns returned %d turns, want 1 (all events share prompt.id)", len(turns))
	}

	// The single turn aggregates data from all events
	if len(turns[0].UserLines) == 0 || turns[0].UserLines[0] != "fix the otel bug" {
		t.Errorf("turn[0].UserLines = %v, want [fix the otel bug]", turns[0].UserLines)
	}
	if turns[0].Model != "claude-opus-4-6" {
		t.Errorf("turn[0].Model = %q, want claude-opus-4-6", turns[0].Model)
	}
	// TokensIn: logRecordToSpan copies raw "input_tokens" AND sets
	// "gen_ai.usage.input_tokens", converter picks gen_ai.usage.* first
	if turns[0].TokensIn != 5000 {
		t.Errorf("turn[0].TokensIn = %d, want 5000", turns[0].TokensIn)
	}
	if len(turns[0].Actions) != 1 || turns[0].Actions[0].Name != "Read" {
		t.Errorf("turn[0].Actions = %v, want 1 action (Read)", turns[0].Actions)
	}
}

// TestReceiver_FallbackEndToEnd verifies that the "/" fallback handler
// correctly processes log payloads (for Gemini CLI which may send to "/" ).
func TestReceiver_FallbackEndToEnd(t *testing.T) {
	store := NewSpanStore()
	port := 14321
	receiver := NewReceiver(store, port)

	if err := receiver.Start(); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	defer receiver.Stop()
	time.Sleep(50 * time.Millisecond)

	sessionID := "gemini-fallback-session"
	now := time.Now()

	req := &collectorlogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{
			{
				ScopeLogs: []*logspb.ScopeLogs{
					{
						LogRecords: []*logspb.LogRecord{
							{
								TimeUnixNano: uint64(now.UnixNano()),
								EventName:    "user_prompt",
								Attributes: []*commonpb.KeyValue{
									{Key: "session.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: sessionID}}},
								},
							},
						},
					},
				},
			},
		},
	}

	body, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("proto.Marshal error: %v", err)
	}

	// Send to "/" instead of "/v1/logs"
	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/", port),
		"application/x-protobuf",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatalf("POST / error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST / status = %d, want 200", resp.StatusCode)
	}

	if !store.HasData() {
		t.Fatal("store should have data after fallback log processing")
	}

	root := store.GetByConversation(sessionID)
	if root == nil {
		t.Fatal("GetByConversation returned nil after fallback processing")
	}
}

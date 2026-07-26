package otel

import (
	"bytes"
	"fmt"
	"net/http"
	"testing"
	"time"

	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

// TestSandboxPipeline_ResourceAttributeAlias verifies the full pipeline
// for remote sandbox OTEL traces:
//
//  1. Claude Code sends OTEL logs with aimux.session_id in resource attributes
//     (via OTEL_RESOURCE_ATTRIBUTES env var)
//  2. The receiver copies resource attrs to span attrs
//  3. The store creates an alias from aimux.session_id → conversation root
//  4. GetByConversation(aimuxSessionID) returns the root span
//  5. SpansToTurns produces correct turns
//
// This test simulates what actually happens in a sandbox: Claude Code
// sends to the receiver with aimux.session_id in the protobuf resource
// attributes (NOT in the query string), because the forwarder proxies
// the request without adding query params.
func TestSandboxPipeline_ResourceAttributeAlias(t *testing.T) {
	store := NewSpanStore()
	port := 14326
	receiver := NewReceiver(store, port)
	receiver.SetBindAll(true)

	if err := receiver.Start(); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	defer receiver.Stop()
	time.Sleep(50 * time.Millisecond)

	aimuxSessionID := "aimux-remote-claude-1234567890"
	claudeSessionID := "832b8c06-135f-4a75-bc67-58067fd4189f"

	// Simulate what Claude Code actually sends: resource attributes include
	// aimux.session_id (from OTEL_RESOURCE_ATTRIBUTES), and each log record
	// has session.id as a log record attribute.
	payload := &collectorlogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{
					{Key: "aimux.session_id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: aimuxSessionID}}},
					{Key: "service.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "claude-code"}}},
					{Key: "service.version", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "2.1.140"}}},
				},
			},
			ScopeLogs: []*logspb.ScopeLogs{{
				LogRecords: []*logspb.LogRecord{
					{
						TimeUnixNano: uint64(time.Now().UnixNano()),
						EventName:    "claude_code.api_request",
						Attributes: []*commonpb.KeyValue{
							{Key: "session.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: claudeSessionID}}},
							{Key: "prompt.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "p1"}}},
							{Key: "model", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "claude-opus-4-7"}}},
							{Key: "input_tokens", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 5000}}},
							{Key: "output_tokens", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 100}}},
						},
					},
					{
						TimeUnixNano: uint64(time.Now().Add(time.Second).UnixNano()),
						EventName:    "claude_code.user_prompt",
						Attributes: []*commonpb.KeyValue{
							{Key: "session.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: claudeSessionID}}},
							{Key: "prompt.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "p1"}}},
							{Key: "prompt", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "what is the capital of France"}}},
						},
					},
				},
			}},
		}},
	}

	body, err := proto.Marshal(payload)
	if err != nil {
		t.Fatalf("proto.Marshal error: %v", err)
	}

	// Send WITHOUT query params — this is what happens through the forwarder
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/logs", port)
	resp, err := http.Post(url, "application/x-protobuf", bytes.NewReader(body)) //nolint:gosec // test URL from localhost
	if err != nil {
		t.Fatalf("POST error: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	// Verify: lookup by aimux session ID (what parserForRemote does)
	root := store.GetByConversation(aimuxSessionID)
	if root == nil {
		t.Fatalf("GetByConversation(%q) returned nil — alias not created from resource attributes", aimuxSessionID)
	}

	// Verify: also reachable by Claude's own session ID
	rootByClaudeID := store.GetByConversation(claudeSessionID)
	if rootByClaudeID == nil {
		t.Fatal("GetByConversation(claudeSessionID) returned nil")
	}

	// Verify: SpansToTurns produces turns
	turns := SpansToTurns(root)
	if len(turns) == 0 {
		t.Fatal("SpansToTurns returned 0 turns")
	}
	if turns[0].Model != "claude-opus-4-7" {
		t.Errorf("turn 0 model = %q, want claude-opus-4-7", turns[0].Model)
	}

	// Verify: aimux.session_id is in the span attributes
	aimuxAttr := root.AttrStr("aimux.session_id")
	if aimuxAttr != aimuxSessionID {
		t.Errorf("root.AttrStr(aimux.session_id) = %q, want %q", aimuxAttr, aimuxSessionID)
	}
}

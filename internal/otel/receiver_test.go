package otel

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/subagent"
	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
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
	defer func() { _ = resp.Body.Close() }()

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
	defer func() { _ = resp.Body.Close() }()

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
	defer func() { _ = resp.Body.Close() }()

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

func TestReceiver_LogsQueryParamSessionAlias(t *testing.T) {
	store := NewSpanStore()
	receiver := NewReceiver(store, 0)
	claudeSessionID := "claude-session-qp"
	aimuxSessionID := "aimux-remote-claude-qp-test"

	payload := &collectorlogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			ScopeLogs: []*logspb.ScopeLogs{{
				LogRecords: []*logspb.LogRecord{{
					TimeUnixNano: uint64(time.Now().UnixNano()),
					EventName:    "claude_code.user_prompt",
					Attributes: []*commonpb.KeyValue{
						{Key: "session.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: claudeSessionID}}},
						{Key: "prompt.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "prompt-1"}}},
						{Key: "prompt", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "proxy-proof test"}}},
					},
				}},
			}},
		}},
	}
	body, err := proto.Marshal(payload)
	if err != nil {
		t.Fatalf("proto.Marshal error: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/logs?aimux_session="+aimuxSessionID, bytes.NewReader(body))
	recorder := httptest.NewRecorder()

	receiver.handleLogs(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("POST /v1/logs status = %d, want 200", recorder.Code)
	}
	root := store.GetByConversation(aimuxSessionID)
	if root == nil {
		t.Fatal("GetByConversation returned nil for the query param session ID")
	}
	if got := root.AttrStr("aimux.session_id"); got != aimuxSessionID {
		t.Errorf("aimux.session_id = %q, want %q", got, aimuxSessionID)
	}
	turns := SpansToTurns(root)
	if len(turns) != 1 || len(turns[0].UserLines) != 1 || turns[0].UserLines[0] != "proxy-proof test" {
		t.Errorf("SpansToTurns = %#v, want one turn with 'proxy-proof test'", turns)
	}
}

func TestReceiver_LogsQueryParamTakesPrecedenceOverHeader(t *testing.T) {
	store := NewSpanStore()
	receiver := NewReceiver(store, 0)

	payload := &collectorlogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			ScopeLogs: []*logspb.ScopeLogs{{
				LogRecords: []*logspb.LogRecord{{
					TimeUnixNano: uint64(time.Now().UnixNano()),
					EventName:    "claude_code.user_prompt",
					Attributes: []*commonpb.KeyValue{
						{Key: "session.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "claude-x"}}},
					},
				}},
			}},
		}},
	}
	body, err := proto.Marshal(payload)
	if err != nil {
		t.Fatalf("proto.Marshal error: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/logs?aimux_session=from-query", bytes.NewReader(body))
	req.Header.Set("X-Aimux-Session-Id", "from-header")
	recorder := httptest.NewRecorder()

	receiver.handleLogs(recorder, req)

	if store.GetByConversation("from-query") == nil {
		t.Fatal("query param session ID should be indexed")
	}
	if store.GetByConversation("from-header") != nil {
		t.Fatal("header should not override query param")
	}
}

func TestReceiver_LogsHeaderSessionAlias(t *testing.T) {
	store := NewSpanStore()
	receiver := NewReceiver(store, 0)
	claudeSessionID := "claude-session"
	aimuxSessionID := "aimux-remote-claude-123"

	payload := &collectorlogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			ScopeLogs: []*logspb.ScopeLogs{{
				LogRecords: []*logspb.LogRecord{{
					TimeUnixNano: uint64(time.Now().UnixNano()),
					EventName:    "claude_code.user_prompt",
					Attributes: []*commonpb.KeyValue{
						{Key: "session.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: claudeSessionID}}},
						{Key: "prompt.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "prompt-1"}}},
						{Key: "prompt", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "show remote traces"}}},
					},
				}},
			}},
		}},
	}
	body, err := proto.Marshal(payload)
	if err != nil {
		t.Fatalf("proto.Marshal error: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/logs", bytes.NewReader(body))
	req.Header.Set("X-Aimux-Session-Id", aimuxSessionID)
	recorder := httptest.NewRecorder()

	receiver.handleLogs(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("POST /v1/logs status = %d, want 200", recorder.Code)
	}
	root := store.GetByConversation(aimuxSessionID)
	if root == nil {
		t.Fatal("GetByConversation returned nil for the header session ID")
	}
	if got := root.AttrStr("aimux.session_id"); got != aimuxSessionID {
		t.Errorf("aimux.session_id = %q, want %q", got, aimuxSessionID)
	}
	turns := SpansToTurns(root)
	if len(turns) != 1 || len(turns[0].UserLines) != 1 || turns[0].UserLines[0] != "show remote traces" {
		t.Errorf("SpansToTurns(root) = %#v, want one remote prompt turn", turns)
	}
}

func TestReceiver_LogsResourceSessionTakesPrecedenceOverHeader(t *testing.T) {
	store := NewSpanStore()
	receiver := NewReceiver(store, 0)
	payload := &collectorlogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				{Key: "aimux.session_id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "resource-session"}}},
			}},
			ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{{
				TimeUnixNano: uint64(time.Now().UnixNano()),
				EventName:    "claude_code.user_prompt",
				Attributes: []*commonpb.KeyValue{
					{Key: "session.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "claude-session"}}},
				},
			}}}},
		}},
	}
	body, err := proto.Marshal(payload)
	if err != nil {
		t.Fatalf("proto.Marshal error: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/logs", bytes.NewReader(body))
	req.Header.Set("X-Aimux-Session-Id", "header-session")
	recorder := httptest.NewRecorder()

	receiver.handleLogs(recorder, req)

	if store.GetByConversation("resource-session") == nil {
		t.Fatal("resource session alias was not indexed")
	}
	if store.GetByConversation("header-session") != nil {
		t.Fatal("header session must not override the resource session")
	}
}

// TestReceiver_FallbackEndToEnd verifies that the "/" fallback handler
// correctly processes log payloads (for agents which may send to "/" ).
func TestReceiver_FallbackEndToEnd(t *testing.T) {
	store := NewSpanStore()
	port := 14321
	receiver := NewReceiver(store, port)

	if err := receiver.Start(); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	defer receiver.Stop()
	time.Sleep(50 * time.Millisecond)

	sessionID := "codex-fallback-session"
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
	defer func() { _ = resp.Body.Close() }()

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

func TestReceiver_FallbackHeaderSessionAlias(t *testing.T) {
	store := NewSpanStore()
	receiver := NewReceiver(store, 0)
	aimuxSessionID := "aimux-remote-codex-456"

	payload := &collectorlogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			ScopeLogs: []*logspb.ScopeLogs{{
				LogRecords: []*logspb.LogRecord{{
					TimeUnixNano: uint64(time.Now().UnixNano()),
					EventName:    "user_prompt",
					Attributes: []*commonpb.KeyValue{
						{Key: "session.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "codex-internal-id"}}},
					},
				}},
			}},
		}},
	}
	body, err := proto.Marshal(payload)
	if err != nil {
		t.Fatalf("proto.Marshal error: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("X-Aimux-Session-Id", aimuxSessionID)
	recorder := httptest.NewRecorder()

	receiver.handleFallback(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("POST / status = %d, want 200", recorder.Code)
	}
	if store.GetByConversation(aimuxSessionID) == nil {
		t.Fatal("fallback handler did not create alias from X-Aimux-Session-Id header")
	}
}

// TestReceiver_RemoteSandboxE2E simulates the full aimux remote agent flow:
// 1. Receiver starts on a port (like aimux does on startup)
// 2. A sandbox sends OTEL logs with ?aimux_session= query param (proxy-proof)
// 3. The store indexes by Claude's session.id AND creates an alias for aimux_session
// 4. parserForRemote looks up by aimux_session → gets root → SpansToTurns → turns
// This is the exact chain that the TUI trace pane relies on.
func TestReceiver_RemoteSandboxE2E(t *testing.T) {
	store := NewSpanStore()
	port := 14325
	receiver := NewReceiver(store, port)
	receiver.SetBindAll(true)

	if err := receiver.Start(); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	defer receiver.Stop()
	time.Sleep(50 * time.Millisecond)

	aimuxSessionID := "aimux-remote-claude-1234567890"
	claudeSessionID := "a1b2c3d4-e5f6-7890-abcd-ef1234567890"
	now := time.Now()

	// Simulate two turns from a sandbox Claude session
	for i, turn := range []struct {
		promptID string
		prompt   string
		model    string
		inTok    int64
		outTok   int64
		tool     string
	}{
		{"p1", "fix the auth bug", "claude-sonnet-4-6", 3000, 800, "Read"},
		{"p2", "now write tests", "claude-sonnet-4-6", 5000, 1500, "Write"},
	} {
		payload := &collectorlogspb.ExportLogsServiceRequest{
			ResourceLogs: []*logspb.ResourceLogs{{
				ScopeLogs: []*logspb.ScopeLogs{{
					LogRecords: []*logspb.LogRecord{
						{
							TimeUnixNano: uint64(now.Add(time.Duration(i*10) * time.Second).UnixNano()),
							EventName:    "claude_code.user_prompt",
							Attributes: []*commonpb.KeyValue{
								{Key: "session.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: claudeSessionID}}},
								{Key: "prompt.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: turn.promptID}}},
								{Key: "prompt", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: turn.prompt}}},
							},
						},
						{
							TimeUnixNano: uint64(now.Add(time.Duration(i*10+1) * time.Second).UnixNano()),
							EventName:    "claude_code.api_request",
							Attributes: []*commonpb.KeyValue{
								{Key: "session.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: claudeSessionID}}},
								{Key: "prompt.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: turn.promptID}}},
								{Key: "model", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: turn.model}}},
								{Key: "input_tokens", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: turn.inTok}}},
								{Key: "output_tokens", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: turn.outTok}}},
							},
						},
						{
							TimeUnixNano: uint64(now.Add(time.Duration(i*10+2) * time.Second).UnixNano()),
							EventName:    "claude_code.tool_result",
							Attributes: []*commonpb.KeyValue{
								{Key: "session.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: claudeSessionID}}},
								{Key: "prompt.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: turn.promptID}}},
								{Key: "tool_name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: turn.tool}}},
								{Key: "success", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "true"}}},
							},
						},
					},
				}},
			}},
		}

		body, err := proto.Marshal(payload)
		if err != nil {
			t.Fatalf("turn %d: proto.Marshal error: %v", i, err)
		}
		url := fmt.Sprintf("http://127.0.0.1:%d/v1/logs?aimux_session=%s", port, aimuxSessionID)
		resp, err := http.Post(url, "application/x-protobuf", bytes.NewReader(body)) //nolint:gosec // test URL from localhost
		if err != nil {
			t.Fatalf("turn %d: POST error: %v", i, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("turn %d: status = %d", i, resp.StatusCode)
		}
	}

	// Verify: lookup by aimux session ID (what parserForRemote does)
	root := store.GetByConversation(aimuxSessionID)
	if root == nil {
		t.Fatal("store.GetByConversation(aimuxSessionID) returned nil — alias not created")
	}

	// Verify: also reachable by Claude's own session ID
	rootByClaudeID := store.GetByConversation(claudeSessionID)
	if rootByClaudeID == nil {
		t.Fatal("store.GetByConversation(claudeSessionID) returned nil")
	}

	// Verify: SpansToTurns produces 2 turns with correct data
	turns := SpansToTurns(root)
	if len(turns) != 2 {
		t.Fatalf("SpansToTurns returned %d turns, want 2", len(turns))
	}
	if turns[0].UserLines[0] != "fix the auth bug" {
		t.Errorf("turn 0 prompt = %q, want 'fix the auth bug'", turns[0].UserLines[0])
	}
	if turns[1].UserLines[0] != "now write tests" {
		t.Errorf("turn 1 prompt = %q, want 'now write tests'", turns[1].UserLines[0])
	}
	if turns[0].Model != "claude-sonnet-4-6" {
		t.Errorf("turn 0 model = %q, want claude-sonnet-4-6", turns[0].Model)
	}
	if turns[0].TokensIn != 3000 {
		t.Errorf("turn 0 tokens_in = %d, want 3000", turns[0].TokensIn)
	}
	if turns[1].TokensIn != 5000 {
		t.Errorf("turn 1 tokens_in = %d, want 5000", turns[1].TokensIn)
	}
	if len(turns[0].Actions) != 1 || turns[0].Actions[0].Name != "Read" {
		t.Errorf("turn 0 actions = %v, want [Read]", turns[0].Actions)
	}
	if len(turns[1].Actions) != 1 || turns[1].Actions[0].Name != "Write" {
		t.Errorf("turn 1 actions = %v, want [Write]", turns[1].Actions)
	}

	// Verify receiver stats
	_, logs, _ := receiver.Stats()
	if logs != 2 {
		t.Errorf("receiver logged %d requests, want 2", logs)
	}
}

func TestEnrichSubagent(t *testing.T) {
	store := NewSpanStore()
	keys := map[string]subagent.AttrKeys{
		"claude-code": {ID: "agent_id", Type: "agent_type", ParentID: "parent_agent_id"},
	}
	r := NewReceiverWithKeys(store, 0, keys)
	span := &Span{
		SpanID: "s1",
		Attrs: map[string]any{
			"service.name": "claude-code", "agent_id": "sub-1",
			"agent_type": "Explore", "parent_agent_id": "main-0",
		},
	}
	r.enrichSubagent(span)
	if span.Subagent.ID != "sub-1" {
		t.Errorf("Subagent.ID = %q, want %q", span.Subagent.ID, "sub-1")
	}
	if span.Subagent.Type != "Explore" {
		t.Errorf("Subagent.Type = %q, want %q", span.Subagent.Type, "Explore")
	}
	if span.Subagent.ParentID != "main-0" {
		t.Errorf("Subagent.ParentID = %q, want %q", span.Subagent.ParentID, "main-0")
	}
}

func TestEnrichSubagentUnknownService(t *testing.T) {
	store := NewSpanStore()
	keys := map[string]subagent.AttrKeys{"claude-code": {ID: "agent_id", Type: "agent_type"}}
	r := NewReceiverWithKeys(store, 0, keys)
	span := &Span{SpanID: "s1", Attrs: map[string]any{"service.name": "unknown", "agent_id": "sub-1"}}
	r.enrichSubagent(span)
	if span.Subagent.ID != "" {
		t.Errorf("unknown service should not extract, got ID=%q", span.Subagent.ID)
	}
}

func TestEnrichSubagentNilKeys(t *testing.T) {
	store := NewSpanStore()
	r := NewReceiver(store, 0)
	span := &Span{SpanID: "s1", Attrs: map[string]any{"service.name": "claude-code", "agent_id": "sub-1"}}
	r.enrichSubagent(span)
	if span.Subagent.ID != "" {
		t.Errorf("nil keysByService should not extract, got ID=%q", span.Subagent.ID)
	}
}

func TestHandleHooks(t *testing.T) {
	store := NewSpanStore()
	port := 14322
	r := NewReceiver(store, port)

	if err := r.Start(); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	defer r.Stop()
	time.Sleep(50 * time.Millisecond)

	payload := `{"session_id":"sess-1","hook_event_name":"tool_result","tool_name":"Read","tool_use_id":"tu-123","agent_id":"agent-A","agent_type":"Explore"}`
	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/v1/hooks", port),
		"application/json",
		bytes.NewBufferString(payload),
	)
	if err != nil {
		t.Fatalf("POST /v1/hooks error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/hooks status = %d, want 200", resp.StatusCode)
	}

	if !store.HasData() {
		t.Fatal("store should have data after hook")
	}

	root := store.GetByConversation("sess-1")
	if root == nil {
		t.Fatal("GetByConversation returned nil for hook session")
	}
	if root.Subagent.ID != "agent-A" {
		t.Errorf("Subagent.ID = %q, want %q", root.Subagent.ID, "agent-A")
	}
	if root.Subagent.Type != "Explore" {
		t.Errorf("Subagent.Type = %q, want %q", root.Subagent.Type, "Explore")
	}
	if root.AttrStr("gen_ai.tool.name") != "Read" {
		t.Errorf("tool name = %q, want Read", root.AttrStr("gen_ai.tool.name"))
	}
}

func TestHandleHooksMethodNotAllowed(t *testing.T) {
	store := NewSpanStore()
	port := 14323
	r := NewReceiver(store, port)

	if err := r.Start(); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	defer r.Stop()
	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/hooks", port))
	if err != nil {
		t.Fatalf("GET /v1/hooks error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /v1/hooks status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}
}

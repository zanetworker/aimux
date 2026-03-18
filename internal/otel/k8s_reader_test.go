package otel

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// Compile-time check that K8sReader has the expected methods.
var _ interface {
	Start() error
	Stop()
	Endpoint() string
	Stats() (int, int, time.Time, error)
} = (*K8sReader)(nil)

func TestNewK8sReader(t *testing.T) {
	store := NewSpanStore()
	reader := NewK8sReader("http://localhost:4318", store)

	if reader == nil {
		t.Fatal("NewK8sReader returned nil")
	}
	if reader.Endpoint() != "http://localhost:4318" {
		t.Errorf("Endpoint() = %q, want %q", reader.Endpoint(), "http://localhost:4318")
	}
	if reader.store != store {
		t.Error("reader.store should be the provided store")
	}
	if reader.client == nil {
		t.Error("reader.client should not be nil")
	}
}

func TestK8sReader_StartStop_NoPanic(t *testing.T) {
	store := NewSpanStore()
	// Use a server that returns empty responses so the reader doesn't error
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{}"))
	}))
	defer ts.Close()

	reader := NewK8sReader(ts.URL, store)

	if err := reader.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Let it poll at least once
	time.Sleep(100 * time.Millisecond)

	reader.Stop()

	// Verify stats were recorded
	polls, _, lastPoll, _ := reader.Stats()
	if polls == 0 {
		t.Error("expected at least one poll after Start")
	}
	if lastPoll.IsZero() {
		t.Error("lastPoll should be non-zero after polling")
	}
}

func TestK8sReader_DoubleStart(t *testing.T) {
	store := NewSpanStore()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{}"))
	}))
	defer ts.Close()

	reader := NewK8sReader(ts.URL, store)
	if err := reader.Start(); err != nil {
		t.Fatalf("first Start() error: %v", err)
	}
	defer reader.Stop()

	err := reader.Start()
	if err == nil {
		t.Error("second Start() should return an error")
	}
}

func TestK8sReader_StopWithoutStart(t *testing.T) {
	store := NewSpanStore()
	reader := NewK8sReader("http://localhost:4318", store)
	// Should not panic
	reader.Stop()
}

func TestK8sReader_ParsesTraceResponse(t *testing.T) {
	// Build a protobuf response with a real span
	traceID := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanID := []byte{1, 2, 3, 4, 5, 6, 7, 8}

	traceResp := &collectorpb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{
			{
				Resource: &resourcepb.Resource{
					Attributes: []*commonpb.KeyValue{
						{Key: "service.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "claude-code"}}},
					},
				},
				ScopeSpans: []*tracepb.ScopeSpans{
					{
						Spans: []*tracepb.Span{
							{
								TraceId:           traceID,
								SpanId:            spanID,
								Name:              "invoke_agent",
								StartTimeUnixNano: uint64(time.Now().UnixNano()),
								EndTimeUnixNano:   uint64(time.Now().UnixNano()),
								Attributes: []*commonpb.KeyValue{
									{Key: "gen_ai.conversation.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "k8s-session-1"}}},
								},
							},
						},
					},
				},
			},
		},
	}

	respBytes, err := proto.Marshal(traceResp)
	if err != nil {
		t.Fatalf("marshal trace response: %v", err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/traces" {
			w.WriteHeader(http.StatusOK)
			w.Write(respBytes)
		} else {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("{}"))
		}
	}))
	defer ts.Close()

	store := NewSpanStore()
	reader := NewK8sReader(ts.URL, store)

	if err := reader.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Wait for at least one poll
	time.Sleep(200 * time.Millisecond)
	reader.Stop()

	// Verify span was added to store
	expectedTraceID := hex.EncodeToString(traceID)
	spans := store.GetSpans(expectedTraceID)
	if len(spans) == 0 {
		t.Fatal("expected spans in store after polling trace endpoint")
	}

	if spans[0].Name != "invoke_agent" {
		t.Errorf("span name = %q, want %q", spans[0].Name, "invoke_agent")
	}

	// Verify stats
	_, spanCount, _, _ := reader.Stats()
	if spanCount == 0 {
		t.Error("expected non-zero span count")
	}
}

func TestK8sReader_ParsesLogResponse(t *testing.T) {
	// Build a logs response simulating Claude Code events
	logsResp := &collectorlogspb.ExportLogsServiceRequest{
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
							{
								TimeUnixNano: uint64(time.Now().UnixNano()),
								Body: &commonpb.AnyValue{
									Value: &commonpb.AnyValue_StringValue{StringValue: "user_prompt"},
								},
								Attributes: []*commonpb.KeyValue{
									{Key: "session.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "k8s-log-session"}}},
									{Key: "prompt", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "fix the bug"}}},
								},
							},
						},
					},
				},
			},
		},
	}

	respBytes, err := proto.Marshal(logsResp)
	if err != nil {
		t.Fatalf("marshal logs response: %v", err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/logs" {
			w.WriteHeader(http.StatusOK)
			w.Write(respBytes)
		} else {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("{}"))
		}
	}))
	defer ts.Close()

	store := NewSpanStore()
	reader := NewK8sReader(ts.URL, store)

	if err := reader.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	reader.Stop()

	// Verify the log event was stored as a span
	root := store.GetByConversation("k8s-log-session")
	if root == nil {
		t.Fatal("expected span stored for k8s-log-session conversation")
	}

	if root.Name != "user_prompt" {
		t.Errorf("span name = %q, want %q", root.Name, "user_prompt")
	}
}

func TestK8sReader_HandlesServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer ts.Close()

	store := NewSpanStore()
	reader := NewK8sReader(ts.URL, store)

	if err := reader.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	reader.Stop()

	// Should have recorded an error but not panicked
	_, _, _, lastErr := reader.Stats()
	if lastErr == nil {
		t.Error("expected a non-nil lastError after server 500")
	}
}

func TestK8sReader_HandlesUnreachableEndpoint(t *testing.T) {
	store := NewSpanStore()
	// Use a port that's almost certainly not listening
	reader := NewK8sReader(fmt.Sprintf("http://127.0.0.1:%d", 19999), store)

	if err := reader.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	reader.Stop()

	// Should have recorded a connection error
	_, _, _, lastErr := reader.Stats()
	if lastErr == nil {
		t.Error("expected a non-nil lastError for unreachable endpoint")
	}
}

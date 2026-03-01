package otel

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// Receiver is an OTLP/HTTP trace receiver that listens for incoming spans
// from Claude Code, Codex CLI, Gemini CLI, or any OTEL-instrumented agent.
// It stores spans in a SpanStore for the TUI to display.
type Receiver struct {
	store  *SpanStore
	server *http.Server
	port   int
}

// NewReceiver creates a new OTLP/HTTP receiver.
func NewReceiver(store *SpanStore, port int) *Receiver {
	return &Receiver{
		store: store,
		port:  port,
	}
}

// Start begins listening for OTLP/HTTP trace data on the configured port.
// Non-blocking -- runs the server in a goroutine.
func (r *Receiver) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/traces", r.handleTraces)

	r.server = &http.Server{
		Addr:         fmt.Sprintf("127.0.0.1:%d", r.port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		if err := r.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			// Log but don't crash -- the TUI continues without OTEL
			_ = err
		}
	}()

	return nil
}

// Stop shuts down the receiver.
func (r *Receiver) Stop() {
	if r.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		r.server.Shutdown(ctx)
	}
}

// Port returns the configured port.
func (r *Receiver) Port() int {
	return r.port
}

// handleTraces processes incoming OTLP/HTTP POST /v1/traces requests.
// Accepts protobuf-encoded ExportTraceServiceRequest.
func (r *Receiver) handleTraces(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer req.Body.Close()

	var traceReq collectorpb.ExportTraceServiceRequest
	if err := proto.Unmarshal(body, &traceReq); err != nil {
		http.Error(w, "invalid protobuf", http.StatusBadRequest)
		return
	}

	// Process and store spans
	for _, resourceSpans := range traceReq.ResourceSpans {
		// Extract resource attributes (service name, etc.)
		resourceAttrs := make(map[string]any)
		if resourceSpans.Resource != nil {
			for _, kv := range resourceSpans.Resource.Attributes {
				resourceAttrs[kv.Key] = extractValue(kv.Value)
			}
		}

		for _, scopeSpans := range resourceSpans.ScopeSpans {
			for _, protoSpan := range scopeSpans.Spans {
				span := protoSpanToSpan(protoSpan, resourceAttrs)
				r.store.Add(span)
			}
		}

		// Assemble trees for all traces we received
		traceIDs := make(map[string]bool)
		for _, scopeSpans := range resourceSpans.ScopeSpans {
			for _, protoSpan := range scopeSpans.Spans {
				tid := hex.EncodeToString(protoSpan.TraceId)
				traceIDs[tid] = true
			}
		}
		for tid := range traceIDs {
			r.store.AssembleTree(tid)
		}
	}

	// Return success (empty ExportTraceServiceResponse)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("{}"))
}

// protoSpanToSpan converts an OTLP protobuf span to our internal Span type.
func protoSpanToSpan(ps *tracepb.Span, resourceAttrs map[string]any) *Span {
	attrs := make(map[string]any)

	// Copy resource attributes
	for k, v := range resourceAttrs {
		attrs[k] = v
	}

	// Copy span attributes
	for _, kv := range ps.Attributes {
		attrs[kv.Key] = extractValue(kv.Value)
	}

	status := StatusUnset
	if ps.Status != nil {
		switch ps.Status.Code {
		case tracepb.Status_STATUS_CODE_OK:
			status = StatusOK
		case tracepb.Status_STATUS_CODE_ERROR:
			status = StatusError
		}
	}

	return &Span{
		SpanID:   hex.EncodeToString(ps.SpanId),
		TraceID:  hex.EncodeToString(ps.TraceId),
		ParentID: hex.EncodeToString(ps.ParentSpanId),
		Name:     ps.Name,
		Start:    time.Unix(0, int64(ps.StartTimeUnixNano)),
		End:      time.Unix(0, int64(ps.EndTimeUnixNano)),
		Status:   status,
		Attrs:    attrs,
	}
}

// extractValue converts an OTLP AnyValue to a Go value.
func extractValue(v *commonpb.AnyValue) any {
	if v == nil {
		return nil
	}
	switch val := v.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return val.StringValue
	case *commonpb.AnyValue_IntValue:
		return val.IntValue
	case *commonpb.AnyValue_DoubleValue:
		return val.DoubleValue
	case *commonpb.AnyValue_BoolValue:
		return val.BoolValue
	default:
		return fmt.Sprintf("%v", v.Value)
	}
}

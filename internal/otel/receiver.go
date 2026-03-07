package otel

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/zanetworker/aimux/internal/subagent"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// Receiver is an OTLP/HTTP trace receiver that listens for incoming spans
// from Claude Code, Codex CLI, Gemini CLI, or any OTEL-instrumented agent.
// It stores spans in a SpanStore for the TUI to display.
type Receiver struct {
	store         *SpanStore
	server        *http.Server
	port          int
	keysByService map[string]subagent.AttrKeys

	mu         sync.Mutex // protects counters and debugLog
	traceCount int        // number of /v1/traces requests received
	logsCount  int        // number of /v1/logs requests received
	otherCount int        // number of other requests received
	debugLog   []string   // recent request log for diagnostics
}

// NewReceiverWithKeys creates a new OTLP/HTTP receiver with per-service
// subagent attribute keys for automatic extraction.
func NewReceiverWithKeys(store *SpanStore, port int, keys map[string]subagent.AttrKeys) *Receiver {
	return &Receiver{
		store:         store,
		port:          port,
		keysByService: keys,
	}
}

// NewReceiver creates a new OTLP/HTTP receiver.
func NewReceiver(store *SpanStore, port int) *Receiver {
	return NewReceiverWithKeys(store, port, nil)
}

// Start begins listening for OTLP/HTTP trace data on the configured port.
// Non-blocking -- runs the server in a goroutine.
func (r *Receiver) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/traces", r.handleTraces)
	mux.HandleFunc("/v1/logs", r.handleLogs)
	mux.HandleFunc("/v1/metrics", r.handleMetrics) // accept but ignore metrics
	mux.HandleFunc("/debug", r.handleDebug)        // diagnostic endpoint
	mux.HandleFunc("/v1/hooks", r.handleHooks)     // hook events from agents
	// Catch-all: Gemini may send to "/" instead of signal-specific paths
	// (known bug: github.com/google-gemini/gemini-cli/issues/15581)
	mux.HandleFunc("/", r.handleFallback)

	r.server = &http.Server{
		Addr:         fmt.Sprintf("127.0.0.1:%d", r.port),
		Handler:      r.loggingMiddleware(mux),
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

// loggingMiddleware records every incoming request for diagnostics.
func (r *Receiver) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		entry := fmt.Sprintf("%s %s %s cl=%d proto=%s",
			time.Now().Format("15:04:05"),
			req.Method, req.URL.Path,
			req.ContentLength, req.Proto)
		r.mu.Lock()
		r.debugLog = append(r.debugLog, entry)
		if len(r.debugLog) > 50 {
			r.debugLog = r.debugLog[len(r.debugLog)-50:]
		}
		r.mu.Unlock()
		next.ServeHTTP(w, req)
	})
}

// handleDebug returns receiver diagnostics as plain text.
// Access via: curl http://localhost:4318/debug
// Add ?events=1 to dump all stored events with attributes.
func (r *Receiver) handleDebug(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	traces, logs, other := r.traceCount, r.logsCount, r.otherCount
	logCopy := make([]string, len(r.debugLog))
	copy(logCopy, r.debugLog)
	r.mu.Unlock()

	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "aimux OTEL receiver debug\n")
	fmt.Fprintf(w, "port: %d\n", r.port)
	fmt.Fprintf(w, "traces: %d, logs: %d, other: %d\n", traces, logs, other)
	fmt.Fprintf(w, "store entries: %d\n", r.store.TraceCount())
	fmt.Fprintf(w, "store conversations: %s\n", strings.Join(r.store.ConversationIDs(), ", "))
	fmt.Fprintf(w, "\n--- recent requests (%d) ---\n", len(logCopy))
	for _, entry := range logCopy {
		fmt.Fprintf(w, "%s\n", entry)
	}

	// Dump events if requested
	if req.URL.Query().Get("events") == "1" {
		fmt.Fprintf(w, "\n--- stored events ---\n")
		for _, convID := range r.store.ConversationIDs() {
			root := r.store.GetByConversation(convID)
			if root == nil {
				continue
			}
			fmt.Fprintf(w, "\nconversation: %s\n", convID)
			dumpSpan(w, root, 0)
			for _, child := range root.Children {
				dumpSpan(w, child, 1)
			}
		}
	}
}

func dumpSpan(w http.ResponseWriter, s *Span, indent int) {
	prefix := strings.Repeat("  ", indent)
	fmt.Fprintf(w, "%s[%s] %s (id=%s)\n", prefix, s.Start.Format("15:04:05"), s.Name, s.SpanID)
	for k, v := range s.Attrs {
		fmt.Fprintf(w, "%s  %s = %v\n", prefix, k, v)
	}
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

// Stats returns request counts for debugging. Thread-safe.
func (r *Receiver) Stats() (traces, logs, other int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.traceCount, r.logsCount, r.otherCount
}

// handleTraces processes incoming OTLP/HTTP POST /v1/traces requests.
// Accepts protobuf-encoded ExportTraceServiceRequest.
func (r *Receiver) handleTraces(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.traceCount++
	r.mu.Unlock()
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	defer req.Body.Close()
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

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
				r.enrichSubagent(span)
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

// handleLogs processes incoming OTLP/HTTP POST /v1/logs requests.
// Claude Code exports events (user_prompt, tool_result, api_request) via
// the OTEL logs protocol. We convert these into our Span model so the
// trace viewer can display them.
func (r *Receiver) handleLogs(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.logsCount++
	r.mu.Unlock()
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	defer req.Body.Close()
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var logsReq collectorlogspb.ExportLogsServiceRequest
	if err := proto.Unmarshal(body, &logsReq); err != nil {
		http.Error(w, "invalid protobuf", http.StatusBadRequest)
		return
	}

	for _, resourceLogs := range logsReq.ResourceLogs {
		// Extract resource attributes
		resourceAttrs := make(map[string]any)
		if resourceLogs.Resource != nil {
			for _, kv := range resourceLogs.Resource.Attributes {
				resourceAttrs[kv.Key] = extractValue(kv.Value)
			}
		}

		for _, scopeLogs := range resourceLogs.ScopeLogs {
			for _, logRecord := range scopeLogs.LogRecords {
				span := logRecordToSpan(logRecord, resourceAttrs)
				if span != nil {
					r.enrichSubagent(span)
					r.store.Add(span)
				}
			}
		}
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("{}"))
}

// logRecordToSpan converts a Claude Code OTEL log event into a Span.
// Claude events have attributes like session.id, event.name, tool_name, etc.
func logRecordToSpan(lr *logspb.LogRecord, resourceAttrs map[string]any) *Span {
	attrs := make(map[string]any)
	for k, v := range resourceAttrs {
		attrs[k] = v
	}
	for _, kv := range lr.Attributes {
		attrs[kv.Key] = extractValue(kv.Value)
	}

	// Extract event name from multiple sources:
	// 1. LogRecord.EventName field (OTLP spec field 12)
	// 2. "event.name" attribute (Claude Code convention)
	// 3. Body string value as fallback
	eventName := lr.EventName
	if eventName == "" {
		eventName, _ = attrs["event.name"].(string)
	}
	if eventName == "" && lr.Body != nil {
		if sv, ok := lr.Body.Value.(*commonpb.AnyValue_StringValue); ok {
			eventName = sv.StringValue
		}
	}
	if eventName == "" {
		// Accept any log record even without event name -- store raw attributes
		eventName = "unknown_event"
	}

	// Use session.id as the conversation ID for store indexing
	sessionID, _ := attrs["session.id"].(string)
	if sessionID != "" {
		attrs["gen_ai.conversation.id"] = sessionID
	}

	ts := time.Unix(0, int64(lr.TimeUnixNano))

	// Map Claude events to our span model
	span := &Span{
		SpanID:  fmt.Sprintf("log-%d", lr.TimeUnixNano),
		TraceID: sessionID,
		Name:    eventName,
		Start:   ts,
		End:     ts,
		Status:  StatusOK,
		Attrs:   attrs,
	}

	// Normalize event name: strip "claude_code." prefix for matching
	shortName := eventName
	if idx := strings.LastIndex(eventName, "."); idx >= 0 {
		shortName = eventName[idx+1:]
	}

	// Enrich based on event type
	switch shortName {
	case "user_prompt":
		span.ParentID = ""
		attrs["gen_ai.operation.name"] = "invoke_agent"
		if prompt, ok := attrs["prompt"].(string); ok {
			attrs["gen_ai.input.messages"] = prompt
		}

	case "api_request":
		attrs["gen_ai.operation.name"] = "chat"
		if model, ok := attrs["model"].(string); ok {
			attrs["gen_ai.request.model"] = model
		}
		if tokens, ok := attrs["input_tokens"]; ok {
			attrs["gen_ai.usage.input_tokens"] = tokens
		}
		if tokens, ok := attrs["output_tokens"]; ok {
			attrs["gen_ai.usage.output_tokens"] = tokens
		}
		if cost, ok := attrs["cost_usd"]; ok {
			attrs["gen_ai.usage.cost"] = cost
		}

	case "tool_result":
		attrs["gen_ai.operation.name"] = "execute_tool"
		if toolName, ok := attrs["tool_name"].(string); ok {
			attrs["gen_ai.tool.name"] = toolName
		}
		if success, ok := attrs["success"].(string); ok && success == "false" {
			span.Status = StatusError
		}

	case "api_error":
		span.Status = StatusError

	case "tool_decision":
		attrs["gen_ai.operation.name"] = "tool_decision"
	}

	return span
}

// handleMetrics accepts but ignores metrics -- we don't process them but
// need to return 200 so the exporter doesn't error.
func (r *Receiver) handleMetrics(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodPost {
		io.ReadAll(req.Body)
		req.Body.Close()
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("{}"))
}

// handleFallback tries to parse the body as traces or logs.
// Gemini CLI may send to "/" instead of signal-specific paths.
// We detect the actual type by checking for non-empty inner records,
// since protobuf can cross-deserialize between trace and log request
// types (same field numbers) but only the correct type has inner data.
func (r *Receiver) handleFallback(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.otherCount++
	r.mu.Unlock()
	if req.Method != http.MethodPost {
		w.WriteHeader(http.StatusOK)
		return
	}

	defer req.Body.Close()
	body, err := io.ReadAll(req.Body)
	if err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Try as traces -- check for actual spans (not just non-empty containers)
	var traceReq collectorpb.ExportTraceServiceRequest
	if err := proto.Unmarshal(body, &traceReq); err == nil && hasValidSpans(&traceReq) {
		for _, rs := range traceReq.ResourceSpans {
			resourceAttrs := make(map[string]any)
			if rs.Resource != nil {
				for _, kv := range rs.Resource.Attributes {
					resourceAttrs[kv.Key] = extractValue(kv.Value)
				}
			}
			for _, ss := range rs.ScopeSpans {
				for _, ps := range ss.Spans {
					span := protoSpanToSpan(ps, resourceAttrs)
					r.enrichSubagent(span)
					r.store.Add(span)
				}
			}
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{}"))
		return
	}

	// Try as logs -- check for actual log records
	var logsReq collectorlogspb.ExportLogsServiceRequest
	if err := proto.Unmarshal(body, &logsReq); err == nil && hasValidLogRecords(&logsReq) {
		for _, rl := range logsReq.ResourceLogs {
			resourceAttrs := make(map[string]any)
			if rl.Resource != nil {
				for _, kv := range rl.Resource.Attributes {
					resourceAttrs[kv.Key] = extractValue(kv.Value)
				}
			}
			for _, sl := range rl.ScopeLogs {
				for _, lr := range sl.LogRecords {
					span := logRecordToSpan(lr, resourceAttrs)
					if span != nil {
						r.enrichSubagent(span)
						r.store.Add(span)
					}
				}
			}
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{}"))
		return
	}

	// Unknown format -- accept silently
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("{}"))
}

// hasValidSpans checks if a trace request has actual spans (not cross-deserialized
// log records). A valid span must have a non-empty TraceId.
func hasValidSpans(req *collectorpb.ExportTraceServiceRequest) bool {
	for _, rs := range req.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			for _, s := range ss.Spans {
				if len(s.TraceId) > 0 {
					return true
				}
			}
		}
	}
	return false
}

// hasValidLogRecords checks if a logs request has actual log records (not
// cross-deserialized spans). A valid log record must have a non-zero timestamp.
func hasValidLogRecords(req *collectorlogspb.ExportLogsServiceRequest) bool {
	for _, rl := range req.ResourceLogs {
		for _, sl := range rl.ScopeLogs {
			for _, lr := range sl.LogRecords {
				if lr.TimeUnixNano > 0 {
					return true
				}
			}
		}
	}
	return false
}

// enrichSubagent extracts subagent identity from span attributes using
// per-service attribute key mappings.
func (r *Receiver) enrichSubagent(span *Span) {
	if r.keysByService == nil {
		return
	}
	serviceName, _ := span.Attrs["service.name"].(string)
	keys, ok := r.keysByService[serviceName]
	if !ok || keys.Empty() {
		return
	}
	span.Subagent = keys.Extract(span.Attrs)
}

// hookPayload is the JSON body for POST /v1/hooks.
type hookPayload struct {
	SessionID string `json:"session_id"`
	HookEvent string `json:"hook_event_name"`
	ToolName  string `json:"tool_name"`
	ToolUseID string `json:"tool_use_id"`
	AgentID   string `json:"agent_id"`
	AgentType string `json:"agent_type"`
}

// handleHooks accepts hook events from agents and stores them as spans.
func (r *Receiver) handleHooks(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer req.Body.Close()
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	var h hookPayload
	if err := json.Unmarshal(body, &h); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	ts := time.Now()
	spanID := fmt.Sprintf("hook-%s", h.ToolUseID)
	if h.ToolUseID == "" {
		spanID = fmt.Sprintf("hook-%d", ts.UnixNano())
	}
	span := &Span{
		SpanID: spanID, TraceID: h.SessionID, Name: "tool_result",
		Start: ts, End: ts, Status: StatusOK,
		Attrs: map[string]any{
			"gen_ai.conversation.id": h.SessionID,
			"gen_ai.tool.name":       h.ToolName,
			"tool_use_id":            h.ToolUseID,
			"source":                 "hook",
		},
		Subagent: subagent.Info{ID: h.AgentID, Type: h.AgentType},
	}
	r.store.Add(span)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("{}"))
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

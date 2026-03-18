package otel

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/proto"
)

// K8sReader connects to a remote OTel Collector and imports spans into the
// local SpanStore. It periodically fetches new spans and merges them with
// locally-collected data. Only instantiated when config.Kubernetes.OTELEndpoint
// is set.
type K8sReader struct {
	endpoint string     // e.g., "http://<elb>:4318"
	store    *SpanStore // shared with local receiver
	client   *http.Client

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{} // closed when polling goroutine exits

	// stats
	pollCount  int
	spanCount  int
	lastPoll   time.Time
	lastError  error
}

// DefaultPollInterval is the time between successive polls to the remote
// OTel Collector.
const DefaultPollInterval = 5 * time.Second

// NewK8sReader creates a new K8sReader that will poll the given endpoint
// for remote agent traces and merge them into store.
func NewK8sReader(endpoint string, store *SpanStore) *K8sReader {
	return &K8sReader{
		endpoint: endpoint,
		store:    store,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Start begins polling the remote OTel Collector in a background goroutine.
// Non-blocking. Returns an error if the reader is already running.
func (r *K8sReader) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cancel != nil {
		return fmt.Errorf("k8s OTEL reader already running")
	}

	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.done = make(chan struct{})

	go r.pollLoop(ctx)
	return nil
}

// Stop stops the background polling goroutine and waits for it to exit.
func (r *K8sReader) Stop() {
	r.mu.Lock()
	cancel := r.cancel
	done := r.done
	r.cancel = nil
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// Endpoint returns the configured remote endpoint.
func (r *K8sReader) Endpoint() string {
	return r.endpoint
}

// Stats returns reader statistics. Thread-safe.
func (r *K8sReader) Stats() (polls, spans int, lastPoll time.Time, lastErr error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pollCount, r.spanCount, r.lastPoll, r.lastError
}

// pollLoop runs until ctx is cancelled, fetching spans at regular intervals.
func (r *K8sReader) pollLoop(ctx context.Context) {
	defer close(r.done)

	ticker := time.NewTicker(DefaultPollInterval)
	defer ticker.Stop()

	// Do an immediate first poll.
	r.poll(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.poll(ctx)
		}
	}
}

// poll fetches traces and logs from the remote collector's OTLP/HTTP endpoint.
// The OTel Collector exposes /v1/traces and /v1/logs receivers. We send an
// empty ExportTraceServiceRequest to trigger a response with any buffered data,
// and parse spans from the response if the collector supports it.
//
// In the standard OTel Collector architecture, the collector receives spans
// from agents and we read them. Since the collector's receiver endpoints
// accept POSTs (not GETs), we POST empty requests and parse any trace data
// the collector may echo back. For collectors that only log/forward, the
// response will be empty and we simply record the poll attempt.
func (r *K8sReader) poll(ctx context.Context) {
	r.mu.Lock()
	r.pollCount++
	r.lastPoll = time.Now()
	r.mu.Unlock()

	// Fetch traces
	tracesErr := r.fetchTraces(ctx)

	// Fetch logs (Claude Code sends events via logs protocol)
	logsErr := r.fetchLogs(ctx)

	// Record the first non-nil error for diagnostics
	r.mu.Lock()
	if tracesErr != nil {
		r.lastError = tracesErr
	} else if logsErr != nil {
		r.lastError = logsErr
	} else {
		r.lastError = nil
	}
	r.mu.Unlock()
}

// fetchTraces sends an empty ExportTraceServiceRequest to the collector
// and parses any spans from the response.
func (r *K8sReader) fetchTraces(ctx context.Context) error {
	url := r.endpoint + "/v1/traces"

	// Send an empty export request — the collector will process it and
	// return an ExportTraceServiceResponse. Some collector configurations
	// may echo received spans; others return an empty response.
	emptyReq := &collectorpb.ExportTraceServiceRequest{}
	body, err := proto.Marshal(emptyReq)
	if err != nil {
		return fmt.Errorf("marshal empty trace request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return fmt.Errorf("create trace request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Body = io.NopCloser(bytesReader(body))
	req.ContentLength = int64(len(body))

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch traces from %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.ReadAll(resp.Body) // drain
		return fmt.Errorf("traces endpoint returned %d", resp.StatusCode)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read traces response: %w", err)
	}

	// Try to parse as ExportTraceServiceRequest (if collector echoes data)
	if len(respBody) > 2 { // skip empty JSON "{}" or empty protobuf
		r.parseAndStoreTraces(respBody)
	}

	return nil
}

// fetchLogs sends an empty ExportLogsServiceRequest to the collector.
func (r *K8sReader) fetchLogs(ctx context.Context) error {
	url := r.endpoint + "/v1/logs"

	emptyReq := &collectorlogspb.ExportLogsServiceRequest{}
	body, err := proto.Marshal(emptyReq)
	if err != nil {
		return fmt.Errorf("marshal empty logs request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return fmt.Errorf("create logs request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Body = io.NopCloser(bytesReader(body))
	req.ContentLength = int64(len(body))

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch logs from %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.ReadAll(resp.Body) // drain
		return fmt.Errorf("logs endpoint returned %d", resp.StatusCode)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read logs response: %w", err)
	}

	if len(respBody) > 2 {
		r.parseAndStoreLogs(respBody)
	}

	return nil
}

// parseAndStoreTraces parses a protobuf response as trace data and adds
// spans to the shared store. Uses the same parsing logic as the local receiver.
func (r *K8sReader) parseAndStoreTraces(data []byte) {
	var traceReq collectorpb.ExportTraceServiceRequest
	if err := proto.Unmarshal(data, &traceReq); err != nil {
		return // not valid trace data
	}

	spansAdded := 0
	for _, resourceSpans := range traceReq.ResourceSpans {
		resourceAttrs := make(map[string]any)
		if resourceSpans.Resource != nil {
			for _, kv := range resourceSpans.Resource.Attributes {
				resourceAttrs[kv.Key] = extractValue(kv.Value)
			}
		}

		for _, scopeSpans := range resourceSpans.ScopeSpans {
			for _, ps := range scopeSpans.Spans {
				span := protoSpanToSpan(ps, resourceAttrs)
				r.store.Add(span)
				spansAdded++
			}
		}

		// Assemble trees for received traces
		traceIDs := make(map[string]bool)
		for _, scopeSpans := range resourceSpans.ScopeSpans {
			for _, ps := range scopeSpans.Spans {
				tid := hex.EncodeToString(ps.TraceId)
				traceIDs[tid] = true
			}
		}
		for tid := range traceIDs {
			r.store.AssembleTree(tid)
		}
	}

	if spansAdded > 0 {
		r.mu.Lock()
		r.spanCount += spansAdded
		r.mu.Unlock()
	}
}

// parseAndStoreLogs parses a protobuf response as log data (Claude Code events)
// and adds spans to the shared store.
func (r *K8sReader) parseAndStoreLogs(data []byte) {
	var logsReq collectorlogspb.ExportLogsServiceRequest
	if err := proto.Unmarshal(data, &logsReq); err != nil {
		return
	}

	spansAdded := 0
	for _, resourceLogs := range logsReq.ResourceLogs {
		resourceAttrs := make(map[string]any)
		if resourceLogs.Resource != nil {
			for _, kv := range resourceLogs.Resource.Attributes {
				resourceAttrs[kv.Key] = extractValue(kv.Value)
			}
		}

		for _, scopeLogs := range resourceLogs.ScopeLogs {
			for _, lr := range scopeLogs.LogRecords {
				span := logRecordToSpan(lr, resourceAttrs)
				if span != nil {
					r.store.Add(span)
					spansAdded++
				}
			}
		}
	}

	if spansAdded > 0 {
		r.mu.Lock()
		r.spanCount += spansAdded
		r.mu.Unlock()
	}
}

// bytesReader wraps a byte slice as an io.Reader. Avoids importing bytes
// just for bytes.NewReader in this file (it's a one-liner).
func bytesReader(b []byte) io.Reader {
	return &byteSliceReader{data: b}
}

type byteSliceReader struct {
	data []byte
	pos  int
}

func (r *byteSliceReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

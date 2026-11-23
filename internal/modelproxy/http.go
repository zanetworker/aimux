package modelproxy

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// NewHandler serves the Anthropic Messages API over backend. Every request must carry key, as x-api-key or as an
// Authorization Bearer token; an empty key rejects every request, so the proxy never runs open.
// HandlerOption configures the handler beyond its backend and key.
type HandlerOption func(*handlerConfig)

type handlerConfig struct{ requests *RequestLog }

// WithRequestLog records one line per answered request, for `aimux model proxy stats`.
func WithRequestLog(log *RequestLog) HandlerOption {
	return func(c *handlerConfig) { c.requests = log }
}

func NewHandler(backend Backend, key string, options ...HandlerOption) http.Handler {
	config := handlerConfig{}
	for _, option := range options {
		option(&config)
	}
	mux := http.NewServeMux()
	// A backend that reports no health leaves the route unregistered, so a caller gets 404 rather than a made-up
	// answer. 503 on degraded lets a monitor act on the status code alone.
	if reporter, ok := backend.(interface{ health() Health }); ok {
		mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
			health := reporter.health()
			status := http.StatusOK
			if health.Status != "ok" {
				status = http.StatusServiceUnavailable
			}
			_ = writeJSON(w, status, health)
		})
	}
	mux.HandleFunc("GET /v1/models", listModels)
	mux.HandleFunc("POST /v1/messages/count_tokens", func(w http.ResponseWriter, r *http.Request) {
		if _, size, ok := readRequest(w, r); ok {
			_ = writeJSON(w, http.StatusOK, map[string]int{"input_tokens": size / 4})
		}
	})
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		serveMessage(w, r, backend, config.requests)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validKey(r, key) {
			writeError(w, http.StatusUnauthorized, "authentication_error", "invalid or missing proxy key")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func validKey(r *http.Request, key string) bool {
	if key == "" {
		return false
	}
	got := r.Header.Get("x-api-key")
	if got == "" {
		bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			return false
		}
		got = bearer
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(key)) == 1
}

func listModels(w http.ResponseWriter, _ *http.Request) {
	type model struct {
		ID          string `json:"id"`
		Type        string `json:"type"`
		DisplayName string `json:"display_name"`
		CreatedAt   string `json:"created_at"`
	}
	data := []model{
		{"codex:" + defaultModel, "model", defaultModel, "1970-01-01T00:00:00Z"},
		{"codex:gpt-6.2-astra", "model", "gpt-6.2-astra", "1970-01-01T00:00:00Z"},
	}
	_ = writeJSON(w, http.StatusOK, map[string]any{
		"data": data, "first_id": data[0].ID, "last_id": data[1].ID, "has_more": false,
	})
}

// maxRequestBytes bounds a request body so one client cannot exhaust the shared proxy's memory. It is well above
// what Claude Code sends: Anthropic's own Messages API limit is 32 MB, images included.
var maxRequestBytes int64 = 64 << 20

func readRequest(w http.ResponseWriter, r *http.Request) (messageRequest, int, bool) {
	var request messageRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", fmt.Sprintf("request body is over %d bytes", maxRequestBytes))
		return request, 0, false
	}
	if err != nil || !json.Valid(body) {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON request body")
		return request, 0, false
	}
	if err := json.Unmarshal(body, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid messages request: "+err.Error())
		return request, 0, false
	}
	return request, len(body), true
}

func serveMessage(w http.ResponseWriter, r *http.Request, backend Backend, requests *RequestLog) {
	var usage Usage
	var session *sessionInput
	var failure, model, effort string
	outcome := "invalid_request"
	logger, hasLogger := backend.(interface {
		logMessage(string, Usage, time.Duration)
	})
	if hasLogger || requests != nil {
		timing := &messageTiming{ResponseWriter: w, started: time.Now(), firstByte: -time.Millisecond}
		w = timing
		defer func() {
			detail := ""
			if session != nil {
				outcome, detail = session.outcome, session.detail
			}
			if hasLogger {
				logger.logMessage(outcome, usage, timing.firstByte)
			}
			requests.Record(Record{
				At: timing.started, Outcome: outcome, Detail: detail, Model: model, Effort: effort,
				Status: timing.status, TTFB: timing.firstByte, Duration: time.Since(timing.started),
				Usage: usage, Error: failure,
			})
		}()
	}
	request, _, ok := readRequest(w, r)
	if !ok {
		return
	}
	turn, err := mapRequest(request)
	if err != nil {
		failure = "unsupported_request"
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	outcome, session = "stateless", turn.session
	model, effort = request.Model, turn.Effort
	if model == "" {
		model = "codex:" + turn.Model
	}
	message := messageResponse{
		ID: "msg_" + rand.Text(), Type: "message", Role: "assistant", Model: model, Content: []responseBlock{},
	}
	stream := messageStream{w: w, message: message}
	reason := "end_turn"
	var text strings.Builder
	var tools []*ToolCall
	usage, err = backend.Run(r.Context(), turn, func(event Event) error {
		if event.Tool != nil {
			reason = "tool_use"
			if name, ok := turn.ToolNames[event.Tool.Name]; ok {
				event.Tool.Name = name
			}
		}
		if request.Stream {
			return stream.emit(event)
		}
		if event.Tool != nil {
			tools = append(tools, event.Tool)
		} else {
			text.WriteString(event.Text)
		}
		return nil
	})
	if r.Context().Err() != nil {
		failure = "client_gone"
		return
	}
	if err != nil {
		// The client is told what went wrong; the history keeps a kind, so no upstream text reaches the file.
		failure = classifyFailure(err)
		if stream.started {
			if stream.closeText() == nil {
				// A lost connection cannot receive an error event.
				_ = stream.write("error", map[string]any{"error": apiError{"api_error", err.Error()}})
			}
		} else {
			writeError(w, http.StatusBadGateway, "api_error", err.Error())
		}
		return
	}
	if request.Stream {
		// Headers or earlier events may already have reached the client; a client that left mid-stream never
		// saw message_stop, so the history should not count the request as delivered.
		if err := stream.finish(reason, usage); err != nil {
			failure = "stream_cut_short"
		}
		return
	}
	if text.Len() > 0 {
		value := text.String()
		message.Content = append(message.Content, responseBlock{Type: "text", Text: &value})
	}
	for _, tool := range tools {
		message.Content = append(message.Content, responseBlock{Type: "tool_use", ID: tool.ID, Name: tool.Name, Input: tool.Input})
	}
	message.StopReason, message.Usage = &reason, usage
	if err := writeJSON(w, http.StatusOK, message); err != nil {
		failure = "response_not_delivered"
	}
}

type messageTiming struct {
	http.ResponseWriter
	started   time.Time
	firstByte time.Duration
	// status is what the client was actually told, which a streamed failure cannot change after the fact.
	status int
}

func (w *messageTiming) WriteHeader(status int) {
	if w.firstByte < 0 {
		w.firstByte = time.Since(w.started)
	}
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *messageTiming) Write(data []byte) (int, error) {
	if w.firstByte < 0 {
		w.firstByte = time.Since(w.started)
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(data)
}

func (w *messageTiming) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// classifyFailure names a failure in a fixed vocabulary, so the history stays free of content a model or a tool
// may have echoed into an error message.
func classifyFailure(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errAppServerExited):
		return "app_server_exited"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "cancelled"
	case strings.Contains(err.Error(), "codex turn"):
		return "turn_failed"
	default:
		return "upstream_error"
	}
}

func writeError(w http.ResponseWriter, status int, kind, message string) {
	// An error response the client cannot receive leaves nothing further to do.
	_ = writeJSON(w, status, map[string]any{"type": "error", "error": apiError{kind, message}})
}

// writeJSON reports a failed write so the caller can record that the client never got the answer; there is no
// second response to send it.
func writeJSON(w http.ResponseWriter, status int, value any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(value)
}

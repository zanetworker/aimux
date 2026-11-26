package modelproxy

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/zanetworker/aimux/internal/ledger"
	"strings"
	"time"
)

// NewHandler serves the Anthropic Messages API over backend. Every request must carry key, as x-api-key or as an
// Authorization Bearer token; an empty key rejects every request, so the proxy never runs open.
// HandlerOption configures the handler beyond its backend and key.
type HandlerOption func(*handlerConfig)

type handlerConfig struct {
	requests  *RequestLog
	ledger    *ledger.Writer
	ledgerOff bool
	turns     *turns
	anthropic *anthropicEngine // nil: every request is answered by Codex, as before there was a second engine
	upstreams map[string]*namedUpstream
	// allowances is the plan allowance last seen in each engine's answers.
	allowances *allowances
	// onLimit is what to do when a plan limit comes back: empty, or OnLimitReport.
	onLimit string
}

// WithRequestLog records one line per answered request, for `aimux model proxy stats`.
func WithRequestLog(log *RequestLog) HandlerOption {
	return func(c *handlerConfig) { c.requests = log }
}

func NewHandler(backend Backend, key string, options ...HandlerOption) http.Handler {
	config := handlerConfig{turns: &turns{}, allowances: &allowances{}}
	for _, option := range options {
		option(&config)
	}
	if config.anthropic != nil {
		config.anthropic.proxyKey = key
	}
	for _, upstream := range config.upstreams {
		upstream.engine.proxyKey = key
	}
	mux := http.NewServeMux()
	reporter, hasHealth := backend.(interface{ health() Health })
	// A backend that reports no health leaves the route unregistered, so a caller gets 404 rather than a made-up
	// answer. 503 on degraded lets a monitor act on the status code alone.
	if hasHealth {
		mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
			health := reporter.health()
			stats := config.ledger.Stats()
			health.Ledger = LedgerHealth{Seq: stats.Seq, Failed: stats.Failed, Off: config.ledgerOff}
			health.Allowance = config.allowances.list()
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
		serveMessage(w, r, backend, &config)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// GET /healthz is the one keyless answer, so `aimux status` never has to hand the key to an unknown
		// listener. It always answers 200 and carries no error text; the keyed /health has the detail.
		if hasHealth && r.Method == http.MethodGet && r.URL.Path == "/healthz" {
			live := liveness(reporter.health())
			stats := config.ledger.Stats()
			live.LedgerSeq, live.LedgerFailed, live.LedgerOff = stats.Seq, stats.Failed, config.ledgerOff
			if nonce := r.URL.Query().Get("challenge"); nonce != "" && len(nonce) <= maxChallenge && key != "" {
				// The port this connection was accepted on, not one the request names.
				if local, ok := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr); ok {
					live.Proof = LivenessProof(key, nonce, local.Port)
				}
			}
			live.Engines = []string{engineCodex}
			if config.anthropic != nil {
				live.Engines = append(live.Engines, engineAnthropic)
			}
			live.Engines = append(live.Engines, config.upstreamNames()...)
			_ = writeJSON(w, http.StatusOK, live)
			return
		}
		carried, path := readCredentials(r, key)
		if carried.viaPath {
			// From here on nothing sees the key: not the routes, not a log line, not the other engine.
			r = withoutKeyPrefix(r, path)
		}
		if serveNamedUpstream(w, r, carried, path, &config) {
			return
		}
		if config.anthropic != nil {
			serveEngines(w, r, carried, path, mux, &config)
			return
		}
		if !carried.proxyKey || carried.badPath {
			writeError(w, http.StatusUnauthorized, "authentication_error", "invalid or missing proxy key")
			return
		}
		mux.ServeHTTP(w, r)
	})
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

// readBody reads a request body up to the proxy's bound, and answers the client itself when it cannot.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", fmt.Sprintf("request body is over %d bytes", maxRequestBytes))
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "the request body could not be read")
		return nil, false
	}
	return body, true
}

func readRequest(w http.ResponseWriter, r *http.Request) (messageRequest, int, bool) {
	var request messageRequest
	body, ok := readBody(w, r)
	if !ok {
		return request, 0, false
	}
	if !json.Valid(body) {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON request body")
		return request, 0, false
	}
	if err := json.Unmarshal(body, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid messages request: "+err.Error())
		return request, 0, false
	}
	return request, len(body), true
}

func serveMessage(w http.ResponseWriter, r *http.Request, backend Backend, config *handlerConfig) {
	requests := config.requests
	// call is the ledger line for this request, set once the request is known to be a model call.
	var call *ledger.Line
	var usage Usage
	var session *sessionInput
	var failure, model, effort string
	outcome := "invalid_request"
	logger, hasLogger := backend.(interface {
		logMessage(string, Usage, time.Duration)
	})
	if hasLogger || requests != nil || config.ledger != nil {
		timing := &messageTiming{ResponseWriter: w, started: time.Now(), firstByte: -time.Millisecond}
		w = timing
		defer func() {
			detail, tag := "", NoSession
			if session != nil {
				outcome, detail, tag = session.outcome, session.detail, session.tag
			}
			if hasLogger {
				logger.logMessage(outcome, usage, timing.firstByte)
			}
			requests.Record(Record{
				At: timing.started, Outcome: outcome, Detail: detail, Session: tag, Model: model, Effort: effort,
				Status: timing.status, TTFB: timing.firstByte, Duration: time.Since(timing.started),
				Usage: usage, Error: failure,
			})
			if call != nil {
				finishModelCall(call, timing.started, timing.firstByte, usage, failure, timing.status)
				config.ledger.Write(*call)
			}
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
	if config.ledger != nil {
		var change *ledger.Line
		call, change = startModelCall(config.turns, callFactsOfMessages(r, request, turn.Effort), engineCodex, "codex:"+turn.Model)
		if change != nil {
			config.ledger.Write(*change)
		}
	}
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
		// A conversation that no longer fits the model is said as the Messages API says it. Claude Code knows
		// that answer and compacts; a server error it sends again, and the same conversation fails the same way.
		status, kind, message := http.StatusBadGateway, "api_error", err.Error()
		if failure == failureContextTooLong {
			status, kind = http.StatusBadRequest, "invalid_request_error"
			message = "prompt is too long: the conversation no longer fits the model's context window (" + err.Error() + ")"
		}
		if stream.started {
			if stream.closeText() == nil {
				// A lost connection cannot receive an error event.
				_ = stream.write("error", map[string]any{"error": apiError{kind, message}})
			}
		} else {
			writeError(w, status, kind, message)
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

// failureContextTooLong is a turn the engine refused because the conversation does not fit its context window.
const failureContextTooLong = "context_too_long"

// classifyFailure names a failure in a fixed vocabulary, so the history stays free of content a model or a tool
// may have echoed into an error message.
func classifyFailure(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errAppServerExited):
		return "app_server_exited"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case strings.Contains(err.Error(), "codex turn") && contextOverflow(err.Error()):
		return failureContextTooLong
	case strings.Contains(err.Error(), "codex turn"):
		return "turn_failed"
	default:
		return "upstream_error"
	}
}

// contextOverflow reports whether a failed turn's own words say the conversation did not fit the model's
// context window. The first is what Codex's app-server says when compacting before a turn also runs out of
// room; the second is the API's error code for the same thing.
func contextOverflow(message string) bool {
	// The exact words, not any mention of a context window: another failure may talk about one too, and must
	// stay a failure that is retried and counted.
	lower := strings.ToLower(message)
	return strings.Contains(lower, "ran out of room in the model's context window") || strings.Contains(lower, "context_length_exceeded")
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

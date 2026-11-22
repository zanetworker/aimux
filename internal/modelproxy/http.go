package modelproxy

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// NewHandler serves the Anthropic Messages API over backend. Every request must carry key, as x-api-key or as an
// Authorization Bearer token; an empty key rejects every request, so the proxy never runs open.
func NewHandler(backend Backend, key string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", listModels)
	mux.HandleFunc("POST /v1/messages/count_tokens", func(w http.ResponseWriter, r *http.Request) {
		if _, size, ok := readRequest(w, r); ok {
			writeJSON(w, http.StatusOK, map[string]int{"input_tokens": size / 4})
		}
	})
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		serveMessage(w, r, backend)
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
	writeJSON(w, http.StatusOK, map[string]any{
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

func serveMessage(w http.ResponseWriter, r *http.Request, backend Backend) {
	request, _, ok := readRequest(w, r)
	if !ok {
		return
	}
	turn, err := mapRequest(request)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	model := request.Model
	if model == "" {
		model = "codex:" + turn.Model
	}
	message := messageResponse{
		ID: "msg_" + rand.Text(), Type: "message", Role: "assistant", Model: model, Content: []responseBlock{},
	}
	stream := messageStream{w: w, message: message}
	reason := "end_turn"
	var text strings.Builder
	var tool *ToolCall
	usage, err := backend.Run(r.Context(), turn, func(event Event) error {
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
			tool = event.Tool
		} else {
			text.WriteString(event.Text)
		}
		return nil
	})
	if r.Context().Err() != nil {
		return
	}
	if err != nil {
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
		// Headers or earlier events may already have reached the client.
		_ = stream.finish(reason, usage)
		return
	}
	if text.Len() > 0 {
		value := text.String()
		message.Content = append(message.Content, responseBlock{Type: "text", Text: &value})
	}
	if tool != nil {
		message.Content = append(message.Content, responseBlock{Type: "tool_use", ID: tool.ID, Name: tool.Name, Input: tool.Input})
	}
	message.StopReason, message.Usage = &reason, usage
	writeJSON(w, http.StatusOK, message)
}

func writeError(w http.ResponseWriter, status int, kind, message string) {
	writeJSON(w, status, map[string]any{"type": "error", "error": apiError{kind, message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// There is no remaining response to send if the client disconnects.
	_ = json.NewEncoder(w).Encode(value)
}

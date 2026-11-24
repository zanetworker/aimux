package modelproxy

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type messageResponse struct {
	ID           string          `json:"id"`
	Type         string          `json:"type"`
	Role         string          `json:"role"`
	Model        string          `json:"model"`
	Content      []responseBlock `json:"content"`
	StopReason   *string         `json:"stop_reason"`
	StopSequence *string         `json:"stop_sequence"`
	Usage        Usage           `json:"usage"`
}

type responseBlock struct {
	Type  string          `json:"type"`
	Text  *string         `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type messageStream struct {
	w        http.ResponseWriter
	message  messageResponse
	started  bool
	textOpen bool
	index    int
}

func (s *messageStream) write(name string, fields map[string]any) error {
	if fields == nil {
		fields = make(map[string]any)
	}
	fields["type"] = name
	raw, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, raw); err != nil {
		return err
	}
	return http.NewResponseController(s.w).Flush()
}

func (s *messageStream) start() error {
	if s.started {
		return nil
	}
	s.started = true
	s.w.Header().Set("Content-Type", "text/event-stream")
	s.w.Header().Set("Cache-Control", "no-cache")
	return s.write("message_start", map[string]any{"message": s.message})
}

func (s *messageStream) closeText() error {
	if !s.textOpen {
		return nil
	}
	err := s.write("content_block_stop", map[string]any{"index": s.index})
	s.textOpen = false
	s.index++
	return err
}

func (s *messageStream) emit(event Event) error {
	if err := s.start(); err != nil {
		return err
	}
	if event.Tool != nil {
		if err := s.closeText(); err != nil {
			return err
		}
		if err := s.write("content_block_start", map[string]any{
			"index": s.index, "content_block": responseBlock{
				Type: "tool_use", ID: event.Tool.ID, Name: event.Tool.Name, Input: json.RawMessage("{}"),
			},
		}); err != nil {
			return err
		}
		if err := s.write("content_block_delta", map[string]any{
			"index": s.index, "delta": map[string]string{"type": "input_json_delta", "partial_json": string(event.Tool.Input)},
		}); err != nil {
			return err
		}
		err := s.write("content_block_stop", map[string]any{"index": s.index})
		s.index++
		return err
	}
	if !s.textOpen {
		empty := ""
		if err := s.write("content_block_start", map[string]any{
			"index": s.index, "content_block": responseBlock{Type: "text", Text: &empty},
		}); err != nil {
			return err
		}
		s.textOpen = true
	}
	return s.write("content_block_delta", map[string]any{
		"index": s.index, "delta": map[string]string{"type": "text_delta", "text": event.Text},
	})
}

func (s *messageStream) finish(reason string, usage Usage) error {
	if err := s.start(); err != nil {
		return err
	}
	if err := s.closeText(); err != nil {
		return err
	}
	if err := s.write("message_delta", map[string]any{
		"delta": map[string]any{"stop_reason": reason, "stop_sequence": nil}, "usage": usage,
	}); err != nil {
		return err
	}
	return s.write("message_stop", nil)
}

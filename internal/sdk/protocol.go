package sdk

// EventType identifies what kind of event this is.
type EventType string

const (
	EventPrompt     EventType = "prompt"
	EventToolCall   EventType = "tool_call"
	EventToolResult EventType = "tool_result"
	EventText       EventType = "text"
	EventCompletion EventType = "completion"
	EventError      EventType = "error"
)

// Event is the envelope for all protocol messages exchanged between
// aimux and an SDK subprocess over JSON lines (stdin/stdout).
type Event struct {
	Type       EventType      `json:"type"`
	Content    string         `json:"content,omitempty"`
	ID         string         `json:"id,omitempty"`
	Name       string         `json:"name,omitempty"`
	Input      map[string]any `json:"input,omitempty"`
	Output     string         `json:"output,omitempty"`
	IsError    bool           `json:"is_error,omitempty"`
	StopReason string         `json:"stop_reason,omitempty"`
	Message    string         `json:"message,omitempty"`
}

// EventFromToolCall converts a ToolCall to a protocol Event.
func EventFromToolCall(tc ToolCall) Event {
	return Event{
		Type:  EventToolCall,
		ID:    tc.ID,
		Name:  tc.Name,
		Input: tc.Input,
	}
}

// EventFromToolResult converts a ToolResult to a protocol Event.
func EventFromToolResult(tr ToolResult) Event {
	return Event{
		Type:    EventToolResult,
		ID:      tr.ID,
		Output:  tr.Output,
		IsError: tr.IsError,
	}
}

// ToToolCall extracts a ToolCall from a tool_call Event.
func (e Event) ToToolCall() ToolCall {
	return ToolCall{
		ID:    e.ID,
		Name:  e.Name,
		Input: e.Input,
	}
}

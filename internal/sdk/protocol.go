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

// StreamEvent is the minimal envelope for Claude's --output-format stream-json lines.
// The Type field is read first to decide which concrete struct to unmarshal into.
type StreamEvent struct {
	Type string `json:"type"`
}

// AssistantStreamEvent represents a "type":"assistant" event from Claude's stream-json output.
type AssistantStreamEvent struct {
	Type      string        `json:"type"`
	Message   StreamMessage `json:"message"`
	SessionID string        `json:"session_id"`
	Timestamp string        `json:"timestamp"`
}

// StreamMessage is the message body inside an AssistantStreamEvent.
type StreamMessage struct {
	ID         string         `json:"id"`
	Content    []ContentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      StreamUsage    `json:"usage"`
}

// ContentBlock represents a single content item in an assistant message.
type ContentBlock struct {
	Type  string         `json:"type"`
	Text  string         `json:"text,omitempty"`
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
}

// StreamUsage tracks token consumption for a single message.
type StreamUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ResultStreamEvent represents the final "type":"result" event from Claude's stream-json output.
type ResultStreamEvent struct {
	Type         string      `json:"type"`
	IsError      bool        `json:"is_error"`
	Result       string      `json:"result"`
	StopReason   string      `json:"stop_reason"`
	NumTurns     int         `json:"num_turns"`
	TotalCostUSD float64     `json:"total_cost_usd"`
	SessionID    string      `json:"session_id"`
	Usage        StreamUsage `json:"usage"`
}

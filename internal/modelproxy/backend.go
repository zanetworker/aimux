package modelproxy

import (
	"context"
	"encoding/json"
)

type TurnRequest struct {
	Model, Effort, System string
	Tools                 []dynamicTool
	History               []any
	Input                 []textPart
	// ToolNames maps each tool name sent to Codex back to the Claude Code name it stands for.
	ToolNames map[string]string
}

type ToolCall struct {
	ID, Name string
	Input    json.RawMessage
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type Event struct {
	Text string
	Tool *ToolCall
}

// Backend emits text and tool events in order, then returns the turn's usage.
type Backend interface {
	Run(context.Context, TurnRequest, func(Event) error) (Usage, error)
}

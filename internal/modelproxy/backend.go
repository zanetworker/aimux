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
	session   *sessionInput
}

type ToolCall struct {
	ID, Name string
	Input    json.RawMessage
}

type Usage struct {
	// InputTokens counts uncached input. Add CacheReadInputTokens for the full prompt.
	InputTokens          int `json:"input_tokens"`
	OutputTokens         int `json:"output_tokens"`
	CacheReadInputTokens int `json:"cache_read_input_tokens"`
	// CacheCreationInputTokens is input written to the prompt cache. Anthropic reports it; Codex does not, so a
	// Codex answer leaves it out.
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	estimated                bool
}

type Event struct {
	Text string
	Tool *ToolCall
}

// Backend emits text and tool events in order, then returns the turn's usage.
type Backend interface {
	Run(context.Context, TurnRequest, func(Event) error) (Usage, error)
}

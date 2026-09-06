package sdk

// ToolCall represents a tool invocation from the SDK subprocess.
type ToolCall struct {
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

// ToolResult represents the result of a tool execution.
type ToolResult struct {
	ID      string `json:"id"`
	Output  string `json:"output"`
	IsError bool   `json:"is_error,omitempty"`
}

// HookDecision indicates how the dispatcher handled a tool call.
type HookDecision int

const (
	// Approve means let the SDK execute the tool locally.
	Approve HookDecision = iota
	// Handled means the dispatcher executed it and has a result.
	Handled
)

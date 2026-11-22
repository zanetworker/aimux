package modelproxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const defaultModel = "gpt-6.2-sol"

type messageRequest struct {
	Model    string          `json:"model"`
	System   json.RawMessage `json:"system"`
	Stream   bool            `json:"stream"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema"`
	} `json:"tools"`
}

type requestBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type textPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type dynamicTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

func decodeContent(raw json.RawMessage) ([]requestBlock, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, err
		}
		return []requestBlock{{Type: "text", Text: text}}, nil
	}
	var blocks []requestBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, errors.New("content must be a string or a list of blocks")
	}
	for _, block := range blocks {
		if block.Type == "image" {
			return nil, errors.New("images are not supported by the codex backend")
		}
	}
	return blocks, nil
}

func joinText(raw json.RawMessage) (string, error) {
	blocks, err := decodeContent(raw)
	if err != nil {
		return "", err
	}
	text := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" {
			text = append(text, block.Text)
		}
	}
	return strings.Join(text, "\n"), nil
}

func mapRequest(req messageRequest) (TurnRequest, error) {
	turn := TurnRequest{
		Model: defaultModel, Effort: "low",
		Input: []textPart{}, Tools: make([]dynamicTool, 0, len(req.Tools)),
	}
	if id, explicit := strings.CutPrefix(req.Model, "codex:"); explicit {
		if id == "" {
			return turn, errors.New("codex model ID is empty")
		}
		turn.Model, turn.Effort = id, ""
	}
	var err error
	turn.System, err = joinText(req.System)
	if err != nil {
		return turn, err
	}
	turn.ToolNames = make(map[string]string, len(req.Tools))
	for _, tool := range req.Tools {
		name := codexToolName(tool.Name)
		if other, ok := turn.ToolNames[name]; ok {
			return turn, fmt.Errorf("tool names %q and %q collide as %q for codex", other, tool.Name, name)
		}
		turn.ToolNames[name] = tool.Name
		turn.Tools = append(turn.Tools, dynamicTool{"function", name, tool.Description, tool.InputSchema})
	}
	// Claude Code also sends role "system" messages (reminders) inside the conversation, including after the
	// final user message. They become developer messages in place, and the final user message is the last
	// message that is not a system message. When system messages follow it, the final user text is injected
	// as history too (the turn then starts with empty input), so Codex sees the reminders after it.
	lastIdx := -1
	for i, msg := range req.Messages {
		if msg.Role != "system" {
			lastIdx = i
		}
	}
	trailingSystem := lastIdx >= 0 && lastIdx < len(req.Messages)-1
	for i, msg := range req.Messages {
		blocks, err := decodeContent(msg.Content)
		if err != nil {
			return turn, err
		}
		last := i == lastIdx
		textType, itemRole := "input_text", msg.Role
		switch msg.Role {
		case "user":
		case "assistant":
			textType = "output_text"
		case "system":
			itemRole = "developer"
		default:
			return turn, fmt.Errorf("unsupported message role %q", msg.Role)
		}

		var text []textPart
		flush := func() {
			if len(text) > 0 {
				turn.History = append(turn.History, struct {
					Type    string     `json:"type"`
					Role    string     `json:"role"`
					Content []textPart `json:"content"`
				}{"message", itemRole, text})
				text = nil
			}
		}
		for _, block := range blocks {
			switch block.Type {
			case "text":
				if !last || trailingSystem {
					text = append(text, textPart{textType, block.Text})
				} else if msg.Role == "user" {
					turn.Input = append(turn.Input, textPart{"text", block.Text})
				}
			case "tool_use":
				if !last {
					flush()
					turn.History = append(turn.History, struct {
						Type      string `json:"type"`
						Name      string `json:"name"`
						CallID    string `json:"call_id"`
						Arguments string `json:"arguments"`
					}{"function_call", codexToolName(block.Name), block.ID, string(block.Input)})
				}
			case "tool_result":
				flush()
				output, err := joinText(block.Content)
				if err != nil {
					return turn, err
				}
				if block.IsError {
					output = "Error: " + output
				}
				turn.History = append(turn.History, struct {
					Type   string `json:"type"`
					CallID string `json:"call_id"`
					Output string `json:"output"`
				}{"function_call_output", block.ToolUseID, output})
			case "thinking", "redacted_thinking":
			default:
				return turn, fmt.Errorf("unsupported content block %q", block.Type)
			}
		}
		flush()
	}
	if lastIdx < 0 || req.Messages[lastIdx].Role != "user" {
		return turn, errors.New("messages must contain a user message at the end")
	}
	return turn, nil
}

// codexToolName renames tools Codex reserves for its own MCP tools: Codex 0.170 rejects a dynamic tool named
// "mcp" or starting with "mcp__", which is how Claude Code names every MCP tool.
func codexToolName(name string) string {
	if name == "mcp" {
		return "mcp_"
	}
	if rest, ok := strings.CutPrefix(name, "mcp__"); ok {
		return "mcp_" + rest
	}
	return name
}

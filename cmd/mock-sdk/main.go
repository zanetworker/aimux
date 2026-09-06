// mock-sdk is a fake SDK subprocess for testing the JSON-lines protocol.
// It reads prompt events from stdin, runs a bash tool_call with the prompt
// as the command, receives the result, and sends text + completion.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/zanetworker/aimux/internal/sdk"
)

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	toolID := 0

	for scanner.Scan() {
		var event sdk.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			_ = encoder.Encode(sdk.Event{
				Type:    sdk.EventError,
				Message: fmt.Sprintf("unmarshal: %v", err),
			})
			continue
		}

		switch event.Type {
		case sdk.EventPrompt:
			toolID++
			id := fmt.Sprintf("tc_%d", toolID)
			command := event.Content
			toolName := "bash"

			if strings.HasPrefix(command, "read ") {
				toolName = "read"
				_ = encoder.Encode(sdk.Event{
					Type:  sdk.EventToolCall,
					ID:    id,
					Name:  toolName,
					Input: map[string]any{"file_path": strings.TrimPrefix(command, "read ")},
				})
			} else if strings.HasPrefix(command, "write ") {
				parts := strings.SplitN(strings.TrimPrefix(command, "write "), " ", 2)
				content := ""
				if len(parts) > 1 {
					content = parts[1]
				}
				toolName = "write"
				_ = encoder.Encode(sdk.Event{
					Type:  sdk.EventToolCall,
					ID:    id,
					Name:  toolName,
					Input: map[string]any{"file_path": parts[0], "content": content},
				})
			} else {
				_ = encoder.Encode(sdk.Event{
					Type:  sdk.EventToolCall,
					ID:    id,
					Name:  toolName,
					Input: map[string]any{"command": command},
				})
			}

		case sdk.EventToolResult:
			output := event.Output
			if event.IsError {
				_ = encoder.Encode(sdk.Event{
					Type:    sdk.EventText,
					Content: fmt.Sprintf("Command failed: %s", output),
				})
			} else {
				_ = encoder.Encode(sdk.Event{
					Type:    sdk.EventText,
					Content: output,
				})
			}
			_ = encoder.Encode(sdk.Event{
				Type:       sdk.EventCompletion,
				StopReason: "end_turn",
			})
		}
	}
}

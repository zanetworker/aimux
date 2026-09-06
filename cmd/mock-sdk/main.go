// mock-sdk is a fake SDK subprocess for testing the JSON-lines protocol.
// It reads events from stdin, responds with tool_call/text/completion events.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/zanetworker/aimux/internal/sdk"
)

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)

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
			_ = encoder.Encode(sdk.Event{
				Type:  sdk.EventToolCall,
				ID:    "tc_1",
				Name:  "bash",
				Input: map[string]any{"command": "echo hello from mock"},
			})

		case sdk.EventToolResult:
			_ = encoder.Encode(sdk.Event{
				Type:    sdk.EventText,
				Content: "Mock analysis: " + event.Output,
			})
			_ = encoder.Encode(sdk.Event{
				Type:       sdk.EventCompletion,
				StopReason: "end_turn",
			})
		}
	}
}

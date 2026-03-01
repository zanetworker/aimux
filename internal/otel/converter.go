package otel

import (
	"strings"

	"github.com/zanetworker/agentmux/internal/trace"
)

// SpansToTurns converts a span tree (from the OTEL receiver) into trace.Turn
// slices that the current TUI can render. This is the bridge between the
// OTEL span model and the existing trace view.
//
// The expected hierarchy is:
//
//	invoke_agent (root)
//	  ├─ chat / turn-N (API call spans)
//	  │   ├─ execute_tool (tool call spans)
//	  │   └─ execute_tool
//	  └─ chat / turn-N
//
// Falls back to treating each direct child of root as a turn.
func SpansToTurns(root *Span) []trace.Turn {
	if root == nil {
		return nil
	}

	var turns []trace.Turn
	turnNum := 0

	for _, child := range root.Children {
		turnNum++
		turn := spanToTurn(child, turnNum)
		turns = append(turns, turn)
	}

	return turns
}

func spanToTurn(s *Span, num int) trace.Turn {
	t := trace.Turn{
		Number:    num,
		Timestamp: s.Start,
		EndTime:   s.End,
		Model:     s.AttrStr("gen_ai.request.model"),
		TokensIn:  s.AttrInt64("gen_ai.usage.input_tokens"),
		TokensOut: s.AttrInt64("gen_ai.usage.output_tokens"),
	}

	// Extract user input
	if input := s.AttrStr("gen_ai.input.messages"); input != "" {
		for _, line := range strings.Split(input, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				t.UserLines = append(t.UserLines, line)
			}
		}
	}

	// Extract assistant output
	if output := s.AttrStr("gen_ai.output.messages"); output != "" {
		for _, line := range strings.Split(output, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				t.OutputLines = append(t.OutputLines, line)
			}
		}
	}

	// Extract cost
	if cost, ok := s.Attr("gen_ai.usage.cost").(float64); ok {
		t.CostUSD = cost
	}

	// Convert child tool spans to actions
	for _, child := range s.Children {
		opName := child.AttrStr("gen_ai.operation.name")
		if opName == "execute_tool" || strings.Contains(child.Name, "execute_tool") {
			action := trace.ToolSpan{
				Name:    child.AttrStr("gen_ai.tool.name"),
				Snippet: truncate(child.AttrStr("gen_ai.tool.call.arguments"), 60),
				Success: child.Status != StatusError,
			}
			if action.Name == "" {
				// Fall back to span name
				action.Name = child.Name
				action.Name = strings.TrimPrefix(action.Name, "execute_tool ")
			}
			if child.Status == StatusError {
				if errType := child.AttrStr("error.type"); errType != "" {
					action.ErrorMsg = errType
				}
			}
			t.Actions = append(t.Actions, action)
		}
	}

	return t
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

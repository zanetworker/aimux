package otel

import (
	"sort"
	"strings"

	"github.com/zanetworker/aimux/internal/trace"
)

// SpansToTurns converts a span tree (from the OTEL receiver) into trace.Turn
// slices that the current TUI can render.
//
// Handles two data formats:
//  1. Claude Code log events: flat events with prompt.id, grouped into turns
//  2. Codex/Gemini trace spans: hierarchical spans where each child is a turn
//
// Auto-detects the format by checking for prompt.id attributes.
func SpansToTurns(root *Span) []trace.Turn {
	if root == nil {
		return nil
	}

	// Check if events use prompt.id grouping (Claude Code log events)
	if hasPromptIDs(root) {
		return logEventsToTurns(root)
	}

	// Legacy path: each direct child of root is a turn (Codex/Gemini traces)
	return spanTreeToTurns(root)
}

// hasPromptIDs checks if any events in the tree have a prompt.id attribute.
func hasPromptIDs(root *Span) bool {
	if root.AttrStr("prompt.id") != "" {
		return true
	}
	for _, child := range root.Children {
		if child.AttrStr("prompt.id") != "" {
			return true
		}
	}
	return false
}

// logEventsToTurns groups Claude Code OTEL log events by prompt.id
// to reconstruct conversation turns.
func logEventsToTurns(root *Span) []trace.Turn {
	// Collect all events: root + children
	var allEvents []*Span
	allEvents = append(allEvents, root)
	allEvents = append(allEvents, root.Children...)

	// Group events by prompt.id
	type promptGroup struct {
		promptID string
		events   []*Span
		earliest int64
	}

	groups := make(map[string]*promptGroup)
	var order []string

	for _, s := range allEvents {
		pid := s.AttrStr("prompt.id")
		if pid == "" {
			pid = s.SpanID // events without prompt.id get their own group
		}

		g, ok := groups[pid]
		if !ok {
			g = &promptGroup{promptID: pid, earliest: s.Start.UnixNano()}
			groups[pid] = g
			order = append(order, pid)
		}
		g.events = append(g.events, s)
		if s.Start.UnixNano() < g.earliest {
			g.earliest = s.Start.UnixNano()
		}
	}

	// Sort groups by earliest timestamp
	sort.Slice(order, func(i, j int) bool {
		return groups[order[i]].earliest < groups[order[j]].earliest
	})

	// Convert each prompt group to a Turn
	var turns []trace.Turn
	turnNum := 0

	for _, pid := range order {
		g := groups[pid]
		turn := eventsToTurn(g.events, turnNum+1)
		if len(turn.UserLines) > 0 || len(turn.Actions) > 0 || turn.TokensIn > 0 {
			turnNum++
			turn.Number = turnNum
			turns = append(turns, turn)
		}
	}

	return turns
}

// eventsToTurn converts a group of OTEL events (sharing the same prompt.id)
// into a single trace.Turn.
func eventsToTurn(events []*Span, num int) trace.Turn {
	t := trace.Turn{Number: num}

	for _, s := range events {
		shortName := s.Name
		if idx := strings.LastIndex(s.Name, "."); idx >= 0 {
			shortName = s.Name[idx+1:]
		}

		switch shortName {
		case "user_prompt":
			if t.Timestamp.IsZero() {
				t.Timestamp = s.Start
			}
			if input := s.AttrStr("gen_ai.input.messages"); input != "" {
				for _, line := range strings.Split(input, "\n") {
					line = strings.TrimSpace(line)
					if line != "" {
						t.UserLines = append(t.UserLines, line)
					}
				}
			}
			if len(t.UserLines) == 0 {
				if prompt := s.AttrStr("prompt"); prompt != "" {
					for _, line := range strings.Split(prompt, "\n") {
						line = strings.TrimSpace(line)
						if line != "" {
							t.UserLines = append(t.UserLines, line)
						}
					}
				}
			}

		case "api_request":
			if model := s.AttrStr("gen_ai.request.model"); model != "" {
				t.Model = model
			}
			if model := s.AttrStr("model"); model != "" && t.Model == "" {
				t.Model = model
			}
			// Use gen_ai.usage.* if set, otherwise raw attributes
			if v := s.AttrInt64("gen_ai.usage.input_tokens"); v > 0 {
				t.TokensIn += v
			} else {
				t.TokensIn += s.AttrInt64("input_tokens")
			}
			if v := s.AttrInt64("gen_ai.usage.output_tokens"); v > 0 {
				t.TokensOut += v
			} else {
				t.TokensOut += s.AttrInt64("output_tokens")
			}

			if c := s.AttrFloat64("gen_ai.usage.cost"); c > 0 {
				t.CostUSD += c
			} else if c := s.AttrFloat64("cost_usd"); c > 0 {
				t.CostUSD += c
			}

			if !s.End.IsZero() {
				t.EndTime = s.End
			}

		case "tool_result":
			toolName := s.AttrStr("gen_ai.tool.name")
			if toolName == "" {
				toolName = s.AttrStr("tool_name")
			}
			if toolName == "" {
				continue
			}
			action := trace.ToolSpan{
				Name:    toolName,
				Success: s.Status != StatusError,
			}
			if success := s.AttrStr("success"); success == "false" {
				action.Success = false
			}
			if errMsg := s.AttrStr("error"); errMsg != "" {
				action.ErrorMsg = truncate(errMsg, 200)
				action.Success = false
			}
			t.Actions = append(t.Actions, action)

		case "api_error":
			if errMsg := s.AttrStr("error"); errMsg != "" {
				t.Actions = append(t.Actions, trace.ToolSpan{
					Name:     "api_error",
					Success:  false,
					ErrorMsg: truncate(errMsg, 200),
				})
			}
		}
	}

	if t.CostUSD == 0 && t.Model != "" {
		t.CostUSD = trace.EstimateTurnCost(t.Model, t.TokensIn, t.TokensOut)
	}

	return t
}

// spanTreeToTurns converts a hierarchical span tree (Codex/Gemini format)
// where each direct child of root represents a turn.
func spanTreeToTurns(root *Span) []trace.Turn {
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

	if input := s.AttrStr("gen_ai.input.messages"); input != "" {
		for _, line := range strings.Split(input, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				t.UserLines = append(t.UserLines, line)
			}
		}
	}

	if output := s.AttrStr("gen_ai.output.messages"); output != "" {
		for _, line := range strings.Split(output, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				t.OutputLines = append(t.OutputLines, line)
			}
		}
	}

	if c := s.AttrFloat64("gen_ai.usage.cost"); c > 0 {
		t.CostUSD = c
	}

	for _, child := range s.Children {
		opName := child.AttrStr("gen_ai.operation.name")
		if opName == "execute_tool" || strings.Contains(child.Name, "execute_tool") {
			action := trace.ToolSpan{
				Name:    child.AttrStr("gen_ai.tool.name"),
				Snippet: truncate(child.AttrStr("gen_ai.tool.call.arguments"), 60),
				Success: child.Status != StatusError,
			}
			if action.Name == "" {
				action.Name = strings.TrimPrefix(child.Name, "execute_tool ")
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

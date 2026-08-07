package otel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// TrackerData is the per-session JSON written to ~/.aimux/data/context/.
// The schema matches what claude-hud's context-breakdown.ts expects.
type TrackerData struct {
	Agents  int                    `json:"agents"`
	Tools   int                    `json:"tools"`
	MCP     int                    `json:"mcp"`
	Calls   int                    `json:"calls"`
	CostUSD float64                `json:"cost_usd,omitempty"`
	ByTool  map[string]ToolDetail  `json:"by_tool,omitempty"`
	Last    string                 `json:"last,omitempty"`
}

// ToolDetail holds per-tool token, call, and cost data.
type ToolDetail struct {
	Tokens  int     `json:"tokens"`
	Calls   int     `json:"calls"`
	CostUSD float64 `json:"cost_usd,omitempty"`
}

// FlushTracker walks all sessions in the SpanStore and writes a tracker
// JSON file per session to dataDir. Existing files are overwritten.
func FlushTracker(store *SpanStore, dataDir string) error {
	ids := store.ConversationIDs()
	if len(ids) == 0 {
		return nil
	}

	if err := os.MkdirAll(dataDir, 0o750); err != nil { // #nosec G301
		return err
	}

	for _, sessionID := range ids {
		root := store.GetByConversation(sessionID)
		if root == nil {
			continue
		}

		data := aggregateSession(root)
		if data.Calls == 0 {
			continue
		}

		path := filepath.Join(dataDir, sessionID+".json")
		b, err := json.Marshal(data)
		if err != nil {
			continue
		}
		_ = os.WriteFile(path, b, 0o600) // #nosec G306
	}

	return nil
}

func aggregateSession(root *Span) TrackerData {
	data := TrackerData{
		ByTool: make(map[string]ToolDetail),
	}

	var totalTokens int64
	var totalCost float64
	seenToolUseIDs := make(map[string]bool)
	var allEvents []*Span
	allEvents = append(allEvents, root)
	allEvents = append(allEvents, root.Children...)

	for _, s := range allEvents {
		shortName := s.Name
		if idx := strings.LastIndex(s.Name, "."); idx >= 0 {
			shortName = s.Name[idx+1:]
		}

		switch shortName {
		case "tool_result", "tool_decision":
			toolName := s.AttrStr("gen_ai.tool.name")
			if toolName == "" {
				toolName = s.AttrStr("tool_name")
			}
			if toolName == "" {
				continue
			}
			tuid := s.AttrStr("tool_use_id")
			if tuid != "" {
				if seenToolUseIDs[tuid] {
					continue
				}
				seenToolUseIDs[tuid] = true
			}

			data.Calls++
			data.Last = toolName

			detail := data.ByTool[toolName]
			detail.Calls++
			data.ByTool[toolName] = detail

		case "api_request":
			in := s.AttrInt64("gen_ai.usage.input_tokens")
			if in == 0 {
				in = s.AttrInt64("input_tokens")
			}
			out := s.AttrInt64("gen_ai.usage.output_tokens")
			if out == 0 {
				out = s.AttrInt64("output_tokens")
			}
			cacheWrite := s.AttrInt64("cache_creation_tokens")
			cacheRead := s.AttrInt64("cache_read_tokens")
			totalTokens += in + out + cacheWrite + cacheRead

			cost := s.AttrFloat64("cost_usd")
			if cost == 0 {
				cost = s.AttrFloat64("gen_ai.usage.cost")
			}
			totalCost += cost

			toolName := s.AttrStr("tool_name")
			if toolName != "" {
				detail := data.ByTool[toolName]
				detail.Tokens += int(in + out + cacheWrite + cacheRead)
				detail.CostUSD += cost
				data.ByTool[toolName] = detail
			}
		}
	}

	data.CostUSD = totalCost
	classifyTokens(&data)
	if totalTokens > 0 {
		distributeUnattributed(&data, totalTokens)
	}
	if totalCost > 0 && data.Calls > 0 {
		distributeCost(&data, totalCost)
	}
	return data
}

// classifyTokens sums by_tool tokens into the Tools/MCP/Agents counters.
func classifyTokens(data *TrackerData) {
	for name, detail := range data.ByTool {
		if name == "Task" || name == "Agent" {
			data.Agents += detail.Tokens
		} else if strings.HasPrefix(name, "mcp__") {
			data.MCP += detail.Tokens
		} else {
			data.Tools += detail.Tokens
		}
	}
}

// distributeUnattributed spreads tokens from api_request events that had
// no tool_name across tools proportionally by call count.
func distributeUnattributed(data *TrackerData, totalTokens int64) {
	attributed := data.Tools + data.MCP + data.Agents
	unattributed := int(totalTokens) - attributed
	if unattributed <= 0 || data.Calls == 0 {
		return
	}

	perCall := unattributed / data.Calls
	remainder := unattributed - (perCall * data.Calls)

	data.Tools = 0
	data.MCP = 0
	data.Agents = 0

	first := true
	for name, detail := range data.ByTool {
		detail.Tokens += perCall * detail.Calls
		if first {
			detail.Tokens += remainder
			first = false
		}
		data.ByTool[name] = detail

		if name == "Task" || name == "Agent" {
			data.Agents += detail.Tokens
		} else if strings.HasPrefix(name, "mcp__") {
			data.MCP += detail.Tokens
		} else {
			data.Tools += detail.Tokens
		}
	}
}

// distributeCost spreads total session cost across tools proportionally by call count.
func distributeCost(data *TrackerData, totalCost float64) {
	perCall := totalCost / float64(data.Calls)
	for name, detail := range data.ByTool {
		detail.CostUSD = perCall * float64(detail.Calls)
		data.ByTool[name] = detail
	}
}

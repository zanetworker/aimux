package otel

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
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

// stateDirName holds each compacted session's running totals, so a
// session that resumes after compaction, or after a collector restart,
// keeps its earlier counts.
const stateDirName = ".state"

// safeSessionID matches session IDs that are safe as file names. IDs come
// from any local client (headers, query, log attributes), so anything else
// could write outside the data directory or onto another session's file.
var safeSessionID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// sessionState is a session's raw running totals, before token and cost
// distribution. It is what Compact saves to disk.
type sessionState struct {
	Calls       int                   `json:"calls"`
	Last        string                `json:"last,omitempty"`
	ByTool      map[string]ToolDetail `json:"by_tool,omitempty"`
	TotalTokens int64                 `json:"total_tokens"`
	TotalCost   float64               `json:"total_cost"`
	Seen        map[string]bool       `json:"seen,omitempty"` // counted tool_use_ids
}

func (st *sessionState) clone() *sessionState {
	c := &sessionState{
		Calls: st.Calls, Last: st.Last, TotalTokens: st.TotalTokens, TotalCost: st.TotalCost,
		ByTool: make(map[string]ToolDetail, len(st.ByTool)),
		Seen:   make(map[string]bool, len(st.Seen)),
	}
	for k, v := range st.ByTool {
		c.ByTool[k] = v
	}
	for k := range st.Seen {
		c.Seen[k] = true
	}
	return c
}

// Tracker writes per-session tracker JSON from a SpanStore and compacts
// idle sessions out of memory into saved state.
type Tracker struct {
	dir     string
	mu      sync.Mutex
	base    map[string]*sessionState // saved state per session, loaded lazily
	pending map[string]bool          // sessions whose state save failed
}

// NewTracker returns a Tracker writing to dataDir.
func NewTracker(dataDir string) *Tracker {
	return &Tracker{dir: dataDir, base: make(map[string]*sessionState), pending: make(map[string]bool)}
}

// FlushTracker writes tracker JSON for every session in the store,
// including totals saved by earlier compactions. Existing files are
// overwritten.
func FlushTracker(store *SpanStore, dataDir string) error {
	return NewTracker(dataDir).Flush(store)
}

// Flush writes a tracker JSON file per session in the store to the data
// directory: saved state plus the spans still in memory.
func (t *Tracker) Flush(store *SpanStore) error {
	ids := store.ConversationIDs()
	if len(ids) == 0 {
		return nil
	}
	if err := os.MkdirAll(t.dir, 0o750); err != nil { // #nosec G301
		return err
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	for _, sessionID := range ids {
		root := store.GetByConversation(sessionID)
		if root == nil || !safeSessionID.MatchString(sessionID) {
			continue
		}
		st := t.loadBase(sessionID).clone()
		accumulate(st, root)
		_ = t.writeTracker(sessionID, st) // retried on the next flush
	}
	return nil
}

// Compact removes sessions idle since cutoff from the store, saves their
// totals and writes their final tracker JSON. Without it the store keeps
// every span the collector has ever received.
func (t *Tracker) Compact(store *SpanStore, cutoff time.Time) error {
	taken := store.TakeIdle(cutoff)

	t.mu.Lock()
	defer t.mu.Unlock()
	stateDir := filepath.Join(t.dir, stateDirName)
	var errs []error
	if len(taken) > 0 || len(t.pending) > 0 {
		// Also creates the data directory that writeTracker needs.
		if err := os.MkdirAll(stateDir, 0o750); err != nil { // #nosec G301
			errs = append(errs, err)
		}
	}
	toSave := make(map[string]*sessionState, len(taken)+len(t.pending))
	for sessionID, root := range taken {
		if !safeSessionID.MatchString(sessionID) {
			continue // dropped with its spans; never written to disk
		}
		st := t.loadBase(sessionID).clone()
		accumulate(st, root)
		toSave[sessionID] = st
	}
	// Retry failed saves. A pending session that resumed is skipped: Flush
	// keeps its tracker current and its next compaction saves everything.
	for sessionID := range t.pending {
		if _, ok := toSave[sessionID]; !ok && store.GetByConversation(sessionID) == nil {
			toSave[sessionID] = t.base[sessionID]
		}
	}
	for sessionID, st := range toSave {
		err := t.writeTracker(sessionID, st)
		if err == nil {
			err = writeFileAtomic(filepath.Join(stateDir, sessionID+".json"), st)
		}
		if err != nil {
			// The spans are gone from the store, so these totals exist only
			// in memory until a later Compact saves them.
			t.base[sessionID] = st
			t.pending[sessionID] = true
			errs = append(errs, fmt.Errorf("save session %s: %w", sessionID, err))
			continue
		}
		delete(t.pending, sessionID)
		delete(t.base, sessionID) // reloaded from disk if the session resumes
	}
	return errors.Join(errs...)
}

// loadBase returns the saved state for a session, or empty state when
// none exists or the file cannot be read. Callers hold t.mu.
func (t *Tracker) loadBase(sessionID string) *sessionState {
	if st, ok := t.base[sessionID]; ok {
		return st
	}
	st := &sessionState{}
	b, err := os.ReadFile(filepath.Join(t.dir, stateDirName, sessionID+".json")) // #nosec G304
	if err == nil && json.Unmarshal(b, st) != nil {
		st = &sessionState{}
	}
	t.base[sessionID] = st
	return st
}

// writeTracker writes the session's tracker JSON, skipping sessions with
// no tool calls.
func (t *Tracker) writeTracker(sessionID string, st *sessionState) error {
	data := finalize(st)
	if data.Calls == 0 {
		return nil
	}
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(t.dir, sessionID+".json"), b, 0o600) // #nosec G306
}

func writeFileAtomic(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil { // #nosec G306
		return err
	}
	return os.Rename(tmp, path)
}

// accumulate adds a session's spans to its running totals.
func accumulate(st *sessionState, root *Span) {
	if st.ByTool == nil {
		st.ByTool = make(map[string]ToolDetail)
	}
	if st.Seen == nil {
		st.Seen = make(map[string]bool)
	}
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
				if st.Seen[tuid] {
					continue
				}
				st.Seen[tuid] = true
			}

			st.Calls++
			st.Last = toolName

			detail := st.ByTool[toolName]
			detail.Calls++
			st.ByTool[toolName] = detail

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
			st.TotalTokens += in + out + cacheWrite + cacheRead

			cost := s.AttrFloat64("cost_usd")
			if cost == 0 {
				cost = s.AttrFloat64("gen_ai.usage.cost")
			}
			st.TotalCost += cost

			toolName := s.AttrStr("tool_name")
			if toolName != "" {
				detail := st.ByTool[toolName]
				detail.Tokens += int(in + out + cacheWrite + cacheRead)
				detail.CostUSD += cost
				st.ByTool[toolName] = detail
			}
		}
	}
}

// finalize turns running totals into tracker JSON, spreading unattributed
// tokens and the session cost across tools. It does not modify st.
func finalize(st *sessionState) TrackerData {
	data := TrackerData{
		Calls:   st.Calls,
		Last:    st.Last,
		CostUSD: st.TotalCost,
		ByTool:  make(map[string]ToolDetail, len(st.ByTool)),
	}
	for k, v := range st.ByTool {
		data.ByTool[k] = v
	}
	classifyTokens(&data)
	if st.TotalTokens > 0 {
		distributeUnattributed(&data, st.TotalTokens)
	}
	if st.TotalCost > 0 && data.Calls > 0 {
		distributeCost(&data, st.TotalCost)
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

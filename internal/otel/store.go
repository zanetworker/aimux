package otel

import (
	"strconv"
	"sync"
	"time"

	"github.com/zanetworker/aimux/internal/subagent"
)

// Span is aimux's internal span representation, using OTEL GenAI semantic
// convention attribute names but as a simple data struct (not the OTEL SDK
// interface which is write-only). Supports hierarchy and aimux extensions.
type Span struct {
	SpanID   string
	TraceID  string
	ParentID string // empty for root spans
	Name     string // e.g., "invoke_agent", "chat", "execute_tool Read"
	Start    time.Time
	End      time.Time
	Status   SpanStatus
	Attrs    map[string]any // gen_ai.* attributes by OTEL convention names
	Children []*Span

	// aimux extensions (not in OTEL spec)
	Label string // GOOD/BAD/WASTE annotation
	Note  string // annotation rationale

	Subagent subagent.Info
}

// SpanStatus represents the outcome of a span.
type SpanStatus int

const (
	StatusOK    SpanStatus = iota
	StatusError
	StatusUnset
)

// Attr returns a span attribute value, or nil if not set.
func (s *Span) Attr(key string) any {
	if s.Attrs == nil {
		return nil
	}
	return s.Attrs[key]
}

// AttrStr returns a span attribute as a string, or "" if not set.
func (s *Span) AttrStr(key string) string {
	v, _ := s.Attr(key).(string)
	return v
}

// AttrInt64 returns a span attribute as int64, or 0 if not set.
// Handles string-encoded numbers (Claude Code sends tokens as strings).
func (s *Span) AttrInt64(key string) int64 {
	switch v := s.Attr(key).(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	return 0
}

// AttrFloat64 returns a span attribute as float64, or 0 if not set.
// Handles string-encoded numbers (Claude Code sends cost_usd as string).
func (s *Span) AttrFloat64(key string) float64 {
	switch v := s.Attr(key).(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	case int:
		return float64(v)
	case string:
		f, _ := strconv.ParseFloat(v, 64)
		return f
	}
	return 0
}

// SpanStore holds received OTEL spans in memory, indexed for fast lookup
// by session/conversation ID. Thread-safe.
type SpanStore struct {
	mu    sync.RWMutex
	// byConversation maps gen_ai.conversation.id -> root span
	byConversation map[string]*Span
	// byTraceID maps trace ID -> list of spans (flat, before tree assembly)
	byTraceID map[string][]*Span
	// seenToolUseIDs deduplicates spans by tool_use_id
	seenToolUseIDs map[string]bool
	// lastUpdate tracks when data was last received
	lastUpdate time.Time
	// convLastSeen and traceLastSeen record the last Add per session and
	// per trace, so TakeIdle can drop finished sessions.
	convLastSeen  map[string]time.Time
	traceLastSeen map[string]time.Time
	now           func() time.Time
}

// NewSpanStore creates an empty span store.
func NewSpanStore() *SpanStore {
	return &SpanStore{
		byConversation: make(map[string]*Span),
		byTraceID:      make(map[string][]*Span),
		seenToolUseIDs: make(map[string]bool),
		convLastSeen:   make(map[string]time.Time),
		traceLastSeen:  make(map[string]time.Time),
		now:            time.Now,
	}
}

// Add inserts a span into the store. If it has a conversation ID attribute,
// it's also indexed by conversation.
func (ss *SpanStore) Add(span *Span) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	// Dedup by tool_use_id (hook arrives first, OTEL batch later).
	// Cap at 10k entries to prevent unbounded growth in long sessions.
	if tuid := span.AttrStr("tool_use_id"); tuid != "" {
		if ss.seenToolUseIDs[tuid] {
			return
		}
		if len(ss.seenToolUseIDs) >= 10000 {
			ss.seenToolUseIDs = make(map[string]bool)
		}
		ss.seenToolUseIDs[tuid] = true
	}

	ss.byTraceID[span.TraceID] = append(ss.byTraceID[span.TraceID], span)
	ss.lastUpdate = ss.now()
	ss.traceLastSeen[span.TraceID] = ss.lastUpdate

	// Index by conversation/session ID
	convID := span.AttrStr("gen_ai.conversation.id")
	if convID == "" {
		convID = span.AttrStr("aimux.session_id")
	}

	if convID != "" {
		ss.convLastSeen[convID] = ss.lastUpdate
		if existing, ok := ss.byConversation[convID]; ok {
			if span.ParentID == "" && span.SpanID != existing.SpanID {
				existing.Children = append(existing.Children, span)
				span.ParentID = existing.SpanID
			}
		} else if span.ParentID == "" {
			ss.byConversation[convID] = span
		}
	}

	// Also index by aimux.session_id as an alias so remote sessions
	// can be looked up by the ID we generated at launch time.
	aimuxID := span.AttrStr("aimux.session_id")
	if aimuxID != "" && aimuxID != convID {
		if _, exists := ss.byConversation[aimuxID]; !exists {
			if root, ok := ss.byConversation[convID]; ok {
				ss.byConversation[aimuxID] = root
			}
		}
	}
}

// TakeIdle removes every session and trace with no span added since cutoff
// and returns the removed sessions' root spans by session ID (an
// aimux.session_id alias maps to the same root). Removal and return happen
// under one lock, so no span can arrive between reading and dropping.
func (ss *SpanStore) TakeIdle(cutoff time.Time) map[string]*Span {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	// A root can be indexed under several IDs (an aimux.session_id alias);
	// it is idle only when none of them has seen activity since cutoff.
	latest := make(map[*Span]time.Time)
	for id, root := range ss.byConversation {
		if seen := ss.convLastSeen[id]; seen.After(latest[root]) {
			latest[root] = seen
		}
	}
	taken := make(map[string]*Span)
	for id, root := range ss.byConversation {
		if latest[root].Before(cutoff) {
			taken[id] = root
			delete(ss.byConversation, id)
			delete(ss.convLastSeen, id)
		}
	}
	// IDs seen only on child spans have no root of their own.
	for id, seen := range ss.convLastSeen {
		if _, ok := ss.byConversation[id]; !ok && seen.Before(cutoff) {
			delete(ss.convLastSeen, id)
		}
	}
	for trace, seen := range ss.traceLastSeen {
		if seen.Before(cutoff) {
			delete(ss.traceLastSeen, trace)
			delete(ss.byTraceID, trace)
		}
	}
	return taken
}

// GetByConversation returns the root span tree for a conversation/session ID.
func (ss *SpanStore) GetByConversation(id string) *Span {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.byConversation[id]
}

// GetSpans returns all spans for a trace ID.
func (ss *SpanStore) GetSpans(traceID string) []*Span {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.byTraceID[traceID]
}

// HasData returns true if the store has any spans.
func (ss *SpanStore) HasData() bool {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return len(ss.byTraceID) > 0
}

// LastUpdate returns when data was last received.
func (ss *SpanStore) LastUpdate() time.Time {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.lastUpdate
}

// TraceCount returns the number of distinct trace IDs stored.
func (ss *SpanStore) TraceCount() int {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return len(ss.byTraceID)
}

// ConversationIDs returns all conversation/session IDs in the store.
func (ss *SpanStore) ConversationIDs() []string {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	ids := make([]string, 0, len(ss.byConversation))
	for id := range ss.byConversation {
		ids = append(ids, id)
	}
	return ids
}

// SubagentInfoBySession returns the subagent identity for a session ID.
func (ss *SpanStore) SubagentInfoBySession(sessionID string) subagent.Info {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	root, ok := ss.byConversation[sessionID]
	if !ok {
		return subagent.Info{}
	}
	if root.Subagent.HasIdentity() {
		return root.Subagent
	}
	for _, child := range root.Children {
		if child.Subagent.HasIdentity() {
			return child.Subagent
		}
	}
	return subagent.Info{}
}

// SubagentsBySession returns all distinct subagent identities seen in OTEL
// events for a session. Used to create virtual agent entries for in-process
// subagents that don't have their own PID.
func (ss *SpanStore) SubagentsBySession(sessionID string) []subagent.Info {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	root, ok := ss.byConversation[sessionID]
	if !ok {
		return nil
	}

	seen := make(map[string]subagent.Info)
	check := func(s *Span) {
		if s.Subagent.HasIdentity() && s.Subagent.ID != "" {
			seen[s.Subagent.ID] = s.Subagent
		}
	}
	check(root)
	for _, child := range root.Children {
		check(child)
	}

	if len(seen) == 0 {
		return nil
	}
	result := make([]subagent.Info, 0, len(seen))
	for _, info := range seen {
		result = append(result, info)
	}
	return result
}

// AssembleTree builds parent-child relationships for all spans in a trace.
// Call after all spans for a trace have been added.
func (ss *SpanStore) AssembleTree(traceID string) *Span {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	spans := ss.byTraceID[traceID]
	if len(spans) == 0 {
		return nil
	}

	byID := make(map[string]*Span)
	var root *Span

	for _, s := range spans {
		byID[s.SpanID] = s
		if s.ParentID == "" {
			root = s
		}
	}

	// Attach children to parents
	for _, s := range spans {
		if s.ParentID != "" {
			if parent, ok := byID[s.ParentID]; ok {
				parent.Children = append(parent.Children, s)
			}
		}
	}

	return root
}

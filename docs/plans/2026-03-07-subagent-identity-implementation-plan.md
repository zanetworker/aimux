# Subagent Identity Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add provider-agnostic subagent identity tracking so aimux shows subagents nested under their parent in the agents table, with type labels from OTEL data.

**Architecture:** New `internal/subagent` leaf package owns identity types and correlation logic. Provider interface gets one new method (`SubagentAttrKeys`). OTEL receiver extracts subagent fields and accepts HTTP hooks. Process tree tags parent-child relationships instead of filtering subagents out.

**Tech Stack:** Go, bubbletea TUI, OTEL protobuf, HTTP JSON

**Design doc:** `docs/plans/2026-03-07-subagent-identity-design.md`

---

### Task 1: Create `subagent` Package — Identity Types

**Files:**
- Create: `internal/subagent/identity.go`
- Create: `internal/subagent/identity_test.go`

**Step 1: Write the failing test**

```go
// internal/subagent/identity_test.go
package subagent

import "testing"

func TestAttrKeysEmpty(t *testing.T) {
	var k AttrKeys
	if !k.Empty() {
		t.Error("zero-value AttrKeys should be empty")
	}
	k = AttrKeys{ID: "agent_id"}
	if k.Empty() {
		t.Error("AttrKeys with ID set should not be empty")
	}
}

func TestAttrKeysExtract(t *testing.T) {
	keys := AttrKeys{
		ID:       "agent_id",
		Type:     "agent_type",
		ParentID: "parent_agent_id",
	}
	attrs := map[string]any{
		"agent_id":        "sub-123",
		"agent_type":      "Explore",
		"parent_agent_id": "main-456",
		"other_field":     "ignored",
	}

	info := keys.Extract(attrs)

	if info.ID != "sub-123" {
		t.Errorf("ID = %q, want %q", info.ID, "sub-123")
	}
	if info.Type != "Explore" {
		t.Errorf("Type = %q, want %q", info.Type, "Explore")
	}
	if info.ParentID != "main-456" {
		t.Errorf("ParentID = %q, want %q", info.ParentID, "main-456")
	}
}

func TestAttrKeysExtractMissingAttrs(t *testing.T) {
	keys := AttrKeys{ID: "agent_id", Type: "agent_type", ParentID: "parent_agent_id"}
	attrs := map[string]any{"unrelated": "value"}

	info := keys.Extract(attrs)

	if info.ID != "" || info.Type != "" || info.ParentID != "" {
		t.Errorf("expected zero Info for missing attrs, got %+v", info)
	}
}

func TestAttrKeysExtractEmptyKeys(t *testing.T) {
	var keys AttrKeys
	attrs := map[string]any{"agent_id": "sub-123"}

	info := keys.Extract(attrs)

	if info.ID != "" {
		t.Errorf("empty keys should produce zero Info, got ID=%q", info.ID)
	}
}

func TestInfoHasIdentity(t *testing.T) {
	if (Info{}).HasIdentity() {
		t.Error("zero Info should not have identity")
	}
	if !(Info{ID: "x"}).HasIdentity() {
		t.Error("Info with ID should have identity")
	}
	if !(Info{Type: "Explore"}).HasIdentity() {
		t.Error("Info with Type should have identity")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/subagent/ -v -run TestAttr`
Expected: FAIL — package does not exist

**Step 3: Write minimal implementation**

```go
// internal/subagent/identity.go
package subagent

// Info holds provider-agnostic subagent identity.
// Populated from OTEL attributes and/or process tree.
type Info struct {
	ID       string // unique subagent identifier
	Type     string // "Explore", "Plan", custom agent name
	ParentID string // parent subagent/session ID
}

// HasIdentity returns true if any identity field is set.
func (i Info) HasIdentity() bool {
	return i.ID != "" || i.Type != ""
}

// AttrKeys tells the OTEL receiver which attribute names
// to extract for subagent identity. Each provider returns
// its own mapping.
type AttrKeys struct {
	ID       string // e.g. "agent_id"
	Type     string // e.g. "agent_type"
	ParentID string // e.g. "parent_agent_id"
}

// Empty returns true if no keys are configured.
func (k AttrKeys) Empty() bool {
	return k.ID == "" && k.Type == "" && k.ParentID == ""
}

// Extract reads subagent identity from a generic attribute map
// using the configured key names.
func (k AttrKeys) Extract(attrs map[string]any) Info {
	if k.Empty() {
		return Info{}
	}
	str := func(key string) string {
		if key == "" {
			return ""
		}
		v, _ := attrs[key].(string)
		return v
	}
	return Info{
		ID:       str(k.ID),
		Type:     str(k.Type),
		ParentID: str(k.ParentID),
	}
}
```

**Step 4: Run tests to verify they pass**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/subagent/ -v`
Expected: PASS (5 tests)

**Step 5: Commit**

```bash
git add internal/subagent/
git commit -m "$(cat <<'EOF'
feat: add subagent identity package

New internal/subagent package with Info and AttrKeys types for
provider-agnostic subagent identity tracking.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Add `SubagentAttrKeys()` to Provider Interface

**Files:**
- Modify: `internal/provider/provider.go:14-48` (add method to interface)
- Modify: `internal/provider/claude.go` (implement)
- Modify: `internal/provider/codex.go` (implement)
- Modify: `internal/provider/gemini.go` (implement)

**Step 1: Write the compile-time interface check test**

The providers already have compile-time checks in their test files. Verify they exist:

Run: `grep 'var _ Provider' internal/provider/*_test.go`

If they exist, adding the method to the interface will cause compile failures in those tests — that's the "failing test."

**Step 2: Add method to Provider interface**

In `internal/provider/provider.go`, add to the `Provider` interface (after the `OTELEnv` method, around line 47):

```go
	// SubagentAttrKeys returns the OTEL attribute names this provider
	// uses for subagent identity. Return zero AttrKeys if the provider
	// doesn't support subagent tracking.
	SubagentAttrKeys() subagent.AttrKeys
```

Add import: `"github.com/zanetworker/aimux/internal/subagent"`

**Step 3: Verify compile failure**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go build ./...`
Expected: FAIL — Claude, Codex, Gemini don't implement `SubagentAttrKeys()`

**Step 4: Implement on all three providers**

In `internal/provider/claude.go`, add:

```go
func (c *Claude) SubagentAttrKeys() subagent.AttrKeys {
	return subagent.AttrKeys{
		ID:       "agent_id",
		Type:     "agent_type",
		ParentID: "parent_agent_id",
	}
}
```

In `internal/provider/codex.go`, add:

```go
func (c *Codex) SubagentAttrKeys() subagent.AttrKeys {
	return subagent.AttrKeys{}
}
```

In `internal/provider/gemini.go`, add:

```go
func (g *Gemini) SubagentAttrKeys() subagent.AttrKeys {
	return subagent.AttrKeys{}
}
```

Add import `"github.com/zanetworker/aimux/internal/subagent"` to each file.

**Step 5: Verify build passes and all tests pass**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go build ./... && go test ./internal/provider/ -v`
Expected: PASS

**Step 6: Commit**

```bash
git add internal/provider/
git commit -m "$(cat <<'EOF'
feat: add SubagentAttrKeys to Provider interface

Each provider declares its OTEL attribute names for subagent identity.
Claude returns agent_id/agent_type/parent_agent_id.
Codex and Gemini return empty (no support yet).

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Add Subagent Fields to Shared Types

**Files:**
- Modify: `internal/agent/agent.go:74-99` (add fields to Agent struct)
- Modify: `internal/otel/store.go:12-26` (add field to Span struct)
- Modify: `internal/trace/trace.go:15-26` (add field to Turn struct)

**Step 1: Add fields to `agent.Agent`**

In `internal/agent/agent.go`, add after `LastAction` field (line ~98):

```go
	ParentPID  int            // process tree parent (0 = top-level)
	Subagent   subagent.Info  // from OTEL correlation
	IsSubagent bool           // true if nested under another agent
```

Add import: `"github.com/zanetworker/aimux/internal/subagent"`

**Step 2: Add field to `otel.Span`**

In `internal/otel/store.go`, add after `Note` field (line ~26):

```go
	Subagent subagent.Info
```

Add import: `"github.com/zanetworker/aimux/internal/subagent"`

**Step 3: Add field to `trace.Turn`**

In `internal/trace/trace.go`, add after `Model` field (line ~26):

```go
	Subagent subagent.Info // subagent identity (empty for main thread)
```

Add import: `"github.com/zanetworker/aimux/internal/subagent"`

**Step 4: Verify everything compiles and all tests pass**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go build ./... && go test ./... -timeout 30s`
Expected: PASS — fields are additive, no existing code breaks

**Step 5: Commit**

```bash
git add internal/agent/agent.go internal/otel/store.go internal/trace/trace.go
git commit -m "$(cat <<'EOF'
feat: add subagent identity fields to Agent, Span, Turn

Additive fields only. ParentPID and IsSubagent on Agent for process
tree nesting. Subagent Info on Span and Turn for OTEL-derived labels.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: SpanStore Dedup by `tool_use_id`

**Files:**
- Modify: `internal/otel/store.go:87-133` (add dedup to SpanStore)
- Modify: `internal/otel/store_test.go` (add dedup tests)

**Step 1: Write the failing test**

Add to `internal/otel/store_test.go`:

```go
func TestSpanStore_DedupByToolUseID(t *testing.T) {
	store := NewSpanStore()

	// First span (from hook)
	span1 := &Span{
		SpanID:  "hook-tu123",
		TraceID: "session-1",
		Name:    "tool_result",
		Start:   time.Now(),
		Attrs: map[string]any{
			"gen_ai.conversation.id": "session-1",
			"tool_use_id":            "tu123",
		},
	}

	// Duplicate span (from OTEL batch, same tool_use_id)
	span2 := &Span{
		SpanID:  "log-999",
		TraceID: "session-1",
		Name:    "tool_result",
		Start:   time.Now(),
		Attrs: map[string]any{
			"gen_ai.conversation.id": "session-1",
			"tool_use_id":            "tu123",
		},
	}

	store.Add(span1)
	store.Add(span2) // should be deduped

	spans := store.GetSpans("session-1")
	if len(spans) != 1 {
		t.Errorf("got %d spans, want 1 (dedup failed)", len(spans))
	}
}

func TestSpanStore_NoDedupWithoutToolUseID(t *testing.T) {
	store := NewSpanStore()

	span1 := &Span{
		SpanID:  "log-1",
		TraceID: "session-1",
		Name:    "user_prompt",
		Start:   time.Now(),
		Attrs:   map[string]any{"gen_ai.conversation.id": "session-1"},
	}
	span2 := &Span{
		SpanID:  "log-2",
		TraceID: "session-1",
		Name:    "api_request",
		Start:   time.Now(),
		Attrs:   map[string]any{"gen_ai.conversation.id": "session-1"},
	}

	store.Add(span1)
	store.Add(span2)

	spans := store.GetSpans("session-1")
	if len(spans) != 2 {
		t.Errorf("got %d spans, want 2", len(spans))
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/otel/ -v -run TestSpanStore_Dedup`
Expected: FAIL — dedup not implemented

**Step 3: Implement dedup in SpanStore**

In `internal/otel/store.go`, add `seenToolUseIDs` field to `SpanStore`:

```go
type SpanStore struct {
	mu             sync.RWMutex
	byConversation map[string]*Span
	byTraceID      map[string][]*Span
	lastUpdate     time.Time
	seenToolUseIDs map[string]bool // dedup hook + OTEL events
}
```

Update `NewSpanStore`:

```go
func NewSpanStore() *SpanStore {
	return &SpanStore{
		byConversation: make(map[string]*Span),
		byTraceID:      make(map[string][]*Span),
		seenToolUseIDs: make(map[string]bool),
	}
}
```

Add dedup check at the top of `Add`, right after `ss.mu.Lock()`:

```go
func (ss *SpanStore) Add(span *Span) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	// Dedup by tool_use_id (hook arrives first, OTEL batch later)
	if tuid := span.AttrStr("tool_use_id"); tuid != "" {
		if ss.seenToolUseIDs[tuid] {
			return
		}
		ss.seenToolUseIDs[tuid] = true
	}

	// ... existing logic unchanged
```

**Step 4: Run tests to verify they pass**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/otel/ -v -run TestSpanStore`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/otel/store.go internal/otel/store_test.go
git commit -m "$(cat <<'EOF'
feat: dedup SpanStore entries by tool_use_id

Prevents double-counting when hook events arrive before OTEL batches.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: OTEL Receiver — Subagent Extraction + Hooks Endpoint

**Files:**
- Modify: `internal/otel/receiver.go:24-42` (add keysByService, enrichSubagent, handleHooks)
- Modify: `internal/otel/receiver_test.go` (add tests)

**Step 1: Write failing tests**

Add to `internal/otel/receiver_test.go`:

```go
func TestEnrichSubagent(t *testing.T) {
	store := NewSpanStore()
	keys := map[string]subagent.AttrKeys{
		"claude-code": {ID: "agent_id", Type: "agent_type", ParentID: "parent_agent_id"},
	}
	r := NewReceiverWithKeys(store, 0, keys)

	span := &Span{
		SpanID: "s1",
		Attrs: map[string]any{
			"service.name":    "claude-code",
			"agent_id":        "sub-1",
			"agent_type":      "Explore",
			"parent_agent_id": "main-0",
		},
	}

	r.enrichSubagent(span)

	if span.Subagent.ID != "sub-1" {
		t.Errorf("Subagent.ID = %q, want %q", span.Subagent.ID, "sub-1")
	}
	if span.Subagent.Type != "Explore" {
		t.Errorf("Subagent.Type = %q, want %q", span.Subagent.Type, "Explore")
	}
	if span.Subagent.ParentID != "main-0" {
		t.Errorf("Subagent.ParentID = %q, want %q", span.Subagent.ParentID, "main-0")
	}
}

func TestEnrichSubagentUnknownService(t *testing.T) {
	store := NewSpanStore()
	keys := map[string]subagent.AttrKeys{
		"claude-code": {ID: "agent_id", Type: "agent_type"},
	}
	r := NewReceiverWithKeys(store, 0, keys)

	span := &Span{
		SpanID: "s1",
		Attrs: map[string]any{
			"service.name": "unknown-agent",
			"agent_id":     "sub-1",
		},
	}

	r.enrichSubagent(span)

	if span.Subagent.ID != "" {
		t.Errorf("unknown service should not extract subagent, got ID=%q", span.Subagent.ID)
	}
}
```

Add import `"github.com/zanetworker/aimux/internal/subagent"` to the test file.

**Step 2: Run test to verify it fails**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/otel/ -v -run TestEnrichSubagent`
Expected: FAIL — `NewReceiverWithKeys` and `enrichSubagent` don't exist

**Step 3: Implement receiver changes**

In `internal/otel/receiver.go`:

Add field to `Receiver` struct:

```go
type Receiver struct {
	store          *SpanStore
	server         *http.Server
	port           int
	keysByService  map[string]subagent.AttrKeys

	mu         sync.Mutex
	traceCount int
	logsCount  int
	otherCount int
	debugLog   []string
}
```

Add constructor:

```go
// NewReceiverWithKeys creates a receiver with provider-specific subagent attr keys.
func NewReceiverWithKeys(store *SpanStore, port int, keys map[string]subagent.AttrKeys) *Receiver {
	return &Receiver{
		store:         store,
		port:          port,
		keysByService: keys,
	}
}
```

Update existing `NewReceiver` to delegate:

```go
func NewReceiver(store *SpanStore, port int) *Receiver {
	return NewReceiverWithKeys(store, port, nil)
}
```

Add `enrichSubagent` method:

```go
// enrichSubagent extracts subagent identity from span attributes
// using the provider's configured key mapping.
func (r *Receiver) enrichSubagent(span *Span) {
	if r.keysByService == nil {
		return
	}
	serviceName, _ := span.Attrs["service.name"].(string)
	keys, ok := r.keysByService[serviceName]
	if !ok || keys.Empty() {
		return
	}
	span.Subagent = keys.Extract(span.Attrs)
}
```

Call `r.enrichSubagent(span)` in `logRecordToSpan` — but since `logRecordToSpan` is a standalone function, refactor the call site. In `handleLogs`, after `logRecordToSpan`:

```go
span := logRecordToSpan(logRecord, resourceAttrs)
if span != nil {
	r.enrichSubagent(span)
	r.store.Add(span)
}
```

Similarly in `protoSpanToSpan` call site in `handleTraces`:

```go
span := protoSpanToSpan(protoSpan, resourceAttrs)
r.enrichSubagent(span)
r.store.Add(span)
```

Add hooks handler registration in `Start()`:

```go
mux.HandleFunc("/v1/hooks", r.handleHooks)
```

Add hooks handler:

```go
// hookPayload matches Claude Code HTTP hook JSON.
type hookPayload struct {
	SessionID string `json:"session_id"`
	HookEvent string `json:"hook_event_name"`
	ToolName  string `json:"tool_name"`
	ToolUseID string `json:"tool_use_id"`
	AgentID   string `json:"agent_id"`
	AgentType string `json:"agent_type"`
}

func (r *Receiver) handleHooks(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	defer req.Body.Close()
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var h hookPayload
	if err := json.Unmarshal(body, &h); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	ts := time.Now()
	spanID := fmt.Sprintf("hook-%s", h.ToolUseID)
	if h.ToolUseID == "" {
		spanID = fmt.Sprintf("hook-%d", ts.UnixNano())
	}

	span := &Span{
		SpanID:  spanID,
		TraceID: h.SessionID,
		Name:    "tool_result",
		Start:   ts,
		End:     ts,
		Status:  StatusOK,
		Attrs: map[string]any{
			"gen_ai.conversation.id": h.SessionID,
			"gen_ai.tool.name":       h.ToolName,
			"tool_use_id":            h.ToolUseID,
			"source":                 "hook",
		},
		Subagent: subagent.Info{
			ID:   h.AgentID,
			Type: h.AgentType,
		},
	}

	r.store.Add(span)

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("{}"))
}
```

Add imports: `"encoding/json"` and `"github.com/zanetworker/aimux/internal/subagent"`

**Step 4: Run all OTEL tests**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/otel/ -v -timeout 30s`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/otel/receiver.go internal/otel/receiver_test.go
git commit -m "$(cat <<'EOF'
feat: OTEL receiver extracts subagent identity + hooks endpoint

enrichSubagent uses provider-specific AttrKeys to populate Span.Subagent.
New /v1/hooks endpoint accepts Claude Code HTTP hook JSON for real-time
subagent activity. Both paths feed the same SpanStore with dedup.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: OTEL Converter — Pass Subagent to Turn

**Files:**
- Modify: `internal/otel/converter.go:105-204` (pass Subagent through)
- Modify: `internal/otel/converter_test.go` (add tests)

**Step 1: Write failing test**

Add to `internal/otel/converter_test.go`:

```go
func TestEventsToTurns_SubagentIdentity(t *testing.T) {
	root := &Span{
		SpanID: "root",
		Name:   "user_prompt",
		Start:  time.Now(),
		Attrs: map[string]any{
			"prompt.id":              "p1",
			"gen_ai.conversation.id": "session-1",
			"gen_ai.input.messages":  "search the codebase",
		},
		Subagent: subagent.Info{ID: "sub-1", Type: "Explore"},
	}

	child := &Span{
		SpanID:   "c1",
		Name:     "tool_result",
		Start:    time.Now(),
		ParentID: "root",
		Attrs: map[string]any{
			"prompt.id":     "p1",
			"gen_ai.tool.name": "Read",
		},
		Subagent: subagent.Info{ID: "sub-1", Type: "Explore"},
	}
	root.Children = append(root.Children, child)

	turns := SpansToTurns(root)
	if len(turns) == 0 {
		t.Fatal("expected at least 1 turn")
	}

	if turns[0].Subagent.Type != "Explore" {
		t.Errorf("Turn.Subagent.Type = %q, want %q", turns[0].Subagent.Type, "Explore")
	}
	if turns[0].Subagent.ID != "sub-1" {
		t.Errorf("Turn.Subagent.ID = %q, want %q", turns[0].Subagent.ID, "sub-1")
	}
}
```

Add import `"github.com/zanetworker/aimux/internal/subagent"` to the test file.

**Step 2: Run test to verify it fails**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/otel/ -v -run TestEventsToTurns_Subagent`
Expected: FAIL — `Subagent` field exists on Turn but is never populated

**Step 3: Implement passthrough**

In `internal/otel/converter.go`, in `eventsToTurn` function (around line 106), add after `t := trace.Turn{Number: num}`:

```go
	// Pick up subagent identity from the first event that has it
	for _, s := range events {
		if s.Subagent.HasIdentity() {
			t.Subagent = s.Subagent
			break
		}
	}
```

In `spanToTurn` function (around line 221), add after the struct literal:

```go
	t.Subagent = s.Subagent
```

Add import `"github.com/zanetworker/aimux/internal/subagent"` if not already present (it may be imported transitively through `trace`).

**Step 4: Run tests**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/otel/ -v -timeout 30s`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/otel/converter.go internal/otel/converter_test.go
git commit -m "$(cat <<'EOF'
feat: pass subagent identity from Span to Turn in converter

eventsToTurn picks up Subagent from the first event that has it.
spanToTurn copies it directly.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: Process Tree Tagging — Replace Filtering with Nesting

**Files:**
- Create: `internal/subagent/correlator.go`
- Create: `internal/subagent/correlator_test.go`
- Modify: `internal/discovery/process.go:140-208` (change ScanProcesses, keep filterSubagents for backward compat)

**Step 1: Write failing test for TagFromProcessTree**

```go
// internal/subagent/correlator_test.go
package subagent

import (
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
)

func TestTagFromProcessTree(t *testing.T) {
	// parentLookup simulates: PID 200's parent is PID 100
	parentLookup := func(pid int) int {
		switch pid {
		case 200:
			return 100
		case 300:
			return 100
		default:
			return 1 // init
		}
	}

	agents := []agent.Agent{
		{PID: 100, Name: "main"},
		{PID: 200, Name: "sub1"},
		{PID: 300, Name: "sub2"},
		{PID: 400, Name: "independent"},
	}

	TagFromProcessTree(agents, parentLookup)

	// PID 100 should be top-level
	if agents[0].IsSubagent {
		t.Error("PID 100 should not be a subagent")
	}

	// PID 200 should be a subagent of 100
	if !agents[1].IsSubagent {
		t.Error("PID 200 should be a subagent")
	}
	if agents[1].ParentPID != 100 {
		t.Errorf("PID 200 ParentPID = %d, want 100", agents[1].ParentPID)
	}

	// PID 300 should be a subagent of 100
	if !agents[2].IsSubagent {
		t.Error("PID 300 should be a subagent")
	}

	// PID 400 should be independent
	if agents[3].IsSubagent {
		t.Error("PID 400 should not be a subagent")
	}
}

func TestTagFromProcessTreeEmpty(t *testing.T) {
	TagFromProcessTree(nil, func(pid int) int { return 0 })
	// Should not panic
}

func TestTagFromProcessTreeSingle(t *testing.T) {
	agents := []agent.Agent{{PID: 100}}
	TagFromProcessTree(agents, func(pid int) int { return 1 })
	if agents[0].IsSubagent {
		t.Error("single agent should not be a subagent")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/subagent/ -v -run TestTagFrom`
Expected: FAIL — `TagFromProcessTree` doesn't exist

**Step 3: Implement correlator**

```go
// internal/subagent/correlator.go
package subagent

import "github.com/zanetworker/aimux/internal/agent"

// ParentPIDFunc returns the parent PID for a given PID.
// Injected to avoid coupling to os/exec.
type ParentPIDFunc func(pid int) int

// TagFromProcessTree walks the PID ancestry of each agent.
// If an agent's ancestor PID matches another agent's PID,
// it's tagged as a subagent with ParentPID set.
func TagFromProcessTree(agents []agent.Agent, getParentPID ParentPIDFunc) {
	if len(agents) <= 1 {
		return
	}

	pidToIdx := make(map[int]int, len(agents))
	for i, a := range agents {
		pidToIdx[a.PID] = i
	}

	for i := range agents {
		parentPID := findAncestorInSet(agents[i].PID, pidToIdx, getParentPID)
		if parentPID > 0 && parentPID != agents[i].PID {
			agents[i].ParentPID = parentPID
			agents[i].IsSubagent = true
		}
	}
}

// findAncestorInSet walks up to 5 PPID levels looking for an ancestor
// whose PID is in the agent set. Returns the ancestor PID or 0.
func findAncestorInSet(pid int, pidSet map[int]int, getParentPID ParentPIDFunc) int {
	cur := pid
	seen := map[int]bool{pid: true}
	for i := 0; i < 5; i++ {
		ppid := getParentPID(cur)
		if ppid <= 1 || seen[ppid] {
			return 0
		}
		if _, ok := pidSet[ppid]; ok {
			return ppid
		}
		seen[ppid] = true
		cur = ppid
	}
	return 0
}

// OTELLookup is the minimal interface the correlator needs
// from the OTEL store.
type OTELLookup interface {
	SubagentInfoBySession(sessionID string) Info
}

// EnrichFromOTEL looks up subagent identity from OTEL data
// and fills in the Subagent.Type label on agents.
func EnrichFromOTEL(agents []agent.Agent, store OTELLookup) {
	if store == nil {
		return
	}
	for i := range agents {
		if agents[i].SessionID == "" {
			continue
		}
		info := store.SubagentInfoBySession(agents[i].SessionID)
		if info.HasIdentity() {
			agents[i].Subagent = info
		}
	}
}
```

**Step 4: Run tests**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./internal/subagent/ -v`
Expected: PASS

**Step 5: Add `SubagentInfoBySession` to SpanStore**

In `internal/otel/store.go`, add method:

```go
// SubagentInfoBySession returns the subagent identity for a session ID
// by scanning stored spans. Returns zero Info if not found.
func (ss *SpanStore) SubagentInfoBySession(sessionID string) subagent.Info {
	ss.mu.RLock()
	defer ss.mu.RUnlock()

	root, ok := ss.byConversation[sessionID]
	if !ok {
		return subagent.Info{}
	}

	// Check root and children for subagent identity
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
```

**Step 6: Update `ScanProcesses` in `discovery/process.go`**

Change `ScanProcesses` (line 163) to return all agents including subagents. Rename `filterSubagents` to be an opt-in call rather than automatic:

```go
func ScanProcesses() ([]agent.Agent, error) {
	// ... existing ps aux parsing ...

	// Return all instances — caller decides whether to filter or tag.
	return instances, nil
}
```

Remove the `filterSubagents(instances)` call at line 163. The `filterSubagents` function and `hasClaudeAncestor` stay in the file (not deleted) for backward compatibility — they just aren't called by default anymore.

**Step 7: Update Claude provider's Discover to call TagFromProcessTree**

In `internal/provider/claude.go`, in `Discover()`, after enrichment loop, add:

```go
	// Tag subagent relationships from process tree
	subagent.TagFromProcessTree(agents, func(pid int) int {
		return getProcessPPID(pid)
	})
```

Add import `"github.com/zanetworker/aimux/internal/subagent"`.

**Step 8: Verify all tests pass**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go build ./... && go test ./... -timeout 30s`
Expected: PASS — some discovery tests may need updating if they relied on subagents being filtered. Check `TestFilterSubagents*` tests. These tests call `filterSubagents` directly, so they should still pass since the function still exists.

**Step 9: Commit**

```bash
git add internal/subagent/correlator.go internal/subagent/correlator_test.go \
       internal/otel/store.go internal/discovery/process.go internal/provider/claude.go
git commit -m "$(cat <<'EOF'
feat: process tree tags subagents instead of filtering them

TagFromProcessTree sets ParentPID and IsSubagent on agents whose
ancestor PID matches another agent. EnrichFromOTEL fills in type
labels when OTEL data arrives. ScanProcesses no longer filters
subagents — the caller decides.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: Wire Up in `app.go`

**Files:**
- Modify: `internal/tui/app.go:131-180` (build keysByService, pass to receiver, call correlator)

**Step 1: Build `keysByService` map in `NewApp`**

After the providers loop (line ~146), add:

```go
	// Build subagent attr key mapping from providers.
	// Maps service.name (from OTEL resource attributes) to the provider's keys.
	keysByService := make(map[string]subagent.AttrKeys)
	serviceNames := map[string]string{
		"claude":  "claude-code",
		"codex":   "codex-cli",
		"gemini":  "gemini-cli",
	}
	for _, p := range providers {
		keys := p.SubagentAttrKeys()
		if !keys.Empty() {
			if sn, ok := serviceNames[p.Name()]; ok {
				keysByService[sn] = keys
			}
		}
	}
```

**Step 2: Pass keys to receiver**

Change receiver creation (line ~175):

```go
	app.otelReceiver = aimuxotel.NewReceiverWithKeys(app.otelStore, cfg.OTELReceiverPort(), keysByService)
```

**Step 3: Call `EnrichFromOTEL` in the discovery refresh**

Find the discovery refresh handler (where `SetAgents` is called). After the orchestrator returns agents, add:

```go
	subagent.EnrichFromOTEL(agents, app.otelStore)
```

**Step 4: Verify build and tests**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go build ./... && go test ./... -timeout 30s`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/tui/app.go
git commit -m "$(cat <<'EOF'
feat: wire subagent identity into app startup and refresh

Build keysByService from providers, pass to OTEL receiver.
Call EnrichFromOTEL on each discovery refresh to fill labels.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: TUI — Nested Subagent Rendering in Agents Table

**Files:**
- Modify: `internal/tui/views/agents.go:57-154` (extend treeRow, buildTreeRows, renderChildRow)

**Step 1: Update `buildTreeRows` to nest subagents**

Replace the current `buildTreeRows` logic (line 136-154). The new logic:

1. Separate agents into parents (IsSubagent=false) and subagents (IsSubagent=true)
2. For each parent, find its subagents (matching ParentPID)
3. Add parent row, then subagent child rows (expanded by default)

```go
func (v *AgentsView) buildTreeRows() {
	filtered := v.filtered()
	v.rows = make([]treeRow, 0, len(filtered))

	// Separate parents and subagents
	var parents []agent.Agent
	subByParent := make(map[int][]agent.Agent) // parentPID -> subagents
	for _, a := range filtered {
		if a.IsSubagent {
			subByParent[a.ParentPID] = append(subByParent[a.ParentPID], a)
		} else {
			parents = append(parents, a)
		}
	}

	for _, a := range parents {
		v.rows = append(v.rows, treeRow{agent: a})

		// Existing process group expansion
		if v.expanded[a.PID] && a.GroupCount > 1 && len(a.GroupPIDs) > 0 {
			for i, pid := range a.GroupPIDs {
				if pid == a.PID {
					continue
				}
				v.rows = append(v.rows, treeRow{
					agent:   a,
					isChild: true,
					childID: i,
					isLast:  i == len(a.GroupPIDs)-1,
				})
			}
		}

		// Subagent children (expanded by default)
		subs := subByParent[a.PID]
		if len(subs) > 0 {
			collapsed := v.expanded[a.PID] == false && len(subs) > 0
			// For subagents, "expanded" map tracks collapse (inverted default)
			_ = collapsed // subagents always shown unless explicitly collapsed
			for i, sub := range subs {
				v.rows = append(v.rows, treeRow{
					agent:      sub,
					isChild:    true,
					isSubagent: true,
					isLast:     i == len(subs)-1,
				})
			}
		}
	}
}
```

Add `isSubagent` field to `treeRow`:

```go
type treeRow struct {
	agent      agent.Agent
	isChild    bool
	childID    int
	isLast     bool
	isSubagent bool // true for subagent rows (vs process group children)
}
```

**Step 2: Update `renderChildRow` to show subagent type**

In `renderChildRow` (line 369), add a branch for subagent rows:

```go
func (v *AgentsView) renderChildRow(r treeRow) string {
	glyph := "├─"
	if r.isLast {
		glyph = "└─"
	}

	if r.isSubagent {
		label := r.agent.Subagent.Type
		if label == "" {
			label = "subagent"
		}
		// Render: "  └─ Explore   haiku-4.5  Active  2m  $0.03"
		return fmt.Sprintf("  %s %s  %s  %s  %s  %s",
			glyph,
			lipgloss.NewStyle().Width(12).Render(label),
			lipgloss.NewStyle().Width(12).Render(r.agent.ShortModel()),
			r.agent.Status.Icon(),
			r.agent.FormatAge(),
			r.agent.FormatCost(),
		)
	}

	// Existing process group child rendering...
```

**Step 3: Verify build**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go build ./... && go vet ./...`
Expected: PASS

**Step 4: Commit**

```bash
git add internal/tui/views/agents.go
git commit -m "$(cat <<'EOF'
feat: nested subagent rendering in agents table

Subagents appear as children under their parent with box-drawing
glyphs. Shows type label from OTEL (or "subagent" as fallback).
Expanded by default.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: TUI — Agent Type Labels in Trace Viewer

**Files:**
- Modify: `internal/tui/views/logs.go:834-862` (add subagent label to turn header)

**Step 1: Add subagent type label to `renderTurnHeader`**

In `renderTurnHeader` (line 834), after the turn number line (line 840), add:

```go
	num := turnHeaderStyle.Render(fmt.Sprintf(" %s Turn %d", arrow, t.Number))

	// Subagent type label
	if t.Subagent.Type != "" {
		num += dimStyle.Render(fmt.Sprintf(" [%s]", t.Subagent.Type))
	}
```

This requires the `TraceTurn` type used in the logs view to expose `Subagent`. Check how `TraceTurn` wraps `trace.Turn` and ensure the field is accessible.

**Step 2: Verify build**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go build ./... && go vet ./...`
Expected: PASS

**Step 3: Commit**

```bash
git add internal/tui/views/logs.go
git commit -m "$(cat <<'EOF'
feat: show subagent type label in trace turn headers

Turns from subagents display [Explore], [Plan], etc. after the
turn number in the trace viewer.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 11: Full Integration Test + Final Verification

**Files:**
- Run all tests across all packages

**Step 1: Run full test suite**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go test ./... -timeout 30s -count=1`
Expected: PASS across all packages

**Step 2: Run vet**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go vet ./...`
Expected: No issues

**Step 3: Build binary**

Run: `cd /Users/azaalouk/go/src/github.com/zanetworker/aimux && go build -o aimux ./cmd/aimux`
Expected: Binary builds successfully

**Step 4: Review all changes**

Run: `git diff main --stat` and `git log --oneline main..HEAD`

Verify:
- New package: `internal/subagent/` (identity.go, correlator.go + tests)
- Modified: agent.go, store.go, trace.go (additive fields)
- Modified: provider.go, claude.go, codex.go, gemini.go (new interface method)
- Modified: receiver.go, converter.go (extraction + passthrough)
- Modified: process.go (no longer filters)
- Modified: app.go (wiring)
- Modified: agents.go, logs.go (rendering)

**Step 5: Final commit if any remaining changes**

```bash
git add -A
git commit -m "$(cat <<'EOF'
chore: final cleanup for subagent identity feature

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

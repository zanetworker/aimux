# Subagent Identity Design

**Date:** 2026-03-07
**Status:** Approved
**Scope:** Provider-agnostic subagent tracking across OTEL, hooks, and process discovery

## Problem

Claude Code 2.1.69 added `agent_id`, `agent_type`, and `parent_agent_id` to OTEL events and hook payloads. aimux can now distinguish subagents (Explore, Plan, custom agents) from the main thread. However, aimux currently:

1. Filters subagents out entirely (`hasClaudeAncestor` in process discovery)
2. Has no subagent concept in its data model
3. Cannot show subagent hierarchy in the agents table or trace viewer

Other providers (Codex, Gemini) may add similar telemetry later. The design must be provider-agnostic.

## Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Architecture | New `subagent` package | Follows existing pattern (discovery/, cost/, trace/). Leaf package, no circular deps. |
| Provider integration | `SubagentAttrKeys()` method on Provider | Each provider declares its own OTEL attribute names. Generic extraction logic. |
| Data ingestion | OTEL primary + HTTP hooks optional | OTEL is automatic (env var). Hooks require user config but give sub-second latency. Both land in same SpanStore. |
| Dedup strategy | By `tool_use_id` | Hook arrives first, OTEL batch 5s later. Same `tool_use_id` = same event. |
| Parent-child nesting | Process tree first, OTEL refines | Process tree gives immediate nesting. OTEL adds type label when data arrives. |
| Hook data destination | Same SpanStore | Single source of truth. TUI consumers don't need to know the data source. |

## Package Structure

```
internal/subagent/
  identity.go     # Info, AttrKeys types, Extract method
  correlator.go   # TagFromProcessTree, EnrichFromOTEL
```

Dependency graph (subagent is a leaf):

```
subagent  <--  (no aimux dependencies)
    ^
    |--- agent       (uses subagent.Info as field)
    |--- otel        (uses subagent.AttrKeys + Info)
    |--- trace       (uses subagent.Info as field)
    |--- provider    (implements SubagentAttrKeys())
    '--- tui/views   (reads IsSubagent, Subagent.Type for rendering)
```

## Data Model

### `subagent.Info`

```go
type Info struct {
    ID       string // unique subagent identifier
    Type     string // "Explore", "Plan", custom agent name
    ParentID string // parent subagent/session ID
}
```

### `subagent.AttrKeys`

```go
type AttrKeys struct {
    ID       string // OTEL attribute name for subagent ID
    Type     string // OTEL attribute name for subagent type
    ParentID string // OTEL attribute name for parent ID
}
```

Has `Empty()` and `Extract(attrs map[string]any) Info` methods. `Extract` reads from a generic attribute map using the provider's configured key names.

### Changes to Existing Types

**`agent.Agent`** adds:

```go
ParentPID   int            // from process tree (0 = top-level)
Subagent    subagent.Info   // from OTEL correlation
IsSubagent  bool           // true if nested under another agent
```

**`otel.Span`** adds:

```go
Subagent subagent.Info
```

**`trace.Turn`** adds:

```go
Subagent subagent.Info
```

## Provider Interface Change

One new method:

```go
SubagentAttrKeys() subagent.AttrKeys
```

Implementations:

- **Claude:** returns `{ID: "agent_id", Type: "agent_type", ParentID: "parent_agent_id"}`
- **Codex:** returns `AttrKeys{}` (empty, no support yet)
- **Gemini:** returns `AttrKeys{}` (empty, no support yet)

## OTEL Receiver Changes

### Subagent Extraction

`Receiver` gains a `keysByService map[string]subagent.AttrKeys` field, built at startup from registered providers. When a span arrives:

1. Look up `service.name` from resource attributes
2. Find matching `AttrKeys` in the map
3. Call `keys.Extract(span.Attrs)` to populate `span.Subagent`

### Hooks Endpoint

New handler: `POST /v1/hooks`

Accepts Claude Code HTTP hook JSON payload:

```json
{
    "session_id": "abc123",
    "hook_event_name": "PostToolUse",
    "tool_name": "Read",
    "tool_use_id": "tu_789",
    "agent_id": "agent_xyz",
    "agent_type": "Explore"
}
```

Converts to `otel.Span` with `SpanID = "hook-<tool_use_id>"` and adds to SpanStore.

### Dedup

`SpanStore.Add` checks `seenToolUseIDs map[string]bool`. If a `tool_use_id` already exists, the insert is skipped. This prevents double-counting when the OTEL batch arrives after the hook event.

## Process Tree Correlation

### `subagent.TagFromProcessTree(agents []agent.Agent)`

Runs during discovery. Walks PPID ancestry of each agent. If agent B's ancestor PID matches agent A's PID, sets:

- `B.ParentPID = A.PID`
- `B.IsSubagent = true`

Replaces the current `hasClaudeAncestor` filtering with tagging.

### `correlator.EnrichFromOTEL(agents []agent.Agent)`

Runs on each TUI refresh tick. For each agent with a `SessionID`, looks up `SubagentInfoBySession` from the OTEL store. If OTEL data has arrived with `agent_type`, fills in `agents[i].Subagent`.

### Progressive Enrichment Timeline

```
t=0s   Process tree tags PID 5678 as child of PID 1234.
       Table shows:
         ● aimux        opus-4.6   Active
           └─ subagent  (unknown)  Active       <- generic label

t=5s   OTEL batch arrives with agent_type="Explore".
       EnrichFromOTEL fills in the label.
       Table updates:
         ● aimux        opus-4.6   Active
           └─ Explore   haiku-4.5  Active       <- labeled
```

## TUI Changes

### Agents Table

Subagents render nested under parents with box-drawing glyphs:

```
 STATUS  PROJECT       MODEL       TYPE      AGE   COST
 ● act   aimux         opus-4.6    --         12m   $0.42
          ├─ Explore   haiku-4.5   Explore    2m   $0.03
          └─ Plan      opus-4.6    Plan       5m   $0.08
 ○ idle  showtime      sonnet-4.6  --        45m   $1.20
```

- Expanded by default (unlike process groups)
- Tab/x toggles collapse
- Cursor tracks parent PID when subagent disappears

### Trace Viewer (Logs View)

Turns show agent type label when `Turn.Subagent.Type != ""`:

```
Turn 3 [Explore] -- Read 5 files, Grep 3 patterns
Turn 4 [main]    -- Edit config.go
Turn 5 [Plan]    -- Created plan.md
```

Preview pane for a subagent row filters turns to only that subagent's activity.

### Converter (`otel/converter.go`)

`eventsToTurn` and `spanToTurn` pass through `Subagent` from `Span` to `Turn`:

```go
for _, s := range events {
    if s.Subagent.ID != "" {
        t.Subagent = s.Subagent
        break
    }
}
```

## User Configuration (Optional Hooks)

For real-time subagent updates, users add to `~/.claude/settings.json`:

```json
{
  "hooks": {
    "PostToolUse": [{
      "matcher": "",
      "hooks": [{
        "type": "http",
        "url": "http://localhost:4318/v1/hooks"
      }]
    }]
  }
}
```

This is optional. OTEL-based tracking works without any user config.

## Files Changed

| File | Change |
|------|--------|
| `internal/subagent/identity.go` | NEW: Info, AttrKeys types |
| `internal/subagent/correlator.go` | NEW: TagFromProcessTree, EnrichFromOTEL |
| `internal/agent/agent.go` | Add ParentPID, Subagent, IsSubagent fields |
| `internal/otel/store.go` | Add Subagent field to Span, seenToolUseIDs dedup, SubagentInfoBySession method |
| `internal/otel/receiver.go` | Add keysByService, enrichSubagent, handleHooks |
| `internal/otel/converter.go` | Pass Subagent from Span to Turn |
| `internal/trace/trace.go` | Add Subagent field to Turn |
| `internal/provider/provider.go` | Add SubagentAttrKeys() to Provider interface |
| `internal/provider/claude.go` | Implement SubagentAttrKeys() |
| `internal/provider/codex.go` | Implement SubagentAttrKeys() (empty) |
| `internal/provider/gemini.go` | Implement SubagentAttrKeys() (empty) |
| `internal/discovery/process.go` | Replace hasClaudeAncestor filtering with tagging |
| `tui/views/agents.go` | Nested subagent rendering |
| `tui/views/logs.go` | Agent type labels on turns |
| `tui/app.go` | Wire up keysByService, correlator |

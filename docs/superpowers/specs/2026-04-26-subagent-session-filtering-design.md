# Subagent Session Filtering

## Problem

Claude Code's Agent tool spawns subagent sessions that each create their own JSONL files in `~/.claude/projects/`. These short-lived, often single-turn sessions (continuous-learning evaluators, session analyzers, skill-dispatched agents) clutter the Sessions view alongside real interactive sessions. In some project directories, subagent sessions outnumber real sessions.

## Solution

Detect subagent sessions during JSONL scanning and hide them by default in the Sessions view, with a toggle to show them.

## Data Model

Add to `history.Session`:

```go
IsSubagent     bool   `json:"is_subagent"`
PermissionMode string `json:"permission_mode"`
```

No sidecar metadata changes. Detection is file-content-based.

## Detection Logic

Location: `internal/history/history.go` in `scanSession`.

Extend `sessionEntry` struct to parse `permissionMode` from JSONL entries. During scanning, track:
- `permissionMode`: from the first entry that has it
- `humanTurnCount`: count of user messages with genuine text content (excludes `tool_result` blocks, `local-command-caveat` wrapped messages, and `/command-name` slash-command messages)

A session is marked `IsSubagent = true` when:
1. `permissionMode == "bypassPermissions"` AND `humanTurnCount == 0`, OR
2. `humanTurnCount == 0` AND session duration < 5 minutes

Rule 1 catches Agent-tool-spawned subagents (which use `bypassPermissions`). The `humanTurnCount == 0` guard prevents false positives from user sessions that also use `--dangerously-skip-permissions`.

Rule 2 catches non-bypass subagents (e.g., hook-spawned evaluators) that have no human interaction and finish quickly.

## UI Changes

Location: `internal/tui/views/sessions.go`.

### SessionsView struct

Add field: `showSubagents bool` (default `false`).

### visibleSessions()

Add filter before existing filters: if `!v.showSubagents && s.IsSubagent`, skip the session. When searching (filter text active), show subagent sessions regardless of toggle so search results are complete.

### Toggle key: `H`

In `Update()`, handle `"H"` (shift+h) to toggle `showSubagents`. Reset cursor to 0 on toggle. (`G` is taken by vim "go to bottom" binding.)

### Visual indicator

When rendering a subagent session row, prefix the title/prompt column with a dim `[agent]` badge using a muted style.

### Hidden count

In the View() header area, when subagent sessions are hidden and the count is > 0, display `(+N agent)` in a dim style next to the session count. This tells the user hidden sessions exist.

### Hint bar

Update the hint to include `H:show-agents` (when hidden, default) or `H:hide-agents` (when shown).

## Testing

### history/history_test.go

Test `scanSession` with synthetic JSONL fixtures:

| Fixture | permissionMode | Human turns | Duration | Expected IsSubagent |
|---------|---------------|-------------|----------|-------------------|
| bypass_no_human | bypassPermissions | 0 | any | true |
| bypass_with_human | bypassPermissions | 3 | any | false |
| default_no_human_short | default | 0 | 2 min | true |
| default_no_human_long | default | 0 | 10 min | false |
| normal_session | default | 5 | 30 min | false |

### views/sessions_test.go

- `TestSessionsView_SubagentHiddenByDefault`: sessions with `IsSubagent=true` excluded from visible list
- `TestSessionsView_SubagentToggle`: pressing `G` shows/hides subagent sessions
- `TestSessionsView_SubagentBadge`: rendered output contains `[agent]` for subagent rows
- `TestSessionsView_SubagentCount`: hidden count displayed correctly

## Files Changed

| File | Change |
|------|--------|
| `internal/history/history.go` | Add fields to Session, extend sessionEntry, detection in scanSession |
| `internal/history/history_test.go` | Add subagent detection tests |
| `internal/tui/views/sessions.go` | Add showSubagents toggle, filter, badge, count, hint |
| `internal/tui/views/sessions_test.go` | Add toggle/filter/badge/count tests |

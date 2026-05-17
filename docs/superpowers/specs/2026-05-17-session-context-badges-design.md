# Session Context Badges

Date: 2026-05-17
Status: Draft

## Problem

The sessions list (TUI and web) shows provider, status, title, age, and cost per row. When multiple sessions share similar titles (e.g., "research #1", "research #5") or have truncated first-prompt titles, it's hard to tell sessions apart at a glance. Users need contextual cues to quickly identify what each session was working on and where it left off.

## Solution

Add inline context badges to each session row: git branch, last action, and improved title selection. No new columns or row height changes. Information density increases without visual clutter.

## Data Layer Changes

### `internal/history/history.go`

Add four fields to the `Session` struct:

```go
GitBranch  string `json:"git_branch"`
LastPrompt string `json:"last_prompt"`
LastAction string `json:"last_action"`
Model      string `json:"model"`
```

All four are already available in the JSONL stream but discarded during scanning:

- `GitBranch`: `sessionEntry.GitBranch` is parsed but never stored. Keep overwriting `s.GitBranch` on every line (last value wins, same pattern as `LastActive`).
- `LastPrompt`: extract the last human message text. Keep overwriting a `lastPrompt` local var during the scan loop, assign to `s.LastPrompt` after the loop completes.
- `LastAction`: reuse `extractLastToolAction` from `discovery/session.go` (move to a shared location or duplicate the small function). Keep overwriting on every assistant message.
- `Model`: already tracked as a local var `model`. Assign to `s.Model` at the end.

### `internal/history/history.go` — `sessionEntry` struct

No changes needed. `GitBranch` is already parsed.

### `internal/history/history.go` — `parseSessionLine`

Extend to track last prompt and last action alongside existing first prompt extraction:

```go
// After existing prompt extraction:
if entry.Message.Role == "user" {
    if text := extractUserText(entry.Message.Content); text != "" {
        s.LastPrompt = text  // always overwrite (last wins)
    }
}

// After existing model extraction:
if entry.Message.Role == "assistant" {
    if action := extractLastToolAction(entry.Message.Content); action != "" {
        s.LastAction = action
    }
}
```

### Shared `extractLastToolAction`

The function currently lives in `internal/discovery/session.go`. Either:
- (a) Move it to a shared package (e.g., `internal/history/` or `internal/trace/`)
- (b) Duplicate the ~40-line function in `internal/history/history.go`

Option (b) is simpler since the two call sites scan different structures. Prefer (b) unless a third consumer appears.

## TUI Sessions List

### `internal/frontend/tui/views/sessions.go`

#### Row layout

Current:
```
★ CLAUDE  IDLE  Fix width clipping in pane-zoom plugin              1d ago  $89.18
```

New:
```
★ CLAUDE  IDLE  feat/resize  Fix width clipping in pane-zoom...  Ed: renderer.go  1d ago  $89.18
```

#### Branch badge

- Position: between status and title
- Style: accent color (`#A78BFA`), compact
- Max width: 15 chars, truncated with `…`
- Behavior: hidden when empty (non-git session). When branch is `main` or `master`, render in dim (`#6B7280`) to reduce noise — most sessions are on the default branch.
- Column allocation: 16 chars (15 + 1 space)

#### Last action

- Position: between title and age
- Style: dim text (`#6B7280`)
- Max width: 20 chars, truncated with `…`
- Format: short action summaries from `extractLastToolAction` (e.g., `Ed: config.go`, `Sh: go test`, `Rd: README.md`)
- Behavior: hidden when empty. Space reclaimed by title column.

#### Title preference

Change the title selection logic in `renderSessionRow`:

```go
// Current: title > firstPrompt
// New: title > lastPrompt > firstPrompt
prompt := s.Title
if prompt == "" {
    prompt = s.LastPrompt
}
if prompt == "" {
    prompt = s.FirstPrompt
}
```

This makes untitled sessions show what the user was *last* working on rather than how they started.

#### Column width calculation

Update `sessionColumns` to account for optional branch and action columns:

```go
type sessionColumns struct {
    age     int  // 8
    branch  int  // 16 (0 if no sessions have branches)
    prompt  int  // elastic (remaining space)
    action  int  // 20 (0 if no sessions have actions)
    turns   int  // 6
    cost    int  // 10
    project int  // 20 (when showing all projects)
}
```

Branch and action columns are only allocated if at least one visible session has data for that field. This avoids wasting space when browsing sessions from non-git contexts.

## TUI Agents Table

### `internal/frontend/tui/views/agents.go`

The agents table already has `Model`, `Dir`, and `LastAction` columns. Add a branch badge:

- Position: after the name column, before provider
- Style: same accent color as sessions list
- Data source: `Agent.GitBranch` (already populated by discovery)
- Same dim treatment for `main`/`master`

## Web Frontend

### API (`internal/frontend/web/handlers.go`)

Add the new fields to the sessions API response:

```go
"git_branch":  s.GitBranch,
"last_prompt": s.LastPrompt,
"last_action": s.LastAction,
"model":       s.Model,
```

### Web sessions list (`web/src/`)

The `AgentCard` component already shows branch and last action for active agents. For the sessions list/table view, render:

- Branch as a small inline badge (same style as `AgentCard` line 172-177)
- Last action as dim monospace text
- Model as a subtle footer element

## What We Are NOT Doing

- No new sortable columns — branch and action are inline decorations
- No second row per session — keeps the dense, scannable table
- No branch for non-git sessions — field stays empty, badge hidden
- No last action for sessions with zero tool calls — badge hidden
- No changes to the starred/annotation/tag system
- No changes to session discovery, file matching, or resume logic

## Testing

- `internal/history/`: test that `scanSession` populates `GitBranch`, `LastPrompt`, `LastAction`, and `Model` from JSONL fixtures
- `internal/frontend/tui/views/`: test `renderSessionRow` output includes branch badge and action when present, omits when empty
- Visual: verify in the TUI that rows render correctly at different terminal widths (80, 120, 200 cols)

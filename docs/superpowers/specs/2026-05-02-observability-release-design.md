# aimux v2.0 -- Observability Release

**Tagline**: Tame the agent sprawl. See all your agents. Trace what they did. Judge if it was good.

**Positioning**: aimux is the observability layer for AI coding agents. Every other tool tells you which agents are running. aimux tells you what they actually did, whether it was good, and exports it for eval. The sprawl is the hook; observability is the depth.

**Competitive context**: cmux (15K stars) owns terminal replacement; agent-deck (2K stars) owns session management; OpenShell (5K stars) owns sandboxing. Nobody has traces, annotations, OTEL export, or cost-per-turn. That is aimux's lane.

**Release strategy**: Single release. Everything lands together.

## 1. Performance: Instant Startup

### 1.1 Startup Cache

Cache the last known agent list to `~/.aimux/cache/last-seen.json`.

**Format:**
```json
[
  {
    "pid": 12345,
    "name": "my-api",
    "provider": "claude",
    "cwd": "/Users/you/src/my-api",
    "model": "opus-4.7",
    "status": "active",
    "cost": 0.42,
    "last_seen": "2026-05-01T10:30:00Z"
  }
]
```

**Startup flow:**
1. Read cache file, render agents immediately with a dim/stale style indicator
2. Fire real discovery as a background `tea.Cmd`
3. When discovery returns, replace stale entries; dead agents disappear silently
4. Write cache on every successful discovery refresh

**Target**: <50ms to first paint with real data. The user sees agents from last session immediately, they update in-place within 1-2 seconds.

**New files:**
- `internal/cache/cache.go` -- read/write agent cache
- `internal/cache/cache_test.go`

### 1.2 Lazy Trace Loading

Currently session file matching (correlating `ps -o lstart=` with JSONL timestamps) happens during discovery. This is expensive and unnecessary until the user actually opens a trace.

**Change:**
- Discovery returns agents with `SessionFile: ""` (unresolved)
- Session file is resolved lazily when the preview pane needs it (on agent selection)
- Resolution runs as an async `tea.Cmd` so it doesn't block the UI; the preview pane shows the header info (provider, model, dir, branch, cost) immediately and adds the trace once the session file is found
- Result is cached on the agent struct; subsequent selections skip resolution

### 1.3 Filesystem Watchers for Discovery

Add `fsnotify` watchers on known session directories:
- Claude: `~/.claude/projects/`
- Codex: `~/.codex/`
- Gemini: `~/.gemini/`

New file events trigger immediate re-discovery (not waiting for 2s tick). Keep the 2s poll as fallback for status changes (process alive/dead). Reduce poll to status-only check (reuse cached `ps` snapshot, skip full re-parse when no new files detected).

**New files:**
- `internal/discovery/watcher.go` -- fsnotify setup and event handling
- `internal/discovery/watcher_test.go`

**New dependency:** `github.com/fsnotify/fsnotify`

### 1.4 Lazy Render for Long Traces

Only parse and render visible lines plus a 50-line buffer above and below. As the user scrolls, parse additional turns on demand. This prevents the trace view from freezing on sessions with thousands of turns.

## 2. Performance: Split Pane & Rendering

### 2.1 VT Emulator Double-Render Fix

`TermView.Write()` currently calls `tv.term.Render()` twice per write: once before and once after, to compare and detect changes for history. This is the single biggest performance drain in the split pane.

**Fix:** Replace the pre/post render comparison with a dirty-tracking approach:
- Track bytes written since last history snapshot
- Snapshot into history on a debounce timer (every 100ms of idle, or every N bytes written)
- Remove the two `Render()` calls from `Write()` entirely
- Only call `Render()` in the actual `Render()` method (when Bubble Tea needs a frame)

### 2.2 History Append Optimization

Currently every screen change appends ALL lines of the current screen to history (50 string allocations per write for a 50-row terminal with rapid output).

**Fix:**
- Only snapshot into history when the screen actually scrolls (cursor at bottom row + newline), not on every content change
- Use a ring buffer (`container/ring` or custom) instead of a slice with `history = history[len-10000:]` copy
- Batch: debounce snapshots to at most one per 100ms

### 2.3 Tmux Poll Interval

`tmux capture-pane` currently runs every 50ms (20 forks/second per agent).

**Fix:**
- Increase base interval to 100ms
- Adaptive polling: 100ms when agent is actively Working, 500ms when Idle or Done
- Hash the capture output (`fnv` or `xxhash`) instead of full string comparison to detect changes cheaply

### 2.4 Render Caching

**Fix:**
- Cache the last rendered string and the scroll position that produced it
- Only rebuild when scroll position changes or content is dirty
- Use `strings.Builder` instead of `strings.Join` for render assembly
- Pre-allocate builder capacity based on terminal dimensions

### 2.5 Keyboard Responsiveness

**Fix:**
- Ensure key events are processed before render in the Bubble Tea `Update` loop (they already should be, but verify no blocking render calls in the hot path)
- Debounce rapid scroll events: batch multiple scroll-up/down keypresses into a single re-render
- In split view, route keystrokes to the PTY without waiting for the render cycle

### 2.6 Split View Load Time

When entering split view, the user currently waits for PTY/tmux connection and trace parsing before seeing anything.

**Fix:**
- Show a placeholder immediately ("Loading session...") while the PTY/tmux session connects
- Lazy-load the trace pane: parse only the last 50 turns initially, load earlier turns on scroll-up
- For tmux mirror sessions: start with the cached `capture-pane` from the preview (already fetched), swap to live mirror in background

## 3. Observability: Live Traces

### 3.1 JSONL File Tailer

New component that watches a session's JSONL file and streams new turns to the trace view in real-time.

**Implementation:**
- New package: `internal/trace/tailer.go`
- Uses `fsnotify` to watch the JSONL file for write events
- On each event, reads new bytes from last-known file offset
- Parses complete JSON lines from the new bytes
- Sends parsed turns as `tea.Msg` to the trace view via a Go channel
- Fallback: 1s poll checks file size against last offset (in case fsnotify misses events)
- Auto-scroll to bottom when new turns arrive, unless user has scrolled up

**New files:**
- `internal/trace/tailer.go`
- `internal/trace/tailer_test.go`

### 3.2 Cross-Session Trace Search

`/` from the agents table greps all agents' JSONL files for the query string.

**Behavior:**
- `/` enters filter mode (reuses existing `filterMode` + `filterInput`)
- On each keystroke (debounced 200ms), fire async grep goroutines per agent
- Each goroutine reads the agent's JSONL file, counts lines matching the query
- Results update the table as they arrive
- New `MATCH` column shows hit count
- Agents with zero matches are hidden
- Enter on a result jumps to that agent's trace with matches highlighted
- Esc clears filter, restores full list

**Changes to existing code:**
- `internal/tui/views/agents.go`: add `MATCH` column, conditional rendering
- `internal/history/search.go`: extend to support content search (currently exists but may need enhancement)

### 3.3 Cost-per-Turn

Each assistant turn in the trace view shows its token cost inline.

**Display:**
```
  17:32 ASST  I'll look at the login.go file...
              [opus-4.7 | 1,247 tokens | $0.03]
  17:32 TOOL  Read /src/auth/login.go
  17:33 ASST  I see the bug. The session token...
              [opus-4.7 | 3,891 tokens | $0.09]
```

- Uses existing `cost/tracker.go` pricing tables
- Toggled with `$` key (off by default to keep trace clean)
- Token counts already available from JSONL parsing

## 4. Observability: Diff Summary

### 4.1 Compact Diff in Preview Pane

Show a file change summary in the preview pane, between the header block and the trace:

```
  aimux
  Provider: claude  Model: opus-4.6  Mode: bypass
  Dir: /Users/azaalouk/go/src/github.com/zanetworker/aimux
  Branch: main
  ...

  Files changed: 4 (+89 / -23)
    M internal/tui/app.go  +45 -12
    M internal/tui/views/preview.go  +38 -3
    A internal/cache/cache.go  +62
    M go.mod  -8

  13 turns | 129 actions | 1 errors | $56.98 total | ...
  • Turn 1  14:57  $0.68  ...
```

**Implementation:**
- Run `git diff --stat --no-color` in the agent's CWD
- Cache the output on agent selection (don't re-run on every render)
- Only show when uncommitted changes exist
- Falls back to parsing tool calls from trace if CWD is not a git repo

**New files:**
- `internal/diff/summary.go`
- `internal/diff/summary_test.go`

### 4.2 Expandable Full Diff

Press `d` in the preview pane to expand the full diff content.

**Behavior:**
- `d` replaces the preview pane content with scrollable `git diff --no-color` output
- Syntax-highlighted: green for additions, red for deletions, cyan for file headers
- Scrollable with `j/k` or arrow keys
- `d` again or `Esc` returns to the normal preview (trace) view
- Under the hood: runs `git diff --no-color` in agent's CWD, caches output

## 5. Notifications & Attention

### 5.1 Attention Counter in Header

Add a counter to the header bar showing agents that need attention:

```
  aimux  5 agents  2 working  [2 need attention]  $1.42 total
```

- Count = agents with status `Waiting` (permission prompt) or `Done` (recently finished)
- `Done` agents drop out of the count after 60 seconds
- Uses existing `prevStatuses` map for transition detection

### 5.2 Terminal Bell

Send `\a` (BEL character) on status transitions:
- Working -> Waiting (permission prompt)
- Working -> Done (agent finished)
- Any -> Error

Fires alongside existing macOS desktop notifications. No configuration needed; works in any terminal and tmux.

### 5.3 Per-Event Notification Config

```yaml
# ~/.aimux/config.yaml
notifications:
  permission: true    # agent hit permission prompt
  done: true          # agent finished
  error: true         # agent errored
  bell: true          # terminal bell
  desktop: true       # macOS notification center
```

All default to `true`. Existing `m` key mutes everything (overrides per-type settings).

## 6. Positioning & README

### 6.1 README Rewrite

New structure:
1. **Hook**: "Tame the agent sprawl" with demo GIF
2. **Three differentiators**: Trace, Annotate, Export
3. **Table-stakes**: Discovery, split view, cost tracking, launcher, diff summary
4. **Install**: brew + source
5. **Screenshots**: one per key feature

Lead paragraph:
```
Every tool tells you which agents are running.
aimux tells you what they actually did -- and whether it was good.

Traces. Annotations. Cost tracking. OTEL export.
The observability layer for AI coding agents.
```

### 6.2 Updated Assets

New screenshots/GIFs needed:
- Live trace streaming (showing turns appearing in real-time)
- Cross-session search (filtered agents table with MATCH column)
- Cost-per-turn in trace view
- Diff summary in preview pane
- Attention counter in header

### 6.3 OpenShell Stub Provider

Architecture placeholder only. No functional implementation.

- Add `internal/provider/openshell.go` with interface skeleton
- Implement `Name() string` returning `"openshell"`
- All other methods return zero values or "not implemented" errors
- Comment: "OpenShell integration planned. Will discover sandboxes via openshell sandbox list."
- Do NOT register in `app.go` or `config.go` (not active)

## 7. New Dependencies

| Package | Purpose |
|---------|---------|
| `github.com/fsnotify/fsnotify` | File system watchers for discovery + trace tailing |

No other new dependencies. `git diff` uses subprocess (already pattern in codebase). Ring buffer is stdlib `container/ring` or a simple custom implementation.

## 8. Files Changed (Estimated)

**New packages:**
- `internal/cache/` -- startup cache (cache.go, cache_test.go)
- `internal/diff/` -- git diff summary (summary.go, summary_test.go)
- `internal/trace/tailer.go` -- JSONL file tailer (tailer.go, tailer_test.go)
- `internal/discovery/watcher.go` -- fsnotify watcher (watcher.go, watcher_test.go)
- `internal/provider/openshell.go` -- stub provider

**Modified packages:**
- `internal/terminal/view.go` -- remove double render, dirty tracking, ring buffer history, render caching, builder
- `internal/terminal/tmux.go` -- adaptive poll interval, hash-based change detection
- `internal/tui/app.go` -- startup cache integration, attention counter, terminal bell, filter-to-search upgrade, `d` key for diff, `$` key for cost toggle
- `internal/tui/views/agents.go` -- MATCH column for search results
- `internal/tui/views/preview.go` -- diff summary section, expandable diff view
- `internal/tui/views/logs.go` -- live trace updates via tailer, cost-per-turn rendering, search highlighting
- `internal/tui/views/header.go` -- attention counter display
- `internal/config/config.go` -- notification config fields
- `internal/discovery/orchestrator.go` -- integrate fsnotify watcher, lazy session file resolution

## 9. Out of Scope

- OpenShell functional integration (waiting for stabilization)
- Landing page / website
- Historical session search (only running agents)
- OTEL-based live trace enrichment (JSONL tail is sufficient for now)
- GUI or native app (the terminal already won)
- MCP socket pooling
- Session forking

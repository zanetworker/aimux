# Launch Modes and Google Tasks Integration

**Date:** 2026-05-08
**Status:** Draft

## Overview

Three launch modes for spawning agent sessions from aimux, plus a Google Tasks integration that surfaces tasks in the UI and allows launching agents from them.

### Architecture Principle

The `tasks.Provider` interface lives in `internal/tasks/` (core package, no UI imports). Both frontends (web HTTP handlers and TUI views) consume the same provider instance. This follows the existing architecture rule: business logic in core packages, UI is a thin adapter layer.

## Launch Modes

### Mode 1: Quick Launch

One-two click launch using a preconfigured list of favorite directories.

**Config:**

```yaml
quick_launch:
  directories:
    - ~/go/src/github.com/zanetworker/aimux    # first = default (pre-selected)
    - ~/go/src/github.com/zanetworker/research
    - ~/go/src/github.com/zanetworker/blog-concept
```

**UI (web):**
- Directory pills showing basename (`aimux`, `research`, `blog-concept`)
- Full path shown as tooltip on hover
- First entry pre-selected
- Pick agent (claude/codex/gemini), launch

**UI (TUI):**
- Same pill-style selection in the existing launcher overlay
- Shown as a new "Quick" tab alongside "Recent" and "Browse" in the directory step

**Behavior:**
- If `quick_launch.directories` is empty or missing, this mode is hidden
- Directories are validated on startup; missing ones are skipped with a warning

### Mode 2: Directory Launch

Browse the filesystem to pick a project directory.

**TUI:** Already works via `launcher.go` (Recent/Browse tabs with filesystem navigation). No changes needed.

**Web UI (currently broken):** Replace the raw text input in `LaunchDialog.tsx` with a proper directory browser.

**New API endpoints:**
- `GET /api/directories/recent` — returns recent directories (same data as TUI's `RecentDirEntry`)
- `GET /api/directories/browse?path=<dir>` — lists entries in a directory (name, isDir). Filters hidden files. Sorted: dirs first, then alphabetical.

**Web UI changes:**
- Two tabs in the launch dialog: "Recent" and "Browse" (matching TUI)
- Recent tab: clickable list of recent directories with age labels
- Browse tab: navigable directory tree with breadcrumb path, click to enter directory, "Select" button to pick current directory
- Type-to-filter in both tabs

### Mode 3: Task Launch

Launch an agent session from a Google Tasks item.

**Flow:**
1. User opens tasks panel (toggle button in top bar)
2. Selects a task list from dropdown (default from config)
3. Clicks Launch on a task
4. Launch dialog opens with task context pre-filled:
   - Task title and notes shown (read-only)
   - Optional text input for additional user prompt
   - Agent picker (claude/codex/gemini pills)
   - Directory picker (quick launch dirs as pills)
5. Agent session spawns with the assembled prompt (via template)
6. When session ends: task is auto-marked complete in Google Tasks, a note is added with session summary (cost, turns, what changed)
7. User can reopen the task from the panel if the result wasn't satisfactory

## Tasks Panel

### Placement

Toggleable right-side panel, visible from any tab (Agents, Sessions, Skills).

**Toggle:** Button in the top bar next to `+ Launch`, showing badge count: `[Tasks(3)]`

**Hidden state:** Panel not rendered. Full space for agents + trace. Badge in top bar still shows count.

**Shown state:** Panel appears on the right (~250px wide). Trace/preview panel narrows to accommodate. Panel has a close button `[x]`.

### Panel contents

```
┌─────────────────────┐
│ Tasks (3)       [x] │
│ [Work v]            │
│                     │
│ PENDING             │
│ [] Fix auth bug     │
│   May 10       [>]  │
│ [] Add REST API     │
│   May 12       [>]  │
│ [] Review PR #42    │
│   May 14       [>]  │
│                     │
│ COMPLETED           │
│ [x] Deploy fix      │
│   May 8    (reopen) │
│ [x] Update docs     │
│   May 7    (reopen) │
└─────────────────────┘
```

- Task list dropdown at top to switch between lists
- Pending tasks grouped above, completed below
- Each pending task has a Launch button `[>]`
- Completed tasks have a Reopen link
- Clicking a task row could expand to show notes inline

### Persistence

- Panel open/closed state persisted in localStorage (web) or config (TUI)
- Selected task list persisted similarly

## Google Tasks Backend

### Interface

```go
// internal/tasks/tasks.go

type Task struct {
    ID        string
    Title     string
    Notes     string
    Due       string    // RFC 3339 date
    Status    string    // "needsAction" or "completed"
    ListID    string
    ListName  string
    Updated   string    // RFC 3339 timestamp
}

type TaskList struct {
    ID   string
    Name string
}

type Provider interface {
    ListTaskLists() ([]TaskList, error)
    ListTasks(listID string) ([]Task, error)
    CompleteTask(listID, taskID string) error
    ReopenTask(listID, taskID string) error
    AddNote(listID, taskID, note string) error
}
```

### Two implementations

**`gws` CLI backend** (`internal/tasks/gws.go`):
- Shells out to `gws tasks tasklists list --params '{}'` and `gws tasks tasks list --params '{"tasklist":"<id>"}'`
- Parses JSON output
- Used when `gws` binary is available and authenticated
- Default for local aimux

**MCP backend** (`internal/tasks/mcp.go`):
- Calls Google Workspace MCP server over HTTP (JSON-RPC to a running MCP server)
- Tools used: `list_task_lists`, `list_tasks`, `manage_task`
- Used when running remotely (K8s pods) or when `gws` is unavailable
- Requires a reachable MCP server endpoint, configured via `tasks.mcp_endpoint` in config
- aimux acts as a lightweight MCP client for this single integration (no full MCP SDK needed; raw HTTP JSON-RPC calls to `tools/call`)

**Backend selection:**

```go
// internal/tasks/resolve.go

func NewProvider(backend string) (Provider, error) {
    switch backend {
    case "gws":
        return NewGWSProvider()
    case "mcp":
        return NewMCPProvider()
    case "auto", "":
        if gwsAvailable() {
            return NewGWSProvider()
        }
        return NewMCPProvider()
    }
}
```

`auto` (default): tries `gws` first (checks binary exists + auth file present), falls back to MCP.

## Config

```yaml
quick_launch:
  directories:
    - ~/go/src/github.com/zanetworker/aimux
    - ~/go/src/github.com/zanetworker/research
    - ~/go/src/github.com/zanetworker/blog-concept

tasks:
  backend: auto          # "gws", "mcp", or "auto"
  mcp_endpoint: ""       # MCP server URL, e.g. "http://localhost:3000" (required for mcp backend)
  default_list: "Work"   # list name or ID
  prompt_template: |
    Work on the following task: {title}

    Details: {notes}

    Additional instructions: {user_prompt}

    When done, summarize what you did.
```

## API Endpoints (Web)

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/directories/recent` | Recent directories for directory browser |
| GET | `/api/directories/browse?path=` | List directory contents |
| GET | `/api/tasks/lists` | All task lists |
| GET | `/api/tasks?list=<id>` | Tasks in a list |
| POST | `/api/tasks/:id/complete` | Mark task complete, add note |
| POST | `/api/tasks/:id/reopen` | Reopen a completed task |
| POST | `/api/agents/launch` | Existing endpoint, extended with task context |

### Launch request extension

The existing `launchRequest` struct gains optional task fields:

```go
type launchRequest struct {
    Provider   string `json:"provider"`
    Dir        string `json:"dir"`
    Model      string `json:"model"`
    Mode       string `json:"mode"`
    TaskID     string `json:"task_id,omitempty"`      // Google Tasks ID
    TaskListID string `json:"task_list_id,omitempty"` // which list
    UserPrompt string `json:"user_prompt,omitempty"`  // additional instructions
}
```

When `TaskID` is set, the backend fetches the task, assembles the prompt via template, and passes it to the agent. It also stores the task-to-session mapping so it can auto-complete the task when the session ends.

## Session-Task Lifecycle

1. **Launch:** User picks task, optionally adds prompt, picks agent + dir. Session spawns.
2. **In progress:** Task stays as `needsAction` in Google Tasks. Optionally, a note is added: "Session started by aimux at <timestamp>".
3. **Session ends:** aimux detects session completion (idle/done status). Auto-marks task complete. Adds note: "Completed by <agent> -- <turns> turns, $<cost>. Summary: <first/last prompt excerpt>".
4. **Reopen:** User clicks "reopen" in tasks panel. Task set back to `needsAction` in Google Tasks. Note added: "Reopened at <timestamp>".

## Scope

**In scope:**
- Quick launch with configurable directory list (web + TUI)
- Directory browser for web UI (API + React component)
- Google Tasks panel (web only for now)
- Task launch flow with prompt template
- Auto-complete on session end with notes
- Reopen from panel
- Two backends: `gws` CLI and MCP

**Out of scope (future):**
- TUI tasks panel (add later, same backend)
- Creating new tasks from aimux
- Sub-task creation from agent discoveries
- Task list creation/management
- Syncing task status during session (only on end)
- Other task providers (Todoist, Linear, Jira)

## File Structure

```
internal/
  tasks/
    tasks.go        # Task, TaskList types, Provider interface
    gws.go          # gws CLI backend
    mcp.go          # MCP backend
    resolve.go      # NewProvider() with auto-detection
    tasks_test.go   # interface compliance + unit tests
  config/
    config.go       # add QuickLaunch and Tasks config structs
  frontend/
    web/
      handlers.go   # add directory browse + tasks API endpoints
      server.go     # wire tasks provider
    tui/
      views/
        launcher.go # add Quick tab to directory step
web/
  src/
    components/
      LaunchDialog.tsx   # refactor: add directory browser, quick launch pills
      TasksPanel.tsx     # new: toggleable right-side tasks panel
      TaskLaunchDialog.tsx  # new: launch-from-task dialog with prompt input
    App.tsx              # wire tasks panel toggle + state
    components/
      StatsBar.tsx       # add tasks toggle button with badge
```

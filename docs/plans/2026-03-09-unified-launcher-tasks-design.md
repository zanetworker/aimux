# Unified Launcher and Tasks Design

**Date**: 2026-03-09
**Status**: Design (approved)
**Author**: azaalouk + Claude

## 1. Problem statement

aimux today is primarily an observability tool for local agents. The vision is a unified control plane where local and remote (K8s) agents are treated identically — same UX, different infrastructure. Two gaps block this:

1. **The launcher only spawns local sessions.** There is no way to start a remote K8s-backed session or fire a task at a K8s agent from aimux.
2. **There is no task view.** Tasks in Redis (from K8s runs) and local task files (from Claude team runs) are invisible in aimux — you need MCP tools or redis-cli to see them.

## 2. Design principles

- **Transparent infrastructure.** Local and remote feel identical to the user. The LOC column is informational, never a required decision point.
- **Claude decides scale.** The launcher never asks "how many agents". Claude (the brain) calls `spawn_agent` as needed. The launcher's job is to give Claude the right context and hands.
- **Roles and counts are implementation details.** Users pick what they want done, not how many pods or which role.
- **Visibility is cross-cutting.** Agents view, Tasks view, Sessions view show everything regardless of where it runs.
- **Core/TUI separation.** All data types and business logic live in core packages with no bubbletea/lipgloss imports. TUI views are thin renderers. A future web or API frontend must be able to use core packages with zero TUI dependency.

## 3. Entry point: `:new` picker

Pressing `n` or `:new` opens a tiny 3-line picker:

```
╭─ New ────────╮
│ [S]ession    │
│ [T]ask       │
╰──────────────╯
```

`s` or `enter` → Session launcher
`t` → Task launcher
`esc` → cancel

## 4. Session launcher (redesigned)

### 4.1 Flow

Same flow for local and remote — Where is a toggle, not a branch:

```
╭─ New Session ──────────────────────────────╮
│                                            │
│  Where:     [ Local ]  [ Remote ]          │
│  Provider:  ▸ claude   codex   gemini      │
│  Directory: ▸ aimux    zanetworker   2m    │
│             blog-concept  zanetworker  1h  │
│                                            │
│  ↵ Launch                                  │
╰────────────────────────────────────────────╯
```

Advanced options (model/mode/runtime/OTEL) remain accessible via `o` but are not shown by default.

### 4.2 What Launch does

**Local**: identical to today — spawns local Claude/Codex/Gemini process in the selected directory.

**Remote**: spawns a local Claude session (identical to local) with two additions:
1. A context hint injected into the session: Claude is informed it has K8s agents available via `spawn_agent` and `create_task` MCP tools.
2. The K8s hook activates: `Agent(team_name=...)` is steered toward MCP tools instead of local subagents.

No K8s pods are pre-launched. Claude decides when to call `spawn_agent` based on the task at hand. This mirrors how local sessions work — Claude decides when to spawn local subagents.

The directory maps to a git remote URL (read via `git remote get-url origin`). This URL is passed as context so the remote Claude session knows which repo the K8s agents should clone.

### 4.3 Implementation changes

- Add `Where` toggle (`local`/`remote`) as the first field in the existing launcher
- Add `Remote bool` and `RepoURL string` to `LaunchMsg`
- `app.go` `handleLaunch()`: if `Remote=true`, inject K8s context into the spawn command and activate the hook
- Existing local flow unchanged

## 5. Task launcher (new)

### 5.1 Flow

Minimal — describe what you want done, pick where:

```
╭─ New Task ──────────────────────────────────╮
│                                             │
│  Where:     [ Local ]  [ Remote ]           │
│  Provider:  ▸ claude ●   gemini ○           │
│             (● available  ○ not configured) │
│                                             │
│  Research MCP frameworks in 2026...         │
│  _                                          │
│                                             │
│  ↵ Launch   Esc cancel                      │
╰─────────────────────────────────────────────╯
```

Provider availability (Remote only): determined by whether the matching Deployment exists in the `agents` namespace.

### 5.2 What Launch does

**Remote**:
1. `spawn_agent(provider, role=researcher, count=1)` — 1 pod, auto-selected role, user never sees this
2. `create_task(prompt)` — task queued in Redis
3. Pod picks up task automatically
4. Task appears in Tasks view

**Local**: deferred (V2). Show a message: "Local tasks available in a future version — use Session for interactive local work."

### 5.3 Task spawning — core/TUI separation

The act of spawning a pod and creating a task in Redis is **not** in the TUI. It lives in a core package:

```go
// internal/task/loader.go
type Spawner interface {
    SpawnTask(provider, prompt string) error
}

// K8s provider implements Spawner:
//   1. scales deployment via K8s API
//   2. writes task to Redis
// TUI calls: spawner.SpawnTask("claude", "Research MCP...")
// TUI never touches Redis or K8s directly.
```

`LaunchTaskMsg` (TUI-internal message) carries only the user inputs. `app.go` finds the provider implementing `Spawner` and delegates:

```go
type LaunchTaskMsg struct {
    Where    string // "local" or "remote"
    Provider string // "claude", "gemini", etc.
    Prompt   string
}
```

## 6. Tasks view (new)

### 6.1 Entry point

`T` keybinding (currently unused) or `:tasks` command. Added to the header hint bar.

### 6.2 Layout

```
 Tasks  ● 2 running  ✓ 14 done  ○ 3 pending  ✗ 1 failed   $4.02
 ─────────────────────────────────────────────────────────────────
 TASK                       AGENT            LOC    STATUS   AGE
 ✓ Research: LangGraph      researcher-1     k8s    done     45m
 ✓ Research: AutoGen        researcher-1     k8s    done     40m
 ● Research: CrewAI         gemini-res-1     k8s    running  20m
 ● Implement API layer       coder-1          k8s    running  30m
 ○ Review implementation    (pending)         k8s    waiting  —
 ✗ Research: Swarm          researcher-2     k8s    failed   35m
```

Select a task → right pane shows full result (reads `team:{id}:task:{id}:result_full` from Redis).

### 6.3 Architecture — core/TUI separation

The `Task` type and all loading logic live in **core packages**, not in views:

```
internal/task/
  task.go       ← Task struct, StatusIcon() — NO bubbletea/lipgloss
  loader.go     ← LoadFromRedis(), LoadFromLocalFiles(), Spawner interface
```

The TUI view is a thin renderer:
```
internal/tui/views/tasks.go  ← renders []task.Task, imports internal/task
```

The provider interface returns core types:
```go
// In internal/provider/provider.go
type TaskLister interface {
    ListTasks() ([]task.Task, error)
}
```

A web or API frontend imports `internal/task` and `internal/provider` directly — zero TUI dependency.

### 6.4 Data sources

- **Remote tasks**: `task.LoadFromRedis(redisURL, teamID)` — reads `team:{id}:tasks:all` sorted set, `HGETALL` each hash, normalizes to `[]task.Task`
- **Local tasks**: `task.LoadFromLocalFiles(teamID)` — reads `~/.claude/tasks/{team}/task-*.json`, normalizes to `[]task.Task`

Both set `task.Task.Loc` to `"k8s"` or `"local"` respectively. The TUI view receives `[]task.Task` and renders without knowing the source.

### 6.4 Header summary

```
Agents 6  Tasks 20 (●2 ✓14 ○3 ✗1)  Cost $4.02  ·  k8s: 3 pods
```

## 7. Visibility: what stays the same

The existing views are unchanged:

| View | Change |
|---|---|
| Agents view | No change — LOC column already added |
| Sessions view | No change |
| Costs view | No change (K8s cost wiring is future work) |
| Teams view | No change |
| Traces/Logs view | No change |

The user experience for existing functionality is not affected. All changes are additive.

## 8. Package layout after this change

```
internal/
  task/
    task.go        ← Task struct, StatusIcon() [NEW, core]
    loader.go      ← LoadFromRedis(), LoadFromLocalFiles(), Spawner interface [NEW, core]
  provider/
    provider.go    ← TaskLister interface (returns []task.Task) [MODIFY]
    k8s.go         ← implements TaskLister + Spawner [MODIFY]
  tui/
    app.go         ← calls task.Load*(), delegates Spawner, no Redis/K8s calls [MODIFY]
    views/
      tasks.go     ← renders []task.Task [NEW, TUI]
      task_launcher.go  ← UI state machine only [NEW, TUI]
      launcher.go  ← add Where toggle [MODIFY, TUI]
```

**Test for correct separation:** `go test ./internal/task/... ./internal/provider/...` must pass with zero bubbletea/lipgloss imports in those packages.

## 9. Implementation order

1. `internal/task/` core package — `Task` struct + `LoadFromRedis()` + `Spawner` interface
2. `provider.TaskLister` and `provider.Spawner` — K8s provider implements both
3. `:new` picker (tiny TUI overlay)
4. Session launcher: add `Where` toggle
5. Task launcher: minimal TUI overlay, delegates to `Spawner`
6. Tasks view: renders `[]task.Task` from `TaskLister`
7. Header: task summary counts

## 9. Out of scope (future)

- Local task routing (fire-and-forget to local agents)
- Auto-scaling based on task queue depth (HPA)
- Task dependency visualization (DAG)
- K8s cost data in cost view
- Sub-agent tracking within K8s pods

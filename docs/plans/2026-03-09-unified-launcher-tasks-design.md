# Unified Launcher and Tasks Design

**Date**: 2026-03-09
**Status**: Design (approved)
**Author**: azaalouk + Claude

## 1. Problem statement

aimux today is primarily an observability tool for local agents. The vision is a unified control plane where local and remote (K8s) agents are treated identically — same UX, different infrastructure. Three gaps block this:

1. **The launcher only spawns local sessions.** No way to start a remote K8s-backed session or fire a task from aimux.
2. **There is no task view.** Tasks in Redis and local task files are invisible — you need MCP tools to see them.
3. **Remote Claude Code sessions** (full Claude Code running on K8s) are not supported as a first-class session type.

## 2. Design principles

- **Transparent infrastructure.** Local and remote feel identical. The LOC column is informational, never a required decision point.
- **Claude decides scale.** The launcher never asks "how many agents". Claude calls `spawn_agent` as needed.
- **Roles and counts are implementation details.** Users describe what they want, not how to run it.
- **Visibility is cross-cutting.** Agents, Tasks, Sessions views show everything regardless of where it runs.
- **Core/TUI separation.** All data types and business logic live in core packages with no bubbletea/lipgloss imports. TUI views are thin renderers. A future web or API frontend uses core packages with zero TUI dependency.
- **Local mode is self-contained.** aimux works perfectly with no K8s cluster configured. K8s features are purely additive, gated by `kubernetes.enabled: true`. No K8s imports are instantiated in local-only mode.

## 3. Two K8s agent types

The K8s infrastructure supports two distinct agent types, each purpose-built:

| Type | Use case | Image | Entry point | Trace visibility |
|---|---|---|---|---|
| **Claude Code pod** | Sessions — full capabilities, interactive | `agent-claude` (MODE=session) | tmux + claude | OTel Collector in K8s |
| **Python coordinator pod** | Tasks — fire-and-forget, parallel | `agent-claude` (MODE=agent) | coordinator loop | Redis heartbeat |

**One image, two modes** — the same UBI9 image handles both via a `MODE` env var:

```dockerfile
CMD ["sh", "-c", "if [ \"$MODE\" = 'session' ]; then \
    tmux new-session -d -s claude && \
    tmux send-keys -t claude 'claude' Enter && \
    tmux attach -t claude; \
  else \
    python /opt/app-root/src/agent/main.py; \
  fi"]
```

This avoids maintaining separate images. Resource limits differ (sessions need more RAM than coordinator pods).

## 4. Entry point: `:new` picker

Pressing `n` or `:new` opens a tiny picker:

```
╭─ New ────────╮
│ [S]ession    │
│ [T]ask       │
╰──────────────╯
```

`s` → Session launcher (start an agent working on a project)
`t` → Task launcher (fire a unit of work at available agents)
`esc` → cancel

## 5. Session launcher (redesigned)

### 5.1 Flow

Same fields for local and remote — Where is a toggle, not a branch:

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

Advanced options (model/mode/runtime/OTEL) via `o`, not shown by default.

### 5.2 What Launch does

**Local**: identical to today — spawns local Claude/Codex/Gemini process in the selected directory.

**Remote**: spawns a **Claude Code pod** on K8s (`MODE=session`):
- Pod starts Claude Code inside a tmux session
- aimux attaches via `kubectl exec` using the existing `TmuxBackend` interface
- The directory maps to a git remote URL (`git remote get-url origin`) — passed as context so Claude knows which repo to clone
- No K8s pods are pre-launched for the brain. Claude decides to spawn task coordinator pods if it needs parallel arms via the MCP `spawn_agent` tool

### 5.3 Split pane for remote sessions

Remote Claude Code sessions use a new `KubectlExecBackend` implementing the existing `terminal.SessionBackend` interface:

```go
// internal/terminal/kubectl.go
type KubectlExecBackend struct {
    namespace string
    podName   string
    tmuxSession string
}
// implements Read/Write/Resize/Close/Alive
// wraps: kubectl exec -it <pod> -n <ns> -- tmux attach -t claude
```

This reuses all existing TUI split-pane rendering. The TUI does not know it is talking to a remote pod vs a local process.

### 5.4 Implementation changes

- Add `Where` toggle to existing launcher (Tab to switch, same fields)
- Add `Remote bool`, `RepoURL string` to `LaunchMsg`
- `app.go` `handleLaunch()`: if `Remote=true`, scale up a `MODE=session` pod, wait for it to be ready (Redis heartbeat), attach via `KubectlExecBackend`
- Existing local flow completely unchanged

## 6. Task launcher (new)

### 6.1 Flow

Minimal — describe the work, pick where and provider:

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

Provider availability (Remote only): whether a `MODE=agent` Deployment exists in the `agents` namespace.

### 6.2 What Launch does

**Remote**:
1. Scales up 1 Python coordinator pod (`MODE=agent`) — user never sees this
2. Creates task in Redis — agent picks it up automatically
3. Task appears in Tasks view

**Local**: deferred (V2). Show: "Local tasks available in a future version — use Session for now."

### 6.3 Core/TUI separation for spawning

Task spawning logic lives in core, not TUI:

```go
// internal/task/loader.go
type Spawner interface {
    SpawnTask(provider, prompt string) error
}
// K8s provider implements: scale deployment + write Redis task
// TUI calls spawner.SpawnTask(), never touches Redis or K8s directly
```

## 7. Tasks view (new)

### 7.1 Entry point

`T` keybinding or `:tasks` command.

### 7.2 Layout

```
 Tasks  ● 2 running  ✓ 14 done  ○ 3 pending  ✗ 1 failed   $4.02
 ─────────────────────────────────────────────────────────────────
 TASK                       AGENT            LOC    STATUS   AGE
 ✓ Research: LangGraph      researcher-1     k8s    done     45m
 ● Research: CrewAI         gemini-res-1     k8s    running  20m
 ● Implement API             coder-1          k8s    running  30m
 ○ Review implementation    (pending)         k8s    waiting  —
 ✗ Research: Swarm          researcher-2     k8s    failed   35m
```

Select task → right pane shows full result from Redis.

### 7.3 Core/TUI separation

```
internal/task/
  task.go    ← Task struct, StatusIcon(), IsTerminal(), FormatAge() — NO TUI imports
  loader.go  ← LoadFromRedis(), LoadFromLocalFiles(), Spawner interface

internal/tui/views/tasks.go  ← renders []task.Task, imports internal/task only
```

Provider interface returns core types:
```go
type TaskLister interface { ListTasks() ([]task.Task, error) }
```

### 7.4 Data sources

- **Remote**: `task.LoadFromRedis(redisURL, teamID)`
- **Local**: `task.LoadFromLocalFiles(teamID)` — reads `~/.claude/tasks/{team}/task-*.json`
- Both set `Task.Loc` — TUI renders without knowing the source

## 8. OTEL architecture — clean local/remote separation

### 8.1 Local mode (unchanged, always runs)

```
Local Claude Code session → localhost:4318 → aimux OTEL receiver → in-memory store → trace view
```

The local OTEL receiver (`otel/receiver.go`) runs unconditionally as part of aimux. It has no K8s dependencies and works fully offline.

### 8.2 Remote mode (additive, gated by config)

Remote Claude Code pods cannot reach `localhost:4318` on the developer's laptop (NAT/firewall). Instead, an OTel Collector runs inside K8s and is exposed via LoadBalancer:

```
Remote Claude Code pod
  OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector.agents.svc:4317 (cluster-internal)
  → OTel Collector (K8s deployment + LoadBalancer service)
         ↓ LoadBalancer endpoint (same pattern as Redis)
         ← aimux connects as client, reads spans
              → merged into same trace store → trace view
```

### 8.3 Gating

```go
// always runs — no K8s dependency
a.otelReceiver = otel.StartReceiver(cfg.OTELPort)

// only when configured — instantiated conditionally
if cfg.Kubernetes.Enabled && cfg.Kubernetes.OTELEndpoint != "" {
    a.k8sOTELReader = otel.NewK8sReader(cfg.Kubernetes.OTELEndpoint)
}
```

No K8s imports anywhere in the local code path. `otel/k8s_reader.go` exists but its constructor is never called in local-only mode. Local mode is fully self-contained.

### 8.4 OTel Collector manifest

```
deploy/k8s/otel-collector.yaml  ← new: OTel Collector Deployment + LoadBalancer Service
```

Config: receives OTLP/gRPC from pods, exports OTLP/HTTP on the LoadBalancer endpoint for aimux to read.

## 9. Package layout after this change

```
internal/
  task/
    task.go          ← Task struct [NEW, core]
    loader.go        ← LoadFromRedis(), LoadFromLocalFiles(), Spawner [NEW, core]
  terminal/
    kubectl.go       ← KubectlExecBackend (implements SessionBackend) [NEW, core]
  otel/
    receiver.go      ← local receiver (unchanged)
    k8s_reader.go    ← reads remote OTel Collector [NEW, core, conditional]
  provider/
    provider.go      ← TaskLister, Spawner interfaces [MODIFY]
    k8s.go           ← implements TaskLister + Spawner [MODIFY]
  tui/
    app.go           ← picker routing, Remote launch, loadTasks() [MODIFY]
    views/
      tasks.go       ← renders []task.Task [NEW, TUI]
      task_launcher.go   ← UI state machine [NEW, TUI]
      launcher.go    ← Where toggle [MODIFY, TUI]
deploy/k8s/
  otel-collector.yaml   ← new
  agent-claude-coder.yaml    ← add MODE=agent env var
  agent-claude-reviewer.yaml ← add MODE=agent env var
  agent-claude-researcher.yaml ← add MODE=agent env var
  agent-claude-session.yaml  ← new: MODE=session deployment
```

**Invariant:** `grep -r "bubbletea\|lipgloss\|charmbracelet" internal/task/ internal/terminal/kubectl.go internal/otel/k8s_reader.go internal/provider/` → no output.

## 10. Implementation order

1. `internal/task/` core package — Task struct, loaders, Spawner interface
2. `provider.TaskLister` + `provider.Spawner` on K8s provider
3. `KubectlExecBackend` in `internal/terminal/kubectl.go`
4. OTel Collector manifest + `otel/k8s_reader.go`
5. Agent manifests: add `MODE` env var, add `agent-claude-session.yaml`
6. `:new` picker (TUI)
7. Session launcher: Where toggle + Remote launch path
8. Task launcher: minimal TUI, delegates to Spawner
9. Tasks view: renders []task.Task from TaskLister
10. Header: task summary counts

## 11. Out of scope (future)

- Local task routing (fire-and-forget to local agents)
- Auto-scaling on Redis queue depth (HPA)
- Task dependency visualization (DAG)
- K8s cost data in cost view
- Sub-agent tracking within K8s pods
- OTel mTLS / Gateway API for production hardening
- Headscale/Tailscale mesh as alternative to LoadBalancer for OTEL

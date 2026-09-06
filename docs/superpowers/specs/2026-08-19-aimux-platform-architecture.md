# aimux Platform Architecture

**Date:** 2026-08-19
**Status:** Design
**Scope:** Restructure aimux from a monitoring TUI into a self-hosted agent management platform

## Problem

aimux grew organically from a TUI that discovers running agents into a system that also spawns, tracks, and coordinates them. Three structural problems block further evolution:

1. **What vs where confusion.** K8s and OpenShell implement the `Provider` interface, but they're infrastructure (where agents run), not agent types (what they are). A Claude agent in a K8s pod shows up as a "k8s" agent, losing its identity. Half the Provider methods are nil stubs.

2. **No managed session lifecycle.** Sessions are discovered passively from process scanning and JSONL files. There's no way to declaratively create a session (bind an agent config to an environment), track it through a lifecycle, or resume it after restart.

3. **Scattered infrastructure logic.** OpenShell code is spread across five packages. Redis coordination is leaked into the MCP server. compose/ and runtime/ overlap. agent-compose is barely used (just `Stop()`).

## Design Principles

1. **Each layer has one job.** Provider owns agent identity. Environment owns compute. Coordination owns task flow. No layer does another's job.
2. **Progressive disclosure.** Local-only usage requires zero config. Each capability tier (OpenShell, Redis, named agent configs) is additive.
3. **agent-compose is the binder.** It connects agent configs to environments. aimux doesn't rebuild this; it surfaces what agent-compose already provides.
4. **Existing UX is the default.** Pick a harness, run locally. Everything else is opt-in.

## Three-Axis Model

```
Provider (WHAT)          Environment (WHERE)         Coordination (HOW)
Claude, Codex            OpenShell, Local, K8s       Redis

Owns:                    Owns:                       Owns:
- Trace format           - Sandbox lifecycle          - Task queue
- Session files          - Network policy             - Inbox messaging
- Models/spawn cmd       - Provider backends          - Heartbeat
- OTEL env               - Compute resources          - Cost tracking
- Embed capability       - Egress rules               - Event broadcast
```

These three axes are orthogonal. A Claude agent (Provider) can run locally or in an OpenShell sandbox (Environment) with or without Redis coordination.

### Axis 1: Provider (What)

Answers: "what kind of agent is this?"

Providers own agent identity: trace format, session file layout, spawn commands, model list, OTEL configuration, embed capability. Every method is meaningful for every implementation. No nil stubs.

Current providers: Claude, Codex.

The `Provider` interface stays largely as-is but gets cleaner:
- Remove: `Kill()` moves to Environment (it's a lifecycle operation)
- Keep: `ParseTrace`, `FindSessionFile`, `SpawnCommand`, `CanEmbed`, `OTELEnv`, `SpawnArgs`, `RecentDirs`, `ResumeCommand`
- Provider no longer does discovery. Discovery is a collaboration between Environment (finds running agents on its infrastructure) and Provider (identifies them by type).

### Axis 2: Environment (Where)

Answers: "where does this agent run?"

An Environment is OpenShell's domain: sandbox lifecycle, network policy, provider backends (Podman, K8s, remote), compute resources. In code, this is agent-compose's `Executor` interface plus the surrounding config.

Three environment types:

**Local** (default, zero config):
- `LocalExecutor`: forks a process. ~20 lines implementing the `Executor` interface.
- Discovery: process scanning (`ps aux`), tmux session listing.
- No policy, no sandbox, no gateway.

**OpenShell** (opt-in, requires gateway):
- `CLIExecutor` today; upgradeable to Go SDK or direct gateway RPC behind the same `Executor` interface (no consumer changes). The CLI has the highest friction (subprocess overhead, output parsing, binary dependency) but the lowest integration cost as a starting point.
- Discovery: `openshell sandbox list`.
- Policy: deny-by-default egress via OpenShell policy files.
- Provider backends: Podman (dev), K8s (staging/prod), remote. Configured on the gateway. OpenShell opinionate on sandboxes on top of these backends; the recommended production path is OpenShell with a K8s provider backend, not raw K8s.

**Raw K8s** (opt-in, requires cluster + Redis):
- `K8sExecutor`: wraps Kubernetes API for pod lifecycle directly, without OpenShell sandbox governance.
- Discovery: Redis heartbeats + pod labels.
- Policy: Kubernetes NetworkPolicy (managed by the team, not by OpenShell).
- Valid for teams that manage their own pod security or workloads where sandbox overhead isn't justified. Not the default for remote deployments; when in doubt, use OpenShell (which can use K8s as its provider backend while adding sandbox governance, policy enforcement, and the gateway abstraction).

Environments are named, CRUD-able resources stored in config:

```yaml
# ~/.aimux/config.yaml
environments:
  local:
    type: local

  dev:
    type: openshell
    gateway: "https://gateway.dev:8443"
    insecure: true
    # OpenShell picks its provider backend (Podman on dev machines)

  staging:
    type: openshell
    gateway: "https://gateway.staging:8443"
    image: "quay.io/aimux/agent:latest"
    # OpenShell with K8s provider backend on staging

  prod:
    type: openshell
    gateway: "https://gateway.prod:8443"
    image: "registry.prod/agents/claude:v2"
    # OpenShell with K8s provider backend on production

  # Raw K8s: agents as pods, no sandbox governance.
  # Use when teams manage their own pod security or sandbox overhead isn't justified.
  # Default for remote is OpenShell; this is opt-in.
  bare-k8s:
    type: k8s
    redis_url: "redis://redis.prod:6379"
    namespace: agents
    kubeconfig: ~/.kube/prod
```

### Axis 3: Coordination (How)

Answers: "how do tasks flow between the dashboard and agents?"

Redis is the coordination plane. It's infrastructure-agnostic: both K8s and OpenShell environments can use Redis for task queuing and messaging. Redis is optional; without it, sessions are tracked locally only.

Six Redis data structures (unchanged from current):

| Structure | Key Pattern | Purpose |
|---|---|---|
| Heartbeat | `team:{id}:heartbeat` | Agent liveness (hash: agentID -> timestamp) |
| Agent metadata | `team:{id}:agent:{agentID}` | Provider, role, model, namespace |
| Task queue | `team:{id}:tasks:pending` + `team:{id}:task:{taskID}` | Sorted set + hash per task |
| Inbox | `team:{id}:inbox:{agentID}` | Per-agent message stream |
| Cost | `team:{id}:cost:{agentID}` | Token counts (HINCRBY) |
| Events | `team:{id}:events` | Broadcast stream |

Redis is extracted from `provider/k8s.go` and `mcpserver/server.go` into a standalone `internal/coordination/` package. Both K8s and OpenShell environments register their agents in Redis. The MCP server talks to Redis through the coordination interface.

## agent-compose as the Binder

agent-compose connects Agent configs to Environments. It already has the right model:

```
Agent (declarative config)
  + RuntimeProfile  -> image, entrypoint, env vars, harness type
  + InferenceSpec   -> model endpoint, egress rules
  + MCPSpec[]       -> MCP server configs, egress rules
  + Policy          -> network policy path
  + Skill[]         -> prompt text, skill mounts
  = ResolvedSpec (concrete: everything the Executor needs)
```

`Engine.Resolve(agentName)` flattens an Agent's references into a `ResolvedSpec`.
`Engine.Run(agentName, opts)` resolves + calls `Executor.CreateSandbox()` + starts the agent.

aimux currently bypasses this pipeline (shells out directly). The refactor promotes it to the primary launch path.

### Agent Configs

Named, declarative agent configurations. Stored in `~/.aimux/agents.yaml` or `agents.yaml` in the project root:

```yaml
agents:
  reviewer:
    runtime: claude
    inference: anthropic
    model: opus
    prompt: "You are a code reviewer. Focus on correctness and security."
    mcp: [github]
    skills: [code-review]
    policy: restricted

  researcher:
    runtime: claude
    inference: anthropic
    model: sonnet
    prompt: "You research topics thoroughly with citations."
    mcp: [web-search]
    skills: [research]
```

Agent configs are optional. Without them, the launcher shows raw harness names (Claude, Codex) as today. With them, named configs appear as additional launcher options.

## Session Lifecycle

A Session is the managed binding of an Agent config + Environment. It has a lifecycle:

```
created -> running <-> idle -> terminated
                   \-> error
```

### Session Object

```go
type Session struct {
    ID            string
    AgentConfig   string        // agent-compose agent name (or raw harness name)
    Environment   string        // environment name
    SandboxName   string        // runtime identifier (process PID, sandbox name, pod name)
    Provider      string        // agent type (claude, codex)
    Status        SessionStatus // created, running, idle, terminated, error
    StartTime     time.Time
    LastActivity  time.Time
    WorkingDir    string
    Model         string
    TokensIn      int64
    TokensOut     int64
    CostUSD       float64
}
```

### Session Creation Flow

```
User picks agent config + environment
  |
  v
agent-compose Engine.Resolve(agentConfig)
  -> ResolvedSpec (image, env vars, policy, MCP, skills)
  |
  v
Executor.CreateSandbox(name, resolvedSpec)
  -> sandbox/process created
  |
  v
Session registered (local store + Redis if configured)
  -> status: running
  |
  v
Agent starts inside sandbox/process
  -> heartbeat begins (Redis) or process monitored (local)
```

### Session Discovery

Discovery is a collaboration:

1. **Local environment**: process scanner finds running agents, tags each with `ProviderName` from the process name/args.
2. **OpenShell environment**: `openshell sandbox list` finds sandboxes, tags with `ProviderName: "claude"` (already does this today in `discovery/sandbox.go`).
3. **K8s environment**: Redis heartbeats + pod labels find agents, tags with `ProviderName` from the `provider` pod label or Redis metadata.
4. **Provider** is looked up by `ProviderName` to handle trace parsing, session files, etc.

## Progressive Disclosure

Three tiers, each additive:

### Tier 1: Solo Developer (zero config)

```
Launcher -> pick Claude/Codex -> LocalExecutor forks process -> done
```

- No `environments` config needed (implicit `local` environment).
- No `agents.yaml` needed (raw harness names in launcher).
- No Redis needed (sessions tracked via process scan + JSONL files).
- Identical to today's experience.

### Tier 2: Team with OpenShell (add gateway config)

```yaml
# ~/.aimux/config.yaml
environments:
  dev:
    type: openshell
    gateway: "https://gateway.dev:8443"
```

```
Launcher -> pick Claude/Codex -> pick environment (local / dev) -> run
```

- Harness runs inside an OpenShell sandbox with governance.
- agent-compose resolves the runtime + policy into the sandbox.
- Sessions tracked locally + via `openshell sandbox list`.
- Still no Redis, no named agent configs required.

### Tier 3: Platform Operator (full config)

```yaml
# ~/.aimux/agents.yaml
agents:
  reviewer:
    runtime: claude
    inference: anthropic
    mcp: [github]
    skills: [code-review]
    policy: restricted

# ~/.aimux/config.yaml
environments:
  prod:
    type: openshell
    gateway: "https://gateway.prod:8443"
coordination:
  redis_url: "redis://redis.prod:6379"
  team_id: "alpha-team"
```

```
Launcher -> pick "reviewer" config -> pick environment (prod) -> run
         -> or pick raw Claude for local quick session
```

- Named agent configs with skills, MCP, inference, policy.
- Multiple named environments selectable at launch.
- Redis coordination: tasks, messaging, heartbeat, cost tracking.
- MCP server exposes: `spawn_agent`, `create_task`, `send_message`, `list_agents`.
- Self-hosted CMA equivalent.

## CMA Equivalence

| CMA Resource | aimux Equivalent | Backed By |
|---|---|---|
| Agent | agent-compose `Agent` config | `agents.yaml` |
| Environment | Named environment config | `config.yaml` environments section |
| Session | Managed `Session` object | Local store + Redis |
| Deployment | Cron schedule + agent + environment | Future (launchd/cron wrapper) |
| Vault | agent-compose `Env` + K8s Secrets | Environment-specific |
| Memory Store | Per-agent memory directory | Filesystem (future: Redis) |
| Skills | agent-compose `Skill` | Skill files (future: registry) |
| `ant beta:worker poll` | Redis inbox + worker | `coordination/` package |

## Package Structure (After)

```
internal/
  provider/
    provider.go          # Provider interface (identity only, no infra)
    claude.go            # Claude provider
    codex.go             # Codex provider
    helpers.go           # Shared utilities
    # DELETED: openshell.go (was 100% nil stubs)
    # DELETED: k8s.go (split into environment + coordination)

  environment/
    environment.go       # Environment interface + named config registry
    local.go             # LocalExecutor (fork process, ps discovery)
    openshell.go         # OpenShell discovery + lifecycle (consolidates
                         #   discovery/sandbox.go, compose/*, otel/session_fetch.go)
    k8s.go               # K8s discovery + pod lifecycle

  coordination/
    coordinator.go       # Coordinator interface
    redis.go             # Redis implementation (extracted from provider/k8s.go
                         #   and mcpserver/server.go)
    local.go             # Local-only fallback (in-memory, no Redis)

  session/
    session.go           # Session struct + lifecycle
    store.go             # Session persistence (replaces controller/session_store.go)
    manager.go           # Create/track/resume/archive sessions

  # Unchanged packages:
  agent/                 # Agent struct, Status enum (display model)
  config/                # Config loading (adds environments + coordination sections)
  controller/            # Shared business logic (sort, filter, attend, export, etc.)
  cost/                  # Per-model pricing
  history/               # Session file scanning (post-mortem records)
  terminal/              # SessionBackend (PTY embed, tmux mirror, openshell connect)
  trace/                 # Turn/ToolSpan types
  otel/                  # OTLP receiver + store + exporter
  mcpserver/             # MCP server (talks to coordination/ interface, not Redis directly)
  spawn/                 # Agent launch (delegates to session/manager.go)
  plugin/                # Plugin system

  frontend/
    tui/                 # Bubble Tea TUI (thin adapter)
      app.go             # Thin router dispatching to coordinators
      coordinators/      # AgentCoord, SessionCoord, TraceCoord, LaunchCoord
      views/             # View components (unchanged)
    web/                 # HTTP + SSE + WebSocket (thin adapter)
    # CLI stays in cmd/aimux/cmd/
```

## Interface Definitions

### Provider (What)

```go
type Provider interface {
    Name() string
    ParseTrace(filePath string) ([]trace.Turn, error)
    FindSessionFile(a agent.Agent) string
    CanEmbed() bool
    ResumeCommand(a agent.Agent) *exec.Cmd
    SpawnCommand(dir, model, mode string) *exec.Cmd
    SpawnArgs() SpawnArgs
    RecentDirs(max int) []RecentDir
    OTELEnv(endpoint string) string
    OTELServiceName() string
    SubagentAttrKeys() subagent.AttrKeys
}
```

No `Discover()`. No `Kill()`. No infrastructure methods. Every method is meaningful for every implementation.

### Environment (Where)

```go
type Environment interface {
    Name() string
    Type() string // "local", "openshell", "k8s"
    Discover() ([]agent.Agent, error)
    CreateSandbox(ctx context.Context, opts SandboxOpts) (string, error)
    DeleteSandbox(ctx context.Context, name string) error
    ListSandboxes(ctx context.Context) ([]SandboxStatus, error)
    Kill(a agent.Agent) error
    CheckHealth() HealthStatus
}
```

Each environment tags discovered agents with the correct `ProviderName` (from process name, pod labels, or Redis metadata).

### Coordinator (How)

```go
type Coordinator interface {
    RegisterAgent(ctx context.Context, agent CoordAgent) error
    Heartbeat(ctx context.Context, agentID string) error
    CreateTask(ctx context.Context, task Task) error
    ListTasks(ctx context.Context) ([]Task, error)
    GetTaskResult(ctx context.Context, taskID string) (string, error)
    SendMessage(ctx context.Context, agentID, text string) error
    GetCosts(ctx context.Context) ([]AgentCost, error)
}
```

Two implementations: `RedisCoordinator` (full) and `LocalCoordinator` (in-memory fallback when Redis isn't configured).

## Migration Path

### Phase 1: Extract Environment from Provider

1. Create `internal/environment/` with the `Environment` interface.
2. Move K8s infrastructure (Redis discovery, pod lifecycle, health) into `environment/k8s.go`.
3. Consolidate OpenShell scatter (5 packages) into `environment/openshell.go`.
4. Create `environment/local.go` wrapping process scan + fork.
5. Delete `provider/openshell.go` (100% nil stubs).
6. Remove infrastructure methods from Provider interface.
7. Update orchestrator to call Environment.Discover() alongside Provider lookups.

### Phase 2: Extract Coordination from K8s

1. Create `internal/coordination/` with the `Coordinator` interface.
2. Move Redis operations from `provider/k8s.go` and `mcpserver/server.go` into `coordination/redis.go`.
3. Create `coordination/local.go` (in-memory fallback).
4. Update MCP server to talk to Coordinator interface, not Redis directly.
5. Wire both K8s and OpenShell environments to register agents in the Coordinator.

### Phase 3: Promote agent-compose

1. Route all launches through `agent-compose Engine.Resolve() + Engine.Run()` instead of direct shell-out.
2. Add `LocalExecutor` to agent-compose (or in aimux as an adapter).
3. Add named environment configs to `~/.aimux/config.yaml`.
4. Update launcher to show environment selection when multiple environments are configured.

### Phase 4: Managed Sessions

1. Create `internal/session/` with Session struct + lifecycle.
2. Replace `controller/session_store.go` (3-field LaunchMeta) with full Session tracking.
3. Wire session creation through agent-compose pipeline.
4. Add session persistence (local JSON + Redis).
5. Update TUI, Web, and CLI to create/track/resume sessions.

### Phase 5: Named Agent Configs

1. Add `agents.yaml` loading to config.
2. Update launcher to show named configs alongside raw harness names.
3. Wire agent-compose resolution for named configs.
4. Expose agent configs through MCP server and web API.

### Phase 6: Decompose app.go

1. Extract AgentCoordinator (list, filter, sort, attend).
2. Extract SessionCoordinator (PTY, zoom, resume).
3. Extract TraceCoordinator (parse, view, export).
4. Extract LaunchCoordinator (spawn, sandbox, kill).
5. app.go becomes thin router (~500 lines).

## What Doesn't Change

- The TUI experience for solo developers (pick harness, run).
- The Provider interface contract (identity-only methods stay the same).
- The controller/ package (shared business logic).
- The terminal/ package (session backends: PTY embed, tmux mirror, openshell connect).
- The OTEL receiver and exporter.
- The history/ package (post-mortem session scanning).
- The cost/ package (per-model pricing).
- The web dashboard and CLI (thin adapters over controller/).

## Success Criteria

1. `go test ./...` passes at every phase boundary.
2. Solo developer experience is unchanged (no config required, pick harness and run).
3. A Claude agent in an OpenShell sandbox shows `ProviderName: "claude"`, not `"openshell"`.
4. Redis coordination works with both K8s and OpenShell environments.
5. Named agent configs appear in the launcher alongside raw harness names.
6. Sessions survive aimux restarts (local persistence + Redis).
7. `app.go` is under 600 lines.

# aimux Backend Architecture

**Date:** 2026-09-10 (updated 2026-09-11)
**Status:** Design
**Scope:** Backend interface, multi-tier execution model, OpenClaw/OpenAI/CMA alignment
**Depends on:** Phases 1-6 (complete), UX restructure (complete)
**Reviewed by:** Codex (gpt-5.6-sol), 2026-09-10

## Problem

aimux runs all agents locally. With 20+ concurrent agents, each consuming 2-3GB of RAM, a developer laptop runs out of memory. There's no way to offload agents to remote infrastructure while keeping the same dashboard experience.

Two motivations drive the backend architecture:

1. **Remote execution** — run agents on remote infrastructure when your machine can't handle the load. This is the primary driver.
2. **Sandbox isolation** — tools execute in an isolated environment, API keys stay off the sandbox. This is a bonus that comes with remote execution.

Local is not a fallback. It's a first-class mode for when you want things right here on your machine.

## Use Cases

aimux's primary use case is AI coding agents (Claude Code, Codex pattern). Level 1 state preservation: git commit is the checkpoint, harness restarts from conversation history, workspace persists on disk.

| Situation | Backend | Why |
|-----------|---------|-----|
| Quick bug fix, one agent | local | Speed. No network latency. Code is right here. |
| Running 10 agents on a refactor | my-server | Your Mac has 16GB free. 10 agents need 30GB. Offload. |
| Background workers overnight | gpu-box | Long-running tasks shouldn't tie up your laptop. |
| Team sharing agent configs | occ-prod | Enterprise: IAM, policies, audit, shared agents. |

## Platform Alignment

The Backend interface aligns with three external platforms. All use the same model: agent is a template, session is a durable instance, sandbox is where tools run.

| Concept | aimux | OpenClaw Gateway | OpenAI Agents API | Claude CMA |
|---------|-------|-----------------|-------------------|------------|
| Agent template | `Agents.Create` | `agents.create` | `POST /agents` | `POST /v1/agents` |
| Running instance | `Sessions.Create` | `sessions.create` | `POST /sessions` | `POST /v1/sessions` |
| Send prompt | `Sessions.Send` | send to session | `POST /sessions/{id}/turns` | send message |
| Event stream | `Sessions.Subscribe` | `sessions.messages.subscribe` | SSE stream | SSE stream |
| Stop execution | `Sessions.Abort` | `sessions.abort` | `POST /sessions/{id}/cancel` | abort |
| Sandbox list | `Sandboxes.List` | `environments.list` | `GET /environments` | N/A (hosted) |

### Platform roles

| Platform | What it is | Who runs the agent | Who runs the sandbox |
|----------|-----------|-------------------|---------------------|
| Local | No platform | Your machine (claude subprocess) | Your machine |
| OpenClaw Gateway | Self-hosted gateway (channels + agent runtime + plugins) | Gateway (your infra) | OpenShell plugin (your infra) |
| OpenAI Agents API | Hosted API | OpenAI (their infra) | Pluggable providers (E2B, Modal, self-hosted) |
| Claude CMA | Hosted API | Anthropic (their infra) | Anthropic (their sandbox) |
| OCC (enterprise) | Self-hosted multi-tenant control plane | Per-namespace gateway | SandboxDriver + ComputeDriver |

OpenClaw includes messaging channels (Slack, WhatsApp, Telegram) natively. When aimux connects to it, aimux IS the channel. OpenAI and CMA are pure APIs where you build the channel yourself.

## Goals

- Named backends configured in `~/.aimux/config.yaml`
- One interface (Sessions/Agents/Sandboxes) that all backends implement
- Frontends (TUI, Web, CLI) never import backend-specific packages
- Agent definitions have a `backend` field for their default; `--backend` flag overrides per session
- Event streaming is resumable (cursor-based, not ephemeral channels)
- Sessions track their sandbox binding for pause/resume
- Sandboxes managed by the platform in remote mode; not an aimux concern
- No replication of OCC concepts (Namespace, AgentRevision, SandboxPolicy) in aimux

## Non-Goals

- Implementing the OCC or OpenAI backends in this phase (future work)
- Multi-namespace support in aimux (OCC Console owns that)
- Workspace/manifest abstraction (future, inspired by OpenAI)
- Session memory across runs (future, inspired by OpenAI)
- Cross-backend agent synchronization (explicit publish workflow, not auto-sync)

## Architecture

```
aimux (developer dashboard)
  |
  +-------------------------------------------------+
  | Frontends                                       |
  |  TUI (tabs: Active, History, Agents, Sandboxes) |
  |  Web (sessions, agents, sandboxes pages)        |
  |  CLI (sessions, agents, backends)               |
  +------------------------+------------------------+
                           |
  +------------------------v------------------------+
  | Backend Layer                                   |
  |                                                 |
  |  Sessions  - create, get, list, send, abort,    |
  |              pause, resume, subscribe           |
  |  Agents    - list, get, create, update, delete  |
  |  Sandboxes - list                               |
  +------+-------------+-------------+-------------+
         |             |             |             |
  +------v-----+ +-----v------+ +----v-------+ +--v-----------+
  | local      | | openclaw   | | openai     | | occ (future) |
  |            | |            | | (future)   | |              |
  | claude CLI | | WebSocket  | | REST + SSE | | OCC API      |
  | subprocess | | to gateway | | to OpenAI  | | client       |
  |            | |            | |            | |              |
  | SQLite     | | SQLite     | | SQLite     | | OCC is       |
  | authority  | | projection | | projection | | authority    |
  |            | |            | |            | |              |
  | YAML agents| | Gateway    | | OpenAI     | | OCC owns     |
  | ~/.aimux/  | | owns agents| | owns agents| | everything   |
  |            | |            | |            | |              |
  | No sandbox | | OpenShell  | | E2B, Modal | | SandboxPolicy|
  |            | | auto-mgd   | | pluggable  | | + Driver     |
  +------------+ +------------+ +------------+ +--------------+
```

## Backend Interfaces

Methods are plain verbs; the interface name provides the noun. Incorporates Codex review: separate concerns, cursor-based streaming, namespaced refs.

### Core interfaces

```go
package backend

import (
    "context"
    "encoding/json"
    "time"
)

// Ref is a namespaced resource identity: "local/abc123" or "my-server/key".
type Ref struct {
    Backend string
    ID      string
}

func (r Ref) String() string { return r.Backend + "/" + r.ID }

// Descriptor identifies a backend instance.
type Descriptor struct {
    ID           string   // stable identifier, e.g. "local", "my-server"
    DisplayName  string   // human-friendly name
    Type         string   // "local", "openclaw", "openai", "occ"
    Capabilities []string // "sessions", "agents", "sandboxes"
}

// Sessions manages session lifecycle and event streaming.
// Sessions are running instances of agents.
type Sessions interface {
    Descriptor() Descriptor

    Create(ctx context.Context, opts CreateOpts) (Session, error)
    Get(ctx context.Context, ref Ref) (Session, error)
    List(ctx context.Context, query Query) (Page, error)

    // Send delivers a prompt to the session. Status comes back via Subscribe.
    Send(ctx context.Context, ref Ref, input Input) error
    Abort(ctx context.Context, ref Ref) error

    // Pause suspends the session. Sandbox preserved, compute released.
    // Resume reconnects to the same sandbox.
    Pause(ctx context.Context, ref Ref) error
    Resume(ctx context.Context, ref Ref) (Session, error)

    // Subscribe returns a resumable event stream. Cursor is the last
    // received event's position; empty string starts from the beginning.
    Subscribe(ctx context.Context, ref Ref, cursor string) (EventStream, error)
}

// Agents manages agent definitions (templates).
// Sessions are running instances of agents.
// Backends that don't support mutation return ErrUnsupported from Create/Update/Delete.
type Agents interface {
    List(ctx context.Context) ([]Agent, error)
    Get(ctx context.Context, ref Ref) (Agent, error)
    Create(ctx context.Context, agent Agent) (Ref, error)
    Update(ctx context.Context, ref Ref, agent Agent) error
    Delete(ctx context.Context, ref Ref) error
}

// Sandboxes provides read access to available sandboxes.
// In local mode, returns nothing. In remote modes, returns what the platform manages.
// aimux never creates sandboxes — the platform does.
type Sandboxes interface {
    List(ctx context.Context) ([]Sandbox, error)
}
```

### Data types

```go
// Session is a durable conversation. Send delivers prompts; status changes
// arrive via Subscribe. Multiple sends can occur within one session.
type Session struct {
    Ref          Ref
    AgentRef     Ref
    SandboxRef   *Ref   // which sandbox this session is bound to (nil for local)
    Status       Status
    DisplayTitle string
    CreatedAt    time.Time
    LastActivity time.Time
}

type Status string

const (
    Ready       Status = "ready"       // session exists, accepting prompts
    Busy        Status = "busy"        // processing a prompt
    Paused      Status = "paused"      // sandbox suspended, can resume
    Archived    Status = "archived"    // finished, read-only
    Unavailable Status = "unavailable" // backend unreachable
)

// Input is what gets sent to a session.
type Input struct {
    Prompt string
    Model  string // override, empty = use agent default
}

// Event is a single event in a session's event stream.
type Event struct {
    ID        string          // backend-assigned event ID
    Sequence  int64           // monotonic sequence for cursor-based resume
    Timestamp time.Time
    Type      string          // "tool_call", "tool_result", "text", "error", "status_change"
    Data      json.RawMessage // typed payload, not double-encoded string
}

// EventStream is a resumable event reader.
type EventStream interface {
    // Next blocks until the next event is available or the stream ends.
    Next(ctx context.Context) (Event, error)

    // Cursor returns the position of the last received event.
    // Pass this to Subscribe() to resume from where you left off.
    Cursor() string

    // Close releases resources.
    Close() error
}

// CreateOpts configures a new session.
type CreateOpts struct {
    AgentRef   Ref
    Backend    string // override agent's default backend
    WorkingDir string
    Model      string // override agent default
}

// Query filters session listings.
type Query struct {
    Status   *Status
    AgentRef *Ref
    Backend  string // filter to one backend
    Limit    int
    Cursor   string // pagination cursor
}

// Page is a paginated session list.
type Page struct {
    Sessions []Session
    Next     string // pagination cursor, empty if no more
    Total    int
}

// Agent is a template definition. Sessions are running instances of agents.
type Agent struct {
    Ref     Ref
    Name    string
    Model   string
    Prompt  string
    Backend string   // default backend for this agent
    Harness string
    MCP     []string
    Skills  []string
    Policy  string
}

// Sandbox is a managed execution environment (OpenShell, E2B, etc.).
type Sandbox struct {
    Name    string
    Type    string // "openshell", "e2b", "modal", "docker"
    Status  string // "ready", "stopped", "unhealthy"
    Scope   string // "session", "agent", "shared"
    Details string
    Backend string // which backend owns this
}
```

## Session Lifecycle

### States

```
            create
              |
              v
  +-------+ send  +------+ idle   +--------+
  | Ready |------>| Busy |------->| Ready  |
  +-------+      +------+        +--------+
     |               |               |
     | pause         | abort         | pause
     v               v               v
  +--------+     +----------+    +--------+
  | Paused |     | Archived |    | Paused |
  +--------+     +----------+    +--------+
     |
     | resume
     v
  +-------+
  | Ready |
  +-------+
```

### Session-to-sandbox binding

Each session can be bound to a sandbox. The binding determines where tools execute:

| Sandbox scope | Binding | Lifecycle |
|---------------|---------|-----------|
| `session` | One sandbox per session | Sandbox dies when session ends |
| `agent` | One sandbox shared across sessions | Sandbox lives as long as the agent |
| `shared` | Pool for multiple agents | Platform manages |

On pause: sandbox compute is released, workspace (PVC) preserved.
On resume: sandbox restarts, workspace remounted, session reconnects.

### Session resume — what happens

```
aimux calls: Sessions.Resume(ref)
  |
  Backend (OpenClaw gateway):
    1. Look up session in session store (conversation history, agent config)
    2. Check sandbox status — if stopped, call OpenShell StartSandbox
    3. Wait for sandbox ready
    4. Start harness process inside sandbox
    5. Feed conversation transcript to harness
    6. Harness reconstructs context, signals ready
    7. Session status -> Ready
    8. Events flow back to aimux via Subscribe
```

**Gap: session resume after sandbox restart does not exist today in OpenClaw.** The gateway has the session store and the OpenShell integration, but there is no flow that automatically reconnects a harness to a restarted sandbox and replays the conversation. This is gateway-level work, not aimux work. In OCC enterprise mode, OCC orchestrates this via the per-namespace gateway.

### State preservation levels

aimux's primary use case (coding agents) needs Level 1 only. Git commit is the checkpoint.

| Level | What survives | Resume cost | Who does it |
|-------|-------------|-------------|-------------|
| L0: Delete | Nothing | Full cold start | Standard K8s |
| L1: Suspend | Disk (PVCs) | Seconds + harness restart | OpenShell stop/start |
| L2: Checkpoint | Disk + memory + process | Sub-second | CRIU, gVisor (not in OpenShell today) |
| L3: VM snapshot | Everything | Sub-second | Cloud Hypervisor (not in OpenShell) |

## Backend Implementations

### local (internal/backend/local.go)

Wraps existing code:

- `Sessions`: starts `claude` CLI subprocess via `sdk.ClaudeSession`, manages lifecycle in SQLite
- `Agents`: reads/writes `~/.aimux/agents/*.agent.yaml` via `config.LoadAgentDir`/`config.SaveAgent`
- `Sandboxes`: returns empty (local mode has no sandboxes)
- `Pause`/`Resume`: not supported (local sessions are ephemeral)

**SQLite is authoritative.** Sessions and events stored locally. No external dependency.

**No sandbox.** Tools execute on the host. If you want sandbox isolation, use a remote backend.

### openclaw (internal/backend/openclaw.go)

Go WebSocket client speaking the OpenClaw gateway protocol.

- `Sessions`: Create -> `sessions.create`, Get -> `sessions.get`, List -> `sessions.list`, Send -> send prompt, Abort -> `sessions.abort`, Subscribe -> `sessions.messages.subscribe`
- `Pause`/`Resume`: Pause -> stop sandbox via OpenShell, Resume -> restart sandbox + reconnect harness (depends on gateway work that doesn't exist yet)
- `Agents`: List -> `agents.list`, Get -> agent detail, Create -> `agents.create`, Update -> `agents.update`, Delete -> `agents.delete`
- `Sandboxes`: List -> `environments.list` + `environments.status`

**OpenClaw is authoritative** for agents, sessions, events, and sandboxes.

**SQLite is a disposable projection.** Caches session metadata for offline TUI display. Records marked cached; stale data shows "last seen N minutes ago."

**Sandbox is automatic.** When an agent has `sandbox: { mode: "all", backend: "openshell" }`, OpenClaw auto-creates on first tool use. Mirror mode syncs workspace per tool call. Remote mode seeds once, then operates directly on sandbox filesystem.

**OpenClaw is a gateway with channels.** Its primary functions are messaging channels (Slack, WhatsApp, Telegram), session management, agent runtime, and plugins. When aimux connects, aimux is one of many possible channels. aimux uses the session and agent runtime capabilities; it doesn't use the messaging channels.

**Protocol:** WebSocket with custom req/res/event frames (NOT JSON-RPC 2.0). Pin client and protocol release together.

### openai (internal/backend/openai.go) — future

REST + SSE client for the OpenAI Agents API.

- `Sessions`: Create -> `POST /sessions`, Send -> `POST /sessions/{id}/turns`, Abort -> `POST /sessions/{id}/cancel`, Subscribe -> SSE stream
- `Pause`/`Resume`: serialized session state via `serialize_session_state` / `deserialize_session_state`
- `Agents`: maps to Agents API CRUD
- `Sandboxes`: maps to Environments API

**OpenAI has features we don't yet:**
- Workspace manifest (blueprint for sandbox contents: files, git repos, cloud mounts, env vars)
- RunState serialization (harness workflow position, not just conversation history)
- Session memory (MEMORY.md files persisting learning across runs)
- Pluggable sandbox providers (E2B, Modal, Cloudflare, Docker, self-hosted)

### occ (internal/backend/occ.go) — future

Client for the OpenClaw Controller (OCC) API per RFC 0027.

- Maps to OCC API for Agents, AgentRevisions, Sessions, Namespaces
- Multi-tenant: aimux connects to one Installation, user selects Namespace
- Agent definitions are OCC resources (Agent + Configuration + Harness)
- SandboxPolicy and ComputeDriver are OCC concerns
- Secrets via SecretBroker, never in aimux
- Session resume orchestrated by OCC: OCC tells namespace gateway to resume, gateway does the work

**aimux does NOT replicate OCC concepts.** No Namespace, AgentRevision, SandboxPolicy, SecretBroker, or Restriction types in aimux.

**Ownership in OCC mode:**
- OCC owns the session store (platform database)
- Gateway is per-namespace, managed by OCC
- Gateway doesn't own session state in enterprise mode; OCC does
- Resume flow: aimux -> OCC API -> OCC tells gateway -> gateway restarts sandbox -> gateway reconnects harness

## Agent Definition Ownership

Each agent definition has exactly one authority:

| Backend | Authority | aimux behavior |
|---------|-----------|---------------|
| local | aimux YAML files | Full CRUD via CLI/TUI |
| openclaw | OpenClaw gateway | Read + mutate via gateway |
| openai | OpenAI API | Read + mutate via API |
| occ | OCC API | Read + mutate via OCC |

**Merged catalog with qualified names:**

```
local:reviewer          — from ~/.aimux/agents/reviewer.agent.yaml
my-server:reviewer      — from OpenClaw gateway
openai:reviewer         — from OpenAI Agents API
```

**No automatic sync.** Explicit publish for portability:

```bash
aimux agents publish reviewer --to my-server
```

## Session Persistence

| Backend | Authority | SQLite role |
|---------|-----------|-------------|
| local | SQLite | Authoritative store |
| openclaw | OpenClaw | Disposable projection |
| openai | OpenAI | Disposable projection |
| occ | OCC | Disposable projection |

**Local projection schema for remote backends:**

```sql
CREATE TABLE session_projections (
    backend_id TEXT NOT NULL,
    remote_key TEXT NOT NULL,
    agent_ref TEXT,
    sandbox_ref TEXT,
    display_title TEXT,
    last_observed_status TEXT,
    last_activity TEXT,
    last_cursor TEXT,
    observed_at TEXT NOT NULL,
    locally_pinned INTEGER DEFAULT 0,
    PRIMARY KEY (backend_id, remote_key)
);
```

Records explicitly marked cached. Network failure shows "last seen N minutes ago."

## Config

```yaml
# ~/.aimux/config.yaml
backends:
  local:
    type: local

  my-server:
    type: openclaw
    endpoint: wss://my-server.example.com

  gpu-box:
    type: openclaw
    endpoint: wss://gpu.internal:3000

  # Future:
  # openai:
  #   type: openai
  #   api_key_env: OPENAI_API_KEY
  #
  # occ-prod:
  #   type: occ
  #   endpoint: https://occ.example.com
  #   namespace: my-team
```

`local` always exists, no configuration needed. Remote backends added by name.

### Agent default backend

```yaml
# ~/.aimux/agents/reviewer.agent.yaml
name: reviewer
model: haiku
backend: my-server    # default backend for this agent

# ~/.aimux/agents/quick-fix.agent.yaml
name: quick-fix
model: sonnet
# no backend field = defaults to local
```

### Per-session override

```bash
aimux sessions start reviewer                    # uses my-server (agent default)
aimux sessions start reviewer --backend local    # override to local this time
aimux sessions start reviewer --backend gpu-box  # override to gpu-box
```

### Backend CLI

```bash
aimux backends list
aimux backends add my-server --type openclaw --endpoint wss://...
aimux backends remove my-server
```

## What Existing Code Becomes

| Current code | In local backend | In openclaw backend |
|-------------|-----------------|-------------------|
| `sdk/claude.go` | Sessions.Create + Send | Unused |
| `sdk/dispatcher.go` | Unused (local = no sandbox) | Unused (OpenClaw routes tools) |
| `sdk/subprocess.go` | Used by ClaudeSession | Unused |
| `config/agent_file.go` | Agents (full CRUD) | Unused |
| `environment/*.go` | Sandboxes (empty list) | Unused |
| `session/sqlite_store.go` | Authoritative store | Projection cache |
| `session/lifecycle.go` | Local session state machine | Unused (gateway owns lifecycle) |
| `controller/agents.go` | Routes to local Agents | Routes to gateway Agents |
| `controller/environments.go` | Routes to local Sandboxes | Read-only from gateway |

Nothing is deleted. Code stays; the Backend interface routes to the right implementation.

## Implementation Phases

### Phase 1: Backend interface + local backend
- Define interfaces in `internal/backend/`
- Implement `local.go` wrapping existing code
- Refactor frontends to call Backend instead of importing `sdk/` directly
- Cursor-based EventStream wrapping the existing channel
- Backends CLI (list/add/remove)

### Phase 2: OpenClaw gateway backend
- Implement `openclaw.go` with WebSocket client
- Protocol frame parsing (req/res/event)
- Map gateway methods to Backend interfaces
- SQLite projection for offline display
- Reconnection with cursor-based resume

### Phase 3: Frontend merge
- TUI shows merged catalogs from all backends
- Backend labels on all resources
- Command palette routes to correct backend
- Agent publish workflow (local -> openclaw)

### Phase 4: Session pause/resume
- Pause/Resume on Sessions interface
- Local: not supported (return ErrUnsupported)
- OpenClaw: stop/start sandbox + reconnect (depends on gateway work)
- Wire into TUI and CLI

### Phase 5: OpenAI backend (future)
- Implement `openai.go` when API stabilizes
- REST + SSE client
- Session state serialization for pause/resume

### Phase 6: OCC backend (future, post-RFC)
- Implement `occ.go` when OCC API ships
- Namespace selection
- Read-only surfacing of OCC concepts

## Dependencies and Gaps

| Gap | Owned by | Impact |
|-----|----------|--------|
| Session resume after sandbox restart | OpenClaw gateway | Pause/Resume won't work end-to-end until gateway reconnects harness to restarted sandbox |
| RunState serialization | OpenClaw gateway / future | Smart resume (skip completed steps) vs dumb resume (replay transcript) |
| Auto-suspend on idle | OpenClaw gateway | Without it, idle sandboxes burn compute |
| Auto-resume on access | OpenClaw gateway | Without it, user must manually resume |
| Workspace manifest | aimux + backends | No portable workspace definition yet (OpenAI has this) |
| Session memory | aimux + backends | No cross-run learning persistence (OpenAI has this) |

## Key Design Decisions

1. **Capability interfaces.** Sessions, Agents, Sandboxes are separate. Agents includes CRUD; backends that don't support mutation return ErrUnsupported.

2. **Namespaced refs.** Every resource identity is `backend/id`. No bare IDs.

3. **Events use json.RawMessage.** Avoids double-encoding. Includes sequence number for cursor-based resume.

4. **Resumable event streams.** Cursor-based Subscribe with EventStream (Next/Cursor/Close). Handles reconnection.

5. **Backend-authoritative persistence.** Local: SQLite is truth. Remote: platform is truth, SQLite is cache.

6. **One authority per agent definition.** Local agents in YAML, remote agents in the platform. Merged catalog for display. Explicit publish for portability.

7. **aimux is a client, not a control plane.** In remote modes, aimux delegates to the platform.

8. **Session-to-sandbox binding.** Sessions track which sandbox they're bound to. Pause preserves the binding. Resume reconnects to the same sandbox.

9. **Four backends, one interface.** Local, OpenClaw, OpenAI, OCC. Each is a new file implementing the same interfaces. Frontend code never changes.

10. **Pause/Resume is session-level.** Sandbox lifecycle is the backend's concern. aimux says "pause this session" and the backend decides what that means (stop the sandbox, serialize state, etc.).

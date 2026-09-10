# aimux Backend Architecture

**Date:** 2026-09-10
**Status:** Design
**Scope:** Backend interface, three-tier execution model, OpenClaw integration, enterprise alignment
**Depends on:** Phases 1-6 (complete), UX restructure (complete)
**Reviewed by:** Codex (gpt-5.6-sol), 2026-09-10

## Problem

aimux runs all agents locally. With 20+ concurrent agents, each consuming 2-3GB of RAM, a developer laptop runs out of memory. There's no way to offload agents to remote infrastructure while keeping the same dashboard experience.

Two motivations drive the backend architecture:

1. **Remote execution** — run agents on remote infrastructure when your machine can't handle the load. This is the primary driver.
2. **Sandbox isolation** — tools execute in an isolated environment, API keys stay off the sandbox. This is a bonus that comes with remote execution.

The design supports three tiers without changing frontend code:

1. **Local** — developer laptop, no infrastructure needed. Fast, direct access. Best for quick tasks with 1-2 agents.
2. **OpenClaw Gateway** — agents run on a remote server with optional OpenShell sandboxes. Best when you have 5+ agents or need isolation.
3. **OpenClaw Enterprise (OCC)** — multi-tenant control plane with IAM, policies, secrets. Best for teams.

Local is not a fallback. It's a first-class mode for when you want things right here on your machine.

## Use Cases

| Situation | Backend | Why |
|-----------|---------|-----|
| Quick bug fix, one agent | local | Speed. No network latency. Code is right here. |
| Running 10 agents on a refactor | my-server | Your Mac has 16GB free. 10 agents need 30GB. Offload. |
| Background workers overnight | gpu-box | Long-running tasks shouldn't tie up your laptop. |
| Team sharing agent configs | occ-prod | Enterprise: IAM, policies, audit, shared agents. |

## Goals

- Named backends configured in `~/.aimux/config.yaml`
- One interface (Sessions/Agents/Sandboxes) that all backends implement
- Frontends (TUI, Web, CLI) never import backend-specific packages
- Agent definitions have a `backend` field for their default; `--backend` flag overrides per session
- Event streaming is resumable (cursor-based, not ephemeral channels)
- Sandboxes managed by the platform in remote mode; not an aimux concern
- No replication of OCC concepts (Namespace, AgentRevision, SandboxPolicy) in aimux

## Non-Goals

- Implementing the OCC backend in this phase (future work)
- Multi-namespace support in aimux (OCC Console owns that)
- Connector, plugin, or approval workflow resources
- Cross-backend agent synchronization (explicit publish workflow, not auto-sync)

## Architecture

```
aimux (developer dashboard)
  │
  ┌──────────────────────────────────────────────────┐
  │ Frontends                                        │
  │  TUI (tabs: Active, History, Agents, Envs)       │
  │  Web (sessions, agents, environments pages)      │
  │  CLI (sessions start/stop, agents, environments) │
  └──────────────────────┬───────────────────────────┘
                         │
  ┌──────────────────────▼───────────────────────────┐
  │ Backend Layer (capability interfaces)            │
  │                                                  │
  │  Sessions — create, get, list, send, abort, subscribe │
  │  Agents — list, get, create, update, delete            │
  │  Sandboxes — list                                      │
  └───────┬──────────────┬──────────────┬────────────┘
          │              │              │
  ┌───────▼──────┐ ┌─────▼──────┐ ┌────▼─────────┐
  │ local        │ │ openclaw   │ │ occ (future) │
  │              │ │            │ │              │
  │ claude CLI   │ │ WebSocket  │ │ OCC API      │
  │ subprocess   │ │ to gateway │ │ client       │
  │              │ │            │ │              │
  │ SQLite       │ │ SQLite     │ │ OCC is       │
  │ authoritative│ │ projection │ │ authoritative│
  │              │ │            │ │              │
  │ YAML agents  │ │ Gateway    │ │ OCC owns     │
  │ ~/.aimux/    │ │ owns agents│ │ everything   │
  │              │ │            │ │              │
  │ No sandbox   │ │ OpenShell  │ │ SandboxPolicy│
  │              │ │ auto-managed│ │ + Driver     │
  └──────────────┘ └────────────┘ └──────────────┘
```

## Backend Interfaces

Incorporates Codex review feedback: split catalogs from execution, use capability interfaces with optional mutation via type assertion. Methods are plain verbs; the interface name provides the noun.

### Core interfaces

```go
package backend

import (
    "context"
    "encoding/json"
    "time"
)

// Ref is a namespaced resource identity: "local/abc123" or "openclaw-prod/key".
type Ref struct {
    Backend string
    ID      string
}

func (r Ref) String() string { return r.Backend + "/" + r.ID }

// Descriptor identifies a backend instance.
type Descriptor struct {
    ID           string   // stable identifier, e.g. "local", "openclaw-prod"
    DisplayName  string   // human-friendly name
    Type         string   // "local", "openclaw", "occ"
    Capabilities []string // "sessions", "agents", "environments", "admin"
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
// In local mode, returns nothing (no sandboxes). In gateway/enterprise mode,
// returns what OpenClaw manages. aimux never creates sandboxes — the platform does.
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
    Status       Status
    DisplayTitle string
    CreatedAt    time.Time
    LastActivity time.Time
}

type Status string

const (
    Ready       Status = "ready"
    Busy        Status = "busy"
    Archived    Status = "archived"
    Unavailable Status = "unavailable"
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
    AgentRef    Ref
    Environment string // environment name (local mode) or empty (gateway mode)
    WorkingDir  string
    Model       string // override agent default
}

// Query filters session listings.
type Query struct {
    Status   *Status
    AgentRef *Ref
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
    Harness string
    MCP     []string
    Skills  []string
    Policy  string
}

// Sandbox is a managed execution environment (OpenShell, K8s pod, etc.).
type Sandbox struct {
    Name    string
    Type    string // "openshell", "k8s"
    Status  string // "ready", "unhealthy", "unknown"
    Details string // gateway URL, namespace, etc.
    Backend string // which backend owns this
}
```

## Backend Implementations

### local (internal/backend/local.go)

Wraps existing code:

- `Sessions`: starts `claude` CLI subprocess via `sdk.ClaudeSession`, manages lifecycle in SQLite
- `Agents`: reads/writes `~/.aimux/agents/*.agent.yaml` via `config.LoadAgentDir`/`config.SaveAgent`
- `Sandboxes`: returns empty (local mode has no sandboxes)

**Session = one claude subprocess.** A local session starts a subprocess and keeps it for one prompt (batch mode). Future: persistent sessions that accept multiple prompts.

**Run = one prompt execution.** In batch mode, Session and Run are 1:1. In persistent mode, one Session has many Runs.

**SQLite is authoritative.** Sessions, runs, and events all stored locally. No external dependency.

**No sandbox.** Tools execute on the host. The local backend does not manage OpenShell or K8s sandboxes. If you want sandbox isolation, use the openclaw backend.

### openclaw (internal/backend/openclaw.go)

Go WebSocket client speaking the OpenClaw gateway protocol.

- `Sessions`: Create → `sessions.create`, Get → `sessions.get`, List → `sessions.list`, Send → send prompt to session, Abort → `sessions.abort`, Subscribe → `sessions.messages.subscribe`
- `Agents`: List → `agents.list`, Get → agent detail, Create → `agents.create`, Update → `agents.update`, Delete → `agents.delete`
- `Sandboxes`: List → `environments.list` + `environments.status`

**OpenClaw is authoritative** for agents, sessions, events, and sandboxes.

**SQLite is a disposable projection.** Caches session metadata for offline TUI display. Records are marked cached; stale data shows "last seen N minutes ago" not current status. On startup/reconnect: query `sessions.list`, update projection, subscribe to changes.

**No local agent CRUD in openclaw mode.** Agents are created/edited via the gateway. aimux's per-file YAML is local-mode only.

**Sandbox is automatic.** When an agent has `sandbox: { mode: "all", backend: "openshell" }`, OpenClaw auto-creates the sandbox on first tool use. Mirror mode syncs workspace before/after each tool. Remote mode seeds once, then operates directly on the sandbox filesystem. aimux does not manage sandbox lifecycle.

**Protocol details:**
- Transport: WebSocket, NOT JSON-RPC 2.0 (custom req/res/event frames)
- Authentication: per gateway configuration
- Reconnection: cursor-based event resume after disconnect
- Client and protocol release should be pinned together

### occ (internal/backend/occ.go) — future

Client for the OpenClaw Controller (OCC) API per RFC 0027.

- Maps to OCC API for Agents, AgentRevisions, Sessions, Namespaces
- Supports multi-tenant: aimux connects to one OCC Installation, user selects Namespace
- Agent definitions are OCC resources (Agent + Configuration + Harness)
- SandboxPolicy and ComputeDriver are OCC concerns, not aimux
- Secrets via SecretBroker, never in aimux

**aimux does NOT replicate OCC concepts.** No Namespace, AgentRevision, SandboxPolicy, SecretBroker, or Restriction types in aimux. These are surfaced read-only via the backend interfaces when relevant to the user (e.g., showing which sandbox policy is active).

## Agent Definition Ownership

Each agent definition has exactly one authority:

| Backend | Authority | aimux behavior |
|---------|-----------|---------------|
| local | aimux YAML files | Full CRUD via CLI/TUI |
| openclaw | OpenClaw gateway | Read via gateway, mutate via gateway |
| occ | OCC API | Read via OCC, mutate via OCC |

**Merged catalog with qualified names:**

```
local:reviewer          — from ~/.aimux/agents/reviewer.agent.yaml
openclaw-prod:reviewer  — from OpenClaw gateway
occ-prod/ns1:reviewer   — from OCC (future)
```

**No automatic sync.** If you want to publish a local agent to OpenClaw:

```bash
aimux agents publish reviewer --to openclaw-prod
```

Stores destination ID and hash of last published source. On conflict, user chooses overwrite/pull/cancel. No hidden startup sync.

## Session Persistence

| Backend | Authority | SQLite role |
|---------|-----------|-------------|
| local | SQLite | Authoritative store for sessions, runs, events |
| openclaw | OpenClaw | Disposable projection for offline display |
| occ | OCC | Disposable projection for offline display |

**Local projection schema for gateway/occ backends:**

```sql
CREATE TABLE session_projections (
    backend_id TEXT NOT NULL,
    remote_key TEXT NOT NULL,
    agent_ref TEXT,
    display_title TEXT,
    last_observed_status TEXT,
    last_activity TEXT,
    last_cursor TEXT,
    observed_at TEXT NOT NULL,
    locally_pinned INTEGER DEFAULT 0,
    PRIMARY KEY (backend_id, remote_key)
);
```

Records are explicitly marked cached. Network failure shows "last seen 14 minutes ago" not pretends cached status is current.

## Environment Ownership

| Backend | Who manages environments | aimux's role |
|---------|------------------------|-------------|
| local | aimux config.yaml | Full CRUD: add, remove, health check |
| openclaw | OpenClaw + OpenShell | Read-only display from `environments.list` |
| occ | OCC + SandboxPolicy + ComputeDriver | Read-only display |

aimux environment CRUD (add/remove) only applies in local mode. In gateway/enterprise mode, aimux shows what the platform reports but doesn't create or destroy environments.

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
  # occ-prod:
  #   type: occ
  #   endpoint: https://occ.example.com
  #   namespace: my-team
```

`local` always exists, no configuration needed. Remote backends are added by name.

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
aimux backends list                              # show all configured backends
aimux backends add my-server --type openclaw --endpoint wss://...
aimux backends remove my-server
```

The `backends` key replaces the scattered `Runtime`, `Execution`, `Remote`, `Runtimes`, and `Kubernetes` config fields. One top-level backend connection choice, backend-specific config nested under each entry.

## Frontend Integration

Frontends merge data from all configured backends:

**Active tab (TUI):** shows running sessions from all backends + discovered native CLI processes.

**History tab:** shows past sessions from local SQLite (authoritative for local, projection for gateway/occ).

**Agents tab:** merged catalog from all backends. Each entry labeled with its backend. CRUD operations routed to the owning backend.

**Environments tab:** merged from all backends. Local envs are editable. Gateway/occ envs are read-only.

**Command palette:** backend-aware.
- `:start local:reviewer` — start via local backend
- `:start openclaw-prod:reviewer` — start via gateway
- If only one backend has the agent, no prefix needed

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
- Implement `local.go` wrapping existing `sdk/claude.go`, `config/agent_file.go`, `session/sqlite_store.go`
- Refactor frontends to call Backend instead of importing `sdk/` directly
- Session/Run distinction in local mode (1:1 for now)
- Cursor-based EventStream wrapping the existing channel

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
- Agent publish workflow (local → openclaw)

### Phase 4: OCC backend (future, post-RFC)
- Implement `occ.go` when OCC API ships
- Namespace selection
- Read-only surfacing of OCC concepts (SandboxPolicy, Harness, etc.)

## Key Design Decisions

1. **Capability interfaces over one fat interface.** Sessions, Agents, Sandboxes are separate. Agents includes CRUD; backends that don't support mutation return ErrUnsupported. (Codex recommendation)

2. **Session/Run distinction.** A Session is durable. A Run is one prompt execution. Abort stops the run, not the session. Maps cleanly to OpenClaw's model. (Codex recommendation)

3. **Namespaced refs.** Every resource identity is `backend/id`. No bare IDs. Prevents confusion when multiple backends have agents with the same name. (Codex recommendation)

4. **Events use json.RawMessage, not string.** Avoids double-encoding. Includes sequence number and backend event ID for proper correlation. (Codex recommendation)

5. **Resumable event streams.** Cursor-based Subscribe, not ephemeral channels. EventStream interface with Next/Cursor/Close. Handles reconnection cleanly. (Codex recommendation)

6. **Backend-authoritative persistence.** Local backend: SQLite is truth. Gateway/OCC: platform is truth, SQLite is cache. No bidirectional sync. (Codex recommendation)

7. **One authority per agent definition.** Local agents in YAML, gateway agents in gateway, OCC agents in OCC. Merged catalog for display. Explicit publish for portability. No hidden sync. (Codex recommendation)

8. **aimux is a client, not a control plane.** In gateway/enterprise mode, aimux delegates to the platform. It does not manage sandboxes, enforce policies, or own agent lifecycle. It provides the developer UX. (Enterprise RFC alignment)

9. **Three tiers, one interface.** Local → gateway → enterprise is an additive path. Each tier is a new Backend implementation. Frontend code doesn't change. (Architecture principle)

10. **Environments are local-mode only.** In gateway/enterprise mode, the platform manages compute and sandboxes. aimux environment CRUD is a development convenience, not a production feature. (Enterprise RFC alignment)

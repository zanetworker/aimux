# aimux SDK Mode: Brain/Hands Separation

**Date:** 2026-09-05
**Status:** Design
**Scope:** Add decoupled execution model (SDK mode), full resource lifecycle (Agent/Session/Environment), SQLite state store

## Problem

aimux runs agents monolithically: the harness (Claude Code, Codex) handles both reasoning and tool execution in one process. This means:

1. API keys are inside the sandbox alongside untrusted tool execution
2. If the sandbox is compromised, the attacker gets credentials + execution
3. No structured visibility into tool calls (aimux observes via JSONL/OTEL after the fact)
4. Agents, sessions, and environments are passive config, not managed resources

CMA, OpenClaw Gateway, and Synapse have proven that separating the brain (reasoning) from the hands (tool execution) is both practical and valuable for credential isolation, blast radius containment, and governance.

## Goals

- Add SDK mode alongside native mode (both must work; native is the default)
- Brain (Agent SDK) runs on host, hands (tools) dispatch to sandbox via hooks
- API key never enters the sandbox
- Full resource lifecycle: Agent, Session, Environment as managed objects
- SQLite for session/run state; YAML stays for config/agent definitions
- CLI, TUI, and Web UI all support creating and managing resources

## Non-Goals

- ACP integration (backlogged; not enough harness adoption)
- OS-level containment (bwrap/seatbelt) for native mode (separate effort)
- Enterprise features: multi-tenancy, IAM, secrets brokering
- Immutable agent revisions (CMA pattern; overkill for self-hosted)
- Session budgets with dollar-denominated caps (future)
- Multi-agent roster with session threads (future)

## Prior Art

Researched in session, documented at `wiki/agentic-platforms/managed-agents/platform-comparison.md`:

| Platform | Brain/hands | How |
|---|---|---|
| **CMA** | Full separation | Anthropic orchestration (brain) + container (hands) |
| **OpenClaw Gateway** | Separation via SSH | Gateway runs harness on host, tools dispatch to sandbox over SSH |
| **Synapse (Seth Jennings)** | Separation via Redis | Agent SDK registers custom tools that write to Redis; workers dequeue and execute in sandbox |
| **Omnigent** | Containment only | bwrap/seatbelt jails the whole monolith; hooks intercept for policy |

**Key decision from Synapse discussion:** Use hooks (PreToolUse), not tool registration. Hooks are cleaner because the SDK owns the tool schemas, hooks allow selective per-tool override, and the same hook switches between local and remote execution based on environment.

## Architecture

### Dual-Mode Execution

```
Native mode (any harness):
  aimux spawns CLI (claude, codex, gemini)
  CLI handles brain + hands in one process
  aimux observes via JSONL/OTEL
  Optionally contained via bwrap/seatbelt (future)

SDK mode (Claude today, others when SDKs ship):
  aimux runs Agent SDK on host (brain)
  PreToolUse hooks intercept tool calls
  Hooks dispatch bash/read/write/edit to sandbox
  API key stays on host
  Results flow back through hooks to SDK
```

### Component Diagram

```
┌─────────────────────────────────────────────────┐
│ aimux (host)                                    │
│                                                 │
│  ┌─────────────────┐  ┌─────────────────────┐   │
│  │ Agent SDK        │  │ Hook Dispatcher     │   │
│  │ (Go subprocess   │  │                     │   │
│  │  running TS/Py)  │  │ PreToolUse:         │   │
│  │                  │  │  bash → sandbox     │   │
│  │ Calls Claude API │  │  read → sandbox     │   │
│  │ Holds API key    │  │  write → sandbox    │   │
│  │ Manages context  │  │  edit → sandbox     │   │
│  └────────┬─────────┘  └──────────┬──────────┘   │
│           │ tool calls            │ dispatch     │
│           └───────────────────────┘              │
│                      │                           │
│  ┌───────────────────┴───────────────────────┐   │
│  │ Environment Interface                      │   │
│  │ env.Exec(sandbox, command) → result        │   │
│  └───────────────────┬───────────────────────┘   │
└──────────────────────│───────────────────────────┘
                       │ openshell sandbox exec
                       ▼
┌──────────────────────────────────────────────────┐
│ Sandbox (OpenShell / K8s pod)                    │
│                                                  │
│  No API key                                      │
│  No model access                                 │
│  Tool execution only: bash, file I/O             │
│  Governed by sandbox policy                      │
└──────────────────────────────────────────────────┘
```

### Hook-Based Tool Dispatch

The Claude Agent SDK supports hooks that fire before every tool use. A PreToolUse hook can return a result directly, skipping the SDK's default (local) execution.

```
Hook behavior per mode:

  Native mode:   no hooks (harness runs as-is)
  SDK mode:
    PreToolUse(tool_call):
      if tool_call.name in [bash, read, write, edit, glob, grep]:
        result = environment.Exec(sandbox, tool_call)
        return Handled(result)    # skip local execution
      else:
        return Approve()          # let SDK handle (e.g., web search)
```

This is selective: only filesystem/execution tools go to the sandbox. Tools that don't touch the filesystem (web search, model calls) execute normally on the host.

### Agent SDK Integration

The Claude Agent SDK is TypeScript/Python. aimux is Go. Integration via subprocess:

1. aimux starts a subprocess running the Agent SDK
2. Communication over stdin/stdout (JSON-RPC or structured events)
3. aimux sends: user prompt, hook responses
4. SDK sends: tool call events, text output, completion
5. aimux routes tool calls through the hook dispatcher

The subprocess model is the same pattern Omnigent uses (each harness runs as a subprocess managed by the orchestrator).

## Resource Model

### Three Resources

```
Agent (what to run)
  Stored in: ~/.aimux/agents.yaml (human-editable, git-friendly)
  Fields: name, harness, runtime, model, prompt, mcp, skills, policy
  CRUD: CLI (aimux agents create/list/update/remove) + Web UI
  Versioned via git (not database revisions)

Session (a run of an agent in an environment)
  Stored in: ~/.aimux/aimux.db (SQLite)
  Fields: id, agent_config, environment, sandbox_name, provider,
          status, model, working_dir, start_time, last_activity,
          tokens_in, tokens_out, cost_usd
  Lifecycle: created → running ↔ idle → terminated
  CRUD: CLI (aimux sessions list/resume/archive) + Web UI + TUI

Environment (where to run)
  Stored in: ~/.aimux/config.yaml (environments section)
  Fields: name, type (local/openshell/k8s), gateway, image, redis_url, namespace
  CRUD: CLI (aimux environments add/remove/check) + Web UI
  Health: connectivity test per type
```

### Session Lifecycle

```
created ──→ running ──→ idle ──→ terminated
               ↑          │
               └──────────┘
                (new input)
```

| Status | Meaning |
|---|---|
| `created` | Session record exists, agent not yet started |
| `running` | Agent is actively working (model calls, tool execution) |
| `idle` | Agent finished current task, waiting for input |
| `terminated` | Session ended (completion or error). Irreversible. |

Transitions are driven by:
- SDK mode: Agent SDK events (turn start, turn complete, error)
- Native mode: process state (running/exited) + OTEL events

### SQLite Schema

```sql
CREATE TABLE sessions (
  id TEXT PRIMARY KEY,
  agent_config TEXT,
  environment TEXT NOT NULL,
  sandbox_name TEXT,
  provider TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'created',
  model TEXT,
  working_dir TEXT,
  harness TEXT NOT NULL DEFAULT 'native',
  start_time TEXT NOT NULL,
  last_activity TEXT,
  tokens_in INTEGER DEFAULT 0,
  tokens_out INTEGER DEFAULT 0,
  cost_usd REAL DEFAULT 0,
  error TEXT,
  metadata TEXT  -- JSON blob for extensibility
);

CREATE TABLE session_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL REFERENCES sessions(id),
  timestamp TEXT NOT NULL,
  type TEXT NOT NULL,  -- 'tool_call', 'tool_result', 'text', 'error', 'status_change'
  data TEXT,           -- JSON payload
  FOREIGN KEY (session_id) REFERENCES sessions(id)
);

CREATE INDEX idx_sessions_status ON sessions(status);
CREATE INDEX idx_sessions_environment ON sessions(environment);
CREATE INDEX idx_session_events_session ON session_events(session_id);
```

## Configuration

### agents.yaml

```yaml
- name: reviewer
  harness: claude-sdk          # decoupled: brain on host, tools in sandbox
  model: opus
  prompt: "Review code for bugs and security issues"
  mcp:
    - github
  policy: strict

- name: quick-fix
  harness: claude-native       # monolithic: CLI runs everything locally
  model: sonnet

- name: background-worker
  harness: claude-sdk
  model: sonnet
  prompt: "Process the task queue"
```

### config.yaml environments

```yaml
environments:
  local:
    type: local
  sandbox:
    type: openshell
    gateway: "https://gateway.example.com"
    image: "ghcr.io/nvidia/openshell-community/sandboxes/base:latest"
  cluster:
    type: k8s
    redis_url: "redis://redis.agents.svc:6379"
    namespace: agents
```

## CLI Surface

### Agent Management

```bash
aimux agents create reviewer --harness claude-sdk --model opus --prompt "Review code"
aimux agents list                    # tabular, all configs
aimux agents list --json             # machine-readable
aimux agents update reviewer --model sonnet
aimux agents remove reviewer
```

### Environment Management

```bash
aimux environments add sandbox --type openshell --gateway https://gw.example.com
aimux environments remove sandbox
aimux environments check             # test connectivity for all
aimux environments list --json
```

### Session Management

```bash
aimux sessions list                  # all sessions with status
aimux sessions list --status running # filter by status
aimux sessions list --json
aimux resume <session-id>            # reconnect to running/idle session
aimux sessions archive <session-id>  # mark terminated
```

### Launch (unchanged, but harness-aware)

```bash
aimux spawn claude                   # native mode (default)
aimux spawn reviewer                 # SDK mode (from agents.yaml harness field)
aimux spawn reviewer --environment sandbox  # SDK mode in OpenShell
aimux spawn claude --environment local      # native mode, explicit local
```

## Web API

### New Endpoints

| Method | Path | Description |
|---|---|---|
| GET | `/api/agents` | List agent configs (existing, updated) |
| POST | `/api/agents` | Create agent config |
| PUT | `/api/agents/:name` | Update agent config |
| DELETE | `/api/agents/:name` | Remove agent config |
| GET | `/api/environments` | List environments with health (existing) |
| POST | `/api/environments` | Add environment |
| DELETE | `/api/environments/:name` | Remove environment |
| GET | `/api/sessions` | List sessions with status (updated to use SQLite) |
| GET | `/api/sessions/:id` | Session detail with events |
| GET | `/api/sessions/:id/events` | Session event stream (SSE) |
| POST | `/api/sessions/:id/archive` | Archive session |

### Updated Endpoints

| Method | Path | Change |
|---|---|---|
| POST | `/api/agents/launch` | Accept `harness` field; dispatch to SDK or native path |

## Frontend Parity

Every operation reachable from all three frontends:

| Operation | TUI | Web | CLI |
|---|---|---|---|
| List agents | Agent list view | GET /api/agents | `aimux agents list` |
| Create agent | Launcher "Configured" | POST /api/agents | `aimux agents create` |
| List environments | Launcher env picker | GET /api/environments | `aimux environments list` |
| Add environment | -- (config edit) | POST /api/environments | `aimux environments add` |
| Health check | Green/red dots | status field | `--check` flag |
| List sessions | Sessions view | GET /api/sessions | `aimux sessions list` |
| Launch (native) | Launcher → native | POST /api/agents/launch | `aimux spawn claude` |
| Launch (SDK) | Launcher → SDK | POST /api/agents/launch | `aimux spawn reviewer` |
| Resume session | `r` key | -- (future) | `aimux resume` |
| Session events | Trace pane | SSE stream | -- (future) |

## Testing Strategy

### Unit Tests
- Hook dispatcher: mock sandbox, verify tool calls are routed correctly
- Session lifecycle: state transitions, invalid transitions rejected
- SQLite store: CRUD, concurrent access, schema migration
- Agent config CRUD: create, update, validate, remove

### Integration Tests
- SDK subprocess starts, receives prompt, returns response
- Hook intercepts bash tool call, dispatches to sandbox, returns result
- Session transitions through full lifecycle (created → running → idle → terminated)
- Environment add/remove/check roundtrip

### E2E Tests
- `aimux spawn reviewer --environment local --dry-run` shows SDK mode
- `aimux sessions list` shows sessions from SQLite
- Web launcher with SDK agent shows correct options

## Implementation Phases

### Phase 1: SQLite Store
Replace sessions.json with SQLite. Migrate existing data. Add session_events table. Update all frontends.

### Phase 2: Agent CRUD
Add `aimux agents create/update/remove`. Web API endpoints. TUI integration.

### Phase 3: Environment CRUD
Add `aimux environments add/remove`. Web API endpoints. Persist to config.yaml.

### Phase 4: Session Lifecycle
Real state machine with transitions. Status driven by process state (native) and SDK events (SDK mode). Resume support.

### Phase 5: Agent SDK Integration
Subprocess management. JSON-RPC communication protocol. Hook dispatcher. Tool call routing to Environment.Exec().

### Phase 6: Hook-Based Tool Dispatch
PreToolUse hooks for bash/read/write/edit/glob/grep. Environment.Exec() implementation for OpenShell (`openshell sandbox exec`). Workspace sync (mirror vs remote mode).

## Evolution Path

Start with the Claude Agent SDK. Evaluate generalization after shipping.

### Phase A: Claude Agent SDK (this spec)
- Claude Agent SDK as subprocess (TypeScript/Python)
- PreToolUse hooks intercept tools, dispatch to sandbox
- Gets Claude Code quality (context mgmt, skills, CLAUDE.md, smart editing)
- Binary is Go + TS/Py subprocess (two processes)

### Phase B: Evaluate (after Phase A ships)
Choose one based on what we learn:

**Option B1: Add more vendor SDKs**
- OpenAI Agents SDK for Codex/OpenAI models
- Each vendor gets its own subprocess integration
- Pro: best quality per vendor. Con: N subprocess integrations to maintain.

**Option B2: Build generic Go harness**
- aimux becomes the harness (like Omnigent, OpenClaw Gateway)
- One Go agent loop, ModelClient interface for any provider
- Project context from agents.md / .aimux/ (vendor-neutral)
- Pro: single binary, any model. Con: rebuild Claude Code features in Go.

**Option B3: Stay Claude-only for SDK mode**
- If Claude is the dominant use case, don't generalize
- Native mode covers other harnesses (Codex, Gemini, etc.)
- Pro: simplest. Con: SDK mode is Claude-locked.

The decision depends on whether users need SDK mode (brain/hands separation)
for non-Claude models, or if native mode (monolithic with optional containment)
is sufficient for those.

## Backlog (Not in This Spec)

| Feature | Why backlogged |
|---|---|
| Generic Go harness (Phase B2) | Evaluate after Claude SDK mode ships |
| ACP integration | Not enough harness adoption; revisit when Claude Code/Codex speak ACP |
| bwrap/seatbelt containment | Separate effort for native mode; doesn't affect SDK mode |
| Session budgets | Requires cost tracking maturity; add after session lifecycle is stable |
| Immutable agent revisions | git versioning covers 90% of the value for self-hosted |
| Multi-agent roster | Requires stable session lifecycle and coordination rework |
| Synapse pub/sub pattern | Redis workers are an optimization over direct dispatch; add when scale demands |

## Key Design Decisions

1. **Hooks, not tool registration.** The SDK owns tool schemas. Hooks intercept at execution time, not definition time. Per-tool selective override. Mode-switchable at runtime.

2. **SQLite for sessions, YAML for config.** Runtime state (sessions, events) needs queries, transactions, concurrent access. Config (agents, environments) needs human-editability and git versioning. Different storage for different concerns.

3. **Subprocess, not in-process.** The Agent SDK is TypeScript/Python. Go calls it as a subprocess with structured communication. Same pattern as Omnigent's harness model. No CGO, no FFI.

4. **Both modes are first-class.** Native mode is not deprecated. It works with any harness, has lower latency, and is simpler. SDK mode adds security (credential isolation) and governance (structured events) for when you need them.

5. **Environment.Exec() is the dispatch interface.** All tool execution in SDK mode goes through `env.Exec(sandbox, command)`. The Environment implementation decides how to execute (openshell sandbox exec, kubectl exec, local fork). Same interface, different backends.

6. **Mirror vs Remote workspace modes.** Adopted from OpenClaw Gateway. Mirror: bidirectional sync per tool call (host-canonical, good for interactive). Remote: one-time seed, then sandbox-canonical (good for background agents). Configurable per session.

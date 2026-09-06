# SDK Mode Foundation — Implementation Plan

**Spec:** `docs/superpowers/specs/2026-09-05-sdk-mode-design.md`
**Branch:** `refactor/managed-sessions`
**Scope:** Phases 1-4 of the spec (SQLite store, Agent CRUD, Environment CRUD gaps, Session lifecycle)
**Out of scope:** Phases 5-6 (Agent SDK subprocess, hook-based dispatch) — depend on this foundation

## Global Constraints

1. **Frontend parity is mandatory.** Every operation must be reachable from CLI, TUI, and Web UI. Controller function first, then wire all three frontends.
2. **Crafted-code P6: test-first.** Write failing test, confirm failure, implement, confirm pass. Every new function needs happy path + error path + boundary tests.
3. **Layer discipline.** Core packages MUST NOT import bubbletea, lipgloss, or net/http. Business logic in controller/ or core packages only.
4. **Existing tests must not break.** Run `go test ./... -timeout 60s` after each task.
5. **No new dependencies without justification.** Prefer stdlib. SQLite driver is the one allowed new dependency (modernc.org/sqlite for pure Go, no CGO).
6. **Error context per P4.** Every error carries file path, operation, and cause.
7. **Session/store types live in `internal/session/`.** Agent config types in `internal/config/`. Environment types in `internal/environment/`.

## Current State

- `internal/session/store.go`: FileStore (JSON-backed). Has Store interface with Create/Get/List/Update/Delete.
- `internal/session/session.go`: Session struct with Status enum (created/running/idle/terminated/error).
- `internal/session/manager.go`: Manager with CreateSession.
- `internal/config/agents.go`: LoadAgents from single agents.yaml, LoadAgentsMerged for project overlay.
- `internal/environment/`: Environment interface + Local/OpenShell/K8s implementations. SandboxOpts, SandboxStatus types.
- `internal/config/config.go`: EnvironmentConfig struct in main config. Environments parsed from config.yaml.
- `cmd/aimux/cmd/environments.go`: `aimux environments list [--check]` exists.
- `cmd/aimux/cmd/configs.go`: `aimux agents list` exists (read-only).
- `internal/frontend/web/handlers.go`: GET /api/agents, GET /api/environments exist.
- `internal/controller/launch.go`: LaunchAgent with harness awareness.

## Tasks

### Task 1: SQLite Session Store

Replace the JSON FileStore with a SQLite-backed store. Keep the existing `Store` interface unchanged so all consumers work without modification.

**Files to create:**
- `internal/session/sqlite_store.go` — SQLiteStore implementing Store interface
- `internal/session/sqlite_store_test.go` — full test coverage
- `internal/session/migrate.go` — schema creation + migration from JSON

**Files to modify:**
- `go.mod` / `go.sum` — add `modernc.org/sqlite` (pure Go, no CGO)
- `internal/session/store.go` — keep the Store interface, remove or deprecate FileStore

**Schema (from spec):**
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
  metadata TEXT
);

CREATE TABLE session_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL REFERENCES sessions(id),
  timestamp TEXT NOT NULL,
  type TEXT NOT NULL,
  data TEXT,
  FOREIGN KEY (session_id) REFERENCES sessions(id)
);

CREATE INDEX idx_sessions_status ON sessions(status);
CREATE INDEX idx_sessions_environment ON sessions(environment);
CREATE INDEX idx_session_events_session ON session_events(session_id);
```

**Store interface methods to implement:**
- `Create(session) error`
- `Get(id) (Session, error)`
- `List() ([]Session, error)`
- `Update(session) error`
- `Delete(id) error`

**Additional methods for events:**
- `AddEvent(sessionID, event) error`
- `ListEvents(sessionID) ([]SessionEvent, error)`

**Migration:** On first open, if `~/.aimux/sessions.json` exists and the SQLite DB is empty, migrate records. Log the migration, don't delete the JSON file (backup).

**Tests:** Create, Get, List, Update, Delete. Concurrent access (3 goroutines). Migration from JSON. Events CRUD. Error paths (not found, duplicate ID). Use `t.TempDir()` for DB path.

### Task 2: Session Lifecycle State Machine

Add validated state transitions to Session. Invalid transitions return errors. Wire process-state detection for native mode sessions.

**Files to create:**
- `internal/session/lifecycle.go` — state machine, Transition function
- `internal/session/lifecycle_test.go` — all valid/invalid transitions

**Files to modify:**
- `internal/session/session.go` — add Transition method, SessionEvent struct
- `internal/session/manager.go` — use Transition for status changes

**State machine (from spec):**
```
created → running → idle → terminated
              ↑       │
              └───────┘ (new input)

Any state → error (on failure)
terminated and error are terminal (no transitions out)
```

**Valid transitions:**
- created → running
- running → idle
- running → terminated
- running → error
- idle → running
- idle → terminated

**Invalid transitions (must return error):**
- terminated → anything
- error → anything
- created → idle (must go through running)
- created → terminated (must go through running)

**SessionEvent struct:**
```go
type SessionEvent struct {
    ID        int
    SessionID string
    Timestamp time.Time
    Type      string // "tool_call", "tool_result", "text", "error", "status_change"
    Data      string // JSON payload
}
```

**Tests:** Every valid transition succeeds. Every invalid transition returns error with descriptive message. Transition updates LastActivity timestamp. Concurrent transitions are safe (mutex or atomic).

### Task 3: Per-File Agent Config CRUD

Move agent configs from single agents.yaml to per-file `~/.aimux/agents/<name>.agent.yaml`. Add CLI create/update/remove commands. Wire web API and TUI.

**Files to create:**
- `internal/config/agent_file.go` — per-file load/save/delete for `~/.aimux/agents/`
- `internal/config/agent_file_test.go` — full coverage
- `internal/controller/agents.go` — controller functions: CreateAgent, UpdateAgent, RemoveAgent, ListAgents
- `internal/controller/agents_test.go` — controller tests

**Files to modify:**
- `internal/config/agents.go` — update LoadAgents to read from per-file directory
- `cmd/aimux/cmd/configs.go` — add `create`, `update`, `remove` subcommands
- `internal/frontend/web/handlers.go` — add POST/PUT/DELETE /api/agents
- `internal/frontend/web/handlers_test.go` — handler tests
- `internal/frontend/web/smoke_test.go` — add new endpoints
- `internal/frontend/tui/views/launcher.go` — show per-file agents in launcher

**Agent YAML format (per-file):**
```yaml
# ~/.aimux/agents/reviewer.agent.yaml
name: reviewer
harness: claude-sdk    # or claude-native, codex-native
model: opus
prompt: |
  Review code for bugs and security issues.
mcp:
  - github
policy: strict
```

**Controller functions (in `internal/controller/agents.go`):**
- `CreateAgent(cfg AgentFileConfig) error` — validate, write to ~/.aimux/agents/<name>.agent.yaml
- `UpdateAgent(name string, updates map[string]any) error` — load, merge, save
- `RemoveAgent(name string) error` — delete file
- `ListAgents() ([]AgentFileConfig, error)` — load all from directory + project overlay

**CLI:**
- `aimux agents create <name> --harness <h> --model <m> [--prompt <p>]`
- `aimux agents update <name> [--model <m>] [--prompt <p>]`
- `aimux agents remove <name>`
- `aimux agents list` (existing, update to use per-file)

**Web API:**
- `POST /api/agents` — body: AgentFileConfig JSON → CreateAgent
- `PUT /api/agents/:name` — body: partial update JSON → UpdateAgent
- `DELETE /api/agents/:name` → RemoveAgent

**TUI:** Launcher "Configured Agents" tab reads from per-file agents. No new keybindings needed.

**Tests:**
- Controller: create/update/remove/list roundtrip with t.TempDir()
- CLI: build binary, run `aimux agents create test-agent --harness claude-native --model sonnet`, verify file exists
- Web: handler tests for POST/PUT/DELETE
- Smoke: all new endpoints respond

### Task 4: Environment CRUD CLI + Web API

Add `aimux environments add` and `aimux environments remove` CLI commands. Add web API endpoints. Wire controller functions.

**Files to create:**
- `internal/controller/environments.go` — controller functions: AddEnvironment, RemoveEnvironment
- `internal/controller/environments_test.go` — controller tests

**Files to modify:**
- `cmd/aimux/cmd/environments.go` — add `add` and `remove` subcommands
- `internal/config/config.go` — add SaveEnvironment/RemoveEnvironment to persist to config.yaml
- `internal/frontend/web/handlers.go` — add POST/DELETE /api/environments
- `internal/frontend/web/handlers_test.go` — handler tests
- `internal/frontend/web/smoke_test.go` — add new endpoints

**Controller functions:**
- `AddEnvironment(name string, cfg EnvironmentConfig) error` — validate type, save to config.yaml
- `RemoveEnvironment(name string) error` — remove from config.yaml

**CLI:**
- `aimux environments add <name> --type <local|openshell|k8s> [--gateway <url>] [--image <img>] [--namespace <ns>]`
- `aimux environments remove <name>`

**Web API:**
- `POST /api/environments` — body: {name, type, gateway?, image?, namespace?} → AddEnvironment
- `DELETE /api/environments/:name` → RemoveEnvironment

**Validation:**
- type must be one of: local, openshell, k8s
- openshell requires gateway
- k8s requires namespace
- name must be non-empty, alphanumeric + hyphens
- cannot remove "local" (built-in)

**Tests:**
- Controller: add/remove roundtrip, validation errors, cannot remove "local"
- Web: handler tests
- Smoke: new endpoints

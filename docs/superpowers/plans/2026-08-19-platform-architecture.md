# Platform Architecture Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restructure aimux from a monitoring TUI into a self-hosted agent management platform with three clean axes: Provider (what), Environment (where), Coordination (how).

**Architecture:** Six phases, each on its own branch, tests passing at every boundary. Phase 1-2 extract and separate concerns. Phase 3-4 promote agent-compose and add managed sessions. Phase 5 adds named agent configs. Phase 6 decomposes app.go.

**Tech Stack:** Go, agent-compose (github.com/zanetworker/agent-compose), Redis (go-redis/v9), OpenShell CLI, Bubble Tea (TUI)

**Spec:** `docs/superpowers/specs/2026-08-19-aimux-platform-architecture.md`

## Global Constraints

- `go test ./... -timeout 30s` must pass at every task boundary
- `go vet ./...` must pass with zero issues
- Core packages (`internal/` except `frontend/tui/`) MUST NOT import `bubbletea` or `lipgloss`
- Every new package needs a compile-time interface check (`var _ Interface = (*Impl)(nil)`)
- Every new function needs tests (happy path + error path minimum)
- No Co-Authored-By trailers in commits
- One branch per phase; each phase is independently mergeable

---

## Phase 1: Extract Environment from Provider

**Branch:** `refactor/extract-environment`

**Goal:** Create the `Environment` interface, move infrastructure concerns out of Provider, delete nil-stub providers.

### Task 1.1: Define the Environment interface

**Files:**
- Create: `internal/environment/environment.go`
- Create: `internal/environment/environment_test.go`

**Interfaces:**
- Produces: `Environment` interface (Name, Type, Discover, CreateSandbox, DeleteSandbox, ListSandboxes, Kill, CheckHealth), `SandboxOpts`, `SandboxStatus`, `HealthStatus` types

- [ ] **Step 1: Create the interface file**

```go
package environment

import (
	"context"

	"github.com/zanetworker/aimux/internal/agent"
)

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

type SandboxOpts struct {
	Image    string
	Provider string // "claude", "codex"
	Mode     string // "worker" or "session"
	Env      map[string]string
	Labels   map[string]string
}

type SandboxStatus struct {
	Name   string
	Status string // "running", "ready", "stopped", "dead"
	Idle   bool
}

type HealthStatus struct {
	Configured bool
	CoordOK    bool
	CoordErr   string
	ComputeOK  bool
	ComputeErr string
	Workloads  []string
}
```

- [ ] **Step 2: Write compile test**

```go
package environment_test

import "testing"

func TestEnvironmentInterfaceExists(t *testing.T) {
	// Compile-time check only; implementations come in later tasks
}
```

- [ ] **Step 3: Verify**

Run: `go vet ./internal/environment/ && go test ./internal/environment/ -timeout 30s`
Expected: PASS

- [ ] **Step 4: Commit**

```
git add internal/environment/
git commit -m "refactor: define Environment interface (what vs where separation)"
```

### Task 1.2: Create LocalEnvironment

**Files:**
- Create: `internal/environment/local.go`
- Create: `internal/environment/local_test.go`

**Interfaces:**
- Consumes: `Environment` interface from Task 1.1
- Produces: `LocalEnvironment` struct implementing `Environment`

- [ ] **Step 1: Write failing test**

```go
package environment_test

import (
	"testing"

	"github.com/zanetworker/aimux/internal/environment"
)

var _ environment.Environment = (*environment.LocalEnvironment)(nil)

func TestLocalEnvironment_Name(t *testing.T) {
	env := environment.NewLocalEnvironment()
	if env.Name() != "local" {
		t.Errorf("expected 'local', got %q", env.Name())
	}
}

func TestLocalEnvironment_Type(t *testing.T) {
	env := environment.NewLocalEnvironment()
	if env.Type() != "local" {
		t.Errorf("expected 'local', got %q", env.Type())
	}
}
```

- [ ] **Step 2: Run test, verify FAIL**

Run: `go test ./internal/environment/ -timeout 30s -run TestLocal`
Expected: FAIL (LocalEnvironment not defined)

- [ ] **Step 3: Implement LocalEnvironment**

Extract process scanning logic from `internal/discovery/process.go` and `internal/discovery/orchestrator.go` into `local.go`. The LocalEnvironment wraps the existing process scanner + tmux discovery. `CreateSandbox` forks a process. `Kill` sends SIGTERM. `CheckHealth` returns always-healthy.

Key: `Discover()` must tag each agent with the correct `ProviderName` from the process name/args, using the existing `parseProviderFromProcess()` logic in `discovery/process.go`.

- [ ] **Step 4: Run tests, verify PASS**

Run: `go test ./internal/environment/ -timeout 30s`
Expected: PASS

- [ ] **Step 5: Commit**

```
git add internal/environment/local.go internal/environment/local_test.go
git commit -m "refactor: add LocalEnvironment (fork + ps scan)"
```

### Task 1.3: Create OpenShellEnvironment

**Files:**
- Create: `internal/environment/openshell.go`
- Create: `internal/environment/openshell_test.go`

**Interfaces:**
- Consumes: `Environment` interface from Task 1.1
- Produces: `OpenShellEnvironment` struct implementing `Environment`

- [ ] **Step 1: Write failing test with compile-time interface check**

```go
var _ environment.Environment = (*environment.OpenShellEnvironment)(nil)
```

- [ ] **Step 2: Implement by consolidating five scattered sources**

Move into `openshell.go`:
- `discovery/sandbox.go` → `Discover()` (already tags `ProviderName: "claude"`)
- `compose/adapter.go` + `compose/backend.go` → `CreateSandbox()`, `DeleteSandbox()`, `ListSandboxes()`
- `otel/session_fetch.go` → `FetchSessionReplies()`, `FetchSessionTurns()` (keep as exported methods on the struct for trace pane to call)
- `provider/health.go` (OpenShell health check portion) → `CheckHealth()`

Do NOT move `terminal/openshell.go` (stays as session backend).

- [ ] **Step 3: Write tests for discovery parsing**

Port `discovery/sandbox_test.go` test cases to the new package, testing `parseSandboxAgents()` produces agents with `ProviderName: "claude"` and correct status mapping.

- [ ] **Step 4: Verify**

Run: `go test ./internal/environment/ -timeout 30s`
Expected: PASS

- [ ] **Step 5: Commit**

```
git add internal/environment/openshell.go internal/environment/openshell_test.go
git commit -m "refactor: consolidate OpenShell into OpenShellEnvironment (was 5 packages)"
```

### Task 1.4: Create K8sEnvironment

**Files:**
- Create: `internal/environment/k8s.go`
- Create: `internal/environment/k8s_test.go`

**Interfaces:**
- Consumes: `Environment` interface from Task 1.1
- Produces: `K8sEnvironment` struct implementing `Environment`

- [ ] **Step 1: Write failing test with compile-time interface check**

```go
var _ environment.Environment = (*environment.K8sEnvironment)(nil)
```

- [ ] **Step 2: Implement by extracting from provider/k8s.go**

Move infrastructure methods into `k8s.go`:
- `Discover()` from `K8s.Discover()` (Redis heartbeats + pod scanning). Fix the Redis path to read `ProviderName` from `meta["provider"]` instead of hardcoding `"k8s"`.
- `CreateSandbox()` from `K8s.SpawnRemote()` + `SpawnSession()`
- `DeleteSandbox()` / `Kill()` from `K8s.Kill()` + `ScaleDownOne()`
- `CheckHealth()` from `K8s.CheckHealth()`
- `ListSandboxes()` from `K8s.discoverSessionPods()`

Keep Redis client management (connection, error marking) in this file since K8s discovery depends on it.

- [ ] **Step 3: Write tests**

Port `provider/k8s_test.go` infrastructure tests. Add a test that verifies `Discover()` returns agents with `ProviderName` from Redis metadata, not hardcoded `"k8s"`.

- [ ] **Step 4: Verify**

Run: `go test ./internal/environment/ -timeout 30s`
Expected: PASS

- [ ] **Step 5: Commit**

```
git add internal/environment/k8s.go internal/environment/k8s_test.go
git commit -m "refactor: extract K8sEnvironment from provider/k8s.go"
```

### Task 1.5: Slim the Provider interface

**Files:**
- Modify: `internal/provider/provider.go`
- Delete: `internal/provider/openshell.go`
- Modify: `internal/provider/k8s.go` (remove infrastructure methods, keep identity methods)
- Modify: `internal/provider/claude.go` (remove Kill if present)
- Modify: `internal/provider/codex.go` (remove Kill if present)

**Interfaces:**
- Consumes: `Environment` interface now owns Discover, Kill, CheckHealth
- Produces: Slimmed `Provider` interface (identity only)

- [ ] **Step 1: Remove infrastructure methods from Provider interface**

Remove from the `Provider` interface: `Kill()`. Remove optional interfaces: `Messenger`, `TaskLister`, `Spawner`, `InfraProvider`. These now live on `Environment` and `Coordinator` (Phase 2).

- [ ] **Step 2: Delete provider/openshell.go**

It's 100% nil stubs. The `OpenShellEnvironment` from Task 1.3 replaces it.

- [ ] **Step 3: Gut provider/k8s.go**

Remove all infrastructure methods (Discover, Kill, SpawnRemote, SpawnSession, ScaleDown, CheckHealth, Status, SendMessage, ListTasks, GetTaskResult). Keep only identity methods that the K8s provider legitimately needs for trace parsing: `ParseTrace` (reads task history from Redis for display). If ParseTrace is the only remaining method, consider whether K8s needs a Provider at all or if trace parsing should move to the Environment.

- [ ] **Step 4: Remove Kill from Claude and Codex providers**

Kill is now on Environment. Remove `Kill(a agent.Agent) error` from both providers' implementations.

- [ ] **Step 5: Verify full suite**

Run: `go test ./... -timeout 30s`
Expected: PASS (will require updating callers in app.go temporarily)

- [ ] **Step 6: Commit**

```
git add internal/provider/
git commit -m "refactor: slim Provider to identity-only, delete openshell.go"
```

### Task 1.6: Wire Environment into the Orchestrator

**Files:**
- Modify: `internal/discovery/orchestrator.go`
- Modify: `internal/frontend/tui/app.go` (NewApp setup)
- Modify: `internal/frontend/tui/app.go` (discovery tick)

**Interfaces:**
- Consumes: `Environment` interface, `LocalEnvironment`, `OpenShellEnvironment`, `K8sEnvironment`
- Produces: Updated `Orchestrator` that calls `Environment.Discover()` alongside provider lookups

- [ ] **Step 1: Add Environment sources to Orchestrator**

```go
type Orchestrator struct {
	providers    []AgentProvider
	environments []environment.Environment
}

func (o *Orchestrator) AddEnvironment(env environment.Environment) {
	o.environments = append(o.environments, env)
}
```

In `Discover()`, call each environment's `Discover()` in parallel alongside providers. Merge results, deduplicate by sandbox name.

- [ ] **Step 2: Update NewApp in app.go**

Replace the K8s provider registration and OpenShell remote discovery with Environment registration:

```go
// Always register local environment
localEnv := environment.NewLocalEnvironment()
orchestrator.AddEnvironment(localEnv)

// OpenShell environment (opt-in)
if cfg.Remote.Backend == "openshell" {
	osEnv := environment.NewOpenShellEnvironment(cfg.Remote)
	orchestrator.AddEnvironment(osEnv)
}

// K8s environment (opt-in)
if cfg.Kubernetes.IsActive() {
	k8sEnv := environment.NewK8sEnvironment(cfg.Kubernetes)
	orchestrator.AddEnvironment(k8sEnv)
}
```

- [ ] **Step 3: Remove old OpenShell/K8s provider registrations**

Remove `o.EnableRemoteDiscovery()`, the K8s provider from `allProviders`, and the `infraProv` variable. The Environment now handles discovery and lifecycle.

- [ ] **Step 4: Verify full suite**

Run: `go test ./... -timeout 30s`
Expected: PASS

- [ ] **Step 5: Commit**

```
git add internal/discovery/ internal/frontend/tui/app.go
git commit -m "refactor: wire Environment into Orchestrator, remove old provider registrations"
```

### Task 1.7: Phase 1 cleanup and verification

**Files:**
- Remove stale files: `internal/discovery/sandbox.go` (moved to environment/openshell.go)
- Remove stale files: `internal/compose/` (moved to environment/openshell.go)
- Remove stale files: `internal/runtime/` (absorbed into environment implementations)
- Update imports across the codebase

- [ ] **Step 1: Remove stale packages**

Delete files that have been consolidated into environment/. Update all import paths.

- [ ] **Step 2: Run full suite**

Run: `go build ./... && go vet ./... && go test ./... -timeout 30s`
Expected: All PASS, zero vet issues, clean build

- [ ] **Step 3: Commit**

```
git add -A
git commit -m "refactor: phase 1 complete — Environment extracted from Provider"
```

---

## Phase 2: Extract Coordination from K8s

**Branch:** `refactor/extract-coordination`

**Goal:** Create the `Coordinator` interface, extract Redis operations into a standalone package, create a local fallback.

### Task 2.1: Define the Coordinator interface

**Files:**
- Create: `internal/coordination/coordinator.go`
- Create: `internal/coordination/types.go`
- Create: `internal/coordination/coordinator_test.go`

**Interfaces:**
- Produces: `Coordinator` interface (RegisterAgent, Heartbeat, CreateTask, ListTasks, GetTaskResult, SendMessage, GetCosts)

- [ ] **Step 1: Create the interface**

```go
package coordination

import "context"

type Coordinator interface {
	RegisterAgent(ctx context.Context, agent AgentInfo) error
	Heartbeat(ctx context.Context, agentID string) error
	CreateTask(ctx context.Context, task TaskSpec) (string, error)
	ListTasks(ctx context.Context) ([]Task, error)
	GetTaskResult(ctx context.Context, taskID string) (string, error)
	SendMessage(ctx context.Context, agentID, text string) error
	GetCosts(ctx context.Context) ([]AgentCost, error)
	Close() error
}
```

- [ ] **Step 2: Define types in types.go**

`AgentInfo` (agentID, provider, role, model, namespace), `TaskSpec` (prompt, required_role, depends_on), `Task` (reuse from `internal/task/task.go`), `AgentCost` (agentID, tokensIn, tokensOut).

- [ ] **Step 3: Verify**

Run: `go vet ./internal/coordination/`
Expected: PASS

- [ ] **Step 4: Commit**

```
git add internal/coordination/
git commit -m "refactor: define Coordinator interface"
```

### Task 2.2: Implement RedisCoordinator

**Files:**
- Create: `internal/coordination/redis.go`
- Create: `internal/coordination/redis_test.go`

**Interfaces:**
- Consumes: `Coordinator` interface from Task 2.1
- Produces: `RedisCoordinator` implementing `Coordinator`

- [ ] **Step 1: Write compile-time check**

```go
var _ coordination.Coordinator = (*coordination.RedisCoordinator)(nil)
```

- [ ] **Step 2: Extract Redis operations from provider/k8s.go and mcpserver/server.go**

Move into `redis.go`:
- `SendMessage` → Redis XADD to inbox stream (from `provider/k8s.go:627`)
- `ListTasks` → Redis SCAN for task hashes (from `provider/k8s.go:651` and `task/redis.go`)
- `GetTaskResult` → Redis HGET result_ref (from `provider/k8s.go:673`)
- `CreateTask` → Redis HSET + ZADD (from `mcpserver/server.go:253`)
- `GetCosts` → Redis SCAN for cost hashes (from `mcpserver/server.go:562`)
- `RegisterAgent` → Redis HSET agent metadata
- `Heartbeat` → Redis HSET heartbeat timestamp

Use `pkg/rediskeys` for all key patterns (already the single source of truth).

- [ ] **Step 3: Write tests**

Test each method with a mock Redis or integration test (build-tagged). At minimum, test key patterns match `rediskeys` package expectations.

- [ ] **Step 4: Verify**

Run: `go test ./internal/coordination/ -timeout 30s`
Expected: PASS

- [ ] **Step 5: Commit**

```
git add internal/coordination/redis.go internal/coordination/redis_test.go
git commit -m "refactor: extract RedisCoordinator from k8s.go and mcpserver"
```

### Task 2.3: Implement LocalCoordinator

**Files:**
- Create: `internal/coordination/local.go`
- Create: `internal/coordination/local_test.go`

**Interfaces:**
- Consumes: `Coordinator` interface from Task 2.1
- Produces: `LocalCoordinator` implementing `Coordinator` (in-memory, no Redis)

- [ ] **Step 1: Write tests**

```go
func TestLocalCoordinator_CreateAndListTasks(t *testing.T) {
	c := coordination.NewLocalCoordinator()
	id, err := c.CreateTask(ctx, coordination.TaskSpec{Prompt: "test"})
	if err != nil { t.Fatal(err) }

	tasks, err := c.ListTasks(ctx)
	if err != nil { t.Fatal(err) }
	if len(tasks) != 1 || tasks[0].ID != id {
		t.Errorf("expected 1 task with id %s, got %v", id, tasks)
	}
}
```

- [ ] **Step 2: Implement**

In-memory maps behind a sync.RWMutex. All methods work but data doesn't survive restarts. This is the zero-config fallback.

- [ ] **Step 3: Verify**

Run: `go test ./internal/coordination/ -timeout 30s`
Expected: PASS

- [ ] **Step 4: Commit**

```
git add internal/coordination/local.go internal/coordination/local_test.go
git commit -m "refactor: add LocalCoordinator (in-memory fallback)"
```

### Task 2.4: Wire Coordinator into MCP server and app.go

**Files:**
- Modify: `internal/mcpserver/server.go` (replace direct Redis with Coordinator interface)
- Modify: `internal/frontend/tui/app.go` (create Coordinator based on config)
- Modify: `internal/config/config.go` (add coordination config section)

**Interfaces:**
- Consumes: `Coordinator` from Task 2.1, `RedisCoordinator` from 2.2, `LocalCoordinator` from 2.3

- [ ] **Step 1: Add coordination config**

```go
type CoordinationConfig struct {
	RedisURL string `yaml:"redis_url"`
	TeamID   string `yaml:"team_id"`
}
```

- [ ] **Step 2: Update MCP server**

Replace `s.rdb *redis.Client` with `s.coord coordination.Coordinator`. Update `handleCreateTask`, `handleListTasks`, `handleSendMessage`, `handleGetCosts` to call coordinator methods instead of Redis directly.

- [ ] **Step 3: Update app.go**

```go
var coord coordination.Coordinator
if cfg.Coordination.RedisURL != "" {
	coord, _ = coordination.NewRedisCoordinator(cfg.Coordination.RedisURL, cfg.Coordination.TeamID)
} else {
	coord = coordination.NewLocalCoordinator()
}
```

- [ ] **Step 4: Verify full suite**

Run: `go test ./... -timeout 30s`
Expected: PASS

- [ ] **Step 5: Commit**

```
git add internal/mcpserver/ internal/frontend/tui/app.go internal/config/
git commit -m "refactor: wire Coordinator interface, Redis encapsulated"
```

### Task 2.5: Phase 2 cleanup

- [ ] **Step 1: Remove Redis imports from mcpserver/server.go**

The MCP server should no longer import `go-redis/v9` directly. All Redis access goes through the Coordinator.

- [ ] **Step 2: Remove Redis from provider/k8s.go**

If K8s provider still exists after Phase 1 (for ParseTrace), remove its Redis client. ParseTrace for K8s can call the Coordinator to get task history.

- [ ] **Step 3: Full verification**

Run: `go build ./... && go vet ./... && go test ./... -timeout 30s`
Expected: All PASS

- [ ] **Step 4: Commit**

```
git add -A
git commit -m "refactor: phase 2 complete — Coordination extracted, Redis encapsulated"
```

---

## Phase 3: Promote agent-compose

**Branch:** `refactor/promote-agent-compose`

**Goal:** Route all launches through agent-compose `Engine.Resolve() + Engine.Run()`. Add named environment configs. Update launcher.

### Task 3.1: Add named environments to config

**Files:**
- Modify: `internal/config/config.go`
- Create: `internal/config/config_environments_test.go`

- [ ] **Step 1: Add environments map to Config**

```go
type EnvironmentConfig struct {
	Type     string `yaml:"type"`     // "local", "openshell", "k8s"
	Gateway  string `yaml:"gateway"`  // OpenShell gateway URL
	Insecure bool   `yaml:"insecure"`
	Image    string `yaml:"image"`
	RedisURL string `yaml:"redis_url"` // K8s only
	Namespace string `yaml:"namespace"`
	Kubeconfig string `yaml:"kubeconfig"`
}

type Config struct {
	// ... existing fields ...
	Environments map[string]EnvironmentConfig `yaml:"environments"`
}
```

- [ ] **Step 2: Write test for loading environments from YAML**

- [ ] **Step 3: Default: if no environments configured, create implicit "local"**

- [ ] **Step 4: Verify and commit**

### Task 3.2: Add LocalExecutor to agent-compose adapter

**Files:**
- Create: `internal/environment/local_executor.go`
- Create: `internal/environment/local_executor_test.go`

- [ ] **Step 1: Implement LocalExecutor**

Wraps `exec.Command` to fork a process. Implements agent-compose's `Executor` interface (`CreateSandbox`, `DeleteSandbox`, `ExecInSandbox`, `ConnectSandbox`, `ListSandboxes`). For local, `CreateSandbox` just starts the process and returns its PID as the name.

- [ ] **Step 2: Test and commit**

### Task 3.3: Route launches through agent-compose pipeline

**Files:**
- Modify: `internal/frontend/tui/app.go` (launch flow)
- Modify: `internal/frontend/tui/views/launcher.go` (environment selection)

- [ ] **Step 1: Update launcher to show environment selection**

When multiple environments are configured, show a second selection step after harness selection. When only "local" exists, skip it (same UX as today).

- [ ] **Step 2: Route launch through Engine.Resolve() + Executor**

Replace direct `exec.Command` in the launch path with:
1. Build agent-compose `Agent` from harness selection + environment
2. Call `Engine.Resolve()` to get `ResolvedSpec`
3. Call `Executor.CreateSandbox()` with the resolved spec

- [ ] **Step 3: Verify the simple path still works**

Pick Claude, no environment selection, verify it forks a process exactly as before.

- [ ] **Step 4: Commit**

```
git commit -m "refactor: phase 3 — launches route through agent-compose pipeline"
```

---

## Phase 4: Managed Sessions

**Branch:** `refactor/managed-sessions`

**Goal:** Replace 3-field LaunchMeta with full Session lifecycle tracking.

### Task 4.1: Define Session struct and store

**Files:**
- Create: `internal/session/session.go`
- Create: `internal/session/store.go`
- Create: `internal/session/store_test.go`

**Interfaces:**
- Produces: `Session` struct (ID, AgentConfig, Environment, SandboxName, Provider, Status, timestamps, tokens, cost), `Store` (Create, Get, List, Update, Delete), `SessionStatus` enum

- [ ] **Step 1: Define Session and Status**

```go
type SessionStatus string

const (
	StatusCreated    SessionStatus = "created"
	StatusRunning    SessionStatus = "running"
	StatusIdle       SessionStatus = "idle"
	StatusTerminated SessionStatus = "terminated"
	StatusError      SessionStatus = "error"
)

type Session struct {
	ID          string
	AgentConfig string
	Environment string
	SandboxName string
	Provider    string
	Status      SessionStatus
	Model       string
	WorkingDir  string
	StartTime   time.Time
	LastActivity time.Time
	TokensIn    int64
	TokensOut   int64
	CostUSD     float64
}
```

- [ ] **Step 2: Implement file-based Store**

JSON file at `~/.aimux/sessions.json`. CRUD operations with mutex. Load on startup. Save on every mutation.

- [ ] **Step 3: Test Store CRUD**

- [ ] **Step 4: Commit**

### Task 4.2: Create SessionManager

**Files:**
- Create: `internal/session/manager.go`
- Create: `internal/session/manager_test.go`

**Interfaces:**
- Consumes: `Store` from 4.1, `Environment` from 1.1, `Coordinator` from 2.1
- Produces: `SessionManager` (CreateSession, TrackSession, ResumeSession, ArchiveSession)

- [ ] **Step 1: Implement CreateSession**

```go
func (m *Manager) CreateSession(agentConfig, envName string) (*Session, error) {
	// 1. Look up environment
	// 2. Resolve agent config via agent-compose Engine
	// 3. Call environment.CreateSandbox()
	// 4. Register in coordinator (if Redis configured)
	// 5. Persist to store
	// 6. Return session
}
```

- [ ] **Step 2: Implement lifecycle tracking**

Update session status on discovery ticks. Match discovered agents to sessions by sandbox name.

- [ ] **Step 3: Test and commit**

### Task 4.3: Wire SessionManager into app.go and replace LaunchMeta

**Files:**
- Modify: `internal/frontend/tui/app.go`
- Remove: `internal/controller/session_store.go` (replaced by session/store.go)

- [ ] **Step 1: Replace LaunchMeta with SessionManager calls**

- [ ] **Step 2: Verify full suite**

- [ ] **Step 3: Commit**

```
git commit -m "refactor: phase 4 — managed sessions replace LaunchMeta"
```

---

## Phase 5: Named Agent Configs

**Branch:** `feat/named-agent-configs`

**Goal:** Add agents.yaml loading, show named configs in launcher alongside raw harness names.

### Task 5.1: Add agents.yaml loading

**Files:**
- Modify: `internal/config/config.go`
- Create: `internal/config/agents.go`
- Create: `internal/config/agents_test.go`

- [ ] **Step 1: Define AgentConfig type**

```go
type AgentConfig struct {
	Name      string   `yaml:"name"`
	Runtime   string   `yaml:"runtime"`
	Inference string   `yaml:"inference"`
	Model     string   `yaml:"model"`
	Prompt    string   `yaml:"prompt"`
	MCP       []string `yaml:"mcp"`
	Skills    []string `yaml:"skills"`
	Policy    string   `yaml:"policy"`
}
```

- [ ] **Step 2: Load from ~/.aimux/agents.yaml and .aimux/agents.yaml (project-local)**

- [ ] **Step 3: Test loading with sample YAML**

- [ ] **Step 4: Commit**

### Task 5.2: Update launcher to show named configs

**Files:**
- Modify: `internal/frontend/tui/views/launcher.go`
- Modify: `internal/frontend/tui/views/newpicker.go`

- [ ] **Step 1: Add named configs section above raw harness names**

When `agents.yaml` has entries, show them as a "Configured Agents" section in the launcher. Raw harness names (Claude, Codex) appear below as "Quick Launch."

- [ ] **Step 2: Wire selection to agent-compose resolution**

Named config selection → `Engine.Resolve(configName)` → environment selection → launch.

- [ ] **Step 3: Test that empty agents.yaml shows only raw harness names (same as today)**

- [ ] **Step 4: Commit**

```
git commit -m "feat: phase 5 — named agent configs in launcher"
```

### Task 5.3: Expose agent configs through MCP server and web API

**Files:**
- Modify: `internal/mcpserver/server.go`
- Modify: `internal/frontend/web/handlers.go`

- [ ] **Step 1: Add list_agent_configs MCP tool**

Returns the named agent configs from the loaded config.

- [ ] **Step 2: Add GET /api/agent-configs web endpoint**

- [ ] **Step 3: Test and commit**

---

## Phase 6: Decompose app.go

**Branch:** `refactor/decompose-app`

**Goal:** Extract view-domain coordinators from the 3,686-line app.go. Each coordinator owns one domain.

### Task 6.1: Define AppContext interface

**Files:**
- Create: `internal/frontend/tui/context.go`

- [ ] **Step 1: Define the thin interface coordinators receive**

```go
type AppContext interface {
	Config() config.Config
	Orchestrator() *discovery.Orchestrator
	SessionManager() *session.Manager
	Coordinator() coordination.Coordinator
	Environments() []environment.Environment
	Providers() []provider.Provider
	Width() int
	Height() int
}
```

- [ ] **Step 2: Commit**

### Task 6.2: Extract AgentCoordinator

**Files:**
- Create: `internal/frontend/tui/coordinators/agent.go`
- Create: `internal/frontend/tui/coordinators/agent_test.go`
- Modify: `internal/frontend/tui/app.go` (remove agent-related methods)

- [ ] **Step 1: Move agent list, filter, sort, attend logic**

Extract `handleKey` cases for: `/` (filter), `s` (sort), `a` (attend), `o` (archive toggle), `*` (star), agent table navigation.

- [ ] **Step 2: Test independently of app.go**

- [ ] **Step 3: Commit**

### Task 6.3: Extract SessionCoordinator

**Files:**
- Create: `internal/frontend/tui/coordinators/session.go`
- Modify: `internal/frontend/tui/app.go`

- [ ] **Step 1: Move PTY embed, zoom, resume, handleEnter, openK8sSession, openRemoteSession**

- [ ] **Step 2: Commit**

### Task 6.4: Extract TraceCoordinator

**Files:**
- Create: `internal/frontend/tui/coordinators/trace.go`
- Modify: `internal/frontend/tui/app.go`

- [ ] **Step 1: Move trace parsing, logs view, export, parserForProvider, parserForRemote**

- [ ] **Step 2: Commit**

### Task 6.5: Extract LaunchCoordinator

**Files:**
- Create: `internal/frontend/tui/coordinators/launch.go`
- Modify: `internal/frontend/tui/app.go`

- [ ] **Step 1: Move launcher, spawn, kill confirmation, handleKillConfirm, promptKill**

- [ ] **Step 2: Commit**

### Task 6.6: Verify app.go is under 600 lines

- [ ] **Step 1: Count lines**

Run: `wc -l internal/frontend/tui/app.go`
Expected: Under 600 lines

- [ ] **Step 2: Full verification**

Run: `go build ./... && go vet ./... && go test ./... -timeout 30s`
Expected: All PASS

- [ ] **Step 3: Final commit**

```
git commit -m "refactor: phase 6 complete — app.go decomposed into coordinators"
```

---

## Verification Checklist (all phases)

- [ ] `go test ./... -timeout 30s` passes
- [ ] `go vet ./...` passes
- [ ] Solo developer experience unchanged (no config, pick harness, run)
- [ ] Claude agent in OpenShell sandbox shows `ProviderName: "claude"`, not `"openshell"`
- [ ] Redis coordination works with both K8s and OpenShell environments
- [ ] Named agent configs appear in launcher when agents.yaml exists
- [ ] Sessions survive aimux restarts (local persistence + Redis)
- [ ] app.go under 600 lines
- [ ] No bubbletea/lipgloss imports in core packages

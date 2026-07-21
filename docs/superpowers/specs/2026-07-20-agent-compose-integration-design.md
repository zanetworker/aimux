# Agent-Compose Integration Design

## Problem

aimux's OpenShell sandbox code (`spawn/sandbox.go`, `runtime/openshell.go`, `openshell/client.go`, `mcpserver/backend_openshell.go`) is a first-pass prototype scoring 6.3/10 on code quality. The `zanetworker/agent-compose` repo (`pkg/compose`) solves the same problem with better abstractions (8/10): pluggable Executor interface, resolver chains, declarative composition, proper cleanup, and no hardcoded values.

## Decision

Replace aimux's OpenShell implementation with a thin adapter over `pkg/compose.Engine`. Keep aimux's discovery, OTEL, TUI, web dashboard, and cost tracking unchanged. K8s backend stays as-is (out of scope).

## Approach: Thin Adapter

A new `internal/compose/` package wraps `compose.Engine` and exposes the same function signatures that aimux consumers already call. Consumer files change only their import paths, not their logic.

## What Gets Deleted

| File | LOC | Reason |
|------|-----|--------|
| `internal/openshell/client.go` | 343 | Replaced by `compose.CLIExecutor` |
| `internal/openshell/client_test.go` | 252 | Tests move to adapter |
| `internal/openshell/client_integration_test.go` | 234 | Integration tests rewritten |
| `internal/runtime/openshell.go` | 152 | Replaced by `compose.Engine` lifecycle |
| `internal/runtime/openshell_test.go` | 157 | Tests move to adapter |
| `internal/spawn/sandbox.go` | 274 | Replaced by adapter `LaunchInSandbox` |
| `internal/spawn/sandbox_test.go` | 107 | Tests move to adapter |
| `internal/mcpserver/backend_openshell.go` | 140 | Replaced by adapter backend |
| `internal/mcpserver/backend_openshell_test.go` | 290 | Tests move to adapter |
| `internal/mcpserver/backend_openshell_integration_test.go` | 158 | Rewritten |

Total removed: ~2,107 LOC

## What Gets Created

### `internal/compose/adapter.go`

Wraps `compose.Engine` for aimux consumers. Key functions:

```go
package compose

// Engine wraps agent-compose's Engine with aimux-specific defaults.
type Engine struct {
    inner *pkgcompose.Engine
    cfg   *pkgcompose.Config
}

// New creates an Engine from aimux config (binary path, gateway, image).
func New(opts Options) (*Engine, error)

// LaunchInSandbox creates a sandbox and connects via tmux.
// Same signature as the old spawn.LaunchInSandbox so callers don't change.
func (e *Engine) LaunchInSandbox(provider, dir string, opts LaunchOpts) (*LaunchResult, error)

// LaunchResult matches the old spawn.LaunchResult.
type LaunchResult struct {
    SandboxName string
    TMuxSession string
}

// LaunchOpts matches the old spawn.SandboxOpts.
type LaunchOpts struct {
    Name         string
    Image        string
    Provider     string
    OTELEndpoint string
    Prompt       string
    Model        string
    Inference    string
    MCP          []string
}
```

Internally:
1. Maps `LaunchOpts` to `compose.Agent` + `compose.RunOpts`
2. Calls `engine.Resolve()` to get the full spec
3. Calls `engine.Start()` with `Interactive: true` for tmux connect
4. Returns `LaunchResult` with sandbox name and tmux session

### `internal/compose/backend.go`

Implements `mcpserver.Backend` interface using `compose.Engine`:

```go
// Backend implements mcpserver.Backend using agent-compose.
type Backend struct {
    engine *Engine
    mu     sync.Mutex
    pool   map[string]*poolEntry
}

func NewBackend(engine *Engine) *Backend

// CreateSandbox creates a sandbox via engine.Start().
func (b *Backend) CreateSandbox(ctx context.Context, opts mcpserver.SandboxOpts) (string, error)

// DeleteSandbox stops a sandbox via engine.Stop().
func (b *Backend) DeleteSandbox(ctx context.Context, name string) error

// ListSandboxes lists via engine.List().
func (b *Backend) ListSandboxes(ctx context.Context) ([]mcpserver.SandboxInfo, error)

// ExecStream runs a command via engine's executor.
func (b *Backend) ExecStream(ctx context.Context, name string, cmd []string) (mcpserver.ExecResult, error)

// IdleCount returns the number of idle sandboxes in the pool.
func (b *Backend) IdleCount(ctx context.Context) (int, error)
```

Pool management (claim/release for task dispatch) stays in this file since it's aimux-specific.

### `internal/compose/kill.go`

```go
// KillSandbox stops a sandbox by name. Used by controller/kill.go.
func (e *Engine) KillSandbox(ctx context.Context, name string) error
```

Replaces the direct `openshell.NewClient()` call in `controller/kill.go`.

### `internal/compose/adapter_test.go`

Tests using `compose.NewDryRunExecutor()`:
- `TestLaunchInSandbox_ResolvesCorrectSpec` — verifies agent-compose receives correct config
- `TestLaunchInSandbox_OTELEnvInjected` — verifies OTEL env vars in resolved spec
- `TestLaunchInSandbox_CleanupOnFailure` — verifies sandbox deleted on error

### `internal/compose/backend_test.go`

Tests using mock executor:
- `TestBackend_CreateSandbox` — creates and adds to pool
- `TestBackend_DeleteSandbox` — removes from pool
- `TestBackend_ListSandboxes` — maps compose.AgentStatus to mcpserver.SandboxInfo
- `TestBackend_ExecStream` — claims idle, executes, releases
- `TestBackend_IdleCount` — counts idle entries
- `TestBackend_ConcurrentExec` — verifies mutex under concurrent access

## What Gets Modified

### Import path changes (5 files)

| File | Old import | New import | Change |
|------|-----------|-----------|--------|
| `cmd/aimux/main.go` | `spawn.LaunchInSandbox` | `compose.Engine.LaunchInSandbox` | Function call receiver |
| `internal/frontend/tui/app.go` | `spawn.LaunchInSandbox` | `compose.Engine.LaunchInSandbox` | Function call receiver |
| `internal/controller/remote.go` | `spawn.LaunchInSandbox` | `compose.Engine.LaunchInSandbox` | Function call receiver |
| `internal/controller/kill.go` | `openshell.NewClient` | `compose.Engine.KillSandbox` | Simplified call |
| `internal/mcpserver/server.go` | `NewOpenShellBackend` | `compose.NewBackend` | Constructor swap |

### `internal/discovery/sandbox.go`

Currently imports `openshell` only for `StripAnsi()`. Inline a 3-line ANSI strip regex instead:

```go
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripAnsi(s string) string { return ansiRe.ReplaceAllString(s, "") }
```

### `go.mod`

Add: `github.com/zanetworker/agent-compose v0.1.0` (or local replace directive during development).

### Config wiring

`cmd/aimux/main.go` creates the `compose.Engine` at startup and passes it to TUI, controller, and MCP server. The engine is configured from `~/.aimux/config.yaml` fields (openshell binary, gateway, default image) mapped to `compose.Config`.

## OTEL Forwarding

The old `spawn/sandbox.go` had a hardcoded Node.js TCP proxy for OTEL forwarding from sandboxes. This is NOT part of agent-compose's scope. Two options:

1. **Keep OTEL forwarder as a separate concern** in `internal/otel/forwarder.go` (called after `engine.Start()`)
2. **Use agent-compose's env injection** to set `OTEL_EXPORTER_OTLP_ENDPOINT` pointing to the host

Option 2 is cleaner: add OTEL env vars to the `compose.Agent.Env` map before calling `engine.Start()`. The sandbox's agent process sends OTEL directly to the host endpoint. No proxy needed if the sandbox egress policy allows it.

## Testing Strategy

**Unit tests** (adapter_test.go, backend_test.go):
- Use `compose.NewDryRunExecutor()` to verify correct commands are generated
- Use mock executor to verify pool management
- Assert on `ResolvedSpec` fields (image, providers, env, egress)

**Existing tests that stay unchanged:**
- `mcpserver/server_test.go` — tests MCP tool handlers against Backend interface (not implementation)
- `mcpserver/backend_k8s_test.go` — K8s backend unaffected
- `mcpserver/pool_test.go` — pool logic unaffected
- `mcpserver/journal_test.go` — journal unaffected
- `controller/remote_test.go` — tests controller functions (mock Backend)
- `controller/kill_test.go` — needs update to use compose.Engine

## Migration Path

1. Add agent-compose dependency to go.mod
2. Create `internal/compose/` with adapter, backend, kill, and tests
3. Update 5 consumer files (import paths only)
4. Inline `stripAnsi` in discovery/sandbox.go
5. Delete old files (openshell/, runtime/openshell.go, spawn/sandbox.go, mcpserver/backend_openshell.go)
6. Run `go build ./...` and `go test ./...`
7. Manual test: `aimux` with a real OpenShell gateway (if available)

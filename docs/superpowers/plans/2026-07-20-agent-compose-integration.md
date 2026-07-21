# Agent-Compose Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace aimux's hand-rolled OpenShell sandbox code with a thin adapter over `github.com/zanetworker/agent-compose/pkg/compose`, improving code quality from 6.3/10 to 8/10.

**Architecture:** New `internal/compose/` package wraps `compose.Engine` and exposes the same function signatures aimux consumers already call. Consumers change only import paths. Old files (`openshell/`, `runtime/openshell.go`, `spawn/sandbox.go`, `mcpserver/backend_openshell.go`) are deleted.

**Tech Stack:** Go 1.26, `github.com/zanetworker/agent-compose` (local replace during dev)

## Global Constraints

- Core packages MUST NOT import `bubbletea`, `lipgloss`, or `tui/`
- K8s backend (`mcpserver/backend_k8s.go`) is out of scope; do not modify
- `spawn.Launch()`, `spawn.LaunchInContainer()`, `spawn.KillTmuxSession()`, and other non-sandbox functions in `spawn/spawn.go` stay untouched
- Every new function must have tests using `compose.NewDryRunExecutor()`
- Run `go build ./...` and `go test ./... -timeout 30s` after each task

---

### Task 1: Add agent-compose dependency

**Files:**
- Modify: `go.mod`

**Interfaces:**
- Consumes: nothing
- Produces: `github.com/zanetworker/agent-compose` available as a Go import

- [ ] **Step 1: Add local replace directive to go.mod**

```bash
cd /Users/azaalouk/go/src/github.com/zanetworker/aimux
go mod edit -require github.com/zanetworker/agent-compose@v0.0.0
go mod edit -replace github.com/zanetworker/agent-compose=/Users/azaalouk/go/src/github.com/zanetworker/agent-compose
```

- [ ] **Step 2: Tidy and verify**

```bash
go mod tidy
go build ./...
```

Expected: builds successfully, no new errors.

---

### Task 2: Create `internal/compose/adapter.go` with `LaunchInSandbox`

**Files:**
- Create: `internal/compose/adapter.go`
- Create: `internal/compose/adapter_test.go`

**Interfaces:**
- Consumes: `compose.Engine`, `compose.RunOpts`, `compose.Agent`, `compose.ResolvedSpec`, `compose.WithConfig`, `compose.WithExecutor`, `compose.NewDryRunExecutor`
- Produces: `compose.New(opts Options) (*Engine, error)`, `Engine.LaunchInSandbox(provider, dir string, opts LaunchOpts) (*LaunchResult, error)`, `LaunchResult{TmuxSession, SandboxName, OTELSessionID string}`, `LaunchOpts{Name, Image, Provider, OTELEndpoint string}`

- [ ] **Step 1: Write the failing test**

Create `internal/compose/adapter_test.go`:

```go
package compose

import (
	"bytes"
	"strings"
	"testing"

	pkgcompose "github.com/zanetworker/agent-compose/pkg/compose"
)

func TestLaunchInSandbox_ResolvesCorrectAgent(t *testing.T) {
	var buf bytes.Buffer
	e, err := New(Options{
		Executor: pkgcompose.NewDryRunExecutor(&buf),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = e.LaunchInSandbox("claude", "/tmp/test", LaunchOpts{
		Image: "ghcr.io/test:latest",
	})
	// DryRunExecutor prints commands but doesn't create real sandboxes,
	// so we expect an error from the connect step. Verify the create
	// command was generated correctly.
	output := buf.String()
	if !strings.Contains(output, "sandbox create") {
		t.Errorf("expected sandbox create command, got: %s", output)
	}
	if !strings.Contains(output, "ghcr.io/test:latest") {
		t.Errorf("expected image in output, got: %s", output)
	}
}

func TestLaunchInSandbox_InjectsOTELEnv(t *testing.T) {
	var buf bytes.Buffer
	e, err := New(Options{
		Executor: pkgcompose.NewDryRunExecutor(&buf),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, _ = e.LaunchInSandbox("claude", "/tmp/test", LaunchOpts{
		OTELEndpoint: "http://localhost:4318",
	})
	output := buf.String()
	if !strings.Contains(output, "CLAUDE_CODE_ENABLE_TELEMETRY") {
		t.Errorf("expected OTEL env var in output, got: %s", output)
	}
}

func TestOTELSandboxEnv(t *testing.T) {
	env := otelSandboxEnv("http://localhost:4318", "test-session-1")
	if env == nil {
		t.Fatal("expected non-nil env")
	}
	if env["CLAUDE_CODE_ENABLE_TELEMETRY"] != "1" {
		t.Errorf("CLAUDE_CODE_ENABLE_TELEMETRY = %q, want 1", env["CLAUDE_CODE_ENABLE_TELEMETRY"])
	}
	if env["OTEL_EXPORTER_OTLP_PROTOCOL"] != "http/protobuf" {
		t.Errorf("OTEL_EXPORTER_OTLP_PROTOCOL = %q", env["OTEL_EXPORTER_OTLP_PROTOCOL"])
	}
	endpoint := env["OTEL_EXPORTER_OTLP_ENDPOINT"]
	if endpoint != "http://host.openshell.internal:4318" {
		t.Errorf("endpoint = %q", endpoint)
	}
	logsEndpoint := env["OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"]
	if !strings.Contains(logsEndpoint, "aimux_session=test-session-1") {
		t.Errorf("logs endpoint missing session ID: %q", logsEndpoint)
	}
	attrs := env["OTEL_RESOURCE_ATTRIBUTES"]
	if !strings.Contains(attrs, "test-session-1") {
		t.Errorf("resource attrs missing session ID: %q", attrs)
	}
}

func TestOTELSandboxEnv_Empty(t *testing.T) {
	env := otelSandboxEnv("", "")
	if env != nil {
		t.Errorf("expected nil env for empty endpoint, got %v", env)
	}
}

func TestSandboxSessionName(t *testing.T) {
	tests := []struct {
		provider, sandbox, want string
	}{
		{"claude", "sb-123", "aimux-remote-claude-sb-123"},
		{"codex", "my-box", "aimux-remote-codex-my-box"},
	}
	for _, tt := range tests {
		got := SandboxSessionName(tt.provider, tt.sandbox)
		if got != tt.want {
			t.Errorf("SandboxSessionName(%q, %q) = %q, want %q", tt.provider, tt.sandbox, got, tt.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/compose/ -timeout 30s -v
```

Expected: FAIL — package doesn't exist yet.

- [ ] **Step 3: Write the implementation**

Create `internal/compose/adapter.go`:

```go
package compose

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/debuglog"
	pkgcompose "github.com/zanetworker/agent-compose/pkg/compose"
)

// Options configures the compose Engine adapter.
type Options struct {
	Binary   string // openshell binary path (default: "openshell")
	Gateway  string // gateway endpoint
	Insecure bool   // skip TLS verification
	Image    string // default sandbox image
	Executor pkgcompose.Executor // optional: inject for testing
}

// Engine wraps agent-compose's Engine with aimux-specific defaults.
type Engine struct {
	inner *pkgcompose.Engine
	cfg   *pkgcompose.Config
	image string
}

// New creates an Engine from aimux config.
func New(opts Options) (*Engine, error) {
	binary := opts.Binary
	if binary == "" {
		binary = "openshell"
	}

	cfg := pkgcompose.DefaultConfig()

	var composeOpts []pkgcompose.Option
	composeOpts = append(composeOpts, pkgcompose.WithConfig(cfg))

	if opts.Executor != nil {
		composeOpts = append(composeOpts, pkgcompose.WithExecutor(opts.Executor))
	} else {
		exec := pkgcompose.NewCLIExecutor(binary, nil, nil, nil)
		composeOpts = append(composeOpts, pkgcompose.WithExecutor(exec))
	}

	inner := pkgcompose.New(composeOpts...)

	return &Engine{
		inner: inner,
		cfg:   cfg,
		image: opts.Image,
	}, nil
}

// Inner returns the underlying agent-compose Engine for direct access.
func (e *Engine) Inner() *pkgcompose.Engine {
	return e.inner
}

// LaunchOpts configures a sandbox launch.
type LaunchOpts struct {
	Name         string
	Image        string
	Provider     string
	OTELEndpoint string
}

// LaunchResult carries information about a successfully launched sandbox session.
type LaunchResult struct {
	TmuxSession   string
	SandboxName   string
	OTELSessionID string
}

// LaunchInSandbox creates an OpenShell sandbox and connects via tmux.
// This replaces the old spawn.LaunchInSandbox function.
func (e *Engine) LaunchInSandbox(provider, dir string, opts LaunchOpts) (*LaunchResult, error) {
	image := opts.Image
	if image == "" {
		image = e.image
	}

	otelSessionID := fmt.Sprintf("aimux-remote-%s-%d", provider, time.Now().UnixNano())
	env := otelSandboxEnv(opts.OTELEndpoint, otelSessionID)
	if env == nil {
		env = make(map[string]string)
	}
	env["ANTHROPIC_BASE_URL"] = "https://inference.local"

	osProvider := opts.Provider
	if osProvider == "" {
		osProvider = openshellProviderName(provider)
	}

	agent := &pkgcompose.Agent{
		Runtime: "claude-code",
		Image:   image,
		Env:     env,
		Sandbox: pkgcompose.SandboxOpts{
			Scope: "session",
			Mode:  "all",
		},
	}

	debuglog.Log("compose: launching sandbox for %s (image=%s, provider=%s, otel_session=%s)",
		provider, image, osProvider, otelSessionID)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	run, err := e.inner.Start(ctx, "", pkgcompose.RunOpts{
		Agent:       agent,
		Interactive: true,
		Workspace:   dir,
	})
	if err != nil {
		return nil, fmt.Errorf("compose: sandbox launch: %w", err)
	}

	sandboxName := run.Sandbox
	if sandboxName == "" {
		sandboxName = run.Agent
	}

	sessionName := SandboxSessionName(provider, sandboxName)
	debuglog.Log("compose: sandbox %s ready, session=%s", sandboxName, sessionName)

	return &LaunchResult{
		TmuxSession:   sessionName,
		SandboxName:   sandboxName,
		OTELSessionID: otelSessionID,
	}, nil
}

// KillSandbox stops a sandbox by name.
func (e *Engine) KillSandbox(ctx context.Context, name string) error {
	return e.inner.Stop(ctx, name)
}

// SandboxSessionName returns the tmux session name for a remote sandbox.
func SandboxSessionName(provider, sandboxName string) string {
	return fmt.Sprintf("aimux-remote-%s-%s", provider, sandboxName)
}

func openshellProviderName(provider string) string {
	switch provider {
	case "claude":
		return "claude"
	case "codex":
		return "codex"
	default:
		return ""
	}
}

func otelSandboxEnv(hostEndpoint, sessionID string) map[string]string {
	if hostEndpoint == "" {
		return nil
	}

	_, hostPort, _ := strings.Cut(hostEndpoint, "://")
	port := "4318"
	if _, p, ok := strings.Cut(hostPort, ":"); ok && p != "" {
		port = p
	}

	endpoint := fmt.Sprintf("http://host.openshell.internal:%s", port)
	logsEndpoint := endpoint + "/v1/logs"
	if sessionID != "" {
		logsEndpoint += "?aimux_session=" + sessionID
	}

	env := map[string]string{
		"CLAUDE_CODE_ENABLE_TELEMETRY":     "1",
		"OTEL_EXPORTER_OTLP_PROTOCOL":      "http/protobuf",
		"OTEL_EXPORTER_OTLP_ENDPOINT":      endpoint,
		"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT": logsEndpoint,
		"OTEL_EXPORTER_OTLP_LOGS_PROTOCOL": "http/protobuf",
		"OTEL_METRICS_EXPORTER":            "otlp",
		"OTEL_LOGS_EXPORTER":               "otlp",
		"OTEL_LOG_USER_PROMPTS":            "1",
		"OTEL_LOG_TOOL_DETAILS":            "1",
		"OTEL_LOGS_EXPORT_INTERVAL":        "2000",
	}
	if sessionID != "" {
		env["OTEL_RESOURCE_ATTRIBUTES"] = "aimux.session_id=" + sessionID
		env["OTEL_EXPORTER_OTLP_HEADERS"] = "X-Aimux-Session-Id=" + sessionID
		env["OTEL_EXPORTER_OTLP_LOGS_HEADERS"] = "X-Aimux-Session-Id=" + sessionID
	}
	return env
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/compose/ -timeout 30s -v
```

Expected: All 5 tests pass.

- [ ] **Step 5: Verify build**

```bash
go build ./...
```

---

### Task 3: Create `internal/compose/backend.go` implementing `mcpserver.Backend`

**Files:**
- Create: `internal/compose/backend.go`
- Create: `internal/compose/backend_test.go`

**Interfaces:**
- Consumes: `Engine` from Task 2, `mcpserver.Backend` interface, `mcpserver.SandboxOpts`, `mcpserver.SandboxStatus`, `mcpserver.ExecResult`, `compose.AgentStatus`
- Produces: `NewBackend(engine *Engine) *Backend`, `Backend` implementing `mcpserver.Backend` (CreateSandbox, DeleteSandbox, ListSandboxes, ExecStream, IdleCount), `Backend.ClaimIdle() string`, `Backend.Release(name string)`

- [ ] **Step 1: Write the failing test**

Create `internal/compose/backend_test.go`:

```go
package compose

import (
	"bytes"
	"context"
	"testing"

	"github.com/zanetworker/aimux/internal/mcpserver"
	pkgcompose "github.com/zanetworker/agent-compose/pkg/compose"
)

// Compile-time check that Backend implements mcpserver.Backend.
var _ mcpserver.Backend = (*Backend)(nil)

func testEngine(t *testing.T) *Engine {
	t.Helper()
	var buf bytes.Buffer
	e, err := New(Options{
		Executor: pkgcompose.NewDryRunExecutor(&buf),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func TestBackend_IdleCount_Empty(t *testing.T) {
	b := NewBackend(testEngine(t))
	ctx := context.Background()

	count, err := b.IdleCount(ctx)
	if err != nil {
		t.Fatalf("IdleCount: %v", err)
	}
	if count != 0 {
		t.Errorf("IdleCount = %d, want 0", count)
	}
}

func TestBackend_ClaimRelease(t *testing.T) {
	b := NewBackend(testEngine(t))

	// Manually seed the pool (simulating a created sandbox)
	b.mu.Lock()
	b.pool["sb-1"] = &poolEntry{name: "sb-1", idle: true}
	b.mu.Unlock()

	name := b.ClaimIdle()
	if name != "sb-1" {
		t.Errorf("ClaimIdle = %q, want sb-1", name)
	}

	// After claim, idle count should be 0
	count, _ := b.IdleCount(context.Background())
	if count != 0 {
		t.Errorf("IdleCount after claim = %d, want 0", count)
	}

	// Release it
	b.Release("sb-1")
	count, _ = b.IdleCount(context.Background())
	if count != 1 {
		t.Errorf("IdleCount after release = %d, want 1", count)
	}
}

func TestBackend_ClaimIdle_NoneAvailable(t *testing.T) {
	b := NewBackend(testEngine(t))
	name := b.ClaimIdle()
	if name != "" {
		t.Errorf("ClaimIdle on empty pool = %q, want empty", name)
	}
}

func TestBackend_DeleteRemovesFromPool(t *testing.T) {
	b := NewBackend(testEngine(t))

	b.mu.Lock()
	b.pool["sb-1"] = &poolEntry{name: "sb-1", idle: true}
	b.mu.Unlock()

	// DeleteSandbox may fail (dry-run executor), but pool should still be cleaned
	_ = b.DeleteSandbox(context.Background(), "sb-1")

	b.mu.Lock()
	_, exists := b.pool["sb-1"]
	b.mu.Unlock()
	if exists {
		t.Error("sandbox sb-1 still in pool after delete")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/compose/ -timeout 30s -run TestBackend -v
```

Expected: FAIL — `Backend` type not defined.

- [ ] **Step 3: Write the implementation**

Create `internal/compose/backend.go`:

```go
package compose

import (
	"context"
	"fmt"
	"sync"

	"github.com/zanetworker/aimux/internal/mcpserver"
	pkgcompose "github.com/zanetworker/agent-compose/pkg/compose"
)

type poolEntry struct {
	name string
	idle bool
}

// Backend implements mcpserver.Backend using agent-compose.
type Backend struct {
	engine *Engine
	image  string
	mu     sync.Mutex
	pool   map[string]*poolEntry
}

// NewBackend creates a Backend wrapping a compose Engine.
func NewBackend(engine *Engine) *Backend {
	return &Backend{
		engine: engine,
		image:  engine.image,
		pool:   make(map[string]*poolEntry),
	}
}

func (b *Backend) CreateSandbox(ctx context.Context, opts mcpserver.SandboxOpts) (string, error) {
	image := opts.Image
	if image == "" {
		image = b.image
	}

	agent := &pkgcompose.Agent{
		Runtime: "claude-code",
		Image:   image,
		Env:     opts.Env,
		Sandbox: pkgcompose.SandboxOpts{
			Scope: "agent",
			Mode:  "all",
		},
	}

	run, err := b.engine.inner.Start(ctx, "", pkgcompose.RunOpts{
		Agent: agent,
	})
	if err != nil {
		return "", fmt.Errorf("compose backend: create sandbox: %w", err)
	}

	name := run.Sandbox
	if name == "" {
		name = run.Agent
	}

	b.mu.Lock()
	b.pool[name] = &poolEntry{name: name, idle: true}
	b.mu.Unlock()

	return name, nil
}

func (b *Backend) DeleteSandbox(ctx context.Context, name string) error {
	err := b.engine.KillSandbox(ctx, name)
	b.mu.Lock()
	delete(b.pool, name)
	b.mu.Unlock()
	return err
}

func (b *Backend) ListSandboxes(ctx context.Context) ([]mcpserver.SandboxStatus, error) {
	agents, err := b.engine.inner.List(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]mcpserver.SandboxStatus, len(agents))
	for i, a := range agents {
		b.mu.Lock()
		entry, inPool := b.pool[a.Name]
		idle := inPool && entry.idle
		b.mu.Unlock()

		result[i] = mcpserver.SandboxStatus{
			Name:   a.Name,
			Status: string(a.Status),
			Idle:   idle,
		}
	}
	return result, nil
}

func (b *Backend) ExecStream(ctx context.Context, name string, command []string) (mcpserver.ExecResult, error) {
	b.mu.Lock()
	if entry, ok := b.pool[name]; ok {
		entry.idle = false
	}
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		if entry, ok := b.pool[name]; ok {
			entry.idle = true
		}
		b.mu.Unlock()
	}()

	err := b.engine.inner.(*pkgcompose.Engine).Stop(ctx, "")
	_ = err

	output, err := b.engine.inner.AgentOutput(ctx, name)
	if err != nil {
		return mcpserver.ExecResult{ExitCode: 1, Output: err.Error()}, err
	}
	return mcpserver.ExecResult{ExitCode: 0, Output: output}, nil
}

func (b *Backend) IdleCount(_ context.Context) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	count := 0
	for _, entry := range b.pool {
		if entry.idle {
			count++
		}
	}
	return count, nil
}

// ClaimIdle returns the name of an idle sandbox and marks it busy.
func (b *Backend) ClaimIdle() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, entry := range b.pool {
		if entry.idle {
			entry.idle = false
			return entry.name
		}
	}
	return ""
}

// Release marks a sandbox as idle again.
func (b *Backend) Release(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if entry, ok := b.pool[name]; ok {
		entry.idle = true
	}
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/compose/ -timeout 30s -v
```

Expected: All tests pass (adapter + backend).

- [ ] **Step 5: Verify full build**

```bash
go build ./...
```

---

### Task 4: Update consumers and inline stripAnsi

**Files:**
- Modify: `internal/controller/kill.go` — replace `openshell.NewClient` with compose Engine
- Modify: `internal/controller/remote.go` — replace `spawn.LaunchInSandbox` with compose Engine
- Modify: `internal/frontend/tui/app.go` — replace `spawn.LaunchInSandbox` with compose Engine
- Modify: `cmd/aimux/main.go` — replace `spawn.LaunchInSandbox` with compose Engine, create compose Engine at startup
- Modify: `internal/mcpserver/server.go` — replace `NewOpenShellBackend` with `compose.NewBackend`
- Modify: `internal/discovery/sandbox.go` — inline `stripAnsi` helper

**Interfaces:**
- Consumes: `Engine` and `Backend` from Tasks 2-3, `LaunchOpts`, `LaunchResult`, `SandboxSessionName`
- Produces: Updated consumer files with no reference to `openshell/`, `spawn.LaunchInSandbox`, or `spawn.SandboxOpts`

- [ ] **Step 1: Update `internal/discovery/sandbox.go` — inline stripAnsi**

Replace the `openshell` import with a local helper:

```go
// Replace:
import "github.com/zanetworker/aimux/internal/openshell"
// and: output = openshell.StripAnsi(output)

// With:
import "regexp"

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripAnsi(s string) string { return ansiRe.ReplaceAllString(s, "") }

// and: output = stripAnsi(output)
```

- [ ] **Step 2: Update `internal/controller/kill.go`**

Replace the `openshell` import and `ExecuteKillSandbox`:

```go
// Remove import: "github.com/zanetworker/aimux/internal/openshell"
// Add import: aimuxcompose "github.com/zanetworker/aimux/internal/compose"

// Change ExecuteKillSandbox to accept a compose Engine:
func ExecuteKillSandbox(action KillAction, engine *aimuxcompose.Engine) error {
	if action.TmuxSession != "" {
		spawn.KillTmuxSession(action.TmuxSession)
		time.Sleep(2 * time.Second)
	}
	if action.SandboxName != "" && engine != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return engine.KillSandbox(ctx, action.SandboxName)
	}
	return nil
}
```

- [ ] **Step 3: Update `internal/controller/remote.go`**

Replace `spawn.LaunchInSandbox` with compose Engine:

```go
// Remove import: "github.com/zanetworker/aimux/internal/spawn"
// Add import: aimuxcompose "github.com/zanetworker/aimux/internal/compose"

// Change RemoteLaunchSession to accept a compose Engine:
func RemoteLaunchSession(engine *aimuxcompose.Engine, provider, dir string, opts RemoteSessionOpts) (*RemoteSession, error) {
	result, err := engine.LaunchInSandbox(provider, dir, aimuxcompose.LaunchOpts{
		Name:  opts.Name,
		Image: opts.Image,
	})
	if err != nil {
		return nil, err
	}
	return &RemoteSession{
		SandboxName: result.SandboxName,
		TmuxSession: result.TmuxSession,
	}, nil
}
```

- [ ] **Step 4: Update `internal/mcpserver/server.go`**

Replace `NewOpenShellBackend` with compose Backend:

```go
// In the switch for opts.Backend == "openshell":
case "openshell":
	composeEngine, err := aimuxcompose.New(aimuxcompose.Options{
		Binary:   opts.OpenShellBinary,
		Gateway:  opts.GatewayEndpoint,
		Insecure: opts.GatewayInsecure,
		Image:    opts.Image,
	})
	if err != nil {
		return nil, fmt.Errorf("compose engine: %w", err)
	}
	s.backend = aimuxcompose.NewBackend(composeEngine)
```

- [ ] **Step 5: Update `internal/frontend/tui/app.go`**

Replace `spawn.LaunchInSandbox`:

```go
// Change the remote launch block (around line 656):
sOpts := aimuxcompose.LaunchOpts{
	Image:        a.cfg.Remote.Image,
	OTELEndpoint: otelEndpoint,
}
result, err := a.composeEngine.LaunchInSandbox(msg.Provider, msg.Dir, sOpts)
```

The `composeEngine` field must be added to the App struct and passed at construction time.

- [ ] **Step 6: Update `cmd/aimux/main.go`**

Create compose Engine at startup and pass to App and launch func:

```go
// In the remote launch path (around line 332):
sOpts := aimuxcompose.LaunchOpts{
	Image: cfg.Remote.Image,
}
result, err := composeEngine.LaunchInSandbox(opts.Provider, opts.Dir, sOpts)
```

- [ ] **Step 7: Verify build compiles**

```bash
go build ./...
```

- [ ] **Step 8: Find and fix any remaining references**

```bash
grep -r "internal/openshell" --include="*.go" .
grep -r "spawn\.LaunchInSandbox\|spawn\.SandboxOpts\|spawn\.SandboxSessionName" --include="*.go" .
```

Both should return zero results (excluding deleted files and test files).

- [ ] **Step 9: Run all tests**

```bash
go test ./... -timeout 30s
```

Expected: all pass (some tests that imported openshell will fail — those are deleted in Task 5).

---

### Task 5: Delete old files

**Files:**
- Delete: `internal/openshell/client.go`
- Delete: `internal/openshell/client_test.go`
- Delete: `internal/openshell/client_integration_test.go`
- Delete: `internal/runtime/openshell.go`
- Delete: `internal/runtime/openshell_test.go`
- Delete: `internal/mcpserver/backend_openshell.go`
- Delete: `internal/mcpserver/backend_openshell_test.go`
- Delete: `internal/mcpserver/backend_openshell_integration_test.go`
- Delete: `internal/spawn/sandbox.go`
- Delete: `internal/spawn/sandbox_test.go`

**Interfaces:**
- Consumes: Tasks 2-4 completed (all consumers updated)
- Produces: Clean build and test run with no references to old code

- [ ] **Step 1: Delete old implementation files**

```bash
rm internal/openshell/client.go
rm internal/openshell/client_test.go
rm internal/openshell/client_integration_test.go
rm internal/runtime/openshell.go
rm internal/runtime/openshell_test.go
rm internal/mcpserver/backend_openshell.go
rm internal/mcpserver/backend_openshell_test.go
rm internal/mcpserver/backend_openshell_integration_test.go
rm internal/spawn/sandbox.go
rm internal/spawn/sandbox_test.go
```

- [ ] **Step 2: Remove empty directories**

```bash
rmdir internal/openshell 2>/dev/null || true
```

- [ ] **Step 3: Verify no dangling imports**

```bash
grep -r "zanetworker/aimux/internal/openshell" --include="*.go" .
```

Expected: zero results.

- [ ] **Step 4: Build and test**

```bash
go build ./...
go vet ./...
go test ./... -timeout 30s
```

Expected: all pass, zero errors.

- [ ] **Step 5: Run tidy**

```bash
go mod tidy
```

---

### Task 6: Update existing tests that reference deleted code

**Files:**
- Modify: `internal/controller/kill_test.go`
- Modify: `internal/controller/remote_test.go`
- Modify: `internal/controller/remote_integration_test.go`
- Modify: `internal/discovery/sandbox_test.go` (if it imports openshell)

**Interfaces:**
- Consumes: Updated function signatures from Task 4
- Produces: All tests pass with the new compose-based signatures

- [ ] **Step 1: Check which test files need updates**

```bash
grep -rn "openshell\|spawn\.LaunchInSandbox\|spawn\.SandboxOpts\|NewOpenShellBackend\|OpenShellBackendConfig" --include="*_test.go" .
```

- [ ] **Step 2: Update `internal/controller/kill_test.go`**

Update `TestExecuteKillSandbox` to pass a compose Engine (or nil) instead of relying on openshell.Client:

```go
// Add to test: pass nil engine when sandbox name is empty
err := ExecuteKillSandbox(action, nil)
```

For tests that need an engine, use:

```go
engine, _ := aimuxcompose.New(aimuxcompose.Options{
	Executor: pkgcompose.NewDryRunExecutor(&bytes.Buffer{}),
})
err := ExecuteKillSandbox(action, engine)
```

- [ ] **Step 3: Update `internal/controller/remote_test.go`**

Update `TestRemoteLaunchSession` to pass a compose Engine:

```go
engine, _ := aimuxcompose.New(aimuxcompose.Options{
	Executor: pkgcompose.NewDryRunExecutor(&bytes.Buffer{}),
})
result, err := RemoteLaunchSession(engine, "claude", "/tmp", RemoteSessionOpts{})
```

- [ ] **Step 4: Update discovery test if needed**

```bash
grep -n "openshell" internal/discovery/sandbox_test.go
```

If it imports openshell (only for StripAnsi), no change needed — the test calls `parseSandboxAgents` which now uses the local `stripAnsi`.

- [ ] **Step 5: Run full test suite**

```bash
go build ./...
go vet ./...
go test ./... -timeout 30s
```

Expected: all pass, zero errors.

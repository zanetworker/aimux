# SDK Mode Runtime — Implementation Plan

**Spec:** `docs/superpowers/specs/2026-09-05-sdk-mode-design.md` (Phases 5-6)
**Branch:** `refactor/managed-sessions`
**Scope:** Hook dispatcher, Environment.Exec(), SDK subprocess protocol, integration wiring
**Depends on:** Phases 1-4 (complete on this branch)

## Global Constraints

1. **Frontend parity.** New operations (launch SDK mode) reachable from CLI, TUI, Web.
2. **Test-first.** Failing test before implementation.
3. **Layer discipline.** Core packages must not import bubbletea/lipgloss/net/http.
4. **Testable without real Claude SDK.** Use a mock subprocess for testing. The real SDK is Phase A (post-ship).
5. **Environment.Exec() works for local first.** OpenShell dispatch is a backend swap, not new logic.

## Architecture (from spec)

```
SDK mode flow:
  aimux starts subprocess (mock or real SDK)
  Subprocess sends tool_call events via stdout (JSON lines)
  aimux hook dispatcher receives tool_call
  If tool is bash/read/write/edit/glob/grep → Environment.Exec(sandbox, cmd)
  Else → approve (let SDK handle locally)
  aimux sends tool_result back via stdin
  Subprocess sends text/completion events
```

## Tasks

### Task 5: Hook Dispatcher

The core routing logic: given a tool call, decide whether to dispatch to sandbox or approve for local execution.

**Files to create:**
- `internal/sdk/dispatcher.go` — Dispatcher struct with Dispatch(toolCall) → (result, handled)
- `internal/sdk/dispatcher_test.go` — comprehensive tests
- `internal/sdk/types.go` — ToolCall, ToolResult, HookDecision types

**Types:**
```go
type ToolCall struct {
    ID        string         `json:"id"`
    Name      string         `json:"name"`
    Input     map[string]any `json:"input"`
}

type ToolResult struct {
    ID      string `json:"id"`
    Output  string `json:"output"`
    IsError bool   `json:"is_error,omitempty"`
}

type HookDecision int
const (
    Approve  HookDecision = iota  // let SDK execute locally
    Handled                       // dispatcher handled it, use the result
)
```

**Dispatcher:**
```go
type Dispatcher struct {
    env      environment.Environment
    sandbox  string
    // sandboxTools: tools that get routed to the sandbox
    sandboxTools map[string]bool  // bash, read, write, edit, glob, grep
}

func (d *Dispatcher) Dispatch(call ToolCall) (ToolResult, HookDecision)
```

Logic:
- If call.Name is in sandboxTools → execute via env.Exec(), return (result, Handled)
- Else → return (empty, Approve)

For now, env.Exec() is a method we need on the Environment interface. Add:
```go
Exec(ctx context.Context, sandbox string, command string) (string, error)
```

**Tests:**
- Bash tool call → Handled, executes via env
- Read tool call → Handled
- WebSearch tool call → Approve (not a sandbox tool)
- Unknown tool → Approve
- Exec returns error → ToolResult with IsError=true
- Empty tool name → Approve

### Task 6: Environment.Exec()

Add Exec() to the Environment interface and implement for Local and OpenShell.

**Files to modify:**
- `internal/environment/environment.go` — add Exec to interface
- `internal/environment/local.go` — implement Exec (os/exec.Command)
- `internal/environment/openshell.go` — implement Exec (openshell sandbox exec)
- `internal/environment/k8s.go` — stub (return not supported error)

**Files to create:**
- `internal/environment/exec_test.go` — test Local.Exec with real commands

**Local.Exec:**
```go
func (l *Local) Exec(ctx context.Context, sandbox string, command string) (string, error) {
    cmd := exec.CommandContext(ctx, "sh", "-c", command)
    cmd.Dir = l.workDir
    out, err := cmd.CombinedOutput()
    return string(out), err
}
```

**OpenShell.Exec:**
```go
func (o *OpenShell) Exec(ctx context.Context, sandbox, command string) (string, error) {
    args := []string{"sandbox", "exec", "--name", sandbox}
    if o.gateway != "" {
        args = append(args, "--gateway-endpoint", o.gateway)
    }
    args = append(args, "--", "sh", "-c", command)
    cmd := exec.CommandContext(ctx, "openshell", args...)
    out, err := cmd.CombinedOutput()
    return string(out), err
}
```

**Tests:**
- Local.Exec runs "echo hello" → "hello\n"
- Local.Exec with failing command → error + stderr output
- Local.Exec respects context cancellation
- OpenShell.Exec builds correct args (unit test, no real openshell)
- K8s.Exec returns not-supported error

### Task 7: SDK Subprocess Protocol

Define the JSON-lines protocol between aimux and the SDK subprocess, build the subprocess manager, and create a mock SDK for testing.

**Files to create:**
- `internal/sdk/protocol.go` — Event types (tool_call, tool_result, text, completion, error)
- `internal/sdk/subprocess.go` — SubprocessManager: Start, Send, Receive, Stop
- `internal/sdk/subprocess_test.go` — tests with mock subprocess
- `internal/sdk/mock_sdk.go` — a Go program that acts as a fake SDK (reads prompts from stdin, sends tool_call events, receives tool_results, sends completion)
- `cmd/mock-sdk/main.go` — buildable mock SDK binary for integration testing

**Protocol (JSON lines on stdin/stdout):**
```json
// aimux → subprocess (stdin)
{"type": "prompt", "content": "Fix the bug in main.go"}
{"type": "tool_result", "id": "tc_1", "output": "file contents...", "is_error": false}

// subprocess → aimux (stdout)
{"type": "tool_call", "id": "tc_1", "name": "read", "input": {"file_path": "/src/main.go"}}
{"type": "text", "content": "I found the issue..."}
{"type": "completion", "stop_reason": "end_turn"}
{"type": "error", "message": "API key invalid"}
```

**SubprocessManager:**
```go
type SubprocessManager struct {
    cmd    *exec.Cmd
    stdin  io.WriteCloser
    stdout *bufio.Scanner
}

func NewSubprocess(binary string, args ...string) (*SubprocessManager, error)
func (s *SubprocessManager) Send(event Event) error
func (s *SubprocessManager) Receive() (Event, error)
func (s *SubprocessManager) Stop() error
```

**Mock SDK behavior:**
1. Reads a "prompt" event
2. Sends a "tool_call" for read (to test the dispatcher)
3. Reads the "tool_result"
4. Sends a "text" event with analysis
5. Sends a "completion" event

**Tests:**
- Start/stop subprocess lifecycle
- Send prompt, receive tool_call, send result, receive completion
- Subprocess exit with error
- Subprocess timeout (context cancellation)

### Task 8: Integration — SDK Mode Launch

Wire dispatcher + subprocess + environment into a working SDK mode launch path. Add an integration test that proves the full loop.

**Files to create:**
- `internal/sdk/session.go` — SDKSession orchestrator: starts subprocess, runs event loop (receive tool_call → dispatch → send result), collects text output
- `internal/sdk/session_test.go` — integration test with mock SDK

**Files to modify:**
- `internal/controller/launch.go` — add launchSDK path alongside launchNative
- `cmd/aimux/cmd/spawn.go` — wire --harness flag to choose SDK vs native
- `internal/frontend/web/handlers.go` — handleLaunch accepts harness field

**SDKSession:**
```go
type SDKSession struct {
    sub        *SubprocessManager
    dispatcher *Dispatcher
    events     []Event  // collected text/completion events
}

func NewSDKSession(binary string, env environment.Environment, sandbox string) (*SDKSession, error)
func (s *SDKSession) Run(ctx context.Context, prompt string) (string, error)
```

Run() loop:
1. Send prompt to subprocess
2. Loop: receive event
   - tool_call → dispatch → send tool_result
   - text → collect
   - completion → break
   - error → return error
3. Return collected text

**Integration test:**
1. Build mock-sdk binary
2. Create SDKSession with mock-sdk binary and Local environment
3. Call Run("list files in /tmp")
4. Assert: mock SDK sent a tool_call, dispatcher routed it, result came back, completion received
5. Assert: collected text contains expected output

**CLI wiring:**
- `aimux spawn reviewer --environment local` → if agent config has `harness: claude-sdk`, use SDK launch path
- `aimux spawn claude` → native mode (default, unchanged)

**Tests:**
- Integration: full loop with mock SDK (build binary, start, prompt, tool_call, result, completion)
- Controller: launchSDK called when harness is "claude-sdk"
- CLI: --harness flag accepted

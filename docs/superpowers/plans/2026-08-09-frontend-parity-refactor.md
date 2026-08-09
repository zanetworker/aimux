# Frontend Parity Refactor — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ensure all aimux interfaces (TUI, web dashboard, CLI) use the same shared backend code for remote sandbox operations: session-id management, trace/conversation fetching, and terminal access.

**Architecture:** Move TUI-only code (session store, trace enrichment, agent command building) into shared backend packages. Rewire web dashboard to use these instead of its stale tmux/file-only paths. CLI and MCP server are headless and need only the session store.

**Tech Stack:** Go, creack/pty, gorilla/websocket, internal packages (compose, otel, terminal, controller)

## Global Constraints

- Branch: `feat/remote-agents-openshell`
- Go 1.25+, all existing tests must pass after each task
- Pre-commit hooks: golangci-lint, gosec, end-of-file-fixer
- File permissions: 0o700 dirs, 0o600 files for anything in ~/.aimux/
- No new dependencies; all packages already in go.mod

## File Structure

| File | Action | Responsibility |
|------|--------|---------------|
| `internal/controller/session_store.go` | **Create** | Persistent sandbox→UUID store (moved from TUI) |
| `internal/controller/session_store_test.go` | **Create** | Unit tests for store |
| `internal/controller/remote_trace.go` | **Create** | Shared trace parser for remote agents (OTEL + session-file fallback) |
| `internal/controller/remote_trace_test.go` | **Create** | Unit tests for remote trace parser |
| `internal/controller/agent_command.go` | **Create** | `remoteAgentCommand` + `uuidValid` (moved from TUI) |
| `internal/controller/agent_command_test.go` | **Create** | Unit tests (moved from TUI app_test.go) |
| `internal/frontend/tui/app.go` | **Modify** | Replace inline session store/trace/command with controller calls |
| `internal/frontend/tui/remote_sessions.go` | **Delete** | Replaced by controller/session_store.go |
| `internal/frontend/web/handlers.go` | **Modify** | Add remote trace fallback in handleGetTrace |
| `internal/frontend/web/terminal.go` | **Modify** | Replace tmux attach with OpenShellExecBackend for remote |
| `internal/frontend/web/server.go` | **Modify** | Accept session store + OTEL store dependencies |
| `cmd/aimux/main.go` | **Modify** | Create shared session store, pass to both TUI and web |

---

### Task 1: Move session store to shared package

**Files:**
- Create: `internal/controller/session_store.go`
- Create: `internal/controller/session_store_test.go`
- Modify: `internal/frontend/tui/app.go` (import change)
- Delete: `internal/frontend/tui/remote_sessions.go`

**Interfaces:**
- Produces: `controller.SessionStore` with `Get(sandboxName) string`, `Put(sandboxName, sessionID)`, `NewSessionStore(configDir string) *SessionStore`

- [ ] **Step 1: Write the failing test**

```go
// internal/controller/session_store_test.go
package controller

import (
    "os"
    "path/filepath"
    "testing"
)

func TestSessionStore_PutGet(t *testing.T) {
    dir := t.TempDir()
    s := NewSessionStore(dir)
    s.Put("ax-cl-1234", "uuid-aaaa-bbbb")
    if got := s.Get("ax-cl-1234"); got != "uuid-aaaa-bbbb" {
        t.Errorf("Get = %q, want %q", got, "uuid-aaaa-bbbb")
    }
}

func TestSessionStore_Persistence(t *testing.T) {
    dir := t.TempDir()
    s1 := NewSessionStore(dir)
    s1.Put("ax-cl-5678", "uuid-cccc-dddd")

    s2 := NewSessionStore(dir)
    if got := s2.Get("ax-cl-5678"); got != "uuid-cccc-dddd" {
        t.Errorf("reloaded Get = %q, want %q", got, "uuid-cccc-dddd")
    }
}

func TestSessionStore_MissingKey(t *testing.T) {
    s := NewSessionStore(t.TempDir())
    if got := s.Get("nonexistent"); got != "" {
        t.Errorf("missing key = %q, want empty", got)
    }
}

func TestSessionStore_FilePermissions(t *testing.T) {
    dir := t.TempDir()
    s := NewSessionStore(dir)
    s.Put("test", "val")
    info, err := os.Stat(filepath.Join(dir, "remote-sessions.json"))
    if err != nil {
        t.Fatal(err)
    }
    if perm := info.Mode().Perm(); perm != 0o600 {
        t.Errorf("file perm = %o, want 0600", perm)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/controller/ -run TestSessionStore -v`
Expected: FAIL — `NewSessionStore` undefined

- [ ] **Step 3: Implement session store**

Copy `remoteSessionStore` from `internal/frontend/tui/remote_sessions.go` to `internal/controller/session_store.go`. Rename the type to `SessionStore` (exported). Remove the `aimuxConfigDir()` helper (caller passes the dir). Keep the same logic: JSON file, mutex, load on init, save on Put.

```go
// internal/controller/session_store.go
package controller

import (
    "encoding/json"
    "os"
    "path/filepath"
    "sync"

    "github.com/zanetworker/aimux/internal/debuglog"
)

type SessionStore struct {
    mu   sync.Mutex
    path string
    data map[string]string
}

func NewSessionStore(configDir string) *SessionStore {
    path := filepath.Join(configDir, "remote-sessions.json")
    s := &SessionStore{path: path, data: make(map[string]string)}
    s.load()
    return s
}

func (s *SessionStore) Get(sandboxName string) string {
    s.mu.Lock()
    defer s.mu.Unlock()
    return s.data[sandboxName]
}

func (s *SessionStore) Put(sandboxName, sessionID string) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.data[sandboxName] = sessionID
    s.save()
}

func (s *SessionStore) load() {
    raw, err := os.ReadFile(s.path)
    if err != nil {
        return
    }
    _ = json.Unmarshal(raw, &s.data)
    debuglog.Log("session-store: loaded %d entries from %s", len(s.data), s.path)
}

func (s *SessionStore) save() {
    raw, err := json.Marshal(s.data)
    if err != nil {
        return
    }
    dir := filepath.Dir(s.path)
    _ = os.MkdirAll(dir, 0o700)
    if err := os.WriteFile(s.path, raw, 0o600); err != nil {
        debuglog.Log("session-store: save failed: %v", err)
    }
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/controller/ -run TestSessionStore -v`
Expected: PASS (all four tests)

- [ ] **Step 5: Rewire TUI to use controller.SessionStore**

In `internal/frontend/tui/app.go`:
- Change `remoteSessionIDs *remoteSessionStore` to `remoteSessionIDs *controller.SessionStore`
- Change `newRemoteSessionStore(aimuxConfigDir())` to `controller.NewSessionStore(aimuxConfigDir())`
- All `.Get()` / `.Put()` calls stay identical (same method signatures)

Delete `internal/frontend/tui/remote_sessions.go` (except keep `aimuxConfigDir()` — move it to a one-liner in app.go or use `os.UserHomeDir` inline).

- [ ] **Step 6: Run full test suite**

Run: `go test ./internal/controller/ ./internal/frontend/tui/ -v`
Expected: ALL PASS

- [ ] **Step 7: Commit**

```bash
git add internal/controller/session_store.go internal/controller/session_store_test.go \
       internal/frontend/tui/app.go
git rm internal/frontend/tui/remote_sessions.go
git commit -m "refactor: move session store to shared controller package

Move remoteSessionStore from internal/frontend/tui/ to
internal/controller/SessionStore so web dashboard and CLI can share
the sandbox→UUID mapping."
```

---

### Task 2: Move agent command builder to shared package

**Files:**
- Create: `internal/controller/agent_command.go`
- Create: `internal/controller/agent_command_test.go`
- Modify: `internal/frontend/tui/app.go` (import change)

**Interfaces:**
- Produces: `controller.RemoteAgentCommand(provider, sessionID string, resume bool) string`
- Produces: `controller.UUIDValid(s string) bool`

- [ ] **Step 1: Write the failing test**

Move `TestRemoteAgentCommand` from `internal/frontend/tui/app_test.go` to `internal/controller/agent_command_test.go`. Change package to `controller`.

- [ ] **Step 2: Run test — verify FAIL**

- [ ] **Step 3: Move `remoteAgentCommand` and `uuidValid` from app.go to `internal/controller/agent_command.go`**

Export as `RemoteAgentCommand` and `UUIDValid`. Keep the uuid import.

- [ ] **Step 4: Update TUI app.go to call `controller.RemoteAgentCommand` and `controller.UUIDValid`**

- [ ] **Step 5: Run tests — verify PASS**

Run: `go test ./internal/controller/ ./internal/frontend/tui/`

- [ ] **Step 6: Commit**

---

### Task 3: Move remote trace parser to shared package

**Files:**
- Create: `internal/controller/remote_trace.go`
- Create: `internal/controller/remote_trace_test.go`
- Modify: `internal/frontend/tui/app.go` (replace parserForRemote body)

**Interfaces:**
- Consumes: `otel.Store` (GetByConversation, ConversationIDs, HasData), `otel.FetchSessionTurns`, `otel.FetchSessionReplies`, `otel.SpansToTurns`, `otel.EnrichTurnsWithReplies`
- Produces: `controller.RemoteTraceParser(otelStore, sessionID, sandboxName string) []trace.Turn`

- [ ] **Step 1: Write the failing test**

```go
// internal/controller/remote_trace_test.go
package controller

import "testing"

func TestRemoteTraceParser_NilStore_FallsBackToSessionFile(t *testing.T) {
    // With nil OTEL store and a valid sandbox+session,
    // it should attempt FetchSessionTurns (returns nil for non-existent sandbox).
    turns := RemoteTraceParser(nil, "fake-uuid", "fake-sandbox")
    if turns != nil {
        t.Errorf("expected nil for non-existent sandbox, got %d turns", len(turns))
    }
}
```

- [ ] **Step 2: Run test — verify FAIL**

- [ ] **Step 3: Extract `parserForRemote` logic from app.go into `controller.RemoteTraceParser`**

The function takes an OTEL store interface (or nil), sessionID, and sandboxName. Returns `[]trace.Turn`. Same logic as the TUI's `parserForRemote` closure body, but as a standalone function callable by any frontend.

Define a minimal interface for the OTEL store dependency:
```go
type OTELLookup interface {
    HasData() bool
    GetByConversation(id string) *otel.Span
    ConversationIDs() []string
}
```

- [ ] **Step 4: Rewire TUI's parserForRemote to delegate to `controller.RemoteTraceParser`**

The TUI's `parserForRemote` becomes a thin wrapper that returns a `views.TraceParser` closure calling the shared function.

- [ ] **Step 5: Run tests — verify PASS**

Run: `go test ./internal/controller/ ./internal/frontend/tui/`

- [ ] **Step 6: Commit**

---

### Task 4: Wire web dashboard trace handler for remote agents

**Files:**
- Modify: `internal/frontend/web/server.go` (add session store + OTEL store deps)
- Modify: `internal/frontend/web/handlers.go` (handleGetTrace remote fallback)

**Interfaces:**
- Consumes: `controller.SessionStore`, `controller.RemoteTraceParser`

- [ ] **Step 1: Add SessionStore and OTELLookup to web Server struct**

```go
// server.go
sessionStore *controller.SessionStore
otelStore    controller.OTELLookup
```

Add setter methods: `SetSessionStore`, `SetOTELStore`.

- [ ] **Step 2: Modify handleGetTrace to handle remote agents**

When the matched agent has `Location == "remote"` and no `SessionFile`, look up the UUID from the session store and call `controller.RemoteTraceParser`:

```go
if a.Location == "remote" && sessionFile == "" {
    sandboxName := a.SandboxName
    if sandboxName == "" { sandboxName = a.Name }
    sid := a.SessionID
    if !controller.UUIDValid(sid) {
        if mapped := s.sessionStore.Get(sandboxName); mapped != "" {
            sid = mapped
        }
    }
    turns := controller.RemoteTraceParser(s.otelStore, sid, sandboxName)
    // encode and return turns...
}
```

- [ ] **Step 3: Wire in cmd/aimux/main.go — pass session store and OTEL store to web server**

The main.go already creates the OTEL store and session store (after Task 1 moves it). Pass them to the web server via the new setter methods.

- [ ] **Step 4: Run tests — verify PASS**

Run: `go test ./internal/frontend/web/ ./internal/frontend/tui/ ./internal/controller/`

- [ ] **Step 5: Verify manually**

Start aimux with `--web`, launch a remote sandbox, navigate to the web dashboard, click the remote agent — trace data should appear (not "agent not found").

- [ ] **Step 6: Commit**

---

### Task 5: Replace web terminal tmux with OpenShellExecBackend

**Files:**
- Modify: `internal/frontend/web/terminal.go`

**Interfaces:**
- Consumes: `terminal.NewOpenShellExec(sandbox, gateway string, insecure bool, cols, rows int)`

- [ ] **Step 1: Add a handleTerminalSandbox handler**

For remote agents, instead of `tmux attach-session`, open an `OpenShellExecBackend` and bridge it to the WebSocket (same `servePTY` pattern but with the PTY from `NewOpenShellExec` instead of `pty.Start(tmux attach)`).

```go
func (s *Server) handleTerminalSandbox(w http.ResponseWriter, r *http.Request) {
    sandboxName := r.PathValue("sandbox")
    cols, rows := parseTermSize(r) // from query params, default 120x40

    backend, err := terminal.NewOpenShellExec(sandboxName, "", false, cols, rows)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }

    conn, err := upgrader.Upgrade(w, r, nil)
    if err != nil {
        _ = backend.Close()
        return
    }
    defer func() { _ = conn.Close(); _ = backend.Close() }()

    // Bridge: backend.Read → WebSocket, WebSocket → backend.Write
    // Reuse servePTY pattern but with backend as the io source
    servePTYBackend(conn, backend)
}
```

- [ ] **Step 2: Register the route**

In `server.go`, add: `mux.HandleFunc("GET /api/terminal/sandbox/{sandbox}", s.handleTerminalSandbox)`

- [ ] **Step 3: Write servePTYBackend**

Similar to `servePTY` but takes a `terminal.SessionBackend` instead of `*exec.Cmd`. The backend already has Read/Write/Resize/Close — map them to the WebSocket the same way.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/frontend/web/`

- [ ] **Step 5: Verify manually**

From the web dashboard, click into a remote sandbox terminal. It should connect via the PTY backend (not tmux) and accept typing.

- [ ] **Step 6: Commit**

---

### Task 6: Wire session-id pinning in web launch path

**Files:**
- Modify: `internal/frontend/web/handlers.go` (launch response includes session UUID)
- Modify: `internal/frontend/web/terminal.go` (handleTerminalResume uses pinned UUID for remote)

**Interfaces:**
- Consumes: `controller.SessionStore`, `controller.RemoteAgentCommand`

- [ ] **Step 1: On web launch of remote sandbox, store UUID in session store**

In the launch handler, after `s.launchFn(opts)` returns a `LaunchResult` with `OTELSessionID`, call `s.sessionStore.Put(result.SandboxName, result.OTELSessionID)`.

- [ ] **Step 2: In handleTerminalResume, for remote agents use `RemoteAgentCommand`**

When resuming a remote agent, build the command via `controller.RemoteAgentCommand(provider, sessionID, true)` instead of the hardcoded `--resume` switch block (lines 93-126). This gives the same `--session-id`/`--resume` behavior the TUI has.

- [ ] **Step 3: Run tests**

Run: `go test ./internal/frontend/web/`

- [ ] **Step 4: Commit**

---

### Task 7: Add KillSandbox to web and CLI kill handlers

**Files:**
- Modify: `internal/frontend/web/handlers.go` (handleKill switch)
- Modify: `cmd/aimux/cmd/kill.go` (kill switch)

**Interfaces:**
- Consumes: `controller.ExecuteKillSandbox(action, composeEngine)`

Both the web dashboard and CLI handle `KillProcess`, `KillPod`, and
`KillRemoveOnly` in their kill switch but silently skip `KillSandbox`.
When a user tries to delete a remote sandbox from the web UI or CLI,
nothing happens.

- [ ] **Step 1: Add KillSandbox case to web handleKill**

In `handlers.go`, find the kill switch (around line 936). Add:
```go
case controller.KillSandbox:
    if s.composeEngine != nil {
        if err := controller.ExecuteKillSandbox(action, s.composeEngine); err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError)
            return
        }
    }
```

Add `composeEngine` field + setter to web Server struct if not already there.

- [ ] **Step 2: Add KillSandbox case to CLI kill.go**

In `cmd/aimux/cmd/kill.go`, find the kill switch (around line 50). Add
the same `controller.ExecuteKillSandbox` call. The compose engine is
available from the parent command's closure in main.go.

- [ ] **Step 3: Run tests**

Run: `go test ./internal/frontend/web/ ./cmd/aimux/...`

- [ ] **Step 4: Verify manually**

From the web dashboard, select a remote sandbox, press kill/delete.
The sandbox should be deleted (not silently ignored).

- [ ] **Step 5: Commit**

---

### Task 8: Verify full parity and clean up

**Files:**
- Modify: `internal/frontend/tui/app.go` (remove any remaining duplicated helpers)

- [ ] **Step 1: Grep for duplicated logic**

```bash
grep -rn "remoteAgentCommand\|uuidValid\|aimuxConfigDir\|remoteSessionStore" internal/frontend/tui/ --include="*.go"
```

Any hits that are NOT import/call sites are duplication to remove.

- [ ] **Step 2: Run full test suite**

```bash
go test ./...
go vet ./...
```

- [ ] **Step 3: Build and verify binary**

```bash
go build -o ~/go/bin/aimux ./cmd/aimux
```

- [ ] **Step 4: Manual verification matrix**

| Test | TUI | Web | Expected |
|------|-----|-----|----------|
| Launch remote sandbox | aimux TUI → remote launch | Web dashboard → new:launch → remote | Sandbox created, terminal interactive |
| Type in terminal | Type in TUI pane | Type in web xterm.js | Keystrokes reach sandbox shell |
| View trace data | TUI trace pane shows turns + replies | Web /api/agents/{id}/trace returns turns + replies | Both show same data |
| Re-enter after exit | TUI Ctrl+], re-select agent | Web: navigate away, navigate back | Conversation resumes, traces persist |
| Restart aimux | Quit aimux, relaunch, click agent | Restart web server, navigate to agent | Session UUID recovered from disk, traces visible |
| Kill remote sandbox | TUI: x → y on remote agent | Web: kill button on remote agent | Sandbox deleted, agent removed from list |

- [ ] **Step 5: Commit**

```bash
git commit -m "chore: verify frontend parity, remove remaining duplication"
```

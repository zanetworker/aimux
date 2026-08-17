# OTEL Remote Traces: Current Status and Next Steps

> **Date:** 2026-06-21
> **Branch:** `feat/remote-agents-openshell`
> **Session:** Two-day integration of OpenShell with aimux
> **Status:** OTEL spans arrive at aimux receiver but trace pane shows "No trace entries found"

## What Works

Everything except OTEL trace display in the trace pane:

- TUI launcher shows `local | container | remote`
- Sandbox creation with `--provider claude` (API credentials injected)
- Claude Code starts inside sandbox (auto-start detection via tmux pane check)
- Split view with tmux mirror of remote session
- OpenShell badge in header (connected/disconnected)
- OpenShell in health view (gateway status, sandbox count)
- Remote sandboxes in agent list (LOC=remote, discovery via `openshell sandbox list`)
- Delete cleans up tmux + sandbox (`x` key)
- Headless MCP task workers (exec-based)
- K8s backend (Redis + Deployments, tested on real cluster)
- OTEL receiver binds `0.0.0.0:4318` (reachable from sandbox)
- OTEL env vars in sandbox: `CLAUDE_CODE_ENABLE_TELEMETRY=1`, all OTEL endpoint vars
- Sandbox policy updated to allow `host.openshell.internal:4318` for Node.js
- Trace pane shows `TRACE [OTEL]` header

## What Doesn't Work

The trace pane shows "No trace entries found for this session" despite OTEL spans arriving.

### The Pipeline (verified each step)

```
Step 1: Sandbox env vars ✅
  CLAUDE_CODE_ENABLE_TELEMETRY=1
  OTEL_EXPORTER_OTLP_ENDPOINT=http://host.openshell.internal:4318
  OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
  OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://host.openshell.internal:4318/v1/logs
  OTEL_EXPORTER_OTLP_LOGS_PROTOCOL=http/protobuf
  OTEL_LOGS_EXPORTER=otlp
  OTEL_LOG_USER_PROMPTS=1
  OTEL_LOG_TOOL_DETAILS=1
  OTEL_RESOURCE_ATTRIBUTES=aimux.session_id=aimux-remote-claude-<timestamp>

  Verified via: openshell sandbox exec -- bash -c 'env | grep OTEL'
  Also verified in Claude's Node.js process via /proc/<pid>/environ

Step 2: Policy allows traffic ✅
  openshell policy update <sandbox> \
    --add-endpoint host.openshell.internal:4318:read-write:rest:enforce \
    --binary /usr/bin/node --binary /usr/local/bin/node \
    --add-allow host.openshell.internal:4318:POST:/** \
    --wait

  Verified via: curl from sandbox returns HTTP 200 (after policy loaded)
  Verified via: Node.js fetch from sandbox returns HTTP 400 (reached receiver, bad protobuf)

  GOTCHA: binary path matters. Sandbox has node at /usr/bin/node, not /usr/local/bin/node.
  GOTCHA: --wait is required. Without it, policy submitted but not loaded by supervisor.

Step 3: Spans arrive at receiver ✅
  curl http://localhost:4318/debug shows:
    traces: 0, logs: 14, other: 0
    store entries: 2
    store conversations: 4f95a524-..., bfd10065-...

  The sandbox Claude's conversation ID is 4f95a524-... (gen_ai.conversation.id).
  Our injected aimux.session_id is aimux-remote-claude-<timestamp>.

Step 4: Store indexes spans ✅
  store.go:134 indexes by gen_ai.conversation.id (Claude's own session ID)
  store.go:136 falls back to aimux.session_id
  We added aliasing: when both IDs exist, store creates an alias so
  GetByConversation("aimux-remote-claude-...") returns the same root as
  GetByConversation("4f95a524-...")

Step 5: Trace pane matches ❌ THIS IS WHERE IT BREAKS
  parserForRemote(otelSessionID) calls:
    a.otelStore.GetByConversation(otelSessionID)

  otelSessionID = "aimux-remote-claude-<timestamp>" (from LaunchResult.OTELSessionID)

  The alias should make this work, but the trace pane still shows empty.
```

### Debugging Hypotheses (not yet tested)

1. **Timing: alias created after parser lookup**
   The alias is created in `store.Add()` when a span with both `gen_ai.conversation.id` AND `aimux.session_id` arrives. But the parser runs on every tick. If the first span doesn't have `aimux.session_id` as a span attribute (only as a resource attribute), the alias is never created.

   Check: Add logging to `store.Add()` to see if `aimux.session_id` is actually in `span.Attrs` after resource attribute copying.

2. **Resource attributes not copied to log records**
   The receiver's `logRecordToSpan()` at receiver.go:328-335 copies resource attrs to span attrs. But `OTEL_RESOURCE_ATTRIBUTES` may not be forwarded by Claude Code's OTEL SDK. Claude Code might use its own resource configuration that doesn't include our env var.

   Check: Log the raw protobuf to see if `aimux.session_id` appears in the resource attributes of incoming log records.

3. **SpansToTurns returns empty for log-based data**
   Claude Code sends OTEL via `/v1/logs` (not `/v1/traces`). The `SpansToTurns()` function may not handle log-based spans correctly.

   Check: Inspect the root span's children after logs arrive. See if SpansToTurns produces turns from log data.

4. **Parser closure captures stale state**
   `parserForRemote` is a closure that captures `a.otelStore` and `otelSessionID` at creation time. If the store is replaced or the app state changes, the closure may reference stale data.

   Check: Add logging inside the parser closure to see if it's called and what it finds.

### Recommended Next Steps

1. **Add debug logging to parserForRemote:**
   ```go
   func (a App) parserForRemote(otelSessionID string) views.TraceParser {
       return func(_ string) ([]trace.Turn, error) {
           debuglog.Log("parserForRemote: looking for %q, store has %d convs: %v",
               otelSessionID, len(a.otelStore.ConversationIDs()), a.otelStore.ConversationIDs())
           // ... existing code
       }
   }
   ```

2. **Add debug logging to store.Add for aimux.session_id:**
   ```go
   aimuxID := span.AttrStr("aimux.session_id")
   if aimuxID != "" {
       debuglog.Log("store: span has aimux.session_id=%s, convID=%s", aimuxID, convID)
   }
   ```

3. **Check the raw incoming data:**
   Add a temporary log in `handleLogs` that dumps resource attributes:
   ```go
   for k, v := range resourceAttrs {
       if strings.Contains(k, "aimux") {
           debuglog.Log("receiver: resource attr %s=%v", k, v)
       }
   }
   ```

4. **Test SpansToTurns directly:**
   After spans arrive, check if `SpansToTurns` produces turns:
   ```go
   for _, id := range store.ConversationIDs() {
       root := store.GetByConversation(id)
       turns := SpansToTurns(root)
       debuglog.Log("conversation %s: root=%v children=%d turns=%d", id, root != nil, len(root.Children), len(turns))
   }
   ```

## Architecture Reference

### OTEL Data Flow

```
Claude Code (sandbox)
  │
  │ OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://host.openshell.internal:4318/v1/logs
  │ CLAUDE_CODE_ENABLE_TELEMETRY=1
  │ OTEL_RESOURCE_ATTRIBUTES=aimux.session_id=aimux-remote-claude-<ts>
  │
  ├─ Node.js OTEL SDK sends protobuf to /v1/logs
  │  (goes through OpenShell egress proxy, allowed by policy)
  │
  ▼
host.openshell.internal:4318 (resolves to 192.168.127.254 on macOS podman)
  │
  │ aimux OTEL receiver (binds 0.0.0.0:4318 when remote backend configured)
  │
  ▼
receiver.go:handleLogs()
  │
  ├─ Extracts resource attributes (including aimux.session_id if present)
  ├─ Copies resource attrs to span attrs (logRecordToSpan line 330-332)
  ├─ Extracts gen_ai.conversation.id from span attrs
  ├─ Indexes by gen_ai.conversation.id
  ├─ Creates alias for aimux.session_id (store.go new code)
  │
  ▼
store.byConversation["4f95a524-..."] = root span
store.byConversation["aimux-remote-claude-<ts>"] = same root span (alias)
  │
  ▼
parserForRemote("aimux-remote-claude-<ts>")
  │
  ├─ Calls store.GetByConversation("aimux-remote-claude-<ts>")
  ├─ Gets root span (via alias)
  ├─ Calls SpansToTurns(root)
  ├─ Returns turns to trace pane
  │
  ▼
Trace pane displays turns (SHOULD work, currently empty)
```

### Key Files

| File | What it does |
|------|-------------|
| `internal/spawn/sandbox.go` | LaunchInSandbox: creates sandbox, injects env, updates policy, connects tmux, detects auto-start |
| `internal/spawn/sandbox.go:otelSandboxEnv()` | Builds all 11 OTEL env vars including CLAUDE_CODE_ENABLE_TELEMETRY and aimux.session_id |
| `internal/spawn/sandbox.go:allowOTELEndpoint()` | Runs `openshell policy update` with --wait to allow traffic |
| `internal/otel/receiver.go` | OTLP/HTTP receiver, binds 0.0.0.0 when remote configured |
| `internal/otel/receiver.go:handleLogs()` | Processes Claude Code's /v1/logs data, copies resource attrs |
| `internal/otel/receiver.go:logRecordToSpan()` | Converts log record to Span, copies resource attrs to span attrs |
| `internal/otel/store.go:Add()` | Indexes by gen_ai.conversation.id, creates aimux.session_id alias |
| `internal/frontend/tui/app.go:parserForRemote()` | Looks up spans by aimux.session_id, converts to turns |
| `internal/frontend/tui/app.go` line ~714 | Remote launch handler, creates trace pane with parserForRemote |

### Key Config

```yaml
# ~/.aimux/config.yaml
remote:
  backend: openshell
  gateway: "http://127.0.0.1:8090"

otel:
  enabled: true
  port: 4318
```

### Gateway Setup

Gateway runs natively (not containerized) on macOS:
```bash
OPENSHELL_PODMAN_SOCKET=$(podman machine inspect --format '{{.ConnectionInfo.PodmanSocket.Path}}') \
  openshell-gateway \
  --config ~/.config/openshell/gateway-podman.toml \
  --db-url "sqlite:$HOME/.config/openshell/gateway-podman.db?mode=rwc"
```

Provider registered: `openshell provider create --name claude --type claude-code --credential ANTHROPIC_API_KEY=...`

### Commits on Branch

```
51c142e docs: remote agents guide, architecture diagrams, OpenShell feedback
e810660 feat: wire remote agents into TUI, Web API, and header
9d1e5b8 feat: remote agent orchestration via OpenShell and K8s backends
```

### OpenShell Issues to File

1. **BUG-1** (confirmed): `sandbox exec` rejects newline characters in command arguments
2. **BUG-3** (design): Provider auto-start inconsistency on `sandbox connect` (aimux workaround in place)
3. **FR: `--detach`** for sandbox create (aimux has 80-line workaround)
4. **FR: JSON output** for `sandbox list` (aimux has ANSI-stripping parser)
5. **FR: document `host.openshell.internal`** in sandbox networking docs (not just inference routing)
6. **NOT a bug (BUG-2)**: `--env` vars DO reach provider processes. Our test was wrong (`/proc/1/environ` is root-owned, unreadable by sandbox user). Verified via `/proc/<claude-pid>/environ`.

### Learnings About OpenShell

1. `--env` vars are in the container env but NOT visible via `/proc/1/environ` (permission denied, not missing)
2. The egress proxy blocks `host.openshell.internal` by default; need `policy update --add-endpoint` with `--wait`
3. Policy binary paths must match exactly (`/usr/bin/node` vs `/usr/local/bin/node`)
4. `--wait` on policy update is critical; without it the policy is submitted but not loaded
5. `CLAUDE_CODE_ENABLE_TELEMETRY=1` is required to enable Claude Code's OTEL exporter
6. Claude Code sends OTEL via `/v1/logs` (not `/v1/traces`)
7. `openshell sandbox create` blocks after printing name; need background + pipe + poll
8. `openshell sandbox connect` sometimes auto-starts Claude (provider profile), sometimes doesn't
9. Node.js `fetch`/undici goes through the proxy differently than `curl` (may bypass binary policy checks)

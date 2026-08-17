# Remote Agents

aimux runs AI coding agents on remote infrastructure: isolated OpenShell sandboxes or Kubernetes pods. Two use cases, one codebase.

**Interactive sessions**: A human picks "remote" in the TUI or web launcher, aimux creates a sandbox, connects via a real PTY (`openshell sandbox connect`), and the user works with Claude/Codex/Gemini inside the sandbox as if it were local.

**Headless task workers**: A lead agent (running locally) dispatches coding tasks to remote workers via MCP tools. Workers execute the task, return structured results, and the lead continues.

## Prerequisites

### OpenShell Gateway (Podman on macOS)

```bash
# Install OpenShell v0.0.96+
gh release download <tag> -R NVIDIA/OpenShell -p '*aarch64-apple-darwin*' -C /tmp
tar -xzf /tmp/openshell-aarch64-apple-darwin.tar.gz -C ~/go/bin/
tar -xzf /tmp/openshell-gateway-aarch64-apple-darwin.tar.gz -C ~/go/bin/
openshell --version   # 0.0.96+

# Ensure podman machine is running
podman machine start

# Start gateway natively (not in a container)
OPENSHELL_PODMAN_SOCKET=$(podman machine inspect --format '{{.ConnectionInfo.PodmanSocket.Path}}') \
  openshell-gateway \
  --config ~/.config/openshell/gateway-podman.toml \
  --db-url "sqlite:$HOME/.config/openshell/gateway-podman.db?mode=rwc"

# Register and select the gateway
openshell gateway add podman-test http://127.0.0.1:8090
openshell gateway select podman-test
openshell status   # should show Connected
```

### Inference Routing (Required for Claude)

Claude Code inside the sandbox routes model traffic through the gateway's inference proxy (`inference.local`). The gateway authenticates to the model provider using its own credentials, so no API keys need to enter the sandbox.

```bash
# Create a Vertex AI provider from your gcloud ADC
openshell provider create --name vertex --type google-vertex-ai --from-gcloud-adc
openshell provider update vertex \
  --config VERTEX_AI_PROJECT_ID=<your-gcp-project> \
  --config VERTEX_AI_REGION=us-east5

# Set the inference route (both user and system)
openshell inference set --provider vertex --model claude-sonnet-4-6
openshell inference set --provider vertex --model claude-sonnet-4-6 --system

# Verify
openshell inference get   # should show provider + model for both routes
```

Without this, Claude's API calls inside the sandbox fail with `503 "cluster inference is not configured"` and retry indefinitely.

### aimux Configuration

```yaml
# ~/.aimux/config.yaml
remote:
  backend: openshell
  gateway: "http://127.0.0.1:8090"
  image: "ghcr.io/nvidia/openshell-community/sandboxes/base:latest"
  warm_pool: 0  # set >0 to pre-create sandboxes
```

## Two Backends

| | OpenShell | K8s |
|-|-----------|-----|
| **Compute** | NVIDIA OpenShell sandboxes (podman/docker/k8s) | Kubernetes Deployments |
| **Task dispatch** | `exec` into sandbox (synchronous) | Redis queue (async) |
| **State** | In-memory pool + JSONL journal | Redis |
| **Isolation** | Landlock + Seccomp + network namespace | Pod-level |
| **Best for** | Dev, single-machine, podman | Multi-node clusters |

Both implement the same `Backend` interface (5 methods), so all MCP tools and controller functions work identically.

## UX Flow

### Interactive Session (human attaches)

1. **Launcher**: User presses `n` in the TUI (or clicks "Launch Agent" in the web dashboard), picks a provider (Claude/Codex/Gemini), and selects Runtime: **remote**
2. **Sandbox creation**: `compose.Engine.LaunchInSandbox()` provisions the sandbox: creates it via `openshell sandbox create`, injects OTEL env vars, sets egress policy for telemetry, and configures the inference proxy via `--provider vertex`
3. **Terminal connect**: A real PTY is opened via `terminal.NewOpenShellExec()`, which runs `openshell sandbox connect <name>` with a controlling terminal. No tmux is involved
4. **Session pinning**: Claude is started with `--session-id <uuid>` (a UUID generated at sandbox creation time). This pins the OTEL telemetry `session.id` to a stable value so traces persist across reconnects
5. **Split view**: The TUI shows the PTY terminal on the right pane and the OTEL trace viewer on the left. The web dashboard shows the same data via its REST API. Tab switches focus
6. **Agent runs**: Claude Code runs inside the sandbox with model traffic routed through `inference.local` (the gateway's in-sandbox proxy). The sandbox has Landlock + Seccomp isolation, network namespacing, and a dedicated workspace at `/sandbox/`

### Re-entry (reconnecting to a running sandbox)

1. User exits the terminal pane (Ctrl+]) and returns to the agent list
2. Clicking the remote agent opens a fresh `openshell sandbox connect` PTY (the sandbox persists as a gateway resource)
3. Claude is started with `--resume <uuid>` (the pinned UUID from launch), resuming the same conversation and telemetry session
4. The trace pane retains all prior turns and accumulates new ones under the same session ID

### Trace and Reply Enrichment

Claude Code's OTEL telemetry emits `user_prompt`, `api_request`, `tool_result`, and `api_error` events, but **not** the model's reply text. To show replies in the trace pane, aimux reads the sandbox's session JSONL file:

1. `FetchSessionReplies()` runs `openshell sandbox exec -- cat /sandbox/.claude/projects/-sandbox/<uuid>.jsonl`
2. `ParseSessionReplies()` extracts assistant reply text, keyed by the preceding user message's `promptId`
3. `EnrichTurnsWithReplies()` joins replies to OTEL turns, populating `OutputLines`

When the OTEL store is empty (e.g., after aimux restart), `FetchSessionTurns()` builds complete turns directly from the session file as a fallback.

### Session UUID Persistence

Launch metadata is persisted to `~/.aimux/remote-sessions.json` via `controller.SessionStore`. Each entry stores `{session_id, provider, dir}` (`LaunchMeta`) so both TUI and web dashboard can enrich sandbox agents with their real working directory and provider after a restart. The file format is migrated transparently from the older `map[string]string` schema.

### Headless Task Worker (agent dispatches)

**OpenShell path:**
1. Lead agent calls `spawn_agent` via MCP -> creates sandbox, tracked in in-memory pool
2. Lead calls `create_task` with a prompt -> server claims an idle sandbox via `claimIdle()`, execs `sh -c "<prompt>"` inside it, captures stdout + exit code, releases sandbox back to pool
3. Result returned immediately as structured JSON. Journal records state transitions
4. Lead calls `scale_down` when done -> all sandboxes deleted

**K8s path:**
1. Lead calls `spawn_agent` -> scales a Deployment from 0 to 1, polls Redis until agent registers heartbeat
2. Lead calls `create_task` -> pushes task to Redis sorted set, returns task ID
3. Agent pod polls Redis, claims task, runs prompt via `claude-code-sdk`, writes result to Redis
4. Lead calls `wait_for_task` -> polls Redis until status is `completed`/`failed`, returns result
5. Lead calls `scale_down` -> `deploymentNameFromPod()` extracts Deployment name, scales to 0

## Architecture

### Package Layout

```
internal/
  compose/                    Sandbox lifecycle (create, env, policy, delete)
    adapter.go                LaunchInSandbox, KillSandbox, OTEL env injection
    backend.go                Backend interface impl for MCP server pool

  terminal/                   Interactive terminal backends
    openshell.go              OpenShellExecBackend: PTY-based sandbox connect
    kubectl.go                KubectlExecBackend: PTY-based k8s pod exec
    tmux.go                   TmuxSession: tmux mirror for local agents

  controller/                 Shared business logic (all frontends use these)
    session_store.go          Persistent sandbox->UUID mapping
    agent_command.go          RemoteAgentCommand (--session-id / --resume)
    remote_trace.go           RemoteTraceParser (OTEL + session-file fallback)
    kill.go                   DetermineKillAction, ExecuteKillSandbox
    remote.go                 RemoteLaunchSession, RemoteSpawn, RemoteScaleDown

  otel/                       Telemetry
    receiver.go               OTLP HTTP receiver (logs + traces from sandbox)
    converter.go              SpansToTurns (OTEL spans -> trace.Turn)
    session_file.go           ParseSessionReplies, ParseSessionTurns
    session_fetch.go          FetchSessionReplies, FetchSessionTurns (sandbox exec)

  mcpserver/                  MCP server and backends
    backend.go                Backend interface (5 methods)
    server.go                 MCP tool handlers, backend switching
    journal.go                Append-only JSONL task journal
    pool.go                   Warm pool management

  discovery/
    sandbox.go                DiscoverSandboxes() via openshell sandbox list
```

### Frontend Parity

All frontends share the same backend packages. No business logic lives in frontend code.

| Capability | TUI | Web Dashboard | CLI | MCP Server |
|-----------|-----|---------------|-----|------------|
| Launch remote sandbox | compose.LaunchInSandbox | compose.LaunchInSandbox | compose.LaunchInSandbox | compose.Backend |
| Interactive terminal | terminal.OpenShellExecBackend | terminal.OpenShellExecBackend via WebSocket | N/A | N/A |
| Session UUID pinning | controller.SessionStore | controller.SessionStore | N/A | N/A |
| Trace enrichment | controller.RemoteTraceParser | controller.RemoteTraceParser | N/A | N/A |
| Agent command (--session-id/--resume) | controller.RemoteAgentCommand | controller.RemoteAgentCommand | N/A | N/A |
| Kill sandbox | controller.ExecuteKillSandbox | controller.ExecuteKillSandbox | controller.ExecuteKillSandbox | compose.Backend |

### OTEL Trace Forwarding

OTEL env vars are injected via `--env` flags at sandbox creation time. The endpoint is `http://host.openshell.internal:<port>` so traces cross the sandbox network boundary. The session ID (a UUID) is injected via:

- `OTEL_RESOURCE_ATTRIBUTES=aimux.session_id=<uuid>`
- `OTEL_EXPORTER_OTLP_HEADERS=X-Aimux-Session-Id=<uuid>`
- `?aimux_session=<uuid>` query parameter on the logs endpoint

Claude Code is started with `--session-id <uuid>`, which makes its telemetry `session.id` attribute equal the UUID. The OTEL receiver indexes spans by this ID, and the trace pane keys on it.

**Future:** OpenShell RFC 0012 proposes a supervisor-local telemetry relay that would replace the current egress-policy approach. When it ships, aimux would stop injecting OTEL env vars (the supervisor auto-injects them) and stop needing the egress policy hack.

## Sandbox Environment

The sandbox receives these env vars from the compose adapter:

| Variable | Value | Purpose |
|----------|-------|---------|
| `ANTHROPIC_BASE_URL` | `https://inference.local` | Routes model traffic through gateway proxy |
| `ANTHROPIC_API_KEY` | `placeholder` | Required by Claude SDK but not used (gateway authenticates) |
| `CLAUDE_MODEL` | `claude-sonnet-4-6` | Default model |
| `CLAUDE_CODE_MODEL` | `claude-sonnet-4-6` | Default model for Claude Code |
| `CLAUDE_CODE_ENABLE_TELEMETRY` | `1` | Enables OTEL export |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `http/protobuf` | OTEL transport |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `http://host.openshell.internal:4318` | Receiver on host |

The provider profile (`--auto-providers --provider vertex`) additionally injects GCP project, region, and proxy settings. **Do not** inject `CLAUDE_CODE_USE_VERTEX` from the host: that makes Claude bypass `inference.local` and dial Vertex directly, which fails because the host's gcloud ADC file doesn't exist inside the sandbox.

## MCP Tools

| Tool | OpenShell | K8s |
|------|-----------|-----|
| `spawn_agent` | Creates sandbox via CLI | Scales Deployment |
| `create_task` | Exec in sandbox (sync) | Push to Redis (async) |
| `list_agents` | Lists sandboxes | Lists Redis heartbeats |
| `scale_down` | Deletes all sandboxes | Scales Deployment to 0 |
| `list_tasks` | N/A (sync) | Lists Redis tasks |
| `get_task` | N/A (sync) | Gets Redis task hash |
| `get_task_result` | N/A (sync) | Gets Redis full result |
| `wait_for_task` | N/A (sync) | Polls Redis |
| `send_message` | N/A | Redis stream |
| `get_costs` | N/A | Redis cost hashes |
| `cleanup_branches` | GitHub API | GitHub API |

## Discovery

Remote sandboxes appear in the agent list automatically. `DiscoverSandboxes()` runs on each discovery tick and calls `openshell sandbox list` with a 3-second timeout. If the gateway is unreachable, it returns nil without blocking local discovery.

Sandboxes show with:
- **LOC** column: `remote`
- **NAME**: sandbox name (e.g., `ax-cl-d414`)
- **STATUS**: Active (Ready phase), Error, or Idle

## Known Gotchas

| Issue | Cause | Fix |
|-------|-------|-----|
| Claude retries indefinitely (503) | Gateway inference not configured | Run `openshell inference set --provider vertex --model claude-sonnet-4-6` (both user and `--system`) |
| "application_default_credentials.json does not exist" | `CLAUDE_CODE_USE_VERTEX` leaked into sandbox | Do not inject host Vertex env vars; the compose adapter deliberately omits them |
| Sandbox name too long | OpenShell 19-char limit | Compose adapter generates short names (`ax-{2char}-{hex}`) |
| Sandbox delete "signal: killed" | Delete timeout too short | Timeout is 60s (raised from 15s) |
| "No conversation data" in preview | Session UUID not persisted | Fixed: `~/.aimux/remote-sessions.json` persists across restarts |
| Traces show "(no output)" | Claude Code OTEL doesn't emit replies | Fixed: session-file enrichment reads replies from sandbox JSONL |
| Scrambled terminal on re-entry | Full-screen TUI needs redraw after reattach | Resize nudge sends SIGWINCH to force repaint |
| Web dashboard missing "remote" option | LaunchDialog.tsx only listed local/container | Fixed: added 'remote' to options array |
| Web/CLI kill doesn't delete sandbox | handleArchive called killFn(PID=0) — no-op for sandboxes | Fixed: handleArchive uses DetermineKillAction → ExecuteKillSandbox |
| Kill button does nothing visible | Sandbox enters "Deleting" phase but card stayed Active | Fixed: Deleting/Terminating phases map to StatusError + LastAction="Deleting"; Kill button hidden while deleting |
| Web sandbox card shows `ax-cl-xxxx` as name/dir | LaunchMeta stored no provider or dir (old Put API) | Fixed: PutMeta stores provider+dir; enrichRemoteAgents fills Name/WorkingDir from store |
| TUI-launched sandboxes show wrong name in web | TUI used Put instead of PutMeta | Fixed: TUI now calls PutMeta with provider and dir at launch |
| claude --resume on fresh sandbox (no conversation) | autoStart flag lost to React 18 batching | Fixed: useRef captures autoStart at RightPanel mount; backend uses RemoteTraceParser to decide resume vs fresh start |
| Trace/kill/archive 404 for sandbox agents | cachedDiscover returns raw agents without SessionID | Fixed: enrichRemoteAgents runs inside cachedDiscover so every handler gets enriched agents |

## Testing

```bash
# Unit tests (no gateway needed)
go test ./internal/compose/ ./internal/controller/ ./internal/terminal/ \
  ./internal/otel/ ./internal/discovery/ -timeout 30s

# Integration tests (requires running gateway + sandbox)
TEST_SANDBOX=<name> TEST_SESSION_ID=<uuid> \
  go test -tags integration ./internal/otel/ -timeout 180s -v

# Full project
go test ./... -timeout 30s
```

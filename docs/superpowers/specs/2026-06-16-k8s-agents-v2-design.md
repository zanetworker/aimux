# Remote Agents v2 Design

**Goal:** Enable aimux users to spawn and orchestrate AI coding agents on remote infrastructure (or sandboxed locally) with a UX that feels as fast and seamless as running agents directly. Two use cases: interactive sessions (human attaches, pair programs) and headless task workers (lead agent dispatches work via MCP).

**Context:** The MCP server and K8s infrastructure were validated end-to-end on 2026-06-14. An agent pod on the workload cluster registered in Redis, claimed a task, executed it via claude-code-sdk, and returned results. The CLI wiring (`aimux mcp serve`, `aimux mcp register`, auto-register) was built the same session. This spec covers the complete design from UX to infrastructure.

## User Experience

### Launcher

The TUI launcher presents two axes:

```
Provider:  [Claude]  Codex  Gemini
Where:     [my machine]  remote
Sandboxed: [no]  yes
```

**Where** = my machine or somewhere else. Always two options when remote is configured.

**Sandboxed** = whether to wrap the agent in OpenShell isolation (Landlock, Seccomp, egress proxy). Only appears when a local OpenShell gateway is available. Remote is always sandboxed implicitly.

| Where | Sandboxed | What happens |
|---|---|---|
| my machine | no | Agent runs as a raw process. Current aimux behavior. |
| my machine | yes | Agent runs in an OpenShell sandbox on your machine (Docker/Podman via local gateway). |
| remote | (always yes) | Agent runs in an OpenShell sandbox on a K8s cluster (via cluster gateway). |

The launcher adapts to what's available:

```
# Nothing configured (today's aimux)
Provider:  [Claude]  Codex  Gemini
Where:     [my machine]

# Cluster gateway configured
Provider:  [Claude]  Codex  Gemini
Where:     [my machine]  remote

# Both local and cluster gateways configured
Provider:  [Claude]  Codex  Gemini
Where:     [my machine]  remote
Sandboxed: [no]  yes

# Local gateway only (no cluster)
Provider:  [Claude]  Codex  Gemini
Where:     [my machine]
Sandboxed: [no]  yes
```

Zero new complexity for users who don't configure remote or sandbox. The launcher shows exactly what's available.

### MCP Task Workers

The lead agent calls MCP tools. It doesn't know or care about OpenShell, K8s, or Docker. It sees:

```
spawn_agent(count=3)               → "3 agents ready"
create_task(prompt="...", ...)     → structured result
wait_for_task(task_id="abc")       → structured result
scale_down()                       → "scaled to 0"
```

Whether workers run on the user's machine (sandboxed) or on a cluster depends on the gateway URL in config, not the MCP tool interface.

### `aimux status`

```
$ aimux status

Local
  tmux            OK (3.4)
  claude          OK
  codex           OK
  gemini          OK

Remote (openshell)
  Gateway         OK (https://gateway.example.com)
  Warm pool       3 idle, 0 active
  MCP registered  OK
```

Each failure shows the exact fix and a docs link. The remote section only appears when configured. `aimux status --json` for programmatic consumption.

## Architecture

### Two Use Cases, Same Infrastructure

**Interactive sessions.** User picks a provider and "remote" (or "sandboxed") in the launcher. Aimux creates an OpenShell sandbox with `AIMUX_MODE=session`, connects via the supervisor relay, renders the terminal in the TUI. Same pane, same keybindings, same trace viewer as local. Session survives disconnects.

**Task workers.** Lead agent calls MCP tools. MCP server creates OpenShell sandboxes, pushes tasks via `exec_stream`, collects structured results. Workers are headless, disposable. The MCP server orchestrates task dependencies and concurrency in-memory.

Both use the same universal image, same OpenShell gateway, same config. The entrypoint (`AIMUX_MODE`) determines behavior.

### Component Ownership

```
User-facing (aimux owns)
  TUI launcher        "my machine" / "remote" / "sandboxed" options
  aimux status         health checks for gateway, warm pool, MCP registration
  aimux mcp serve      starts the MCP stdio server
  aimux mcp register   wires MCP into Claude Code settings

Coordination (MCP server owns, fully self-contained)
  spawn_agent          pre-create sandboxes, ensure idle capacity
  create_task          push task to a sandbox via exec_stream
  wait_for_task        block until exec_stream completes
  send_message         exec a command in a running sandbox
  scale_down           delete sandboxes
  get_costs            token usage tracking
  Backend interface    OpenShell implementation inside mcpserver package

Infrastructure (user/platform team owns)
  OpenShell gateway    local (Docker/Podman) or cluster (K8s)
  Container images     pre-built universal image or BYO
  Git credentials      PAT, GitHub App, or External Secrets (for repo tasks)
  K8s cluster          only if using remote (gateway handles the rest)
```

### MCP Server Independence

The MCP server is fully self-contained. Backend interface and implementation live inside the mcpserver package:

```
internal/mcpserver/
  server.go              MCP tools, calls Backend interface
  backend.go             Backend interface definition
  backend_openshell.go   OpenShell implementation (CLI/SDK calls to gateway)
  server_test.go         Tests
```

Zero imports from any other aimux package. Two entry points:

| Entry point | Who uses it | Reads config from |
|---|---|---|
| `aimux mcp serve` | aimux users | `~/.aimux/config.yaml` |
| `cmd/mcp/` standalone binary | CI, Codex, Gemini, custom agents | Environment variables |

### How It Works (Task Workers)

```
Lead agent → MCP spawn_agent(count=3)
                   ↓
             MCP server calls gateway.CreateSandbox() x3
             Sandboxes start, supervisors connect back to gateway
             MCP server adds them to idle pool
                   ↓
Lead agent → MCP create_task(prompt="write tests", repo="github.com/org/proj", provider="claude")
                   ↓
             MCP server picks idle sandbox
             Calls gateway.ExecStream(sandbox_id, "run_task.py --provider claude --prompt '...' --repo '...'")
                   ↓
             Sandbox: clones repo, runs claude-code-sdk, commits to task branch, pushes
             stdout streams back structured JSON result
                   ↓
             MCP server returns to lead agent:
             {"type":"branch", "branch":"task-abc123", "commit":"a1b2c3d", "files_changed":3}
```

No Redis. No queue. No Lua. The MCP server pushes tasks to specific sandboxes. Sandboxes don't pull work; they get told what to do.

### How It Works (Interactive Sessions)

```
User → aimux launcher → picks Claude + remote → Enter
                   ↓
         aimux calls gateway.CreateSandbox(AIMUX_MODE=session)
         Sandbox starts, supervisor connects to gateway
                   ↓
         aimux calls gateway.Connect(sandbox_id)
         Supervisor relay streams terminal I/O
                   ↓
         TUI renders terminal pane (RemoteExec backend)
         Same split-pane, trace viewer, keybindings as local
                   ↓
         User presses Esc → detach (sandbox stays alive)
         User selects agent again → reattach via same relay
```

### Clash with Claude's Native Agent Tool

Claude Code's built-in Agent tool spawns local subagents. The MCP tools spawn remote workers. Both "spawn agents."

Solution: every MCP tool description explicitly says "remote." The descriptions guide the LLM:

- Local parallel work (same machine, shared context) → Agent tool
- Remote parallel work (dedicated compute, cross-provider, repo tasks) → MCP tools
- Long-running or expensive tasks (isolate from local session) → MCP tools

## Universal Worker Image

### Design

One image. All providers. No roles. The task payload determines what the worker does.

```dockerfile
FROM --platform=$TARGETPLATFORM node:20-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    git python3 python3-pip ca-certificates curl wget \
    build-essential tmux \
    # OpenShell sandbox requirements
    iproute2 nftables \
    && apt-get clean && rm -rf /var/lib/apt/lists/*

# OpenShell sandbox user (required until NVIDIA/OpenShell#1959 lands)
RUN useradd -m -s /bin/bash -u 1000660000 sandbox

# Agent CLIs (pinned to tested versions)
ARG CLAUDE_CODE_VERSION=2.1.183
ARG CODEX_VERSION=0.141.0
RUN npm install -g \
    @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION} \
    @openai/codex@${CODEX_VERSION}

# No Python SDKs. run_task.py shells out to CLI headless modes:
#   claude -p --output-format stream-json
#   codex exec --json
#   gemini -p --output-format stream-json
# codex-sdk-python is not the official SDK. google-genai is a model API,
# not the Gemini coding harness.

COPY run_task.py /opt/agent/run_task.py
COPY entrypoint.sh /opt/entrypoint.sh
RUN chmod +x /opt/entrypoint.sh /opt/agent/run_task.py

ENV PYTHONUNBUFFERED=1
ENTRYPOINT ["/opt/entrypoint.sh"]
```

**Entrypoint:**

```bash
#!/bin/bash
# Emergency version override (skip if not set)
[ -n "$CLAUDE_VERSION" ] && npm install -g @anthropic-ai/claude-code@${CLAUDE_VERSION}
[ -n "$CODEX_VERSION" ] && npm install -g @openai/codex@${CODEX_VERSION}

case "${AIMUX_MODE:-worker}" in
  worker)   exec python3 /opt/agent/run_task.py "$@" ;;
  session)  exec sleep infinity ;;
esac
```

**`run_task.py`** (called by MCP server via `exec_stream`):

```python
"""Execute a single task. No coordinator, no Redis, no queue.
Called with CLI args, outputs structured JSON to stdout."""

async def main():
    args = parse_args()  # --provider, --prompt, --repo, --task-id

    cwd = None
    if args.repo:
        clone_or_pull(args.repo, "/sandbox")
        create_branch(f"task-{args.task_id}", "/sandbox")
        cwd = "/sandbox"

    if args.provider == "claude":
        result = await run_claude(args.prompt, cwd)
    elif args.provider == "codex":
        result = await run_codex(args.prompt, cwd)
    elif args.provider == "gemini":
        result = await run_gemini(args.prompt)

    if args.repo:
        commit_and_push(f"task-{args.task_id}", args.prompt, "/sandbox")
        print(json.dumps({"type": "branch", "branch": ..., "commit": ..., "files_changed": ...}))
    else:
        print(json.dumps({"type": "text", "full_text": result, "tokens_used": ..., "duration_seconds": ...}))
```

### Image Flavors

| Image | Runtimes | Size | Use when |
|---|---|---|---|
| `agent-worker:latest` | Node 20, Python 3 | ~630MB | Web/JS/Python projects |
| `agent-worker:full` | + Go, Rust, Java | ~2GB | Compiled language projects |

No sudo, no runtime installation at task time. Users pick the flavor or extend the base:

```dockerfile
FROM quay.io/azaalouk/agent-worker:latest
RUN apt-get update && apt-get install -y --no-install-recommends elixir && rm -rf /var/lib/apt/lists/*
```

### Version Pinning

CLI versions are pinned at build time. Weekly CI pipeline:

1. Build with `latest` for both CLIs
2. Run smoke test (create sandbox, run task, verify result)
3. If passes: read actual installed versions (`claude --version`, `codex --version`), pin those exact versions in the Dockerfile, commit
4. Push image with `latest` tag (now a resolved, tested version) + date tag (`2026-06-20`)
5. If fails: keep previous image, alert maintainer

The Dockerfile in the repo always shows exactly which versions are running.

**Emergency override:** Set `CLAUDE_VERSION` or `CODEX_VERSION` env var. Entrypoint upgrades before starting. Adds 30-60s.

### BYO Images

```yaml
remote:
  image: registry.internal.com/my-team/agent-worker:v2
```

Contract: image must have `/opt/agent/run_task.py`, respond to `AIMUX_MODE` env var, and satisfy OpenShell's sandbox requirements: a `sandbox` user (UID 1000660000) and `iproute2`/`nftables` installed. The sandbox user requirement will be removed once [NVIDIA/OpenShell#1959](https://github.com/NVIDIA/OpenShell/issues/1959) lands (the compute driver will inject the UID at runtime).

### Multi-Architecture Builds

ARM Macs can't cross-build reliably with podman (validated during this design session; `--platform` and `--arch` flags only set metadata). Solution:

- GitHub Action on ubuntu amd64 runners, `docker/build-push-action` with `platforms: linux/amd64`
- CI triggers on push to main when `runtime/**` changes
- Local dev: `docker buildx build --platform linux/amd64` (Docker Desktop has QEMU) + push via `skopeo` with podman's quay.io auth

## Warm Pool

One pool of pre-created sandboxes. Universal image, any provider.

```yaml
remote:
  gateway: "https://gateway.example.com"
  warm_pool: 3
  image: quay.io/azaalouk/agent-worker:latest
```

**`spawn_agent` behavior:**

1. Count idle sandboxes in MCP server's in-memory pool
2. If idle >= requested: return immediately (<1s)
3. If idle < requested: call `gateway.CreateSandbox()` for the deficit, wait for supervisor connection, return
4. `create_task` is separate. `spawn_agent` ensures capacity.

On MCP server startup, if `warm_pool > 0`, pre-create that many sandboxes. They idle, waiting for `exec_stream` calls.

**Sizing (from session history):** Typical dispatch: 2-3 concurrent agents. `warm_pool: 3` covers the common case.

**Cold start fallback:** If `warm_pool: 0` or all sandboxes are busy, `spawn_agent` creates on demand. The cold start latency depends on the gateway's compute driver.

## Task Results

All results are structured JSON. Two types:

**Prompt-only (no repo):**

```json
{
  "type": "text",
  "summary": "Library X uses OAuth2 with PKCE...",
  "full_text": "... complete response, no truncation ...",
  "tokens_used": 4200,
  "duration_seconds": 12
}
```

**Repo task:**

```json
{
  "type": "branch",
  "branch": "task-abc123",
  "commit": "a1b2c3d",
  "files_changed": 3,
  "summary": "Added tests for auth.go",
  "tokens_used": 8500,
  "duration_seconds": 45
}
```

Results come from `exec_stream` stdout (the last line of `run_task.py` output). No Redis storage. The MCP server holds results in-memory until the lead agent retrieves them.

## Task Orchestration

The MCP server orchestrates tasks in-memory. No distributed coordination.

**Concurrency:** MCP server maintains a pool of idle sandboxes. `create_task` picks one, calls `exec_stream`, marks it busy. When `exec_stream` returns, sandbox goes back to idle.

**Dependencies:**

```
create_task(id="A", prompt="write tests")
create_task(id="B", prompt="refactor handler", depends_on=["A"])

MCP server:
  1. Push task A to sandbox-1 via exec_stream
  2. Task B queued in-memory, blocked on A
  3. A completes → B becomes eligible
  4. Push task B to sandbox-2 (passing A's result as context)
```

**If MCP server crashes:** Task state is durable via a JSONL task journal (`~/.aimux/tasks.jsonl`). On restart, the journal is replayed to rebuild in-memory state. Sandboxes survive (still running on the gateway). The MCP server reconnects to existing sandboxes via `ListSandboxes`. Incomplete tasks that were mid-execution may need to be re-submitted, but their creation and prompt are preserved in the journal.

## Task Branches (Repo Tasks)

`create_task` accepts an optional `repo` field:

```
create_task(prompt="write tests for auth.go", repo="github.com/org/project", provider="claude")
```

The worker (`run_task.py`) clones the repo into `/sandbox` (PVC-backed, persists across tasks on the same sandbox), creates a `task-{id}` branch, runs the agent against the codebase, commits, and pushes.

**Git credentials:** Workers use standard git credential helpers via `.gitconfig` with per-host env var routing (`GITHUB_TOKEN`, `GITLAB_TOKEN`). Env vars come from the gateway's provider configuration or sandbox env settings.

With the OpenShell backend, the egress proxy can inject git credentials on outbound requests. The worker never sees the token. This is the most secure option.

| Approach | Best for | Setup |
|---|---|---|
| Fine-grained PAT via sandbox env | Solo dev, quick | Set env on sandbox creation |
| GitHub App + token manager | Teams, auto-rotation | GitHub App + operator |
| OpenShell egress proxy injection | Best security | Policy config on gateway |

**PVC caching:** OpenShell K8s driver creates PVC-backed `/sandbox` workspace automatically. First clone persists. Subsequent tasks on the same sandbox do `git fetch` instead of full clone.

## Remote Sessions in TUI

The `RemoteExec` session backend wraps the OpenShell supervisor relay:

```
SessionBackend interface
  DirectPTY      local process (existing)
  TmuxMirror     local tmux mirror (existing)
  RemoteExec     OpenShell supervisor relay (new)
```

The connection goes through the gateway's relay, not kubectl exec or raw SSH. The TUI renders the remote terminal identically to local. Same split-pane, trace viewer, keybindings.

The user never sees `openshell` commands. They pick "remote" in the launcher and get a terminal pane.

## Config

```yaml
# Minimal: just remote workers
remote:
  gateway: "https://gateway.example.com"
  warm_pool: 3
  image: quay.io/azaalouk/agent-worker:latest

# Full config
remote:
  gateway: "https://gateway.example.com"
  warm_pool: 3
  image: quay.io/azaalouk/agent-worker:full
  max_agents: 10
  max_cost_usd: 100

# Local sandboxed (local OpenShell gateway)
remote:
  gateway: "https://127.0.0.1:17670"
  warm_pool: 2
  image: quay.io/azaalouk/agent-worker:latest
```

When `remote` is not configured, aimux works exactly as it does today. No OpenShell dependency. No new launcher options.

## Design Benefits

- **Near-instant spawning.** Warm pool of pre-created sandboxes. `spawn_agent` returns in <1s when idle sandboxes are available.
- **One image, all providers.** Universal image with Claude, Codex, Gemini. Task payload picks the provider. One pool, one number.
- **No Redis, no Lua, no coordinator.** MCP server pushes tasks to sandboxes via `exec_stream`. Orchestration is in-memory Go code. No distributed state to manage.
- **Full isolation when you want it.** OpenShell provides Landlock, Seccomp, egress proxy. Available for both local (sandboxed) and remote. Opt-in, not mandatory.
- **Credential injection.** OpenShell's egress proxy can inject git tokens on outbound requests. Workers never see credentials. Best-in-class security for git operations.
- **PVC workspace persistence.** OpenShell K8s driver auto-creates PVC-backed `/sandbox`. No re-cloning repos between tasks.
- **Backend-agnostic MCP server.** Self-contained, zero aimux imports. Works standalone for CI, Codex, Gemini, or any MCP client.
- **Adaptive UX.** Launcher only shows options that are available. No dead controls, no configuration the user doesn't need.
- **Git credentials are the user's problem.** Multiple approaches documented, aimux doesn't manage them.

## Design Tradeoffs

- **Requires OpenShell gateway for remote/sandboxed.** One more service to run. For local-only users, no impact (they don't configure `remote`).
- **Warm pool costs idle resources.** 3 sandboxes at ~256MB each. Set `warm_pool: 0` for zero idle cost with cold start tradeoff.
- **Universal image is ~630MB.** Smaller than 1.4GB (previous design) but bigger than a per-provider image. The `full` flavor is ~2GB. Tradeoff is simplicity vs size.
- **MCP server is single point of failure.** In-memory task state. If it crashes, task assignments lost (sandboxes survive). Acceptable at current scale.
- **No per-tool-call policy in aimux.** Agent CLIs have their own permission modes. OpenShell has Landlock/Seccomp/OPA at the sandbox level. Aimux has `max_agents` and `max_cost` limits.
- **Two image flavors to maintain.** `latest` (Node+Python) and `full` (+Go/Rust/Java). Users with niche runtimes extend the base.

## What We're NOT Building

- **A2A protocol support.** Standards still settling.
- **Checkpoint/restore.** Git is our checkpoint. Sandbox dies, PVC persists, re-clone is fast.
- **Policy engine in aimux.** Agent CLIs + OpenShell handle this.
- **Local MCP mode.** Claude's native Agent tool handles local subagents.
- **`aimux mcp setup` wizard.** Infrastructure is the user's responsibility.
- **Credential management in aimux.** Workers use git credential helpers. Provisioning is out of scope.
- **Agent Sandbox CRD direct usage.** OpenShell's K8s driver uses them internally; aimux doesn't manage CRDs directly.
- **Redis/coordinator for the new design.** The existing Redis+Lua+coordinator code remains for backward compatibility but is not the path forward.

## Phased Rollout

All phases are the scope. Phasing is execution order.

### Phase 1: Core

| Item | What |
|---|---|
| OpenShell backend in MCP server | `backend_openshell.go`: CreateSandbox, ExecStream, Connect via gateway. Replaces the K8s+Redis backend for new deployments. |
| Warm pool | Pre-create sandboxes on MCP server startup. `spawn_agent` checks idle pool. |
| `aimux status` | New cobra command + `internal/healthcheck/`. Checks gateway, warm pool, MCP registration. |
| Structured results | JSON with `type`, `summary`, `full_text`/`branch`, `tokens_used`, `duration_seconds`. |
| MCP tool descriptions | All tools say "remote." Guides LLM to use Agent tool for local work. |
| Universal image | Dockerfile with Claude+Codex CLIs, Python SDKs, `run_task.py`, entrypoint. |
| Version pinning CI | Weekly build, smoke test, pin resolved versions, push with date tags. |
| amd64 CI | GitHub Action on ubuntu runners, `docker buildx`. |

### Phase 2: Code Output

| Item | What |
|---|---|
| `repo` field on `create_task` | Worker clones, branches, commits, pushes. Result JSON has `type: "branch"`. |
| PVC workspace | OpenShell K8s driver handles PVC creation. Init seeds from image. |
| Git credential docs | PAT, GitHub App, GitLab, External Secrets, OpenShell egress proxy. |
| `full` image flavor | Add Go, Rust, Java to the base image. Separate Dockerfile or build arg. |

### Phase 3: Sessions in TUI

| Item | What |
|---|---|
| Launcher UX | "Where: my machine / remote" and "Sandboxed: no / yes" options. Adaptive visibility. |
| `RemoteExec` backend | New `SessionBackend` wrapping OpenShell supervisor relay. |
| TUI parity | Same split-pane, trace viewer, keybindings as local sessions. |
| Sandboxed local | "my machine + sandboxed" uses local OpenShell gateway. Same UX as remote, runs locally. |

### Phase 4: Cleanup

| Item | What |
|---|---|
| Remove legacy K8s backend | Delete `backend_k8s.go`, Redis connection code, K8s client-go imports from MCP server. |
| Remove coordinator library | `runtime/coordinator/` becomes unused. Keep for reference or delete. |
| Remove per-provider Dockerfiles | `runtime/agents/claude/`, `runtime/agents/gemini/` replaced by universal image. |
| Update deploy/k8s/ | Remove agent Deployments, Redis manifests. Replace with OpenShell gateway deployment docs. |

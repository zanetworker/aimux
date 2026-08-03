# OTEL Remote Traces: Updated Status

> **Date:** 2026-07-22 (updated from 2026-06-29)
> **Branch:** `feat/remote-agents-openshell`
> **Previous status doc:** `2026-06-21-otel-remote-traces-status.md`
> **Status:** Fixed. Compose adapter now updates egress policy after sandbox creation.

## Root Cause (2026-07-22)

Claude Code DOES honor `OTEL_EXPORTER_OTLP_ENDPOINT` (verified: sent 51,980 bytes to a
non-standard port 9999 when the env var was set). Previous sessions incorrectly concluded
it was hardcoded to localhost:4318. The real issue was simpler:

1. **Missing egress policy update**: The compose adapter set OTEL env vars pointing at
   `host.openshell.internal:4318` but never updated the sandbox egress policy to allow
   the traffic. The old `spawn/sandbox.go` had `allowOTELEndpoint()` but it was lost
   during the agent-compose refactor.

2. **Port collision in testing**: The running aimux held `127.0.0.1:4318`, so test
   receivers couldn't bind. This made it look like Claude Code wasn't sending when it was.

## Fix Applied

Added `allowOTELEndpoint()` to `internal/compose/adapter.go`. After sandbox creation,
it runs `openshell policy update` to allow node processes to reach the host OTEL collector.
No forwarder needed.

## To Activate

1. `go build -o aimux ./cmd/aimux && make install`
2. Restart `aimux collect` (kill old PID, start new)
3. Ensure config has `remote.backend: openshell` and `otel.enabled: true`

## What Works

- aimux OTEL receiver pipeline: receiver → store → SpansToTurns → turns (unit tested, E2E test passes)
- Query param session ID (`?aimux_session=<id>`) for proxy-proof session matching
- OpenShell v0.0.66 `--env` vars reach `sandbox connect` sessions
- Universal worker image (`quay.io/azaalouk/agent-worker:latest`) built from OpenShell community base with Claude Code v2.1.185 + Codex v0.141.0
- `inference.local` routing: `claude -p` responds through the gateway proxy (confirmed "pong" response)
- Local Claude Code OTEL data arrives at receiver (all local sessions visible)

## What Doesn't Work

**Sandbox Claude Code OTEL data never reaches the receiver.**

Pipe mode (`claude -p`) exits before the OTEL SDK flushes its batch. Interactive mode requires OAuth which can't complete in a sandbox (no browser).

## Root Causes (confirmed with evidence)

### 1. Claude Code pipe mode doesn't flush OTEL on exit

| Test | OTEL data arrived? |
|------|--------------------|
| Local interactive Claude v2.1.170 | Yes (all sessions visible) |
| Sandbox `claude -p` v2.1.185 | No (process exits before batch flush) |
| Sandbox interactive Claude v2.1.185 | Can't test (requires OAuth) |

### 2. Claude Code interactive mode requires OAuth in sandboxes

Claude Code has two auth modes:
- **Interactive** (`claude`): requires OAuth web login. `ANTHROPIC_API_KEY` alone triggers "Not logged in."
- **Pipe** (`claude -p`): works with `ANTHROPIC_API_KEY` only. No OAuth needed.

Confirmed this is NOT version-specific (v2.1.140 and v2.1.185 both behave the same). It's NOT about key format (`sk-ant-*` vs `openshell:resolve:*` both fail the same way in interactive mode).

### 3. OpenShell provider type gap (issue #896)

- `claude-code` provider type cannot be used for `openshell inference set` (rejected: "unsupported type for cluster inference")
- Must create a separate `anthropic` type provider with the same API key
- Known gap tracked in NVIDIA/OpenShell#896 "Enhanced Provider Management"

### 4. OpenShell credential reference format

- `--provider claude` injects `ANTHROPIC_API_KEY=openshell:resolve:env:...` (credential reference)
- This only works with `inference.local` routing (gateway resolves at proxy layer)
- Direct API calls to `api.anthropic.com` with this value fail (not a valid API key)
- Must set `ANTHROPIC_BASE_URL=https://inference.local` for the gateway to intercept and resolve

## Working Setup (for pipe mode)

```bash
# Prerequisites (one-time):
openshell provider create --name anthropic-inference --type anthropic --credential ANTHROPIC_API_KEY=<key>
openshell inference set --provider anthropic-inference --model claude-sonnet-4-6

# Create sandbox:
openshell sandbox create --provider claude \
  --from quay.io/azaalouk/agent-worker:latest \
  --env ANTHROPIC_BASE_URL=https://inference.local \
  --env CLAUDE_CODE_ENABLE_TELEMETRY=1 \
  --env OTEL_EXPORTER_OTLP_ENDPOINT=http://host.openshell.internal:4318 \
  --env 'OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://host.openshell.internal:4318/v1/logs?aimux_session=<id>' \
  --env OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf \
  --env OTEL_EXPORTER_OTLP_LOGS_PROTOCOL=http/protobuf \
  --env OTEL_LOGS_EXPORTER=otlp \
  --env OTEL_LOG_USER_PROMPTS=1 \
  --env OTEL_LOG_TOOL_DETAILS=1

# Apply OTEL policy:
openshell policy update <sandbox> \
  --add-endpoint 'host.openshell.internal:4318:read-write:rest:enforce' \
  --binary /usr/bin/node \
  --add-allow 'host.openshell.internal:4318:POST:/**' \
  --wait

# Test (pipe mode works, OTEL doesn't flush):
echo "say pong" | openshell sandbox exec --name <sandbox> -- claude -p
# Returns "pong" but no OTEL data reaches receiver
```

## Blockers

| Blocker | Owner | Issue |
|---------|-------|-------|
| OTEL batch not flushed in pipe mode | Claude Code | No known issue filed |
| Interactive mode requires OAuth (no browser in sandbox) | Claude Code / OpenShell | NVIDIA/OpenShell#1925 |
| Two provider types needed (agent + inference) | OpenShell | NVIDIA/OpenShell#896 |

## Next Steps

1. **File issue on Claude Code**: OTEL telemetry should flush before `claude -p` exits
2. **Watch OpenShell #1925**: OAuth support for sandboxes (unblocks interactive mode + OTEL)
3. **Watch OpenShell #896**: Unified provider management (eliminates two-provider workaround)
4. **Alternative**: Use `claude-code-sdk` (Python) instead of CLI for headless tasks; the SDK might handle OTEL differently

## aimux Code Changes (ready, tested)

All receiver pipeline code is implemented and tested. Once sandbox OTEL data arrives, it will work:

- `internal/otel/receiver.go`: `extractResourceAttrs` with query param + header fallback
- `internal/spawn/sandbox.go`: OTEL env vars with `?aimux_session=` in logs endpoint URL
- `internal/otel/receiver_test.go`: E2E test simulating full sandbox → receiver → SpansToTurns chain
- `runtime/agents/universal/Dockerfile`: Multi-arch image from OpenShell community base

## Universal Worker Image

```
quay.io/azaalouk/agent-worker:latest
  Base: ghcr.io/nvidia/openshell-community/sandboxes/base:latest
  Claude Code: v2.1.185 (npm, replaces base image's v2.1.156 standalone binary)
  Codex: v0.141.0
  Arch: linux/amd64 + linux/arm64
  Config: claude.json (onboarding bypass), settings.json (bypass permissions)
```

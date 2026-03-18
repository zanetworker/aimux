# Agent Runtimes

Python runtimes for K8s agent workers and the shared coordinator library.

## Directory Structure

```
runtime/
  coordinator/
    __init__.py
    coordinator.py          # AgentCoordinator class
    pyproject.toml          # Package metadata, deps: redis[asyncio]
    lua/
      claim_task.lua        # Atomic task claiming (Redis Lua script)
    tests/
      test_coordinator.py           # Unit tests (fakeredis)
      test_coordinator_integration.py  # Integration tests (real Redis)
  agents/
    claude/
      Dockerfile            # UBI9 + Claude Code CLI + coordinator
      main.py               # Worker loop: claim task -> claude-code-sdk -> store result
    gemini/
      Dockerfile            # UBI9 + google-genai SDK + coordinator
      main.py               # Worker loop: claim task -> Gemini API -> store result
    codex/                  # (placeholder, not yet implemented)
    session/
      Dockerfile            # Ubuntu + Claude Code CLI + tmux (interactive pod)
      claude-settings.json  # Pre-configured MCP server for brain+arms variant
```

## Coordinator Library

`coordinator.AgentCoordinator` is the shared Redis-backed coordination library used by all agent workers. It handles agent registration, heartbeats, direct and broadcast messaging via Redis Streams, and task lifecycle (create, claim via Lua script, complete/fail). Each worker imports it as `from coordinator import AgentCoordinator`.

```bash
cd runtime/coordinator && pip install -e ".[test]" && pytest
```

## Building Images

All Dockerfiles use the repo root as build context so they can COPY the coordinator library.

### Claude Worker

```bash
docker build -t agent-claude -f runtime/agents/claude/Dockerfile .
docker push quay.io/your-org/agent-claude:latest
```

### Gemini Worker

```bash
docker build -t agent-gemini -f runtime/agents/gemini/Dockerfile .
docker push quay.io/your-org/agent-gemini:latest
```

### Session Pod

```bash
# Requires k8s-agents-mcp binary in repo root for brain+arms variant
docker build -t agent-session -f runtime/agents/session/Dockerfile runtime/agents/session/
docker push quay.io/your-org/agent-session:latest
```

Two variants from the same image:
- **Standalone**: Claude CLI only, no MCP tools. User attaches via `kubectl exec`.
- **Brain+arms**: Claude CLI + bundled `k8s-agents-mcp` server. Pod can spawn its own worker pods via MCP.

## Environment Variables

### Claude Worker (`agents/claude/main.py`)

| Variable | Required | Description |
|----------|----------|-------------|
| `REDIS_URL` | yes | Redis connection string (e.g., `redis://:password@redis:6379`) |
| `TEAM_ID` | yes | Team identifier for Redis key namespace |
| `AGENT_ID` | yes | Unique agent identifier |
| `ROLE` | no | Agent role: `coder`, `reviewer`, `researcher` (default: `coder`) |
| `ALLOWED_TOOLS` | no | Comma-separated Claude Code tools (default: `Read,Grep,Glob`) |
| `ANTHROPIC_API_KEY` | yes | Anthropic API key for Claude Code SDK |

### Gemini Worker (`agents/gemini/main.py`)

| Variable | Required | Description |
|----------|----------|-------------|
| `REDIS_URL` | yes | Redis connection string |
| `TEAM_ID` | yes | Team identifier for Redis key namespace |
| `AGENT_ID` | yes | Unique agent identifier |
| `ROLE` | no | Agent role (default: `researcher`) |
| `MODEL` | no | Gemini model name (default: `gemini-2.0-flash`) |
| `GOOGLE_API_KEY` | yes | Google AI API key |

# Phase 1A: MCP Server OpenShell Backend — COMPLETE

> All 9 tasks implemented and verified. 35+ tests passing.

**Status:** DONE (2026-06-21)

**What was built:**
- `internal/openshell/client.go` — shared CLI adapter (CreateSandbox, DeleteSandbox, Exec, ListSandboxes, Status)
- `internal/mcpserver/backend.go` — Backend interface (CreateSandbox, DeleteSandbox, ListSandboxes, ExecStream, IdleCount)
- `internal/mcpserver/backend_openshell.go` — OpenShell implementation with in-memory pool tracking
- `internal/mcpserver/backend_k8s.go` — K8s+Redis implementation (extracted from server.go)
- `internal/mcpserver/journal.go` — JSONL task journal for durable state across restarts
- `internal/mcpserver/pool.go` — Warm pool (pre-create sandboxes on startup)
- `internal/mcpserver/result.go` — Structured TaskResult (text + branch types)
- `internal/mcpserver/server.go` — Wired with Backend switch, journal, warm pool, remote-oriented tool descriptions

**Review findings addressed during implementation:**
- Both backends operational (K8s stays until OpenShell proven)
- Injectable CommandRunner for all CLI tests (no live binary needed)
- JSONL journal survives MCP server restarts
- Tool descriptions say "remote" not "Kubernetes"
- `spawn_agent` still has `role` parameter (to be removed in next pass)

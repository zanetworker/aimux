# MCP Proxy Per-Server Endpoints Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Serve each proxied MCP server at its own daemon endpoint under its original name, so tool names and permission rules never change, and remove the permission translation layer.

**Architecture:** The daemon registers one MCP server instance per backend at `/mcp/<server>`, all sharing the existing Pool, and reloads endpoints in place through `POST /reload`. Install becomes one atomic `jsonedit` edit of `mcpServers` that swaps moved stdio entries for HTTP entries with the same names; uninstall swaps them back per entry. `aimux mcp proxy edit <server>` changes a moved server's definition safely.

**Tech Stack:** Go, mark3labs/mcp-go (Streamable HTTP), internal/jsonedit, launchd.

**Spec:** `docs/superpowers/specs/2026-11-02-mcp-proxy-per-server-endpoints-design.md`

## Global Constraints

- Tool names exposed by an endpoint are the backend's original tool names, unchanged and untruncated.
- Endpoint path: `/mcp/` + `url.PathEscape(server)`. Entry written by install: `{"type": "http", "url": "http://127.0.0.1:<port>/mcp/<escaped server>"}` under the same server name.
- `/status`, `/status/probe` and `/reload` are loopback-only (`netguard.LocalOnly`); `/status/probe` and `/reload` also require the `X-Aimux-Probe` header (reuse `mcpproxy.ProbeHeader`).
- The daemon never restarts for configuration changes; install and edit call `/reload`. Restart only on crash or binary upgrade.
- `~/.claude.json` and settings files are edited only through `internal/jsonedit`; tests never touch the real home, `~/Library/LaunchAgents` or launchctl.
- No permission handling anywhere in the proxy code after this plan.
- Never print env values, tokens or tool arguments.
- No em dashes in docs or output.

## Review Focus

1. A Claude session connected to `/mcp/slack` while another server is reloaded: its connection and calls keep working (Task 2 test).
2. A server name with characters needing escaping (spaces, slashes, unicode): endpoint path and written URL agree, and the entry name stays the original (Task 1 and Task 3 tests).
3. Install re-run after adding one new stdio server: only that entry changes, byte-for-byte elsewhere, and the daemon reloads without restart (Task 3 test).
4. `edit` with a broken definition (bad command, invalid JSON): nothing saved, the running server untouched (Task 4 test).
5. Uninstall after the owner edited one proxied entry: that entry kept and reported, the others restored to their exact original raw bytes (Task 3 test).

## Task 1: One endpoint per server

**Files:** Modify `internal/mcpproxy/server.go`, `internal/mcpproxy/daemon.go`, `scripts/mcp-proxy-conformance.sh` (proxy run targets `/mcp/everything`); tests in `server_test.go`, `daemon_test.go`, `status_test.go`.

**Interfaces:** Produces `func NewServers(cat *Catalog, pool *Pool) map[string]*server.MCPServer` (one per catalog server, tools under original names; the unregister hook closes that session's per-session backends) and a daemon handler that routes `/mcp/<escaped>` to the matching server. Removes `NewServer`'s combined registration, `ExposedName`-based naming in the server, and the `proxy_status` tool.

- [ ] Failing tests: each server's endpoint lists exactly its tools by original name; a tool call on `/mcp/slack` reaches the slack backend; unknown path → 404; escaped names route; per-session server gets one backend per client session; `/status` unchanged; no `/mcp` combined endpoint.
- [ ] Implement; conformance script points at one endpoint (`everything`), baseline adjusted only if a scenario changes (justify in report).
- [ ] `go test ./internal/mcpproxy/ -race`, conformance exits 0.
- [ ] Commit `feat(mcp-proxy): one endpoint per server, original tool names`.

## Task 2: Reload in place

**Files:** `internal/mcpproxy/daemon.go` (or new `reload.go`), `cmd/aimux/cmd/mcp_proxy.go` (serve wiring); tests.

**Interfaces:** `POST /reload` (LocalOnly + ProbeHeader) re-reads the servers file and catalog and applies the difference: new server → endpoint added; removed → endpoint removed and its backends closed; changed definition → its backends drained (in-flight calls finish) then closed, next call starts the new definition. Returns `{"added":[],"removed":[],"changed":[]}`. A Go helper `ReloadDaemon(port) (ReloadResult, error)` for the CLI.

- [ ] Failing tests: add, remove and change each apply without touching other endpoints; an open session on another endpoint keeps working across a reload; an in-flight call on a changed server completes; missing header → 403; unchanged config → empty result, nothing closed.
- [ ] Implement with the endpoint map behind a lock; no process restart.
- [ ] `go test ./internal/mcpproxy/ -race`.
- [ ] Commit `feat(mcp-proxy): reload servers in place`.

## Task 3: Install, uninstall and status on per-server entries

**Files:** rewrite `internal/mcpproxy/install.go` and its tests; `cmd/aimux/cmd/mcp_proxy_install.go`; delete `internal/mcpproxy/permissions.go` (and tests), `install_local_test.go`, `install_restore_test.go` parts that only cover permissions; keep `jsonedit`, `launchd.go`.

**Interfaces:**
- `Install(skip []string) error`: plan stdio servers not yet moved → refresh their catalog entries → save resolved definitions (with `resolvedByInstall`) → ensure the agent runs (start if absent) → `/reload` → probe exactly those servers → one `jsonedit` edit of `mcpServers` swapping each moved entry for its HTTP entry → write/extend the manifest (`{version, port, servers: {name: {original: raw, written: raw}}}`) → print summary including "moved servers are configured with `aimux mcp proxy edit`". Re-running moves only new servers. Failure before the edit restores the daemon-side files if this run changed a previously working daemon (then `/reload`); nothing in Claude's config changed.
- `Uninstall() error`: one edit; per server, entry JSON-equal to `written` → `original` raw restored, otherwise kept and reported; stop and remove the agent; delete the manifest. No manifest → only remove a leftover agent.
- `InstallStatus`: per-server drift (entry no longer its URL), stdio servers not behind the proxy (with "run install"), "upgrade restart pending" when the running daemon's version differs from the binary.
- `--dry-run` (temp catalog as today), `--skip`. Removed: `--reapply`, `--local-rules`, all permission handling, pending/done states, hybrid restore.

- [ ] Failing tests: install → uninstall gives back `~/.claude.json` byte-identical (fixture with non-canonical formatting); re-run moves only a newly added server and changes only its entry; probe failure → config untouched, daemon files restored; owner-edited entry kept on uninstall; escaped names; drift and not-proxied reporting; dry-run writes nothing; settings files never read or written.
- [ ] Implement; delete the permission code and its tests; full suite.
- [ ] Commit(s) `refactor(mcp-proxy): install swaps entries per server, no permission translation`.

## Task 4: `aimux mcp proxy edit <server>`

**Files:** `cmd/aimux/cmd/mcp_proxy_edit.go` (new), helpers in `internal/mcpproxy`; tests.

**Interfaces:** opens a temp copy of the server's definition (from the servers file) in `$EDITOR` (injectable for tests); on save: valid JSON, command resolves (absolute or on PATH), probe the server with the new definition through the daemon (or a one-off start); only then write the servers file atomically and `/reload`. Any failure leaves the old definition. Unknown or not-moved server → clear error naming `aimux mcp proxy install`.

- [ ] Failing tests: good edit saved and reloaded; invalid JSON, bad command, failing probe → nothing saved; env values never printed; editor exits non-zero → nothing changes.
- [ ] Implement.
- [ ] Commit `feat(mcp-proxy): edit a moved server's definition safely`.

## Task 5: Docs, live check, acceptance

**Files:** `docs-site/src/content/docs/guides/mcp-proxy.mdx` (rewrite around per-server entries: install, re-run for new servers, edit, status, uninstall, reload, the restart trade-off; remove permission sections), `RELEASE_NOTES.md`, `.claude/CLAUDE.md` package map, `scripts/mcp-proxy-live.sh` (probe unchanged; nothing permission-related).

- [ ] Docs build passes; live check still passes on a fake home.
- [ ] Commit `docs: MCP proxy per-server endpoints`.
- [ ] Controller, with the owner: the acceptance check from the spec (two concurrent Claude sessions on proxied endpoints, daemon killed and restarted by launchd, a tool call from each), recorded in the release notes either way.

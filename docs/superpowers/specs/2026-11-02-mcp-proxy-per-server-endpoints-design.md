# MCP proxy: one endpoint per server, original names kept

Updates `docs/superpowers/specs/2026-11-01-mcp-proxy-rollout-design.md`. Not installed on the owner's machine yet, so there is nothing to migrate.

## Intent

The proxy exists so the owner's local MCP servers run once, shared by every Claude Code session, instead of one copy per session. Today it exposes every server behind one `mux` entry, which renames each tool from `mcp__<server>__<tool>` to `mcp__mux__<server>__<tool>`. Claude Code's permission rules match tool names, so the rename forced a permission translation layer: proxied copies of every allow, ask and deny rule, refusals where a rule could not be carried over, ownership records for undo, and a sync step for rules added later. That layer is where nine review rounds found their bugs, and its worst failure is silent: a deny that no longer covers the proxied name.

The owner wants this "simple, efficient, works". Keeping the original tool names removes the translation layer instead of maintaining it.

## Goals

1. Same core value: one shared, lazily started process per server, stopped when idle, health visible.
2. Tool names unchanged, so every existing and future permission rule, in every scope, keeps working with no translation and no sync.
3. Install and uninstall touch only `~/.claude.json`'s `mcpServers`, one entry per server.

## Non-goals

- Plugin MCP servers, project-level servers, remote (http, sse) servers: unchanged, as before.
- Any permission handling in aimux.

## Design

### Daemon

- The daemon serves one MCP endpoint per server at `/mcp/<server>` (the server name path-escaped). Each endpoint is its own MCP server instance whose tools are that server's catalog tools under their original names. All endpoints share the existing Pool, so a backend still starts on its first call, is shared across sessions, and stops when idle. Per-session servers (playwright, chrome-devtools, linkedin) keep one backend per client session.
- The combined `/mcp` endpoint and the `proxy_status` tool are removed (the CLI `aimux mcp proxy status` and `/status` cover health). `/status` and `/status/probe` stay as they are.
- The Pool key is already (server, session) (`internal/mcpproxy/pool.go:49`), so session ids from different endpoints cannot collide.
- The launchd agent, `--only`, health, the stderr scrubbing and the exit watch are unchanged.
- **No restarts for configuration changes.** The daemon adds, replaces and removes endpoints in place. `POST /reload` (loopback only, behind the same custom-header check as `/status/probe`) makes it re-read the servers file and catalog: a new server gets its endpoint, a removed one loses it, and a changed one gets its new definition, with its running backend finishing in-flight calls before it is closed, so the next call starts the new definition. Sessions connected to other servers are not touched. `install` (moving new servers) and `edit` call `/reload`, never a restart. The daemon restarts only when it crashes (launchd brings it back) or when the aimux binary itself is upgraded; install and `status` say when an upgrade restart is pending, so it can happen at a convenient time.

### Install

- Order: refresh the moved servers' catalog, save their resolved `${VAR}` definitions for the daemon, start or restart the daemon, probe exactly the moved servers. Only then edit, so a failing server is refused (or `--skip`ped) before anything in Claude's config changes.
- The edit is one atomic `jsonedit` change of `mcpServers`: every moved entry becomes `{"type": "http", "url": "http://127.0.0.1:<port>/mcp/<server>"}` under the same name. One edit means no half-switched state, so there is no pending/done manifest: the manifest is written once, after the edit succeeds, and records per server the original raw entry and the entry install wrote. Nothing about permissions.
- If preparation changed a previously running daemon and install then fails before the edit, the daemon's previous plist and servers file are put back. Nothing else needs restoring, because Claude's config was not touched.
- Kept: backups, `--dry-run`, `--skip`. Removed: `--reapply` and the delayed re-check (drift is reported by `status`; the fix is uninstall, then install).

### Uninstall

- One atomic edit of `mcpServers`, per server: if the entry still equals what install wrote (compared as JSON), put the original raw entry back; if the owner changed it, keep theirs and report it. Then stop the agent and delete the manifest. No whole-value restore.

### After install: new servers and changed settings

- **New servers.** A stdio server added to `~/.claude.json` after install runs directly until it is moved. Running `aimux mcp proxy install` again moves only the stdio servers not yet moved (same refresh, daemon prepare and probe, then one atomic edit of just those entries) and adds them to the manifest; the daemon reloads in place, so open sessions keep their connections. Servers already moved are left alone. `status` lists stdio servers that are not behind the proxy, with this command.
- **Changing a moved server's settings.** Once moved, a server's `~/.claude.json` entry is only its proxy URL; its command, arguments and env live in `~/.aimux/mcp-servers.json`. Editing `~/.claude.json` therefore no longer changes the server. `aimux mcp proxy edit <server>` opens that server's definition in `$EDITOR` (a temp copy), validates it on save (valid JSON, a command that resolves), probes the server with the new definition, and only then writes it to `~/.aimux/mcp-servers.json` and reloads that one server in place; on any failure the old definition stays. Env values are never printed. The fallback, documented next to it: `uninstall`, edit `~/.claude.json`, `install`.
- **Saying it plainly.** `install`'s summary, `status` and the guide state that moved servers are configured through `aimux mcp proxy edit`, not `~/.claude.json`.

### Status

- Drift is per entry: a moved server whose entry is no longer its proxy URL (a running session saved a stale copy, or the owner edited it) is listed, with "run uninstall, then install" to fix it.
- Claude Code's `/mcp` shows each server's own connection state, as before.

### Removed

The permission translation (`permissions.go` translation, `translatePermissions`, the rule ownership and planned-rule records, the restrictions-before-routing ordering, the server-wide deny or ask refusal, project and managed scope scanning, `--local-rules`, the `mcp__aimux__` reference scan), exposed-name truncation, the tool-name collision check, the combined `/mcp` endpoint and `proxy_status`, `--reapply`, the pending/done manifest states and the hybrid restore. Docs and tests for them go with them.

## Second opinion

Codex reviewed this design (read-only, gpt-5.6-sol). It agreed with the root choice, and its suggestions are folded in: cut `--reapply`, the delayed re-check, the pending/done states (one atomic edit), the hybrid restore and `proxy_status`; keep probe-before-switch and the per-server manifest (a plain backup restore would erase later changes); and add the restart acceptance check.

## Trade-offs

- Each Claude session holds one lightweight HTTP connection per moved server (23) to the daemon instead of one. These are idle sockets, not processes; sessions start a little slower because each entry is initialized and its cached tools listed.
- `/mcp` keeps 23 lines instead of collapsing to one. That is a gain for visibility.
- If the daemon is down, all moved servers fail at once, as before; `/mcp` now shows it per server. Claude Code may not reconnect to a restarted daemon on its own mid-session, so a daemon restart can leave live sessions without these tools until they reconnect or restart.

## Acceptance

Beyond unit tests, one live check before the owner switches: two concurrent Claude Code sessions on proxied endpoints, the daemon killed and restarted by launchd, then a tool call from each session. If sessions do not recover, the docs and `status` say so plainly and install prints it, rather than hiding it.

## Approaches considered

- Keep one `mux` entry and add `sync` plus a SessionStart hook: keeps the translation layer and its silent-failure mode; rejected.
- Map names in a PreToolUse hook or enforce rules in the proxy: a second permission engine that cannot reproduce Claude's scopes or interactive `ask`; rejected.
- One endpoint per server (chosen): the only option that removes the rename.

## Left to the builder

How endpoints are registered (one `server.NewStreamableHTTPServer` per server on a sub-path, or a dispatcher), session-id handling per endpoint, test structure, and the order of code removal.

# MCP proxy rollout: tests, conformance, install, health

`aimux mcp proxy`, its daemon, the owner's Claude Code config.

## Intent

The proxy exists and passes its unit tests, but it has never served the owner's real servers, its conformance check runs only by hand, setting it up means hand-editing `~/.claude.json`, and once it runs, `/mcp` shows one "mux" entry and hides how each server behind it is doing. The owner wants it tested for real, guarded by the official conformance suite on every relevant push, switched on and off with one very reliable command, and its servers' health easy to see. Putting aimux's own session tools behind it also ends the 19-helper index fight: one shared, current `aimux mcp serve` instead of one per session.

## Goals

1. **Real test:** an opt-in check against the owner's actual stdio servers (23 today), through the proxy, that reports by server name only.
2. **Conformance always:** the official suite (`@modelcontextprotocol/conformance`, pinned) runs against the proxy on every push that touches proxy code, locally and in CI, with the known tools-only gaps in the suite's own expected-failures baseline. Any new failure fails the push; a baseline entry that starts passing is reported so the baseline stays honest.
3. **Install / uninstall:** `aimux mcp proxy install` switches the owner's global stdio servers to the proxy; `uninstall` puts back exactly what install changed. Both are safe to run twice, safe while Claude Code sessions are writing the same file, and recoverable by hand from a backup.
4. **Health:** `aimux mcp proxy status`, a `status` endpoint on the daemon, and a `proxy_status` tool available from any session, showing each server's state, tools, last start, last call and last error.

## Non-goals

- Proxying prompts, resources or logging (tools only, as today).
- Project-level `mcpServers` (under `projects` in `~/.claude.json`) and remote (http, sse) servers: left as they are.
- A TUI or dashboard panel for proxy health (later, on top of the status endpoint).

## Approaches

- **Install edits only `mcpServers` and records a manifest (chosen).** Read the file as raw JSON, change only the `mcpServers` key, write atomically, read back and verify, retry when a Claude Code session wrote in between. Uninstall replays the manifest (re-add the removed entries verbatim, remove `mux`) instead of restoring the whole backup, so nothing Claude Code wrote since is lost.
- Restore the backup file on uninstall: simple, but throws away everything Claude Code saved after install (session state, project trust, other settings).
- Leave the file to the owner (today): reliable but manual, which is the problem.

## Design

### Install

Preconditions, all checked before touching anything, each with its own message:
1. The daemon runs under launchd (`~/Library/LaunchAgents/com.aimux.mcp-proxy.plist`, `RunAtLoad`, `KeepAlive`, logs under `~/.aimux/logs/`) and answers its status endpoint. Install creates and loads the agent if missing.
2. Every server to move has a fresh catalog entry (install runs `refresh` for missing ones and refuses servers that fail to list tools, naming them; `--skip <name>` leaves one direct).
3. No `mux` entry exists unless the manifest says install put it there.

Then:
- **Backup:** `~/.claude.json` copied to `~/.aimux/backups/claude.json.<UTC time>` (owner-only); the last 10 are kept.
- **Edit:** decode the top level as `map[string]json.RawMessage`, change only `mcpServers` (remove the moved stdio entries, add `"mux": {"type": "http", "url": "http://127.0.0.1:7381/mcp"}`), keep every other key byte for byte, write a temp file in the same folder with the original mode, rename over. Read back: the other keys must equal what was read and `mcpServers` must be the intended one; if the file changed in between (another writer), redo from a fresh read, up to 5 times, then stop with the file left as the other writer made it and nothing half-done.
- **Manifest:** `~/.aimux/mcp-proxy-install.json` records each moved entry's raw JSON, the `mux` entry, the backup path, the permission rules added and the time. Written before the config edit (as "pending") and marked done after the verify, so an interrupted install is detected and finished or undone by the next `install`/`uninstall`.
- **Permissions:** for every `allow` rule in `~/.claude/settings.json` naming a moved server's tool (`mcp__<server>__<tool>` or `mcp__<server>`), add the proxied name (`mcp__mux__<server>__<tool>`, cut and hashed as the proxy names it). Same atomic write, own backup, recorded in the manifest. Existing rules are never removed.
- **aimux's own tools:** the `aimux` stdio entry (`aimux mcp serve`) moves like any other. Its tools become `mcp__mux__aimux__…`. Install prints where the old `mcp__aimux__` name is referenced in the owner's `~/.claude/` instruction files (grep, names and counts only), without editing them.
- Sessions already running keep their direct servers until they restart; new sessions use the proxy. Install says so.
- `--dry-run` prints the exact changes (names only, never env values) and exits.

### Uninstall

From the manifest: re-add each removed entry verbatim unless an entry with that name exists now (the owner's newer one wins, reported), remove `mux` only if it still equals what install wrote, remove the permission rules install added, then unload and remove the launchd agent. Same atomic write and verify. Without a manifest it changes nothing and says how to restore from a backup by hand. Running it twice is a no-op.

### Health

- The daemon keeps per server: state (`not_started`, `starting`, `running`, `idle_stopped`, `failed`), pid, started at, last call, calls, failures, last error (first line, plus the last 5 stderr lines of a failed start), tool count, catalog age.
- `GET /status` on the daemon (same loopback and Host checks as `/mcp`) returns it as JSON.
- `aimux mcp proxy status` prints a table (and `--json`); it says "not running" or "not installed" plainly. `--probe` also starts each idle server once and lists its tools (a deep check).
- The proxy exposes one tool of its own, `proxy_status` (so `mcp__mux__proxy_status`), returning the same summary as text, so any session can ask.

### Tests

- Unit: install/uninstall against fixture configs: other keys byte-identical, idempotence (install twice, uninstall twice, uninstall without install), a concurrent writer between read and rename (retry, then give up cleanly), interrupted install (pending manifest), owner re-added a server, owner changed `mux`, permission rules mapped with cut names, env values never printed.
- Live (opt-in, `scripts/mcp-proxy-live.sh`): reads the real `~/.claude.json` read-only, runs refresh and serve on a temp port with temp catalog and servers files, then for each stdio server initializes and lists tools through the proxy and reports `ok`/`failed` by name; the only tool it calls is aimux's own read-only `list_sessions` with `limit: 1`. Run by the controller with the owner's approval.
- Conformance: `scripts/mcp-proxy-conformance.sh` pins the suite version, passes `--expected-failures scripts/mcp-proxy-conformance-baseline.yaml` (the documented tools-only gaps and the `-32602` difference) and exits non-zero on any unexpected failure; runs as a pre-push hook (`.pre-commit-config.yaml`, stage `pre-push`, files under `internal/mcpproxy/`, `cmd/aimux/cmd/mcp_proxy*`, the script and baseline) and as a CI job with the same path filter.

## Constraints

- Never print env values, tokens or tool arguments; names and counts only.
- Every file aimux writes is owner-only; every edit of a file it does not own is atomic, verified and backed up.
- No new Go dependencies; the conformance suite runs through `npx` at a pinned version.
- `install` and `uninstall` change the owner's live setup and run only when the owner asks.

## Left to the builder

The status JSON field names, the table layout, the launchd plist details beyond the keys above, how the pending manifest is resumed.

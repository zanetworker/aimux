# MCP proxy: environment parity and `aimux mcp proxy check`

## Intent

The owner moves every Claude Code session to the proxy only when it is proven to behave like the direct stdio servers it replaces. A live test on 2026-11-07 found three servers that the install probe called healthy but that behaved differently under the daemon:

- slack (`podman run -e SLACK_XOXC_TOKEN ...`): every call failed with `KeyError 'SLACK_XOXC_TOKEN'`.
- github (`podman run -e GITHUB_PERSONAL_ACCESS_TOKEN ...`): ran unauthenticated; `get_me` answered with a device-login prompt as a successful result.
- x-mcp: offered 7 tools instead of 11, because its write tools need `X_CONSUMER_KEY` and `X_SECRET_KEY`, read from the environment and never declared in its entry.

One cause: Claude Code starts a stdio server with its own full environment (the owner's shell), while the daemon gives a server only the plist's `PATH` and `HOME` plus the entry's declared `env`. Whatever a server reads from the environment without declaring it is lost. The install probe (`initialize` and `tools/list` under the daemon's environment) cannot see this: both work without a token.

Success: install catches the failure classes found here (pass-through variables, tool lists that change with the environment) and leaves such a server direct; the owner has one command, run before a migration and after every upgrade, that smoke-checks every moved server against a direct launch with canary calls the owner approved. This is evidence of parity, not proof: arbitrary tool behaviour can depend on input, time, external state or context the check does not model (listed under Known gaps).

## Goals

1. Install declares the variables a container command passes through by name, so slack and github work after install with no hand edits.
2. Install detects a server whose tool definitions differ under the daemon, or whose comparison could not run, and leaves it direct, saying why.
3. `aimux mcp proxy check` compares each moved server through the running proxy with a direct launch, by tool definitions and by owner-approved canary calls, and exits non-zero on any difference.

## Non-goals

- Copying the whole shell environment into the daemon (rejected earlier: it puts every secret in the daemon and hides what each server needs).
- Guessing whether a result "looks like" an auth failure. Parity with the direct launch is the signal; no text heuristics.
- Calling any tool the owner did not approve as a canary. `readOnlyHint` is server-supplied metadata, not a guarantee (a "read" can mark messages read, refresh tokens, cost money or open a browser), so nothing is called automatically.

## Anti-goals

- A check that passes while a server is broken (the install probe's failure).
- A check that changes anything: it must be safe to run at any time, including on a schedule.

## Design

### A. Pass-through variables (install)

For a command whose base name (`filepath.Base`) is `podman`, `podman-remote` or `docker`, with a `run` or `exec` subcommand (global options may come first), and whose arguments contain `-e NAME`, `-eNAME`, `--env NAME` or `--env=NAME` (a name with no `=`; `NAME=` is an explicit empty value and stays as written), install treats `NAME` like an entry `env` value `${NAME}`: resolved from the installing shell and written into the moved definition, and listed in install's output ("slack: passes SLACK_XOXC_TOKEN, SLACK_XOXD_TOKEN through from your shell; saved with its definition"). A name unset in the installing shell leaves that server direct with the message "slack passes SLACK_XOXC_TOKEN from the environment, but it is not set here; set it and re-run install, or `--skip slack`". `--env-file`, compose, and wrappers (`sh -c`, `bash -lc`, `env`, `mise exec`, scripts) are not analyzed; install says so per server ("not analyzed: runs through sh -c"). Resolved values are copies: after rotating a credential, re-run install (it re-resolves a moved server's pass-through names and `${VAR}` refs from the shell it runs in) or use `edit`. Uninstall restores the original entry and removes the copies with the rest of the server's definition.

### B. Environment parity probe (install)

Install already starts each server under the daemon's environment to list its tools. It also starts it a second time with the installing shell's environment plus the entry's `env`, which is what Claude Code gives it today, and compares the two tool lists by their full definitions (`ToolsFingerprint`), reporting missing or added names first and other definition changes after. The two launches of one server run one after the other, never at once (singleton locks, fixed ports, browser profiles and OAuth refresh rotation make simultaneous launches collide); different servers still run in parallel within the probe parallelism (4). This detects tool-registration differences only; differences that show only in calls are what `check` is for. A difference leaves the server direct: "x-mcp lists 4 tools fewer under the proxy (delete_tweet, like_tweet, post_tweet, retweet): it reads variables from your shell that its entry does not declare. Add them with `aimux mcp proxy edit x-mcp` after install, or declare them as `${VAR}` in ~/.claude.json and re-run install." Either launch failing or timing out makes the comparison inconclusive, and the server stays direct ("could not compare x-mcp: the direct launch timed out"), reported separately from a known mismatch.

### C. `aimux mcp proxy check [server...]`

For each moved server (all by default):

1. Direct: start it from its original entry in the install manifest, with this shell's environment plus the entry's `env` (Claude Code's way). Proxy: connect to `http://127.0.0.1:<port>/mcp/<server>`.
2. Compare tool-name sets.
3. Calls: only canaries the owner approved, saved in the manifest. `aimux mcp proxy check --add server=tool[:json-args]` adds one after showing the tool's description and annotations and asking for a yes (`--yes` for scripts); `--remove` drops one; `--list` shows them. The first `check` with no canaries offers the seed list used in the 2026-11-07 test (one call per server, e.g. `slack=whoami`, `github=get_me`, `pypi=get_package_info:{"package_name":"requests"}`) for approval as a whole or one by one. A server with no canary gets the tool-definition comparison only, and its line says so.
4. Make each call both ways, one after the other, and compare: error or not (`isError`, JSON-RPC error code), number of content items, and for JSON text the top-level key set. A canary may add an assertion that both sides must meet: `keys` that must be present, or `absent` text that must not appear (e.g. `login/device`). Values and sizes are not compared (live data changes). Nothing of the content is printed. The output calls this a smoke check.
5. Print one line per server: `match`, or the differences ("tools: proxy lacks learnings_list"; "get_me: proxy ok with keys [message], direct ok with keys [login id ...]"). Also the timings: proxy and direct latency per call, so the same command answers "is it slower".

Exit 0 when every server matches, 1 on any difference, 2 when the proxy is not running. `--json` prints the full result. Per-session servers (browsers) are checked like the others; the proxy session the check opens is closed with DELETE so their backend stops.

### Manifest

The install manifest gets a new version: each moved entry keeps its raw original, and adds the pass-through names install resolved (names only), the fingerprint of the tool list it compared, and the approved canaries with their assertions. Values stay in the owner-only servers file.

### Known gaps (reported, not detected)

- Working directory: Claude Code starts a stdio server in the session's folder; the daemon starts it in `/`. A server that reads or writes relative paths behaves differently. Install and `check` print this once; a server known to need its session's folder stays direct (`--skip`).
- No terminal: backends run without a controlling terminal; a server that prompts on a tty fails.
- macOS permissions: protected access is asked for the daemon's binary, not iTerm, and the unsigned binary's grants do not survive an upgrade (App Management is denied without a prompt). Deferred: signed and notarized releases (Developer ID), a signing script for source builds, and a guide section.
- Shared backends: one backend serves every session (except per-session servers), so in-memory account or session state is shared.
- Rotated credentials need a re-run of install or `edit` (above).
- Clients: only Claude Code is tested. Codex keeps its own servers (`~/.codex/config.toml` is not touched); a check against Codex as the client, its timeouts and session handling, is follow-up work.

## Approaches considered

1. Fix the three servers by hand and add docs. Cheapest; the next server with a pass-through or an undeclared variable breaks the same quiet way. Rejected: the owner asked for solid.
4. A thin stdio launcher per session that forwards each session's environment and folder to the proxy (Codex's suggestion for closest parity). Rejected for now: backends would be keyed by context, so most sharing (the proxy's point) goes; revisit if the cwd gap bites.
2. Pass the daemon the owner's full login-shell environment. Fixes every case at once; rejected before (secrets in the daemon, no record of what a server needs, a shell change silently changes servers).
3. Declare what can be seen (A), detect what cannot (B), and give a repeatable proof (C). Chosen: each failure class found today is either fixed at install or stops the move, and C is the gate the owner runs before a migration and after an upgrade.

## Constraints

- No values of variables in output, logs or test fixtures; names and set/unset only.
- The manifest stays the record of what install did; probe calls added with `--call` live there.
- The check calls only approved canaries and changes no file except the manifest's canary list.
- Same Go packages and patterns as today (`internal/mcpproxy`, `cmd/aimux/cmd/mcp_proxy*.go`); standard library plus the existing mcp-go dependency.

## Left to the builder

- Output wording beyond the messages above, and the JSON field names.
- How the parity probe shares code with the existing probe (`ProbeServerDirect`).

## Review

Codex (gpt-5.6-sol) reviewed the first draft on 2026-11-07: it agreed the design is a useful regression detector and rejected the parity guarantee, automatic calls on `readOnlyHint`, names-only comparison, simultaneous launches and treating a failed reference launch as fine. Each is addressed above.

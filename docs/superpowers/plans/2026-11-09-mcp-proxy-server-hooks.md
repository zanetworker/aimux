# MCP Proxy Server Hooks Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `aimux mcp proxy install` adds two Claude Code hooks: one moves a server added in a session behind the proxy and reports it, the other names servers still direct when a session starts.

**Architecture:** The settings.json hook editing that `internal/waiting` does for the remote hooks moves into a new package `internal/claudehooks`, parameterised by a spec (event, matcher, command suffix, timeout), so both hook sets share one tested merge, backup and uninstall. `internal/mcpproxy` gains the decisions (is this command a server add, which servers are project-scoped, the messages) and installs the hooks the way it already sets the idle timeout (`ensureIdleTimeout`, recorded in the manifest). A hidden `aimux mcp proxy hooks added|check` command reads the hook input and answers.

**Tech Stack:** Go, cobra, `internal/jsonedit`.

**Spec:** `docs/superpowers/specs/2026-11-09-mcp-proxy-server-hooks-design.md`

<!-- no-images: the flow figure is in the spec (images/mcp-proxy-server-hooks.png); this plan adds no new structure to draw -->

## Global Constraints

- Hook commands: `<aimux> mcp proxy hooks added || true` (PostToolUse, matcher `Bash`, timeout 120) and `<aimux> mcp proxy hooks check || true` (SessionStart, timeout 10). `<aimux>` from `stableBinary("aimux mcp proxy install")`.
- Add commands that trigger a move: `claude mcp add`, `claude mcp add-json`, `claude mcp add-from-claude-desktop`.
- Only `hooks` (and the existing idle timeout) change in `~/.claude/settings.json`, through `jsonedit.EditKey`, backup `settings.json.aimux-bak-<time>` first, the owner's hooks kept. Removing the proxy's hooks never touches the remote hooks, and the reverse.
- A hook never fails a tool call: errors go into the message, exit code 0.
- Copy, verbatim:
  - Moved: `<names> moved behind the aimux proxy (tools unchanged; it starts on first use). Open sessions keep their direct copy until they restart.`
  - Failed: `<names> stays a direct server: <reason>. Run aimux mcp proxy install after fixing it.`
  - Project-scoped: `<name> was added for this project only; the proxy shares user-scope servers. To share it: claude mcp remove <name>, then claude mcp add --scope user …`
  - Session start: `<N> MCP servers are not behind the aimux proxy: <names>. Run aimux mcp proxy install.` (`1 MCP server is` for one)
- Writing: no em dashes in copy or docs.

## Review Focus

1. A Bash call that is not an add (the common case, every tool call): exits fast and silent; pinned by the no-match benchmark and matcher tests (Task 2).
2. An add whose move fails because a *different* direct server is broken: install is all-or-nothing, so the message must name the failing server and say how to leave it direct (`--skip`); pinned in Task 4.
3. Proxy not installed (no manifest): both hooks stay silent and change nothing; pinned in Task 4.
4. Settings that already hold the remote hooks and the owner's hooks in the same event: proxy install and uninstall leave both alone; pinned in Task 1.
5. A `claude mcp add` that only mentions the words inside a quoted string or a comment (`echo "claude mcp add"`): no move; pinned in Task 2.

## File map

- Create `internal/claudehooks/claudehooks.go` (+ `_test.go`): Spec-driven Install, Uninstall, Status of hook entries in a settings file. Code moved from `internal/waiting/install.go`.
- Modify `internal/waiting/install.go`: keep its exported API (`HookCommand`, `CommandBinary`, `Install`, `Uninstall`, `Status`, `Events`, `Change`, `State`) as thin wrappers over claudehooks with the remote spec; its tests must pass unchanged.
- Create `internal/mcpproxy/hooks.go` (+ `_test.go`): `IsServerAdd`, `ProjectServers`, message functions, `HookSpec`, `Installer.ensureHooks`, `Installer.dropHooks`, `Installer.HookAdded`, `Installer.HookCheck`.
- Modify `internal/mcpproxy/install.go`: `Manifest.Hooks`, `InstallState.Hooks`, `Installer.HookBinary`, calls beside `ensureIdleTimeout` / `dropIdleTimeout`, status line.
- Create `cmd/aimux/cmd/mcp_proxy_hooks.go` (+ `_test.go`): hidden `aimux mcp proxy hooks added|check`.
- Modify `cmd/aimux/cmd/mcp_proxy_install.go`: set `HookBinary`; fix the install help text.
- Docs: `docs-site/src/content/docs/guides/mcp-proxy.mdx`, `RELEASE_NOTES.md` (Unreleased), `.claude/CLAUDE.md` (mcpproxy and claudehooks lines).

### Task 1: `internal/claudehooks`, shared by the remote hooks

**Files:** Create `internal/claudehooks/claudehooks.go`, `internal/claudehooks/claudehooks_test.go`; Modify `internal/waiting/install.go`.

**Interfaces:**
- Produces:
  - `type Hook struct { Event, Matcher, Suffix string; Timeout int }` (Suffix like `" remote hooks notify"`; the command is `shellQuote(binary) + Suffix + " || true"`)
  - `type Spec []Hook`
  - `type Change struct { Added, Updated, Removed []string; Backup string }`
  - `type State struct { Installed, Missing, Commands []string }`
  - `func Command(binary, suffix string) string`
  - `func CommandBinary(command, suffix string) (string, bool)`
  - `func Install(path string, spec Spec, binary string, now time.Time) (Change, error)`
  - `func Uninstall(path string, spec Spec, now time.Time) (Change, error)` (removes only handlers whose command ends in one of spec's suffixes)
  - `func Status(path string, spec Spec) (State, error)`
- `waiting` keeps its API; `waiting.Install(path, command, now)` becomes a wrapper that recovers the binary with `CommandBinary`.

- [ ] **Step 1: Write the failing tests** in `claudehooks_test.go`:
  - `TestInstall_TwoCommandsTwoEvents`: spec `{PostToolUse, "Bash", " mcp proxy hooks added", 120}`, `{SessionStart, "", " mcp proxy hooks check", 10}`; after Install the file has exactly those two groups with commands `/bin/aimux mcp proxy hooks added || true` and `/bin/aimux mcp proxy hooks check || true`; a second Install returns an empty Change and writes nothing (mtime unchanged).
  - `TestUninstall_LeavesOtherSpecsAndOwnerHooks`: settings holding the remote hooks (suffix `" remote hooks notify"`), an owner hook in the same PostToolUse group, and the proxy hooks; `Uninstall(proxySpec)` removes only the proxy handlers; the remote hooks and the owner handler stay byte-identical.
  - `TestInstall_BacksUpFirst`: an existing file gets `settings.json.aimux-bak-<time>` with the old bytes.
- [ ] **Step 2: Run** `go test ./internal/claudehooks/` and expect FAIL (package missing).
- [ ] **Step 3: Move** the object/group/settings/edit/startObject code from `internal/waiting/install.go` into `claudehooks`, replacing the fixed `commandSuffix`, `Events()` and `matcherFor` with the Spec; ours-detection takes the spec's suffixes.
- [ ] **Step 4: Rewire `waiting`** to call claudehooks with its spec (`SetEvent` with `SetMatcher`, then `ClearEvents`, suffix `" remote hooks notify"`, timeout `hookTimeout`).
- [ ] **Step 5: Run** `go test ./internal/claudehooks/ ./internal/waiting/ ./cmd/aimux/cmd/ -run 'Hook|Remote'` and expect PASS with waiting's tests unchanged.
- [ ] **Step 6: Commit** `refactor(hooks): move settings.json hook editing into internal/claudehooks`.

### Task 2: What a hook needs to decide

**Files:** Create `internal/mcpproxy/hooks.go`, `internal/mcpproxy/hooks_test.go`.

**Interfaces:**
- Produces:
  - `func IsServerAdd(command string) bool`
  - `func ProjectServers(claudeConfig, cwd string) ([]string, error)`: sorted stdio server names in `projects[cwd].mcpServers` of `~/.claude.json` and in `<cwd>/.mcp.json`; missing files are not errors.
  - `func MovedMessage(names []string) string`, `func FailedMessage(names []string, reason string) string`, `func ProjectMessage(name string) string`, `func CheckMessage(direct []string) string` (empty for none); exact copy in Global Constraints.

- [ ] **Step 1: Write the failing tests:**
  - `TestIsServerAdd`: true for `claude mcp add foo -- npx foo`, `claude mcp add -s user foo npx foo`, `claude mcp add-json foo '{"command":"x"}'`, `claude mcp add-from-claude-desktop`, `cd x && claude mcp add foo npx foo`, `/opt/bin/claude mcp add foo x`; false for `claude mcp list`, `claude mcp remove foo`, `claude mcp get foo`, `echo "claude mcp add foo"`, `# claude mcp add foo`, `git commit -m 'claude mcp add'`.
  - `TestProjectServers`: a config with `projects["/w"].mcpServers` holding one stdio and one http server, plus `/w/.mcp.json` with one stdio server, gives the two stdio names sorted; a cwd with neither gives none and no error.
  - `TestMessages`: each function's output equals the Global Constraints copy for one and two names; `CheckMessage(nil) == ""`.
  - `BenchmarkIsServerAdd_NoMatch` on a 200-byte unrelated command.
- [ ] **Step 2: Run** `go test ./internal/mcpproxy/ -run 'IsServerAdd|ProjectServers|Messages'` and expect FAIL.
- [ ] **Step 3: Implement.** `IsServerAdd` splits the command into shell words (quotes and `#` comments honoured; split on `&&`, `||`, `;`, `|`) and matches a segment whose words are `<…/claude> mcp add|add-json|add-from-claude-desktop`; an unsplittable line is false.
- [ ] **Step 4: Run** the tests and `go test -bench IsServerAdd -benchmem ./internal/mcpproxy/`; expect PASS and well under 1 ms per op.
- [ ] **Step 5: Commit** `feat(mcp-proxy): decide when a Bash call added an MCP server`.

### Task 3: Install, uninstall and status manage the hooks

**Files:** Modify `internal/mcpproxy/install.go`, `internal/mcpproxy/hooks.go`, `internal/mcpproxy/hooks_test.go`, `cmd/aimux/cmd/mcp_proxy_install.go`, `cmd/aimux/cmd/mcp_proxy_style.go`.

**Interfaces:**
- Consumes: `claudehooks.Install/Uninstall/Status` (Task 1).
- Produces:
  - `var HookSpec claudehooks.Spec` (the two hooks in Global Constraints)
  - `Installer.HookBinary string` (empty: hooks left alone, like an empty `Settings`)
  - `Manifest.Hooks string` (`"added"` or empty), `InstallState.Hooks bool`
  - `func (i Installer) ensureHooks(m *Manifest)`, `func (i Installer) dropHooks(m *Manifest)`

- [ ] **Step 1: Write the failing tests** (`hooks_test.go`, using the installer test fixtures):
  - `TestInstall_AddsHooks`: an install (and the "nothing to move" path) leaves both proxy hooks in settings and `Manifest.Hooks == "added"`; a second install changes nothing.
  - `TestUninstall_RemovesOnlyProxyHooks`: with remote hooks present, uninstall removes the proxy hooks only.
  - `TestInstallStatus_Hooks`: `InstallState.Hooks` true after install, false after the owner deletes them by hand.
  - `TestInstall_NoHookBinaryLeavesSettings`: `HookBinary == ""` writes no hooks.
- [ ] **Step 2: Run** them; expect FAIL.
- [ ] **Step 3: Implement** `ensureHooks` and `dropHooks` beside `ensureIdleTimeout` / `dropIdleTimeout` (`install.go` lines ~341, ~446, ~993); a hook write error is printed as a warning, never fails install. `InstallStatus` sets `Hooks` from `claudehooks.Status(i.Settings, HookSpec)` (all installed).
- [ ] **Step 4: Wire** `HookBinary` in `proxyInstaller` from `stableBinary("aimux mcp proxy install")` (a refusal leaves it empty and prints the reason once). Status prints `Hooks: on` or `Hooks: off (run aimux mcp proxy install)` in the install section. Replace "no settings file is read or written" in the install help with: `In ~/.claude/settings.json it sets CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT and adds two hooks: one moves a server you add in a session behind the proxy, one names servers still direct when a session starts (uninstall removes both).`
- [ ] **Step 5: Run** `go test ./internal/mcpproxy/ ./cmd/aimux/cmd/`; expect PASS.
- [ ] **Step 6: Commit** `feat(mcp-proxy): install adds the server hooks; status shows them`.

### Task 4: The hooks answer

**Files:** Modify `internal/mcpproxy/hooks.go`, `internal/mcpproxy/hooks_test.go`; Create `cmd/aimux/cmd/mcp_proxy_hooks.go`, `cmd/aimux/cmd/mcp_proxy_hooks_test.go`.

**Interfaces:**
- Consumes: `IsServerAdd`, `ProjectServers`, the message functions (Task 2); `Installer.Install`, `Installer.InstallStatus` (existing).
- Produces:
  - `type HookReply struct { User, Claude string }` (both empty: print nothing)
  - `func (i Installer) HookAdded(command, cwd string) HookReply`
  - `func (i Installer) HookCheck(cwd string) HookReply`
  - hidden cobra `aimux mcp proxy hooks added|check`: reads Claude Code's hook JSON on stdin (`tool_input.command`, `cwd`), prints `{"systemMessage": User, "hookSpecificOutput": {"hookEventName": <event>, "additionalContext": Claude}}` when non-empty, always exit 0.

- [ ] **Step 1: Write the failing tests:**
  - `TestHookAdded_MovesNewServer`: not an add → empty reply, nothing read; proxy not installed (no manifest) → empty reply; an add with one new user-scope stdio server → install runs and the reply is `MovedMessage([foo])` for both User and Claude.
  - `TestHookAdded_FailedMoveNamesTheServer`: `Probe` fails for `bar` → reply is `FailedMessage([foo bar], <reason naming bar and --skip>)`, `~/.claude.json` unchanged.
  - `TestHookAdded_ProjectScoped`: the add put `foo` under `projects[cwd]` only → reply `ProjectMessage("foo")`, no install.
  - `TestHookCheck`: no manifest → empty; all moved or skipped → empty; two direct → `CheckMessage([a b])`; a project-scoped one for cwd is listed in the Claude text.
  - cmd: `TestProxyHooksCmd_AnswersJSON` feeds stdin for both subcommands and checks the JSON shape and exit 0, including malformed stdin (exit 0, no output).
- [ ] **Step 2: Run** them; expect FAIL.
- [ ] **Step 3: Implement.** `HookAdded`: return empty unless `IsServerAdd(command)` and a manifest exists; take `InstallStatus().NotProxied` (with `Status` nil, so no daemon call) as the names; none and a project-scoped new name → `ProjectMessage`; else `Install(nil)` and map success or error to the messages. `HookCheck` reads `InstallStatus` and `ProjectServers` only.
- [ ] **Step 4: Run** `go test ./internal/mcpproxy/ ./cmd/aimux/cmd/`; expect PASS.
- [ ] **Step 5: Commit** `feat(mcp-proxy): hooks move servers added in a session and name direct ones at start`.

### Task 5: Docs and the live check

**Files:** Modify `docs-site/src/content/docs/guides/mcp-proxy.mdx`, `RELEASE_NOTES.md`, `.claude/CLAUDE.md`.

- [ ] **Step 1: Docs.** Guide: a "New servers" section (what each hook does, the messages, project scope, `uninstall` removes them, hookify unaffected). Release notes Unreleased: one entry. CLAUDE.md: the `claudehooks` package line and the mcpproxy hooks sentence. `cd docs-site && npm run build` passes.
- [ ] **Step 2: Full suite.** `go build ./... && go vet ./... && go test ./... -timeout 120s`; all pass.
- [ ] **Step 3: Live check** (owner's Mac, after `make` install and `aimux mcp proxy restart`): `aimux mcp proxy install` reports the hooks; `aimux mcp proxy status` shows `Hooks: on`; in a scratch session, `claude mcp add -s user hooktest -- npx -y @modelcontextprotocol/server-everything` shows the moved message and `~/.claude.json` has its http entry; a new session's start is silent; then `claude mcp remove hooktest` and `aimux mcp proxy install` takes it off. Record whether a terminal-typed `claude mcp add` fired any hook. Time a no-match run of the real binary: `echo '{"tool_input":{"command":"ls"}}' | aimux mcp proxy hooks added` averaged over 20 runs must be under 30 ms (the spec's target, process start included).
- [ ] **Step 4: Commit** `docs(mcp-proxy): server hooks`, then push the branch and open a PR.

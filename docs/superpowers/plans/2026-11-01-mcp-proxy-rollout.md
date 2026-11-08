# MCP Proxy Rollout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `aimux mcp proxy` safe to switch on for the owner's whole setup: conformance on every relevant push, per-server health, a self-restarting daemon, and an install/uninstall that edits `~/.claude.json` reliably and undoes exactly what it did.

**Architecture:** Health lives in `mcpproxy.Pool` (it already starts, shares and reaps backends) and is exposed by the daemon at `/status`, as a `proxy_status` tool, and by `aimux mcp proxy status`. Install is three small, separately tested editors (Claude config, Claude settings permissions, launchd agent) driven by one manifest-backed command. The conformance script gains the suite's own expected-failures baseline and runs on pre-push and in CI.

**Tech Stack:** Go, mark3labs/mcp-go (already used), launchd (`launchctl bootstrap/bootout gui/<uid>`), `npx @modelcontextprotocol/conformance` pinned, pre-commit (stage `pre-push`), GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-11-01-mcp-proxy-rollout-design.md`

## Global Constraints

- Never print env values, tokens or tool arguments; names and counts only (also in errors and dry-run output).
- Files aimux writes are owner-only (0600, dirs 0700). Edits of files aimux does not own are atomic (temp file in the same dir, original mode kept, rename), verified by reading back, and backed up first.
- `~/.claude.json`: decode the top level as `map[string]json.RawMessage`; only `mcpServers` may change; every other key byte-identical after the write.
- No new Go dependencies. The conformance suite runs through `npx -y @modelcontextprotocol/conformance@0.1.16`.
- Tests never touch the real `~/.claude.json`, `~/.claude/settings.json`, `~/Library/LaunchAgents` or launchctl: every path and the launchctl runner are injected.
- `install` and `uninstall` change the owner's live setup; only the controller runs them, only when the owner asks.
- Proxied name for a tool: `mcp__mux__` + `ExposedName(server, tool, MaxToolNameLen("mux"))` (existing `internal/mcpproxy/names.go`).
- Default port 7381, proxy name `mux`, launchd label `com.aimux.mcp-proxy`, manifest `~/.aimux/mcp-proxy-install.json`, backups `~/.aimux/backups/` (keep the newest 10 per file).
- Writing in docs and output: no em dashes.

## Review Focus

1. A Claude Code session writes `~/.claude.json` between install's read and rename: install redoes from a fresh read (up to 5 tries) and never leaves a half edit (Task 5 test).
2. Install interrupted after the manifest says pending: the next `install` or `uninstall` detects it and finishes or reverts cleanly (Task 7 test).
3. The owner re-added a moved server, or edited `mux`, after install: uninstall keeps the owner's version and says so (Task 7 test).
4. A server fails to start: `status` shows `failed` with the last stderr lines; other servers keep serving (Task 2 test).
5. The daemon is not running: `status` says "not running" (exit 0 with `--json` `{"running":false}`), install refuses with a clear message rather than editing the config (Tasks 3 and 7).

## Task 1: Conformance on every relevant push

**Files:**
- Modify: `scripts/mcp-proxy-conformance.sh`
- Create: `scripts/mcp-proxy-conformance-baseline.yaml`
- Modify: `.pre-commit-config.yaml`, `.github/workflows/ci.yml`
- Modify: `docs-site/src/content/docs/guides/mcp-proxy.mdx` (Conformance check section)

- [ ] **Step 1:** Pin the suite: `npx -y @modelcontextprotocol/conformance@0.1.16`. Read the suite's README "Expected Failures" section for the baseline YAML format (`gh api repos/modelcontextprotocol/conformance/contents/README.md`) and write the baseline from today's documented gaps: `logging-*`, `prompts-*`, `resources-*`, `tools-call-error`, `tools-call-simple-text`.
- [ ] **Step 2:** The proxy run passes `--expected-failures scripts/mcp-proxy-conformance-baseline.yaml`; the script exits non-zero on any unexpected failure and prints baseline entries that now pass (stale). Keep the direct run as the reference.
- [ ] **Step 3:** Verify locally: `bash scripts/mcp-proxy-conformance.sh` exits 0; then temporarily remove one line from the baseline and confirm it exits non-zero; restore.
- [ ] **Step 4:** Pre-commit hook `mcp-proxy-conformance`, `stages: [pre-push]`, `files: ^(internal/mcpproxy/|cmd/aimux/cmd/mcp_proxy|scripts/mcp-proxy-conformance)`, `pass_filenames: false`, language `system`. CI job in `ci.yml` with the same path filter (use `dorny/paths-filter` only if the workflow already uses it; else `on.push.paths`/`on.pull_request.paths` in a separate workflow file `mcp-proxy-conformance.yml`), setup-go and setup-node, runs the script.
- [ ] **Step 5:** Commit `ci(mcp-proxy): conformance with a baseline on pre-push and in CI`.

## Task 2: Per-server health in the Pool

**Files:**
- Modify: `internal/mcpproxy/pool.go`, `internal/mcpproxy/stdio.go`
- Create: `internal/mcpproxy/health.go`, `internal/mcpproxy/health_test.go`

**Interfaces:**
- Produces:
  ```go
  type ServerState string // "not_started", "starting", "running", "idle_stopped", "failed"
  type ServerStatus struct {
      Server     string
      State      ServerState
      Instances  int        // >1 for per-session servers
      PID        int        // 0 unless one instance runs
      StartedAt  time.Time
      LastCall   time.Time
      Calls      int
      Failures   int
      LastError  string     // first line only
      StderrTail []string   // last 5 lines of a failed start
  }
  func (p *Pool) Status(servers []string) []ServerStatus // every configured server, sorted by name
  ```
  `Backend` gains optional `interface{ PID() int; StderrTail() []string }` (stdioBackend keeps a 5-line ring of the child's stderr; never logs it elsewhere).
- State rules: never started → `not_started`; start in progress → `starting`; started and not reaped → `running`; reaped idle → `idle_stopped`; last start failed, or the process exited / call timed out and no new start since → `failed`.

- [ ] **Step 1: Failing tests** with fake Starter/Backend: `TestStatus_LifeCycle` (not_started → starting → running → idle_stopped after Reap), `TestStatus_FailedStartKeepsStderrTail`, `TestStatus_CallTimeoutMarksFailedUntilRestart`, `TestStatus_PerSessionCountsInstances`, `TestStatus_CountsCallsAndFailures`, `TestStatus_UnknownServerIsNotStarted`.
- [ ] **Step 2:** `go test ./internal/mcpproxy/ -run Status` fails.
- [ ] **Step 3:** Implement under the Pool's existing lock; per-server stats survive slot removal.
- [ ] **Step 4:** `go test ./internal/mcpproxy/ -count=1 -race` passes.
- [ ] **Step 5:** Commit `feat(mcp-proxy): per-server health in the pool`.

## Task 3: Show health: /status, proxy_status, `aimux mcp proxy status`

**Files:**
- Modify: `internal/mcpproxy/daemon.go`, `internal/mcpproxy/server.go`, `cmd/aimux/cmd/mcp_proxy.go`
- Test: `internal/mcpproxy/daemon_test.go`, `internal/mcpproxy/server_test.go` (create if missing), `cmd/aimux/cmd/mcp_proxy_test.go`

**Interfaces:**
- Consumes: `Pool.Status`.
- `GET /status` (behind the existing `netguard.LocalOnly`) returns `{"running":true,"pid":…,"startedAt":…,"port":…,"servers":[ServerStatus…]}` with lower-camel JSON names.
- MCP tool `proxy_status` (exposed as `mcp__mux__proxy_status`), no arguments, returns the table as text.
- `aimux mcp proxy status [--json] [--probe] [--port N]`: reads `/status`; not running → prints "MCP proxy is not running on 127.0.0.1:<port>" (exit 0; `--json` `{"running":false}`); `--probe` asks the daemon to start each configured server once and list its tools (new `POST /status/probe`, also LocalOnly), then prints the table. Table columns: SERVER, STATE, TOOLS, LAST CALL (relative), LAST ERROR.

- [ ] **Step 1: Failing tests:** `/status` JSON shape and a non-loopback Host rejected; `proxy_status` tool lists every server with state; CLI table and `--json` against an httptest daemon; not-running output; `--probe` calls the probe endpoint; no env value ever appears in any output (fixture env `SECRET_TOKEN=abc123` must not be printed).
- [ ] **Step 2:** run, fails. **Step 3:** implement. **Step 4:** `go test ./internal/mcpproxy/ ./cmd/aimux/cmd/ -count=1 -race`.
- [ ] **Step 5:** Commit `feat(mcp-proxy): status endpoint, proxy_status tool and status command`.

## Task 4: Self-restarting daemon under launchd

**Files:**
- Create: `internal/mcpproxy/launchd.go`, `internal/mcpproxy/launchd_test.go`

**Interfaces:**
- Produces:
  ```go
  type Agent struct {
      Label, PlistPath, Binary, LogDir string
      Args []string                       // ["mcp","proxy","serve","--port","7381"]
      Run  func(name string, args ...string) ([]byte, error) // launchctl; injected
      UID  int
  }
  func (a Agent) Plist() []byte          // RunAtLoad, KeepAlive, StandardOut/ErrorPath in LogDir, ProgramArguments [Binary, Args...]
  func (a Agent) Install() error         // write plist 0644 atomically, bootout (ignore "not loaded"), bootstrap gui/<uid>
  func (a Agent) Uninstall() error       // bootout, remove plist; both idempotent
  func (a Agent) Loaded() (bool, error)  // launchctl print gui/<uid>/<label>
  ```
- [ ] **Step 1: Failing tests** with a fake Run recording calls: plist content (keys above, absolute binary path), install order (bootout then bootstrap), install twice is safe, uninstall when not installed is nil, Loaded parses a fake `print` result and a "could not find service" error.
- [ ] **Step 2–4:** fail, implement, `go test ./internal/mcpproxy/ -run Agent -count=1`.
- [ ] **Step 5:** Commit `feat(mcp-proxy): launchd agent for the daemon`.

## Task 5: Atomic, verified edit of a JSON config

**Files:**
- Create: `internal/mcpproxy/jsonedit.go`, `internal/mcpproxy/jsonedit_test.go`

**Interfaces:**
- Produces:
  ```go
  // EditKey rewrites one top-level key of the JSON object at path with fn(old) (old is nil when absent),
  // keeping every other key byte-identical, writing atomically (temp in same dir, original mode, rename),
  // re-reading to verify, and retrying from a fresh read up to 5 times if the file changed in between.
  func EditKey(path, key string, fn func(old json.RawMessage) (json.RawMessage, error)) error
  // Backup copies path to dir/<base>.<UTC 20060102T150405Z>, 0600, keeping the newest keep copies; returns the copy's path.
  func Backup(path, dir string, keep int) (string, error)
  var ErrConcurrentWrite = errors.New("config changed while editing; nothing written")
  ```
  Key order: re-encode the top level preserving the original key order (write a small ordered encoder over the raw map using the original order from a `json.Decoder` token pass); nested values are the original raw bytes.
- [ ] **Step 1: Failing tests:** other keys byte-identical (fixture with nested objects, unicode, numbers like `1e5`, large arrays); key order kept; mode kept; a writer that changes the file between read and rename (inject a hook) triggers retry and the final file has both changes; 5 straight conflicts give `ErrConcurrentWrite` and the other writer's content intact; absent key added; invalid JSON refused without writing; Backup rotation keeps 10.
- [ ] **Step 2–4:** fail, implement, `go test ./internal/mcpproxy/ -run 'EditKey|Backup' -count=1 -race`.
- [ ] **Step 5:** Commit `feat(mcp-proxy): atomic verified edit of one JSON key`.

## Task 6: Permission rules for proxied names

**Files:**
- Create: `internal/mcpproxy/permissions.go`, `internal/mcpproxy/permissions_test.go`

**Interfaces:**
- Consumes: `EditKey` (on `~/.claude/settings.json`, key `permissions`), `ExposedName`, `MaxToolNameLen`.
- Produces: `func ProxiedRules(allow []string, moved map[string][]string /*server→tools from the catalog*/) []string` (for `mcp__<server>__<tool>` → `mcp__mux__<ExposedName>`; for `mcp__<server>` → one rule per catalog tool of that server; rules for servers not moved are ignored; result de-duplicated, never contains a rule already in allow) and `func AddAllow(path string, rules []string) (added []string, err error)` / `func RemoveAllow(path string, rules []string) error` (only exact matches removed).
- [ ] **Step 1: Failing tests:** tool rule, server-wide rule, a long name gets the hashed form, existing proxied rule not duplicated, unrelated rules untouched, RemoveAllow removes only listed ones, missing `permissions` key handled.
- [ ] **Step 2–4:** fail, implement, test.
- [ ] **Step 5:** Commit `feat(mcp-proxy): permission rules for proxied tool names`.

## Task 7: install / uninstall

**Files:**
- Create: `internal/mcpproxy/install.go`, `internal/mcpproxy/install_test.go`
- Modify: `cmd/aimux/cmd/mcp_proxy.go` (commands `install`, `uninstall`), its test

**Interfaces:**
- Consumes: Tasks 3–6, existing `LoadClaudeServers`, `SaveServers`, `RefreshCatalog`, catalog load.
- Produces:
  ```go
  type Manifest struct {
      State      string                     // "pending" | "done"
      At         time.Time
      Moved      map[string]json.RawMessage // name → original raw entry from mcpServers
      Mux        json.RawMessage            // the entry install added
      Backup     string
      SettingsBackup string
      RulesAdded []string
  }
  type Installer struct { ClaudeConfig, Settings, ManifestPath, BackupDir, ServersPath, CatalogPath string; Agent Agent; Status func() (running bool, err error); Refresh func(servers []string) map[string]error; Out io.Writer; ClaudeDir string /* for the reference scan */ }
  func (i Installer) Plan(skip []string) (InstallPlan, error) // names only, for --dry-run
  func (i Installer) Install(skip []string) error
  func (i Installer) Uninstall() error
  ```
- Install order: (1) resume or revert a pending manifest; (2) agent loaded and daemon answering (Install the agent if missing, wait up to 10 s), else refuse; (3) refresh catalog entries missing for servers to move; servers failing to list tools are refused by name unless skipped; (4) backups; (5) manifest `pending` with Moved, Mux, RulesAdded planned; (6) `EditKey(mcpServers)`; (7) `AddAllow`; (8) manifest `done`; (9) print moved names, rules added count, "running sessions keep their direct servers until they restart", and where `mcp__aimux__` appears under ClaudeDir (`*.md` files, path and count only).
- Uninstall: no manifest → change nothing, print how to restore from the newest backup; else re-add each Moved entry unless a same-name entry exists (report it), remove `mux` only if equal to Manifest.Mux (else report), `RemoveAllow(RulesAdded)`, `Agent.Uninstall()`, delete the manifest. Twice → second is a no-op message.
- The `aimux` stdio entry is moved like any other server.
- `--dry-run` prints the plan without touching anything.

- [ ] **Step 1: Failing tests** (temp dirs, fake Agent runner, fake Status/Refresh): full install then uninstall restores mcpServers byte-identically when nothing else changed; other keys untouched throughout; install twice no-op; uninstall twice no-op; uninstall without manifest changes nothing; pending manifest resumed; owner re-added server kept; owner-edited mux kept; daemon not running → refuse with no file changed; failing server refused unless `--skip`; env values never printed; reference scan prints counts only.
- [ ] **Step 2–4:** fail, implement, `go test ./internal/mcpproxy/ ./cmd/aimux/cmd/ -count=1 -race`.
- [ ] **Step 5:** Commit `feat(mcp-proxy): reliable install and uninstall`.

## Task 8: Live check against the owner's servers (opt-in)

**Files:**
- Create: `scripts/mcp-proxy-live.sh`

- [ ] **Step 1:** Script: build aimux to a temp dir; `refresh` and `serve` on port 17462 with temp `--servers` and `--catalog` (reads the real `~/.claude.json` read-only); for each stdio server: initialize through the proxy and list its tools; print `ok <name> <tools>` or `failed <name> <first error line>`; the only tool call is aimux's `list_sessions` with `limit: 1`, printing only whether it answered. Cleans up every process on exit.
- [ ] **Step 2:** `bash -n` and shellcheck if available. Do not run it; the controller runs it with the owner's approval.
- [ ] **Step 3:** Commit `test(mcp-proxy): opt-in live check of the owner's servers`.

## Task 9: Docs

**Files:**
- Modify: `docs-site/src/content/docs/guides/mcp-proxy.mdx` (install/uninstall, status, health states, launchd, tradeoffs table, renamed tools), `RELEASE_NOTES.md` Unreleased, `.claude/CLAUDE.md` package map.

- [ ] **Step 1:** Write; **Step 2:** docs-site build passes; **Step 3:** commit `docs: MCP proxy install, status and health`.

# aimux -- Project Guide for Claude

## Git Policy

NEVER commit or push code without explicit user approval. Always ask before running `git commit` or `git push`. Show what will be committed (files, summary) and wait for confirmation.

## Coding Discipline

Always invoke the `development-tools:crafted-code` skill before writing the first line of any new feature, bug fix, refactor, or code review. Follow all nine principles in order.

Separation of concerns is non-negotiable: core packages (everything under `internal/` except `tui/`) MUST NOT import `bubbletea`, `lipgloss`, or anything from `tui/`. Business logic belongs in core packages; `tui/` is a thin adapter layer for rendering and key handling only. When in doubt, ask: "does this function reference `tea.Model`, `tea.Cmd`, or `lipgloss`?" If no, it belongs in a core package.

## Frontend Parity

aimux has three frontends: TUI, web dashboard, CLI. **Every user-visible feature MUST be reachable from all three, or the gap must be explicitly recorded in the parity tech-debt table.**

**Before implementing any feature that lives in a frontend:**
1. Read `.claude/skills/frontend-parity.md` — name the shared controller function first
2. Implement in `internal/controller/` with zero frontend imports
3. Wire TUI (keypress → controller call)
4. Wire web (HTTP handler → same controller call)
5. Update `smoke_test.go` with the new endpoint

**Before committing any change to `internal/frontend/`:**
- Use the `frontend-parity` skill to run the parity audit
- If only one frontend changed, either fix the other or add `# parity: N/A` to the commit message explaining why

Skipping the controller layer and writing business logic directly in `handlers.go` or `app.go` is a parity violation: it means the feature can never be ported without rewriting.

## Code Reuse vs New Dependencies

**Before adding any package to go.mod:**
- Check if an existing dependency covers the use case (`go.mod` lists them)
- Check if a core package already implements it (`internal/controller/`, `internal/otel/`, etc.)

**Before writing a new function in a frontend file:**
- Use the `code-reuse` skill to check if the function belongs in `internal/controller/`
- A handler method > 30 lines is a signal to extract a controller function

The pre-commit hook `layer-discipline` enforces the hard constraint (no TUI imports in core). The `frontend-parity-hint` hook reminds you when only one frontend changes.

## Pre-Commit Checklist

Before committing or pushing ANY code:
1. Run `go build ./...` -- must compile with zero errors
2. Run `go vet ./...` -- must pass with zero issues
3. Run `go test ./... -timeout 30s` -- ALL packages must pass
4. Check for missing tests: every new method, function, or behavior MUST have tests
5. When fixing something for one provider, verify the same fix applies to all three (Claude, Codex, Gemini)

Never push code that hasn't been built and tested. Never claim work is done without running the test suite.

6. Update documentation: every new feature, config option, keybinding, API endpoint, or UX change MUST include updates to the relevant docs-site page(s) in `docs-site/src/content/docs/`. If a new guide page is needed, add it to the sidebar in `astro.config.mjs`. Rebuild with `cd docs-site && npm run build` to verify.

## Three-Tier Testing Policy

Every change must pass all applicable tiers before merge:

**Tier 1: Unit tests** (`go test ./...`, always)
- Every function/method has tests (happy path, error path, boundary)
- Controller functions tested independently of TUI/web
- Build tags separate integration tests (`//go:build integration`)

**Tier 2: E2E CLI tests** (`go test -tags e2e ./internal/e2e/`, on feature changes)
- Binary compiled and run as subprocess
- Tests every CLI command with `--json` output validation
- Catches: flag parsing, cobra registration, output format bugs

**Tier 3: API smoke tests** (`go test ./internal/frontend/web/ -run Smoke`, on API changes)
- Boots real Server, hits every endpoint, validates status + JSON
- Every new API endpoint MUST have a corresponding handler test AND a smoke test entry
- Catches: route registration, handler wiring, response format

**Endpoint coverage rule:** Before merging any PR that adds or modifies a web API endpoint:
1. Add a handler test in `handlers_test.go` (tests the handler logic)
2. Add the endpoint to the smoke test in `smoke_test.go` (tests the wiring)
3. Run the full smoke test to verify all endpoints still respond

**Benchmark baseline:** Performance-sensitive code (fade colors, discovery, trace parsing) has
benchmarks in `*_bench_test.go`. Run `go test -bench=. -benchmem` before and after changes to
detect regressions.

## Refactoring Rule: Tests Travel With the Code

When moving logic from one package to another (e.g., extracting from `tui/app.go` to `controller/`):
1. The destination package MUST have tests covering the moved logic before the refactor is complete
2. If the source had tests, they move or are rewritten for the new package
3. If the source had NO tests, write them now — refactoring without tests is how regressions happen
4. Run `go test ./...` after every move to confirm nothing broke
5. The controller package (`internal/controller/`) is UI-agnostic — its tests must NOT import `bubbletea` or `lipgloss`

## What This Is

aimux is a Go TUI tool that provides a TUI dashboard for managing multiple AI coding agent sessions. It discovers running agents (Claude, Codex, Gemini), displays their status, lets you zoom into live sessions, view conversation traces, annotate agent behavior, and export traces via OTEL. Single binary, provider-extensible.

## Project Structure

```
cmd/aimux/main.go           # CLI entry point
internal/
  agent/agent.go                # Agent struct, Status enum, SourceType
  config/config.go              # Config struct, YAML loading (~/.aimux/config.yaml)
  cost/tracker.go               # Per-model pricing, cost estimation
  discovery/
    orchestrator.go             # Multi-provider discovery, unique suffix assignment
    process.go                  # Process scanning, subagent filtering (ancestor chain)
    session.go                  # Session file discovery, JSONL parsing
    tmux.go                     # Tmux session listing, matching
    registry.go                 # Claude Code's session registry (~/.claude/sessions/<pid>.json): ReadClaudeRegistry and ReadClaudeRegistryStrict over one read (readClaudeRegistry: every decodable entry plus the first failure), IsLiveHolder (pid alive, start time within 2 s)
    live.go                     # LiveCache: harness.LiveSessions for Claude Code, Holders(List) and Holders(Strict) from one read (the table on harness.Mode): registry holders by IsLiveHolder's rule, and for Strict an unreadable registry is an error and a live `claude` process naming the session on its command line (no live entry of its own) holds it too; 1 s TTL (LiveTTL), single-flight, one batched `ps -o pid=,lstart=` per read; the `ps -axo pid=,args=` scan (CommandLines, injectable) runs only for Strict; a Strict read is never served from the cache (it takes only a read begun after the call, so callers arriving together share one, and the router always sees a terminal opened a moment ago), while its result serves List for the TTL; a read is stamped when it starts; remote serve creates one and every open-session reader (LocalIndex, queue, terminal input, waiting, LastFocus, LayoutReader) and the router share it, so open/closed and the registry busy status may be up to 1 s old; the send path's safety does not depend on that for terminal input (terminput's locate re-checks the tty, foreground pid and command fresh before typing, Enter and every key); `aimux chat` makes its own with TTL 0
  evaluation/                   # Annotation persistence, JSONL export
  jump/                         # Session resumption (tmux split, iTerm2)
  otel/
    receiver.go                 # OTLP/HTTP receiver (port 4318)
    store.go                    # Span data model + in-memory store
    converter.go                # OTEL span -> trace.Turn bridge
    exporter.go                 # trace.Turn -> OTLP/HTTP export
  provider/
    provider.go                 # Provider interface (10 methods)
    registry.go                 # Registry: the one place providers are built (NewRegistry(home, cfg)); Local() is Claude then Codex whatever the config (CLI, web dashboard, orchestrator), Enabled() adds K8s when active and keeps cfg.IsProviderEnabled ones (TUI), ByName(name); Claude's paths come from home; TestNoProviderConstructionOutsideRegistry fails on a provider struct literal or New constructor outside internal/provider (tests excepted)
    claude.go                   # Claude: full discovery, PTY embed, JSONL parsing
    codex.go                    # Codex: full discovery, tmux mirror, JSONL parsing
    gemini.go                   # Gemini: full discovery, tmux mirror, JSON parsing
    helpers.go                  # Shared: process tree grouping, start time, CWD extraction
    claude_store.go             # Claude's harness.SessionStore: SessionFile (under ProjectsDir, default ~/.claude/projects), SessionCwd (history.SessionCwd: the first recorded cwd, the session list's line, read only up to it), Fingerprint (size and mtime); the router and the chat manager read transcripts through it; also catalog.Store's Sessions (history.Discover over ProjectsDir) and ParseTail (ParseTraceTail)
    (RecentDirs vs catalog Projects (remoteapi LocalIndex.Projects): kept, they differ. RecentDirs decodes ~/.claude/projects dir keys with no filters; Projects uses recorded cwd, drops automated and subagent sessions unless created here, stats the dir and reads every registered provider's Sessions. Not duplicates.)
  spawn/spawn.go                # Launch agents into tmux/iTerm
  team/reader.go                # Team config reading
  gitinfo/                      # The one place git is asked about a folder: Top (top-level and common dir in one rev-parse, remembered 60 s per folder), Origin, Branch; folders and gitremote both read through it
  Session titles, one rule: `history.ShownTitle` (the owner's `.meta.json` title (`TitleBy` "owner"), then Claude Code's `/rename` (`CustomTitle`, custom-title entries), then the namer's `.meta.json` title, then Claude Code's ai-title, then the first message); `history.ShownTitleForFile`/`ShownTitleForFileMeta` are the same for a live agent's file path (appended lines only, streamed, 512-file cache warmed by `WarmTitles` in the TUI agent refresh); `history.DisplayTitle` is the list form for the remote API (first message labelled and cut at 80 bytes). TUI (rows, sort, filter, delete, cleanup, launcher, preview), CLI table and fallback picker (`internal/sessions`), dashboard (`shownTitle` on `/api/history`, the agents stream cut at 120 bytes) and remote search hits call these; the React app never picks a title; the search index stores `history.ShownTitle` (history's first prompt via `FirstPromptReader`) and re-reads a session when its `.meta.json` mtime changes (`sessions.meta_mtime`). Never add another Title-or-FirstPrompt rule.
  gitremote/
    gitremote.go                # WebURL (git remote to web address, no credentials; ssh becomes https, http(s) keeps scheme and port), Lookup (origin + branch + repo root of a folder, 2 s git timeout, 60 s cache swept of expired entries on write); feeds SessionDetail.repo
  catalog/                      # The session catalog, read by the remote API and the MCP session tools: Catalog over Providers ([]catalog.Provider: a Store, which is harness.SessionStore plus Name, Sessions and ParseTail, and the provider's harness.LiveSessions; ProvidersOf picks them from the registry's providers by capability); Scan (cached for Lookup; Cache shares it; Refresh rereads one session through the store's optional Rereader, history.Reread for Claude, and updates that cache entry, used by MCP get_session), List(history.VisibleOpts) plus the Created exception (CreatedStore, ~/.aimux/remote-created.json: sessions aimux started are listed though automated, and a running one without a file yet), Get, Projects, ListableIDs (the search scope), Searchable, live state (Open, State/StateOf: busy through BusyReader, open_in_terminal, idle; Working; TerminalPID(s), RegistryStatus); Entry.Title is history.DisplayTitle; records a store gives for another provider are dropped, a session belongs to the first store with its file, open sessions merge every provider's Holders(List); NewProvider (default: the first provider's name) is the Provider of sessions created here; imports no net/http, remoteapi, remoteapp, terminput, frontend, provider, discovery or acprun (boundary_test.go), so `aimux mcp serve` gets it without the cluster clients; no harness names inside
  remoteapi/local.go            # LocalIndex: the contract-shaped layer over catalog (LocalIndex.Catalog() builds one from its fields, sharing its scan cache): Session/SessionDetail JSON, turns and step details, Record, repo lookup and terminal status in details, search hits; remoteapi.Store, Provider, CreatedStore and the State constants are the catalog's
  remoteapp/                    # Composes `aimux remote serve` (cmd/aimux/cmd/remote.go only resolves flags and config, then calls `remoteapp.New(cfg, Options).Run(ctx)`): server.go builds every service in one place (provider list, LiveCache, chat manager and router, broker, the two keyword search services, terminal input, chat queue and dispatcher, namer, folders resolver, layout reader, Last on your Mac, waiting tracker, push, device login), the background jobs as a list (`job{name, await, run}`: device login, queue events, chat queue, namer, last focus, folders, waiting alerts, reap idle runs, search refresh) and the shutdown order (HTTP drains; awaited jobs return; then the listener, queue file, audit log, terminal input log, terminal drivers and chat manager close, last opened first); `Options.Reserve` defaults to `acprun.Reservation(Home)`, the session lock `aimux chat` takes too; the startup lines (`startup.lines`, pinned by server_test.go); push.go has the push decisions (`buildPush`, `approvalHooks`, `waitingAlerts`, `runWaitingAlerts`); services.go the builders, `Providers(home)` and `AuditPath(home)`; terminal_driver_*.go the iTerm2 driver on macOS; imports net/http (the composition root) and no cmd, frontend or clients package (boundary_test.go; the phone web client comes in as `Options.Client`)
  remoteapi/devicelogin/
    store.go                    # Paired devices (~/.aimux/remote-paired-devices.json), pending code (remote-pairing.json, SHA-256 only, attempts counted), last seen (remote-paired-seen.json); every write under flock on remote-paired-devices.lock; all 0600
    codes.go                    # Pairing codes: 8 Crockford base32 characters shown XXXX-XXXX, CanonicalCode, ParseCode
    verify.go                   # Signature checks, standard library only: Secure Enclave P-256 (aimux-pair-v1 / aimux-login-v1 strings) and WebAuthn create/get
    sessions.go                 # Login sessions in memory (SHA-256 of the token), idle 1 h, absolute 12 h, Done() for streams
    handler.go                  # /v1/auth/pair, challenge, login, logout; Middleware (401 login_required) inside RequireTailnetUser; health session state
    audit.go                    # ~/.aimux/logs/remote-audit.log JSON lines (never codes, challenges, tokens, keys or text); identical failures collapse to one a minute
  harness/
    harness.go                  # Leaf package of types shared by the core and harness-specific code: Key and its constants, Option, Prompt, Screen, Match, RoomKeys, PasteText, the Composer interface (Read, BoxShows, IsAgentProcess, RefuseText, RoomKeys, ClearKeys); imports no internal package; boundary_test.go enforces the Harness Boundary imports
    live.go                     # Holder{PID, SessionID, Status}, Mode (List: the session list's rule, never fails; Strict: the router's, an unreadable registry is an error and command-line holders count; a comment table of every case), the LiveSessions interface (Holders(mode): every live holder, one per process and session) and OpenSessions (by session, leaving out the caller's own processes, the highest pid when two hold one session: the open_in_terminal rule)
    store.go                    # SessionStore interface (SessionFile, SessionCwd, Fingerprint): a harness's transcripts as continuing a session needs them
    claude/screen.go            # Parses Claude Code's screen: InputBoxEmpty, BoxHasText, WordsInBox, WorkingSeconds, ParsePrompt (none when its top rule is off the screen; fixtures in testdata/screens/, also read by terminput's tests)
    claude/cmdline.go           # CommandLineSessions: the words of a `claude` command line that may name its session (each argument, and the text after an argument's last =); LiveCache's Strict fallback
    claude/composer.go          # Composer: Claude Code's harness.Composer over screen.go (one prompt and one box parse per Read); processes claude and node; refuses a leading ! (bash mode) and, unless allowed, / (slash command); the grey-suggestion probe (space, then backspace) and the clear keys
  remoteapi/chatqueue/
    queue.go                    # Item and queue types, states (waiting, sending, answering; queue running or paused), limits (20 per session), the text rules re-checked before a send
    store.go                    # Queue per session in ~/.aimux/remote-queue.json (0600, atomic writes): add (idempotent by key), edit, move, delete, pause, resume; a restart pauses an in-flight item as "may have been sent"
    dispatch.go                 # Dispatcher: one message at a time per session, next only after the answer finished (ACP turn done; iTerm2 history turn seen plus 2 s with Claude not working, or a later turn of the owner's); fixed pause reasons
    prompt.go                   # Reads Claude Code's "while you were working" wrapper, paste tags and slash-command/bash-mode tags when matching an item to a history turn
  terminput/
    terminput.go                # Service: safety checks (enabled, registry pid, foreground is the harness's process, in-use guard on the focused pane, busy refusal, input box showing, prompt id; makeRoom presses the harness's probe and clear keys to replace a suggestion or clear a draft, or refuses it with keep_drafts, re-checking before every key), Driver/System/Sessions interfaces, error codes; every screen read, the process check, the text rule and the room keys go through its Composer (NewService: Claude Code's; NewServiceFor: another, nil panics); CheckText and CheckMessage apply Claude Code's rules
    types.go                    # Aliases of the harness types (Key, Option, Prompt, Composer), PasteText
    probe.go                    # SystemProbe: tty, foreground process group (ps), HID idle time (ioreg)
    log.go                      # Attempt log ~/.aimux/logs/terminal-input.log (0600, never the text)
    layout.go                   # LayoutReader: joins the iTerm2 windows, tabs and panes to open sessions by tty (MacPlace for the session list; 3 s cache)
    iterm2/iterm2.go            # iTerm2 driver over a persistent JXA helper (helper.js, embedded); live tests behind the iterm2 build tag
  waiting/                      # Permission alerts for terminal sessions: markers in ~/.aimux/waiting (0700/0600, atomic; Write, Remove, Read, List), HandleHook (Claude Code hook stdin: Notification permission_prompt sets, keeping transcript_path and agent_id; PostToolUse, PostToolUseFailure (not a subagent's for a main-thread marker), UserPromptSubmit, Stop, SessionEnd clear), Install/Uninstall/Status (merge into ~/.claude/settings.json with a backup, key order and the owner's hooks kept; `<path> remote hooks notify || true`, either form recognised, also inside a group of the owner's; CommandBinary), Tracker (open sessions only; drops a marker once its transcript is written 2 s after the prompt; a missing session's marker goes once its pid is gone or after 5 s of misses; the first Poll skips markers older than 2 min); used by `aimux remote hooks` (cmd/aimux/cmd/remote_hooks.go) and remote serve (`waiting` on sessions; its push loop is `internal/remoteapp`'s `waitingAlerts`)
  folders/                      # Resolver (git through gitinfo): a session path to Folder{Repo, Worktree, Sub, Label} (git or not), cached in ~/.aimux/folders.json
  terminal/
    backend.go                  # SessionBackend interface
    embed.go                    # Direct PTY backend (Claude)
    tmux.go                     # Tmux mirror backend (Codex, Gemini)
    view.go                     # VT emulator rendering
  trace/trace.go                # Shared Turn/ToolSpan types (ToolSpan.Input is the full tool input)
  tui/
    app.go                      # Root Bubble Tea model
    command.go                  # Command palette
    views/
      agents.go                 # Agent list table
      preview.go                # Right-side preview pane
      session.go                # Interactive session view
      logs.go                   # Trace viewer with annotations
      launcher.go               # Agent launcher overlay
      costs.go                  # Cost dashboard
      teams.go                  # Teams overview
      header.go                 # Top bar
      help.go                   # Help overlay
```

## Key Patterns

- **Provider interface**: All agent types implement `provider.Provider` with 10 methods: Name, Discover, ResumeCommand, CanEmbed, FindSessionFile, RecentDirs, SpawnCommand, SpawnArgs, ParseTrace, OTELEnv. Adding a provider = one Go file + register in `provider.NewRegistry` (`internal/provider/registry.go`).
- **SessionBackend interface**: `terminal.SessionBackend` (Read/Write/Resize/Close/Alive) with two implementations: direct PTY (Claude) and tmux mirror (Codex/Gemini). `DirectRenderer` optional interface skips VT emulator for tmux.
- **Trace parsing**: Each provider owns its parser via `ParseTrace`. Shared types in `internal/trace/`. LogsView receives a `TraceParser` function from app.go.
- **OTEL dual mode**: File-based parsing for display (full responses). OTEL receiver (port 4318) collects live telemetry for export. `parserForProvider` checks file first, falls back to OTEL for new sessions. Trace header shows [FILE] (otel:N). Claude Code sends events via OTEL logs protocol (no response text -- Anthropic privacy design). Export to MLflow via `e` → `o` in split view or `:export-otel`.
- **Export**: `e` key in trace pane opens export menu: `j` for JSONL (to `~/.aimux/exports/`), `o` for OTEL (to configured endpoint). MLflow requires `x-mlflow-experiment-id` header, set via `export.experiment_id` in config.
- **Config**: `~/.aimux/config.yaml` -- providers, shell, export (endpoint + experiment_id), OTEL receiver. Each provider's `OTELEnv(endpoint)` returns the right env vars for its OTEL mechanism.
- **Stable agent ordering**: `sort.SliceStable` with status priority (active first), then alphabetical. Cursor preserved by PID tracking.
- **Multi-session support**: Multiple sessions in the same directory appear as separate entries. Process tree dedup groups child processes (node wrappers) while keeping separate sessions distinct. `assignUniqueSuffixes` adds `#1`, `#2` when names collide.
- **Session file matching**: for running Claude processes, the session ID comes from Claude Code's own registry (`~/.claude/sessions/<pid>.json`, see `discovery.ReadClaudeRegistry`), guarded against PID reuse by start time. Only when the registry has no entry are sessions matched to their JSONL files by correlating process start time (`ps -o lstart=`) with file first-write timestamp. Gemini sessions use per-session `session-*.json` chat files instead of shared `logs.json`.
- **Subagent filtering**: `hasClaudeAncestor` walks up to 5 PPID levels to filter subagents spawned via Agent tool (handles `claude -> node -> claude` chains).
- **Expandable process tree**: Agents table supports expand/collapse (Tab/x) for sessions with grouped processes. `treeRow` struct flattens the tree for rendering with box-drawing glyphs.

## UI Consistency Rules

- **Header hint bar and view logic must stay in sync.** When adding, removing, or renaming a keybinding in any view (`Update()` method), always update the corresponding `SetHint()` call in `tui/app.go` `updateHints()`. The hint bar is the user's only discoverability mechanism for keybindings.

## Reusability and Extensibility

aimux has three frontends (TUI, Web, CLI) and must prevent feature divergence. Every
user-visible operation lives in a shared core layer; frontends are thin adapters that
wire keys, HTTP endpoints, or CLI flags to the same function.

### Layer Model

```
Frontends (thin adapters, rendering + input only)
  TUI:  internal/frontend/tui/    — Bubble Tea keybindings, lipgloss rendering
  Web:  internal/frontend/web/    — HTTP handlers, SSE, WebSocket
  CLI:  cmd/aimux/cmd/            — Cobra flags, JSON output

Core (UI-agnostic, MUST NOT import bubbletea/lipgloss/net/http; net/http exceptions: internal/llm, otel/k8s_reader.go and internal/remoteapp, the remote serve composition root)
  controller/  — operations: sort, filter, attend, archive, kill, notify, export, session_meta, badges, live agent filter/dedup, costs by project
  agent/       — data types, status enum, fade colors
  badge/       — project file badge evaluation
  config/      — global + project-local config, runtime/sandbox profiles
  cost/        — per-model pricing, token counting
  discovery/   — multi-provider orchestrator, process scanning
  history/     — session scanning, metadata, titles, search; namer.go (economical naming, daily limit, pause on failures), namer_backend.go (`NamingBackends` and `namingClient`: the one naming engine over internal/llm, label "naming", used by the Namer and by titler.go's `GenerateTitles` for the TUI and dashboard, which keep their own prompt, call size and session rules), metawrite.go (`UpdateMeta`: locked, atomic `.meta.json` writer; the only writer), cwd.go (`SessionCwd`: a session's first recorded cwd, decoding each line as scanSession does, stopping at that line), visible.go (`Visible`: which history sessions a list shows; `Kinds` classifies, each surface's options side by side: `TUIOpts`, `DashboardOpts` with `DashboardReveals` (subagents, automated, analyzers, search: the `hidden` list on `/api/history`); one `KindAnalyzer` (hook prompts or SESSION ANALYZER text) on every surface, `RemoteOpts`, `CLIOpts`, `MCPOpts` (MCP list_sessions: analyzers and automated only with include_automated); live agents are `controller.DropEphemeral`/`DedupBySession`, a different rule); history must never import provider (provider imports history)
  evaluation/  — annotation persistence, JSONL export
  llm/         — the one model client: `Client` (Primary, once-only Fallback (claude -p) with `FallbackModel` (default haiku) on model not found, a usage line per successful call in ~/.aimux/data/usage.jsonl under the caller's `Label`: `naming` for history, `insight` for insight), `API` (Messages API; system field only when set; errors keep status and error type only), `CLI` (`claude -p --safe-mode` from an empty `aimux-llm-*` temp folder, own process group, proc_*.go), `Backends(key, url)`, `APIKey` (sessions.api_key, else ANTHROPIC_API_KEY; returns the key, never log it), `ResolveModel` (haiku/sonnet/opus); imports only cost (and net/http, a listed exception); the only model-name table (insight uses it too)
  otel/        — OTLP receiver, span store, exporter
  provider/    — Provider interface, Claude/Codex/Gemini/K8s implementations
  runtime/     — Runtime interface (local/container/k8s), PolicyEnforcer, OpenShell stub
  trace/       — shared Turn/ToolSpan types, file tailer
  terminal/    — SessionBackend (PTY embed, tmux mirror)
  spawn/       — agent launch into tmux/direct
  team/        — team config reader
  remoteapi/chatqueue/: per-session chat queue and its dispatcher (phone messages sent one at a time)
  catalog/: the session catalog (list, get, visibility, provider, live state, projects) behind the remote API and the MCP list_sessions/get_session
  terminput/: typing phone messages and prompt keys into terminal Claude sessions; iterm2/ driver
  harness/: types and interfaces for harness-specific knowledge (Composer, LiveSessions, SessionStore); claude/ is Claude Code's
  acprun/: continuing idle sessions through an ACP adapter; Router refuses a session a terminal holds (SessionLookup: harness.SessionStore plus LiveSessions under Strict); `Reservation(home)` is the cross-process session lock (~/.aimux/locks) shared by aimux chat and remote serve
  remoteapp/: composes `aimux remote serve` (services, background jobs, shutdown order, push decisions); cmd only parses flags
```

### The Rule: Controller First, Frontend Second

Every new feature that involves user-visible behavior MUST follow this pattern:

1. **Core function in `controller/`** — pure logic, no UI types. Tested independently.
2. **TUI wires the key** — `app.go` calls the controller function on keypress.
3. **Web API wires the endpoint** — `handlers.go` calls the same controller function on HTTP request.
4. **CLI wires the flag** — `cmd/*.go` calls the same controller function on flag.

If a feature exists in only one frontend, it is tech debt. Track it.

**Pre-merge checklist for any new keybinding in `app.go`:**
- The logic is in `controller/` (or another core package), not inline
- There is a corresponding web API endpoint (even if the React UI doesn't consume it yet)
- The controller function has tests independent of any UI framework

### Operations in `controller/` (shared across frontends)

| Operation | Function | TUI | Web API | CLI |
|-----------|----------|-----|---------|-----|
| Sort agents | `SortAgents()` | `s` key | -- TODO | -- TODO |
| Filter agents | `FilterAgents()` | `/` key | -- TODO | -- TODO |
| Smart attend | `NextAttend()` | `a` key | -- TODO | -- |
| Auto-archive | `PartitionByArchive()` | `o` key | -- TODO | -- |
| Notify decision | `ShouldNotify()` | `maybeNotify` | -- TODO (SSE) | -- |
| Kill action | `DetermineKillAction()` | `x` key | `POST /archive` | -- TODO |
| Toggle star | `ToggleStar()` | `*` key | `POST /sessions/meta` | -- TODO |
| Set annotation | `SetAnnotation()` | sessions view | `POST /sessions/meta` | -- TODO |
| Set tags | `SetTags()` | sessions view | `POST /sessions/meta` | -- TODO |
| Set note | `SetNote()` | sessions view | `POST /sessions/meta` | -- TODO |
| Export JSONL | `ExportJSONL()` | `:export` | `POST /export/jsonl` | -- TODO |
| Export OTEL | `ExportOTEL()` | `:export-otel` | `POST /export/otel` | -- TODO |
| Delete session | `DeleteSession()` | `d` key | -- TODO | -- TODO |
| Filter hidden | `FilterHidden()` | auto | auto | -- |
| History sessions shown | `history.Visible()` | sessions view (`TUIOpts`: H, search) | `GET /api/history` (`DashboardOpts`, `hidden` read by React, `sessionFilter.ts` applies it; Subagents, Automated and Analyzers switches) | `aimux sessions` (`CLIOpts`; `--live` and the query filter stay outside); remote API uses `RemoteOpts` and MCP `list_sessions` `MCPOpts`, both through `catalog.List` |
| Project badges | `ApplyBadges()` | agent refresh | SSE `/api/events` | -- |
| Drop ephemeral agents | `DropEphemeral()` | -- (shows all) | SSE `/api/events` | -- |
| One agent per session | `DedupBySession()` | -- (shows all) | SSE `/api/events` | -- |
| Cost by project | `CostsByProject()` | cost view (empty provider shown "unknown") | `GET /api/costs` (provider sent empty) | -- TODO |
| Update meta fields | `UpdateSessionMeta()` | -- (per-field setters) | `POST /sessions/meta` | -- |

"-- TODO" means the controller function exists but the frontend hasn't wired it yet.

### What Frontends Own (and nothing else)

**TUI owns:** lipgloss styling, Bubble Tea `tea.Cmd`/`tea.Model` wiring, key routing, terminal rendering, PTY embedding, VT emulator display.

**Web owns:** HTTP routing, SSE streaming, WebSocket terminal, React components, CSS, browser layout, CORS.

**CLI owns:** Cobra command tree, flag parsing, `--json` output formatting, `--dry-run` simulation, delivery targets.

**None of these should contain business logic.** If a function in `app.go` doesn't reference `tea.Model`, `tea.Cmd`, or `lipgloss`, it belongs in a core package.

### Runtime Layer (new)

Sessions can run locally or in containers (Podman, K8s). Sandboxing is a separate
policy layer that wraps the runtime. The runtime package uses the optional-capability
pattern.

```
Runtime interface (internal/runtime/)
  Local          — process on host (current default, no-op lifecycle)
  Container      — Podman/Docker container
  OpenShellRuntime — NVIDIA OpenShell sandbox (stub, implements Runtime + PolicyEnforcer)

PolicyEnforcer interface (optional capability)
  ApplyPolicy()   — apply network/filesystem/process policy
  UpdatePolicy()  — hot-reload policy
  CurrentPolicy() — inspect active policy
```

**Not yet wired:** The runtime package defines interfaces and implementations but
`spawn.Launch()` still uses the old direct path. Wiring runtime selection into the
launcher is a future task.

### Config Layering

```
~/.aimux/config.yaml        — global defaults
.aimux/config.yaml          — project-local overrides (committed to git, shared with team)
CLI flags                   — per-invocation overrides
```

`config.LoadProject(dir, global)` merges project-local over global. Non-zero values win.
Not yet wired into `app.go` startup (the `LoadProject` function exists but isn't called).

### Parity Gaps to Close (tech debt)

**TUI has, Web API missing:**
- Teams view (`/api/teams`)
- Health check (`/api/health/providers`)
- Agent filter/sort query params on `/api/events`
- Trace search within turns
- Session delete/bulk cleanup endpoints
- Spawn with OTEL toggle

**TUI has, CLI missing:**
- Trace viewing, annotations, cost tracking, kill agent, export

**CLI has, TUI and Web missing:**
- Chat with an idle session (`aimux chat`, `internal/acprun`); web and phone come with the remote listener
- MCP proxy (`aimux mcp proxy`); a daemon, no UI planned
- Remote API (`aimux remote serve`, `internal/remoteapi`); a daemon whose clients are other devices; device login (`remote_access.device_login`, default true; `internal/remoteapi/devicelogin`) admits only devices paired with `aimux remote pair --name <name>` (listed by `aimux remote devices`, cut off by `aimux remote revoke <name>`, which writes the revoke audit line), logged in with a Secure Enclave key (iPhone) or a passkey (web) through `/v1/auth/*`; push registrations stay in `~/.aimux/remote-devices.json`; with `terminal_input.enabled` it also types into Claude Code sessions open in iTerm2 (`POST /v1/sessions/{id}/terminal/messages` and `/terminal/keys`, `terminal` on the session detail, `internal/terminput`; `repo` on the session detail (`internal/gitremote`) and `links` on health (`remote_access.links.jira_base`, `jira_projects`) feed the clients' reference links, pinned by `api/testdata/clients/references/`; `aimux remote terminal-input check` asks macOS for iTerm2 Automation); `aimux remote hooks install|uninstall|status` (hidden `notify`, `internal/waiting`) puts terminal sessions at a permission prompt under Needs you (`waiting` on sessions, pinned by `api/testdata/clients/now/waiting-*.json`) and pushes them like approvals, with its clients under `clients/`: the phone web client at `/app/` (`clients/web`, Go package `webclient`) and the iPhone app (`clients/ios`, SwiftUI, XcodeGen; see its AGENTS.md), the iPhone chat queue is in `clients/ios/AimuxKit/Sources/ChatQueue.swift`, `ChatModelQueue.swift` and `clients/ios/App/Components/QueueList.swift`; both clients of the published contract `api/remote-v1.openapi.yaml`, ranking Now and All with the shared fixtures in `api/testdata/clients/`. Web client files in `clients/web/webapp/`: `index.html`, `app.css`, `app.js` (chat transport and state, new session, settings), `voice.js`, `tabs.mjs` (tab bar and the Now and All screens), `now.mjs` (Now ranking: Needs you, Pinned, the Mac's windows, tabs and panes, Elsewhere; the chat's Next bar target), `nowModel.mjs` (Now state and polling), `nowView.mjs` (Now rendering), `all.mjs` (All sections), `rows.mjs` (shared rows, dots and icons), `apiDate.mjs` (timestamp parser), `timeline.mjs` (the chat timeline from history and events, and a saved message without Claude Code's wrappers, as text, slash commands and command output, pinned by `api/testdata/clients/display/`), `chatView.mjs` (chat rendering), `login.mjs` (passkey pairing, unlock state machine and the `/v1` request wrapper), `chatLoad.mjs` (reading an opened chat consistently, and which read carries `Aimux-Interaction`), `queue.mjs` (the chat queue: composer action, queue section and rows, pinned by `api/testdata/clients/queue/`), `queueView.mjs` (the queue under the composer: edit in place, Move up and down, Delete, Resume), `markdown.mjs` (Markdown to the shared block tree), `highlight.mjs` (syntax colours for code blocks, pinned by `api/testdata/clients/highlight/`; iPhone `Highlight.swift` and `CodeColors.swift`), `references.mjs` (issue, commit, Jira and path links in that tree, and media cards for a reply paragraph that is only a video, image or audio link, pinned by `api/testdata/clients/media/`), `render.mjs` (the tree as DOM, user bubbles' inline-only form, the inline text of thoughts, plan entries, step targets and errors, and an opened step's command, error or edit lines), `sessionInfo.mjs` (the session info sheet and its number formats), `terminal.mjs` (typing into a terminal session: reason lines, pending line, header, polling pause, prompt card); `all`, `apiDate`, `now`, `nowModel`, `tabs` (its poller), `timeline`, `sessionInfo`, `terminal`, `login`, `chatLoad`, `queue`, `queueView`, `markdown` and `references` (which also covers `render`'s pure helpers and, with a small fake document, its code, table, copy and media DOM) have a `.test.mjs`, as does `chatView` for the user bubble's commands and output; the other view files do not; the local web dashboard has no chat view yet; finding sessions: `description` and `pinned` on sessions, `lastOnMac` on the list (focus probe every 15 s, the `last focus` job in `internal/remoteapp`, memory only, clients hide it after 12 h), `PATCH /v1/sessions/{id}/meta` and `POST /v1/sessions/{id}/describe` (capability `describe`, `naming` on health) in `internal/remoteapi/naming_api.go`, titles by `history.ShownTitle` (the owner's `.meta.json` title, Claude Code's `/rename`, `history.Namer`'s, Claude Code's ai-title, the first message); descriptions from the owner, else the newer of Claude Code's away summary and `history.Namer`'s (`history.ShownTitle`, `ShownDescription`) (`sessions.auto_title`, `naming_daily_limit` 20, `title_model`); web files `now.mjs` (`descriptionLine`), `folders.mjs` (All's Folders tree), `search.mjs` (the one search of Now and All), `nowModel.mjs` (pin, `editMeta`, `describe`), `nowView.mjs`, `sessionInfo.mjs` (edit, Update summary, naming line); iPhone files `SessionMeta.swift`, `NowRanking.swift`, `NowModel.swift`, `SearchModel.swift`, `SessionSearch.swift`, `FolderTree.swift`, `NowRows.swift`, `PinnableRow.swift`, `App/Components/SearchField.swift`, `DemoFinding.swift`, pinned by `api/testdata/clients/now/`; Mac layout and folders: `Session.folder`, `mac` and `working` from `internal/terminput/layout.go` and `internal/folders`

**Web has, TUI missing:**
- Plugin listing/execution, AI insights (prompt in `insight.DashboardPrompt`), task complete/reopen

## Provider Architecture

All agent types implement `provider.Provider` (11 methods). This interface must remain the ONLY coupling point between the core system and individual agent backends. Current providers are local CLI agents (Claude, Codex, Gemini), but future providers include remote backends (Kubernetes pods, SSH hosts, cloud APIs).

**Provider design rules:**
1. **Interface is the contract.** Never type-assert to a concrete provider (e.g., `p.(*Claude)`) outside the provider package itself. All provider-specific logic stays inside the provider's own file.
2. **No shared state between providers.** Each provider is self-contained. Shared utilities go in `provider/helpers.go`, not cross-provider imports.
3. **Discovery is provider-owned.** Each provider knows how to find its agents (local process scan, Kubernetes API, SSH, etc.). The orchestrator just calls `Discover()` and merges results.
4. **Local assumptions are isolated.** Methods like `ResumeCommand() *exec.Cmd` and `SpawnCommand() *exec.Cmd` assume local execution. When adding remote providers, these may return nil (not applicable) — the provider signals capabilities via `CanEmbed()`, and future methods like `CanSpawnLocally() bool` can be added. Do NOT force remote semantics into the existing interface; extend it instead.
5. **Trace format is provider-owned.** Each provider implements `ParseTrace` for its own log format. Never add provider-specific parsing logic outside the provider's file.
6. **New providers = one file + register.** Adding a provider should require only creating `internal/provider/yourprovider.go` and registering it in `provider.NewRegistry` (`internal/provider/registry.go`), the only place providers are built. Callers get providers from the registry (`Local`, `Enabled`, `ByName`) and reach a provider-specific capability through an interface (`harness.SessionStore`, `remoteapi.Store`, `provider.LiveSource`, `provider.TranscriptRoot`), never a concrete type. If it requires changes to other providers or core packages, the abstraction is leaking.

## Harness Boundary

Knowledge of one harness (its screen layout, prompt wording, working line, paste marker, process names, text rules, composer keys) must live behind the interfaces in `internal/harness`, implemented once per harness (`internal/harness/claude` for Claude Code). Core packages such as `terminput` call the interface (`terminput.Service` holds a `harness.Composer`) and must not branch on a harness themselves. A new harness (Codex) adds its own implementation and screen fixtures; it adds no cases to the core.

- `internal/harness` must not import any internal package.
- `internal/harness/<name>` must import no internal package other than `internal/harness`.
- `internal/terminput` must not import `internal/provider`; `internal/provider` must not import `internal/terminput`.
- `internal/harness/boundary_test.go` enforces these imports.

Capabilities, and who implements them for Claude Code:
- `harness.Composer`: the terminal composer (screen reads, process names, text rules, room and clear keys); `internal/harness/claude`.
- `harness.LiveSessions`: which processes hold a session, `Holders(harness.List)` for the session list (LocalIndex, terminal input, queue, waiting, layout) and `Holders(harness.Strict)` for the router (`acprun.SessionLookup`); `discovery.LiveCache` over the registry, with `claude.CommandLineSessions` for Strict's command-line fallback. One source and one table of cases (`internal/harness/live.go`), so the list and the router differ only where it says.
- `harness.SessionStore`: transcript file, recorded cwd, fingerprint; `provider.Claude`.
- `provider.LiveSource` (`LiveSessions(ttl)`: the harness's `harness.LiveSessions`) and `provider.TranscriptRoot` (`TranscriptsDir()`: the transcript directory the search index reads); `provider.Claude`, whose `RegistryDir` and `ProjectsDir` the registry sets from home (`~/.claude/sessions`, `~/.claude/projects`), so cmd holds no Claude paths for them.
- `catalog.Store` (alias `remoteapi.Store`): `harness.SessionStore` plus `Name` (the API's `provider` value), `Sessions` and `ParseTail`; `provider.Claude`. It lives in catalog, not harness, because its types (`history.Session`, `trace.Turn`) may not enter `internal/harness`.

Adding a harness to the remote API: register its provider in `provider.NewRegistry` and implement `catalog.Store` and `provider.LiveSource` on it; `remoteapp.Providers` (`internal/remoteapp/services.go`, `catalog.ProvidersOf` over the registry; used by remote serve, `aimux remote terminal-input check` and `aimux mcp serve`) lists every registry provider (`Local()`, in order) with both, so neither it, remoteapp's composition, catalog nor remoteapi changes. Today only Claude has both, which is also the default (the first entry; `NewProvider` falls back to it when unset): sessions a client creates (`NewProvider`), the router's `SessionLookup` and the chat manager's fingerprint are its, since the ACP adapter is Claude Code's. A non-default provider's sessions are listed, followed and searched, but cannot be continued until the router (`SessionLookup`) and the manager's `Fingerprint` dispatch per provider; that comes with the harness's own ACP adapter. When two providers report the same session id, the first store wins.

The Claude Code implementation of the Composer lives in `internal/harness/claude`, not `internal/provider`: `terminput.NewService` defaults to it, and taking it from provider would make terminput depend on provider and everything it imports. When the Service looks up a Composer per session, provider can expose it as an optional capability and the default can go.

Known exceptions, next steps:
- Error codes and texts name Claude (`not_claude`, "Claude is working"); they are API contract and change with a contract version.
- `settle` and `settleReads` in terminput (the pause between typing and Enter, the reads after typing) are tuned to Claude Code's redraw; they belong on the Composer.
- The package functions `terminput.CheckText` and `CheckMessage` apply Claude Code's rules for their callers (the iTerm2 driver, the chat queue); those callers should ask the session's Composer.
- terminput's package doc, `Target` and `ForegroundCommand` docs still say claude.
- Still Claude-only in the remote API: the hooks installer (`internal/waiting`, `~/.claude/settings.json`), the ACP adapter command (`--adapter`, `defaultClaudeAdapter`), the OpenAPI `provider` enum (`enum: [claude]` in `api/remote-v1.openapi.yaml`, a contract change that comes with the Codex work) and the clients' parsing of Claude Code's message wrappers (`timeline.mjs`, `chatqueue/prompt.go`).

## Single-File Verification

```bash
gofmt internal/agent/agent.go           # Check formatting for one file
go vet ./internal/agent/                 # Vet a single package
go test ./internal/agent/ -timeout 30s -run TestName  # Run one test
```

## Building and Testing

```bash
go build -o aimux ./cmd/aimux    # Build
go test ./... -timeout 30s              # All tests (120+ tests)
node --test "clients/web/webapp/*.test.mjs"   # Phone web client (the plain directory form fails on Node 22)
node --test web/src/components/*.test.ts      # Dashboard pure modules (sessionFilter.ts; Node strips the types)
cd clients/ios && xcodegen generate && xcodebuild test -scheme Aimux -project Aimux.xcodeproj -destination 'platform=iOS Simulator,name=<iPhone>'   # iPhone app
make build                              # Build via Makefile
make install                            # Build and copy to /usr/local/bin
```

## Pattern References

- New provider: follow the pattern in `internal/provider/claude.go` and see `docs/adding-a-provider.md` for the full guide
- New TUI view: use `internal/tui/views/agents.go` as a template
- New API endpoint: based on `internal/frontend/web/` existing handlers
- New keybinding: see `internal/tui/app.go` Update() switch for examples

## Adding a New Provider

See `docs/adding-a-provider.md` for the full guide. Summary:
1. Create `internal/provider/yourprovider.go` implementing all 10 Provider interface methods
2. Register in `provider/registry.go` `NewRegistry()` and `config/config.go` `Default()`
3. Add model pricing to `cost/tracker.go`
4. Add tests (compile-time interface check + all methods)

## Dependencies

| Package | Purpose |
|---------|---------|
| `charmbracelet/bubbletea` | TUI framework |
| `charmbracelet/lipgloss` | Terminal styling |
| `charmbracelet/x/vt` | VT emulator for PTY rendering |
| `creack/pty` | Pseudo-terminal creation |
| `go.opentelemetry.io/otel` | OTEL span construction + export |
| `go.opentelemetry.io/proto/otlp` | OTLP protobuf types for receiver |
| `gopkg.in/yaml.v3` | Config file parsing |

## Key Config

```yaml
# ~/.aimux/config.yaml
providers:
  claude:
    enabled: true
  codex:
    enabled: true
  gemini:
    enabled: true
shell: /bin/zsh
otel:
  enabled: true
  port: 4318
export:
  endpoint: "localhost:5001"
  insecure: true
  mlflow:
    experiment_id: "1"
  experiment_id: "1"          # MLflow experiment ID
```

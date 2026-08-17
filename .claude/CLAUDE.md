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
  evaluation/                   # Annotation persistence, JSONL export
  jump/                         # Session resumption (tmux split, iTerm2)
  otel/
    receiver.go                 # OTLP/HTTP receiver (port 4318)
    store.go                    # Span data model + in-memory store
    converter.go                # OTEL span -> trace.Turn bridge
    exporter.go                 # trace.Turn -> OTLP/HTTP export
  provider/
    provider.go                 # Provider interface (10 methods)
    claude.go                   # Claude: full discovery, PTY embed, JSONL parsing
    codex.go                    # Codex: full discovery, tmux mirror, JSONL parsing
    gemini.go                   # Gemini: full discovery, tmux mirror, JSON parsing
    helpers.go                  # Shared: process tree grouping, start time, CWD extraction
  spawn/spawn.go                # Launch agents into tmux/iTerm
  team/reader.go                # Team config reading
  terminal/
    backend.go                  # SessionBackend interface
    embed.go                    # Direct PTY backend (Claude)
    tmux.go                     # Tmux mirror backend (Codex, Gemini)
    view.go                     # VT emulator rendering
  trace/trace.go                # Shared Turn/ToolSpan types
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

- **Provider interface**: All agent types implement `provider.Provider` with 10 methods: Name, Discover, ResumeCommand, CanEmbed, FindSessionFile, RecentDirs, SpawnCommand, SpawnArgs, ParseTrace, OTELEnv. Adding a provider = one Go file + register in app.go.
- **SessionBackend interface**: `terminal.SessionBackend` (Read/Write/Resize/Close/Alive) with two implementations: direct PTY (Claude) and tmux mirror (Codex/Gemini). `DirectRenderer` optional interface skips VT emulator for tmux.
- **Trace parsing**: Each provider owns its parser via `ParseTrace`. Shared types in `internal/trace/`. LogsView receives a `TraceParser` function from app.go.
- **OTEL dual mode**: File-based parsing for display (full responses). OTEL receiver (port 4318) collects live telemetry for export. `parserForProvider` checks file first, falls back to OTEL for new sessions. Trace header shows [FILE] (otel:N). Claude Code sends events via OTEL logs protocol (no response text -- Anthropic privacy design). Export to MLflow via `e` → `o` in split view or `:export-otel`.
- **Export**: `e` key in trace pane opens export menu: `j` for JSONL (to `~/.aimux/exports/`), `o` for OTEL (to configured endpoint). MLflow requires `x-mlflow-experiment-id` header, set via `export.experiment_id` in config.
- **Config**: `~/.aimux/config.yaml` -- providers, shell, export (endpoint + experiment_id), OTEL receiver. Each provider's `OTELEnv(endpoint)` returns the right env vars for its OTEL mechanism.
- **Stable agent ordering**: `sort.SliceStable` with status priority (active first), then alphabetical. Cursor preserved by PID tracking.
- **Multi-session support**: Multiple sessions in the same directory appear as separate entries. Process tree dedup groups child processes (node wrappers) while keeping separate sessions distinct. `assignUniqueSuffixes` adds `#1`, `#2` when names collide.
- **Session file matching**: Claude sessions matched to their JSONL files by correlating process start time (`ps -o lstart=`) with file first-write timestamp. Gemini sessions use per-session `session-*.json` chat files instead of shared `logs.json`.
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

Core (UI-agnostic, MUST NOT import bubbletea/lipgloss/net/http)
  controller/  — operations: sort, filter, attend, archive, kill, notify, export, session_meta
  agent/       — data types, status enum, fade colors
  badge/       — project file badge evaluation
  config/      — global + project-local config, runtime/sandbox profiles
  cost/        — per-model pricing, token counting
  discovery/   — multi-provider orchestrator, process scanning
  history/     — session scanning, metadata, titles, search
  evaluation/  — annotation persistence, JSONL export
  otel/        — OTLP receiver, span store, exporter
  provider/    — Provider interface, Claude/Codex/Gemini/K8s implementations
  runtime/     — Runtime interface (local/container/k8s), PolicyEnforcer, OpenShell stub
  trace/       — shared Turn/ToolSpan types, file tailer
  terminal/    — SessionBackend (PTY embed, tmux mirror)
  spawn/       — agent launch into tmux/direct
  team/        — team config reader
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
- Cost dashboard (`/api/costs`)
- Teams view (`/api/teams`)
- Health check (`/api/health/providers`)
- Agent filter/sort query params on `/api/events`
- Trace search within turns
- Session delete/bulk cleanup endpoints
- Spawn with OTEL toggle

**TUI has, CLI missing:**
- Trace viewing, annotations, cost tracking, kill agent, export

**Web has, TUI missing:**
- Plugin listing/execution, AI insights, task complete/reopen

## Provider Architecture

All agent types implement `provider.Provider` (11 methods). This interface must remain the ONLY coupling point between the core system and individual agent backends. Current providers are local CLI agents (Claude, Codex, Gemini), but future providers include remote backends (Kubernetes pods, SSH hosts, cloud APIs).

**Provider design rules:**
1. **Interface is the contract.** Never type-assert to a concrete provider (e.g., `p.(*Claude)`) outside the provider package itself. All provider-specific logic stays inside the provider's own file.
2. **No shared state between providers.** Each provider is self-contained. Shared utilities go in `provider/helpers.go`, not cross-provider imports.
3. **Discovery is provider-owned.** Each provider knows how to find its agents (local process scan, Kubernetes API, SSH, etc.). The orchestrator just calls `Discover()` and merges results.
4. **Local assumptions are isolated.** Methods like `ResumeCommand() *exec.Cmd` and `SpawnCommand() *exec.Cmd` assume local execution. When adding remote providers, these may return nil (not applicable) — the provider signals capabilities via `CanEmbed()`, and future methods like `CanSpawnLocally() bool` can be added. Do NOT force remote semantics into the existing interface; extend it instead.
5. **Trace format is provider-owned.** Each provider implements `ParseTrace` for its own log format. Never add provider-specific parsing logic outside the provider's file.
6. **New providers = one file + register.** Adding a provider should require only creating `internal/provider/yourprovider.go` and registering in `app.go` NewApp(). If it requires changes to other providers or core packages, the abstraction is leaking.

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
2. Register in `tui/app.go` `NewApp()` and `config/config.go` `Default()`
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

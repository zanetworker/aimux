# aimux -- Project Guide for Claude

## Git Policy

NEVER commit or push code without explicit user approval. Always ask before running `git commit` or `git push`. Show what will be committed (files, summary) and wait for confirmation.

## Coding Discipline

Always invoke the `development-tools:crafted-code` skill before writing the first line of any new feature, bug fix, refactor, or code review. Follow all nine principles in order.

Separation of concerns is non-negotiable: core packages (everything under `internal/` except `tui/`) MUST NOT import `bubbletea`, `lipgloss`, or anything from `tui/`. Business logic belongs in core packages; `tui/` is a thin adapter layer for rendering and key handling only. When in doubt, ask: "does this function reference `tea.Model`, `tea.Cmd`, or `lipgloss`?" If no, it belongs in a core package.

## Pre-Commit Checklist

Before committing or pushing ANY code:
1. Run `go build ./...` -- must compile with zero errors
2. Run `go vet ./...` -- must pass with zero issues
3. Run `go test ./... -timeout 30s` -- ALL packages must pass
4. Check for missing tests: every new method, function, or behavior MUST have tests
5. When fixing something for one provider, verify the same fix applies to all three (Claude, Codex, Gemini)

Never push code that hasn't been built and tested. Never claim work is done without running the test suite.

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

## Architecture Rules (UI-Agnostic Core)

The codebase is split into **core packages** (UI-agnostic) and **TUI packages** (Bubble Tea specific). This separation exists to support future alternative frontends (web UI, API server).

**Core packages** (MUST NOT import `tui/`, `bubbletea`, or `lipgloss`):
- `agent/`, `config/`, `cost/`, `correlator/`, `subagent/` — data types and utilities
- `discovery/`, `provider/` — agent discovery and process scanning
- `history/` — session scanning, metadata, titles, cleanup
- `evaluation/` — annotation storage, JSONL export
- `otel/` — receiver, exporter, converter
- `trace/` — shared turn/span types
- `terminal/` — PTY backends (embed, tmux), VT emulator
- `spawn/`, `jump/`, `team/` — agent launching and session management

**TUI packages** (Bubble Tea specific, thin adapter layer):
- `tui/app.go` — wires core logic to Bubble Tea; should NOT contain business logic
- `tui/views/` — rendering and key handling

**When adding new features:**
1. Put business logic in core packages (e.g., `history.FindDuplicates`, `cost.Calculate`)
2. Put only UI wiring in `tui/` (event handling, rendering, navigation)
3. Never import `charmbracelet/*` from core packages
4. If a function in `app.go` doesn't reference `tea.Model`, `tea.Cmd`, or `lipgloss`, it probably belongs in a core package

## Thin Frontend Rule

The web frontend (`web/src/`) is a rendering layer only. All business logic lives in Go core packages, exposed via HTTP API endpoints in `internal/frontend/web/`.

**Backend owns:**
- Trace parsing (via `provider.ParseTrace`)
- Cost calculation (via `cost.Calculate`)
- Token counting, model identification
- Tool input extraction and snippet generation
- Session discovery and matching
- Search (via `history.SearchContent`)

**Frontend owns:**
- Rendering (React components, styles, layout)
- UI state (expanded/collapsed, selected, fullscreen)
- User interaction (click, keyboard, resize)

**When adding a web feature:**
1. If it needs data transformation, add a Go API endpoint using core packages
2. The frontend fetches and renders; no parsing, no business logic
3. If the TUI already does it, the web must use the same core function
4. Never reimplement Go logic in TypeScript

## Provider Architecture

All agent types implement `provider.Provider` (11 methods). This interface must remain the ONLY coupling point between the core system and individual agent backends. Current providers are local CLI agents (Claude, Codex, Gemini), but future providers include remote backends (Kubernetes pods, SSH hosts, cloud APIs).

**Provider design rules:**
1. **Interface is the contract.** Never type-assert to a concrete provider (e.g., `p.(*Claude)`) outside the provider package itself. All provider-specific logic stays inside the provider's own file.
2. **No shared state between providers.** Each provider is self-contained. Shared utilities go in `provider/helpers.go`, not cross-provider imports.
3. **Discovery is provider-owned.** Each provider knows how to find its agents (local process scan, Kubernetes API, SSH, etc.). The orchestrator just calls `Discover()` and merges results.
4. **Local assumptions are isolated.** Methods like `ResumeCommand() *exec.Cmd` and `SpawnCommand() *exec.Cmd` assume local execution. When adding remote providers, these may return nil (not applicable) — the provider signals capabilities via `CanEmbed()`, and future methods like `CanSpawnLocally() bool` can be added. Do NOT force remote semantics into the existing interface; extend it instead.
5. **Trace format is provider-owned.** Each provider implements `ParseTrace` for its own log format. Never add provider-specific parsing logic outside the provider's file.
6. **New providers = one file + register.** Adding a provider should require only creating `internal/provider/yourprovider.go` and registering in `app.go` NewApp(). If it requires changes to other providers or core packages, the abstraction is leaking.

## Building and Testing

```bash
go build -o aimux ./cmd/aimux    # Build
go test ./... -timeout 30s              # All tests (120+ tests)
make build                              # Build via Makefile
make install                            # Build and copy to /usr/local/bin
```

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

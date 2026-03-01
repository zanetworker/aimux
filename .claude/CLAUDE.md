# agentmux -- Project Guide for Claude

## Pre-Commit Checklist

Before committing or pushing ANY code:
1. Run `go build ./...` -- must compile with zero errors
2. Run `go vet ./...` -- must pass with zero issues
3. Run `go test ./... -timeout 30s` -- ALL packages must pass
4. Check for missing tests: every new method, function, or behavior MUST have tests
5. When fixing something for one provider, verify the same fix applies to all three (Claude, Codex, Gemini)

Never push code that hasn't been built and tested. Never claim work is done without running the test suite.

## What This Is

agentmux is a Go TUI tool that provides a k9s-style dashboard for managing multiple AI coding agent sessions. It discovers running agents (Claude, Codex, Gemini), displays their status, lets you zoom into live sessions, view conversation traces, annotate agent behavior, and export traces via OTEL. Single binary, provider-extensible.

## Project Structure

```
cmd/agentmux/main.go           # CLI entry point
internal/
  agent/agent.go                # Agent struct, Status enum, SourceType
  config/config.go              # Config struct, YAML loading (~/.agentmux/config.yaml)
  cost/tracker.go               # Per-model pricing, cost estimation
  discovery/                    # Process scanning, session file discovery, tmux
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
    helpers.go                  # Shared helpers
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
- **Export**: `e` key in trace pane opens export menu: `j` for JSONL (to `~/.agentmux/exports/`), `o` for OTEL (to configured endpoint). MLflow requires `x-mlflow-experiment-id` header, set via `export.experiment_id` in config.
- **Config**: `~/.agentmux/config.yaml` -- providers, shell, export (endpoint + experiment_id), OTEL receiver. Each provider's `OTELEnv(endpoint)` returns the right env vars for its OTEL mechanism.
- **Stable agent ordering**: `sort.SliceStable` with status priority (active first), then alphabetical. Cursor preserved by PID tracking.

## Building and Testing

```bash
go build -o agentmux ./cmd/agentmux    # Build
go test ./... -timeout 30s              # All tests (107+ provider tests)
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
# ~/.agentmux/config.yaml
providers:
  claude: { enabled: true }
  codex: { enabled: true }
  gemini: { enabled: true }
shell: /bin/zsh
otel:
  enabled: true
  port: 4318
export:
  endpoint: "localhost:5001"
  insecure: true
  experiment_id: "1"          # MLflow experiment ID
```

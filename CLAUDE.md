# agentmux -- Project Guide for Claude

## What This Is

agentmux is a Go TUI tool that provides a k9s-style dashboard for managing multiple AI coding agent sessions. It discovers running agents (Claude, Codex, Gemini), displays their status, lets you zoom into live PTY sessions, view conversation traces, and track costs. Single binary, provider-extensible.

## Project Structure

```
cmd/agentmux/main.go           # CLI entry point. Creates TUI app and runs it.
internal/
  agent/
    agent.go                    # Agent struct, Status enum, SourceType, helper methods
  cost/
    tracker.go                  # Token-based cost estimation per model
  discovery/
    orchestrator.go             # Multi-provider discovery orchestrator
    process.go                  # Process table scanning (ps aux)
    session.go                  # Session file discovery (~/.claude/projects)
    cwd.go                      # Working directory resolution for PIDs
    tmux.go                     # tmux session discovery
  provider/
    provider.go                 # Provider interface + Segment/Role types
    claude.go                   # Claude provider: discover, resume, parse conversation
    codex.go                    # Codex provider stub
    gemini.go                   # Gemini provider stub
  jump/
    resume.go                   # Session resumption logic
    tmux.go                     # tmux split-pane jumping
    iterm.go                    # iTerm2 split-pane via AppleScript
  team/
    reader.go                   # Team config reading (~/.claude/teams)
  terminal/
    embed.go                    # Embedded PTY management (creack/pty)
    view.go                     # VT emulator rendering (charmbracelet/x/vt)
  tui/
    app.go                      # Root Bubble Tea model, wires all views
    layout.go                   # Layout engine (split view, zoomed, sub-views)
    command.go                  # Command palette parsing and tab completion
    styles.go                   # Shared TUI color constants and styles
    views/
      agents.go                 # Agent list table with status, provider, model columns
      preview.go                # Right-side preview pane with agent details
      session.go                # Full-screen zoomed PTY session view
      logs.go                   # Conversation trace viewer
      costs.go                  # Cost dashboard aggregated by project
      teams.go                  # Teams overview
      header.go                 # Top bar with status badges and breadcrumbs
      help.go                   # Help overlay with keybindings
```

## Key Patterns

- **Provider interface**: All agent types implement `provider.Provider` with `Name()`, `Discover()`, `ResumeCommand()`, and `ParseConversation()`. The orchestrator calls all providers in parallel.
- **Agent struct**: The `agent.Agent` struct is the universal data model. It includes `ProviderName` to identify which provider discovered it. All views operate on `[]agent.Agent`.
- **Three-state layout**: The TUI has three layout modes: split view (agents + preview), zoomed session (full-screen PTY), and sub-views (costs, teams, help as full-screen non-interactive).
- **PTY embedding**: When zooming into a session, the provider's `ResumeCommand()` builds an `exec.Cmd` which is run inside a creack/pty pseudo-terminal. Output is rendered through charmbracelet/x/vt into the Bubble Tea view. `Ctrl+]` zooms out without killing the process.
- **Discovery refresh**: The orchestrator runs every 2 seconds via Bubble Tea's tick mechanism. Each provider scans for its processes, enriches with session data, and returns agents.
- **Cost estimation**: Token counts come from session JSONL files. The cost tracker applies per-model rates (opus, sonnet, haiku) to estimate USD spend.

## Building and Testing

```bash
go build -o agentmux ./cmd/agentmux    # Build
go test ./... -timeout 30s              # All tests
make build                              # Build via Makefile
make install                            # Build and copy to /usr/local/bin
```

## Adding a New Provider

1. Create `internal/provider/yourprovider.go`
2. Implement the `Provider` interface: `Name()`, `Discover()`, `ResumeCommand()`, `ParseConversation()`
3. Register it in `tui/app.go` in the `NewApp()` constructor alongside Claude, Codex, Gemini
4. The orchestrator and all views will pick it up automatically

## Dependencies

| Package | Purpose |
|---------|---------|
| `charmbracelet/bubbletea` | TUI framework |
| `charmbracelet/lipgloss` | Terminal styling |
| `charmbracelet/x/vt` | VT emulator for embedded PTY rendering |
| `creack/pty` | Pseudo-terminal creation |

## Environment Variables

- `TERM` -- Set to `xterm-256color` in embedded PTY sessions.

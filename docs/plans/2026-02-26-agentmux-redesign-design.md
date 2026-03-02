# aimux Redesign — Design Document

**Date:** 2026-02-26
**Goal:** Rename claudetopus to aimux, add TUI chrome, embed live sessions in-TUI via PTY, and generalize for multi-agent CLI support (Claude, Codex, Gemini).

## What Changes

### 1. Rename: claudetopus → aimux
- Module path: `github.com/zanetworker/aimux`
- Binary: `aimux`
- All imports, references, strings updated

### 2. Data Model: Instance → Agent
- `model.Instance` becomes `agent.Agent`
- PID is an internal field, not shown in the UI
- UI shows: name (project), agent type, model, permission mode, uptime, cost

### 3. AgentProvider Interface
```go
type Provider interface {
    Name() string
    Discover() ([]agent.Agent, error)
    ResumeCommand(a agent.Agent) *exec.Cmd
    ParseConversation(sessionPath string) ([]Segment, error)
}
```
- `Segment` is the common conversation format (Role + Content + Tool + Detail)
- Claude provider wraps existing discovery code
- Codex and Gemini stubs return empty slices

### 4. TUI
- Header: info boxes (active/waiting/idle counts), session cost, aimux branding
- Table: colored header row, agent-centric columns (NAME, AGENT, MODEL, MODE, AGE, COST)
- Status bar: context-aware hints
- Crumb trail for navigation

### 5. In-TUI Session Pane (PTY Embedding)
- Default: vertical split — agent list (left ~35%) + live preview (right ~65%)
- Preview: read-only conversation view, updates in real-time from session files
- Zoom: Enter expands preview to full screen, routes keystrokes to embedded PTY
- Zoom out: Ctrl+] returns to split view, session continues in background
- PTY managed via `creack/pty`, VT parsing via `charmbracelet/x/vt`

## Architecture

```
aimux
├── cmd/aimux/main.go
├── internal/
│   ├── provider/
│   │   ├── provider.go          # Interface, Segment, Role types
│   │   ├── claude.go            # Claude CLI provider (real)
│   │   ├── codex.go             # Codex stub
│   │   └── gemini.go            # Gemini CLI stub
│   ├── agent/
│   │   └── agent.go             # Agent data model (was model/instance.go)
│   ├── discovery/
│   │   ├── orchestrator.go      # Iterates providers, merges results
│   │   ├── process.go           # Shared ps-based process scanning
│   │   ├── session.go           # Session file lookup utilities
│   │   ├── cwd.go               # lsof-based CWD resolution
│   │   └── tmux.go              # tmux session matching
│   ├── terminal/
│   │   ├── embed.go             # PTY spawn, read loop, resize
│   │   └── view.go              # VT → lipgloss rendering for Bubble Tea
│   ├── tui/
│   │   ├── app.go               # Root model: split pane, zoom, state machine
│   │   ├── layout.go            # Split pane geometry calculator
│   │   ├── styles.go            # color palette, component styles
│   │   ├── keymap.go            # Key bindings (extracted for testability)
│   │   ├── commands.go          # Command palette (was command.go)
│   │   └── views/
│   │       ├── header.go        # Info boxes + branding
│   │       ├── agents.go        # Agent table (was instances.go)
│   │       ├── preview.go       # Live conversation preview (read-only)
│   │       ├── session.go       # Zoomed interactive PTY session
│   │       ├── costs.go         # Cost dashboard
│   │       ├── teams.go         # Teams overview
│   │       └── help.go          # Help overlay
│   ├── cost/
│   │   └── tracker.go           # Unchanged
│   └── team/
│       └── reader.go            # Unchanged
└── go.mod
```

## State Machine

```
                    ┌─────────────┐
                    │  AgentList  │ (split: list + preview)
                    └──────┬──────┘
                  Enter    │    Esc
                    ┌──────▼──────┐
                    │   Zoomed    │ (full-screen interactive PTY)
                    └──────┬──────┘
                  Ctrl+]   │
                    ┌──────▼──────┐
                    │  AgentList  │ (back to split)
                    └─────────────┘

Commands (:costs, :teams, :help) navigate to sub-views from AgentList.
Esc from any sub-view returns to AgentList.
```

## PTY Embedding Flow

1. User selects agent in list, right pane shows conversation preview (parsed from session JSONL)
2. User presses Enter → aimux spawns PTY with provider's ResumeCommand
3. PTY output is fed through charmbracelet/x/vt → rendered as cells in the view
4. Keystrokes routed to PTY stdin
5. User presses Ctrl+] → PTY stays alive in background, view returns to split
6. Re-selecting the same agent reconnects to the running PTY (no new spawn)

## Agent Data Model

```go
type Agent struct {
    Name           string        // project name (derived from working dir)
    ProviderName   string        // "claude", "codex", "gemini"
    Model          string        // "opus-4", "sonnet", "gpt-4o", etc.
    Status         Status        // Active, Idle, Waiting
    PermissionMode string        // "dangerously", "plan", "default"
    PID            int           // internal, not shown in UI
    SessionID      string        // provider-specific session identifier
    WorkingDir     string        // project root
    TMuxSession    string        // matched tmux session name
    SessionFile    string        // path to conversation log
    StartedAt      time.Time     // process start time
    TokensIn       int64
    TokensOut      int64
    EstCostUSD     float64
}
```

## Key Design Decisions

- **PTY per agent, not per view**: PTY lifecycle tied to agent, survives zoom in/out
- **Preview is file-based, zoom is PTY-based**: preview reads JSONL (cheap, read-only), zoom spawns real terminal (interactive)
- **Provider stubs are honest**: Codex/Gemini stubs return empty, not mock data
- **No plugin system**: providers are compiled in, added by appending to a slice
- **Segment as common currency**: all providers convert to Segment, TUI never sees provider internals
- **Existing code restructured, not rewritten**: discovery, cost, team code moves to new packages with minimal changes

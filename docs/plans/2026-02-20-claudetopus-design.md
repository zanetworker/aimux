# Claudetopus Design

A TUI control plane for managing multiple Claude Code instances.

## Problem

Power users run many Claude Code instances simultaneously: CLI sessions in tmux, VS Code panels, remote-claude SDK agents. There is no unified way to see what each instance is doing, check logs, track costs, or switch between them. Current workflow relies on tmux shortcuts and manual process inspection.

## Solution

Claudetopus provides a terminal UI that discovers all running Claude Code instances, shows their status in a unified dashboard, and lets users drill into logs, jump to sessions, manage lifecycle, and track costs.

## Tech Stack

- **Language:** Go
- **TUI Framework:** Bubble Tea (bubbletea + lipgloss + bubbles)
- **File Watching:** fsnotify
- **Process Discovery:** os/exec (ps) + procfs parsing

## Architecture

### Instance Discovery

Discovery is process-based. Claudetopus polls `ps aux` every 2 seconds, filtering for processes whose binary path contains `claude`. Command-line arguments are parsed to extract structured metadata.

No daemon or hooks required. Works immediately with zero setup.

### Data Model

```go
type SourceType int

const (
    SourceCLI    SourceType = iota // Terminal / tmux session
    SourceVSCode                   // VS Code embedded panel
    SourceSDK                      // remote-claude / Agent SDK
)

type Status int

const (
    StatusActive            Status = iota // Recent JSONL activity
    StatusIdle                            // No activity >30s
    StatusWaitingPermission               // Permission prompt detected
    StatusUnknown                         // Process exists, no session data
)

type Instance struct {
    PID            int
    SessionID      string
    Model          string     // e.g. "claude-opus-4-6[1m]"
    PermissionMode string     // default, plan, bypassPermissions, etc.
    WorkingDir     string     // cwd of the process
    Project        string     // derived from WorkingDir (last 2 path segments)
    Source         SourceType
    StartTime      time.Time
    Status         Status
    TMuxSession    string     // matched tmux session name, if any
    MemoryMB       uint64     // RSS
    GitBranch      string     // from session JSONL

    // Cost tracking
    TokensIn       int64
    TokensOut      int64
    EstCostUSD     float64

    // Team context
    TeamName       string
    TaskID         string
    TaskSubject    string
}
```

### Data Sources

| Source | Location | What it provides |
|--------|----------|-----------------|
| Process table | `ps aux \| grep claude` | PID, binary path, CLI flags, memory, start time |
| Session JSONL | `~/.claude/projects/*/` | Messages, tool calls, progress events, usage/tokens |
| Debug logs | `~/.claude/debug/` | Plugin loading, MCP connections, errors |
| History | `~/.claude/history.jsonl` | Command history across all sessions |
| Teams | `~/.claude/teams/*/config.json` | Team membership, agent names/types |
| Tasks | `~/.claude/tasks/*/` | Task lists, status, ownership |
| tmux | `tmux list-sessions` | Session names for jump-to support |

### Status Detection

- **Active** -- process running AND recent writes to session JSONL (<30s)
- **Idle** -- process running AND no recent JSONL activity (>30s)
- **WaitingPermission** -- JSONL contains a `permission_prompt` event as the latest entry
- **Unknown** -- process exists but no matching session data found

### Cost Tracking

Token usage is parsed from `usage` fields in session JSONL entries. Per-model pricing:

| Model | Input (per 1M) | Output (per 1M) |
|-------|----------------|-----------------|
| claude-opus-4-6 | $15.00 | $75.00 |
| claude-sonnet-4-5 | $3.00 | $15.00 |
| claude-haiku-3-5 | $0.80 | $4.00 |

Pricing table is hardcoded but trivially updatable. Costs tracked per session and aggregated per day.

## TUI Design

### Navigation (k9s-style)

The `:` key opens a command palette at the bottom of the screen. Resource types are switched via commands, not number keys.

| Command | Alias | View |
|---------|-------|------|
| `:instances` | `:i` | Instance list (default view) |
| `:logs` | `:l` | Live log stream for selected instance |
| `:session` | `:s` | Session detail (messages, tool calls) |
| `:teams` | `:t` | Teams and task coordination |
| `:costs` | `:c` | Cost dashboard |
| `:help` | `:?` | Command reference |
| `:new` | `:n` | Launch new session |
| `:kill` | | Kill selected instance |
| `:quit` | `:q` | Exit claudetopus |

Tab completion is supported in the command palette.

### Key Bindings

| Key | Action |
|-----|--------|
| `j` / `k` | Navigate up/down |
| `Enter` | Drill into selected item |
| `Esc` | Back to previous view |
| `J` | Jump to session (attach/focus) |
| `/` | Filter/search |
| `?` | Help |
| `q` | Quit (from top-level) |

### Instances View (default)

```
┌─ claudetopus ── Instances(15) ── $4.23 today ── [ctx: all] ──────────┐
│                                                                       │
│  PID    STATUS   MODEL        PROJECT          PERM     MEM    COST   │
│  3629   ●active  opus-4.6     claudetopus      bypass   405M   $0.82 │
│  96297  ●active  opus-4.6     demoharness      bypass   1.4G   $2.10 │
│  14152  ○idle    opus-4.6     excalidraw-mcp   default  128M   $0.45 │
│  3347   ◐wait    opus-4.6     app-interface    plan     108M   $0.31 │
│  97069  ●active  opus-4.6     remote-claude    bypass   402M   $0.55 │
│  ...                                                                  │
│                                                                       │
├───────────────────────────────────────────────────────────────────────┤
│ :command  j/k:nav  Enter:drill  /:filter  ?:help                     │
└───────────────────────────────────────────────────────────────────────┘
```

### Logs View

Entered by pressing `Enter` on an instance from the Instances view. Shows a live-streamed tail of the session JSONL, formatted for readability. Breadcrumb updates: `Instances > [PID 3629] > Logs`.

### Session View

Detailed message-level view: user prompts, assistant responses, tool calls with results. Parsed from the session JSONL file.

### Costs View

```
┌─ claudetopus ── Costs ── Today: $12.47 ── This Week: $84.20 ─────────┐
│                                                                        │
│  PROJECT              MODEL       TOKENS IN   TOKENS OUT   COST       │
│  demoharness          opus-4.6    245,000     48,000       $7.28      │
│  claudetopus          opus-4.6    128,000     22,000       $3.57      │
│  app-interface        opus-4.6    45,000      8,000        $1.28      │
│  excalidraw-mcp       sonnet-4.5  12,000      3,000        $0.08      │
│  ─────────────────────────────────────────────────────────────────     │
│  TOTAL                            430,000     81,000       $12.47     │
│                                                                        │
└────────────────────────────────────────────────────────────────────────┘
```

### Teams View

```
┌─ claudetopus ── Teams(2) ──────────────────────────────────────────────┐
│                                                                        │
│  ▸ default (3 members)                                                 │
│    researcher    ●active   Task #2: Explore codebase patterns          │
│    implementer   ○idle     Task #3: Implement auth module (blocked)    │
│    reviewer      ◐waiting  --                                          │
│                                                                        │
│  ▸ openai-responses-api-research (2 members)                           │
│    lead          ●active   Task #1: Compare API response formats       │
│    analyst       ○idle     --                                          │
│                                                                        │
└────────────────────────────────────────────────────────────────────────┘
```

## Session Management

### Jump To Session (hybrid approach)

When pressing `J` on a selected instance:

1. **tmux session detected** -- `tmux attach-session -t <name>`
2. **iTerm2 detected** -- AppleScript to focus the iTerm2 tab/window containing the PID
3. **Neither** -- Fall back to built-in log viewer (same as pressing Enter)

Detection: check if PID's parent is a tmux server process, or if the terminal is iTerm2 via environment variable inspection.

### Launch New Session

`:new` or `N` triggers a dialog:
- Select project directory (autocomplete from `~/.claude/projects/`)
- Select model (opus/sonnet/haiku)
- Select permission mode (default/plan/bypass)
- Spawns `claude` in a new tmux session named `claude-<project>`

### Kill Session

`:kill` or `ctrl-k` on selected instance:
- Confirmation prompt
- Sends SIGTERM to the process
- Instance removed from list on next poll cycle

## Package Structure

```
claudetopus/
├── cmd/
│   └── claudetopus/
│       └── main.go           # Entry point
├── internal/
│   ├── discovery/
│   │   ├── process.go        # Process table scanning
│   │   ├── session.go        # JSONL session parsing
│   │   ├── tmux.go           # tmux session matching
│   │   └── watcher.go        # fsnotify file watching
│   ├── model/
│   │   └── instance.go       # Instance data model
│   ├── cost/
│   │   └── tracker.go        # Token counting and cost calculation
│   ├── team/
│   │   └── reader.go         # Team/task file parsing
│   ├── tui/
│   │   ├── app.go            # Root Bubble Tea model
│   │   ├── views/
│   │   │   ├── instances.go  # Instances table view
│   │   │   ├── logs.go       # Log stream view
│   │   │   ├── session.go    # Session detail view
│   │   │   ├── teams.go      # Teams/tasks view
│   │   │   ├── costs.go      # Cost dashboard view
│   │   │   └── help.go       # Help overlay
│   │   ├── command.go        # : command palette
│   │   ├── filter.go         # / filter input
│   │   └── styles.go         # lipgloss styles
│   └── jump/
│       ├── tmux.go           # tmux attach
│       ├── iterm.go          # iTerm2 AppleScript focus
│       └── fallback.go       # Built-in log viewer
├── docs/
│   └── plans/
├── go.mod
├── go.sum
├── Makefile
└── .gitignore
```

## Non-Goals for v1

- No web UI (terminal only)
- No persistent database (all data read from filesystem at runtime)
- No remote monitoring (local machine only)
- No Claude Code source modifications required

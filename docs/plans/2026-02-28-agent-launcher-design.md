# Agent Launcher Design

## Context

aimux currently discovers and inspects running agents but can't spawn new ones. Users must open a separate terminal, cd to the project directory, and run `claude`/`codex` manually. This adds friction and defeats the purpose of a unified dashboard.

The launcher lets users spawn agents from within aimux — pick a provider, pick a directory, configure options, launch. The agent runs in a tmux session (durable, survives aimux exit) or iTerm pane (visible), and appears in the agent list via normal discovery.

## User Flow

`:new` (or `:n`) opens a launcher overlay with three steps:

1. **Pick provider** — claude, codex, gemini (j/k to select, Enter to confirm)
2. **Pick directory** — Two tabs switchable with Tab:
   - **Recent**: directories from session file history, sorted by last used, type to fuzzy-filter
   - **Browse**: filesystem navigator starting from `~`, j/k/Enter to descend, Backspace to go up
3. **Pick options** — Model (default/opus/sonnet/haiku), Mode (default/bypass/plan), Runtime (tmux/iterm). Arrow keys to select, all have defaults so Enter×3 is the fast path.

Enter launches. Esc cancels at any step.

## Architecture

### New files

```
internal/
  spawn/
    spawn.go       # command construction + execution (tmux or iTerm)
    spawn_test.go
    recent.go      # scan session dirs for recent project directories
    recent_test.go
  tui/
    views/
      launcher.go      # overlay UI component
      launcher_test.go
```

### spawn.go

Pure functions, no TUI concerns:

```go
type LaunchConfig struct {
    Provider string // "claude", "codex", "gemini"
    Dir      string // absolute path to working directory
    Model    string // "" for default
    Mode     string // "" for default, "bypass", "plan"
    Runtime  string // "tmux" or "iterm"
}

func Spawn(cfg LaunchConfig) error
func buildCommand(cfg LaunchConfig) *exec.Cmd
func tmuxSessionName(provider, dir string) string
```

- `buildCommand`: constructs the exec.Cmd with provider-specific flags
  - Claude: `claude [--model X] [--dangerously-skip-permissions | --permission-mode Y]`
  - Codex: `codex [--model X] [--full-auto | --sandbox Y]`
  - Gemini: `gemini`
- `Spawn`: either creates a tmux session or calls iTerm2SplitPane
- tmux session name: `aimux-<provider>-<basename(dir)>` (e.g., `aimux-claude-blog`)

### recent.go

```go
func RecentDirs() []RecentDir
type RecentDir struct {
    Path     string
    LastUsed time.Time
    Provider string // which provider was used here
}
```

Scans:
- `~/.claude/projects/*/` — decode dir-key back to path, stat newest .jsonl for timestamp
- `~/.codex/sessions/YYYY/MM/DD/*.jsonl` — read session_meta for cwd

Deduplicates by path, sorts by LastUsed descending. Capped at 20 entries.

### launcher.go

Bubble Tea component with three states:

```go
type launcherState int
const (
    statePickProvider launcherState = iota
    statePickDirectory
    statePickOptions
)

type LauncherView struct {
    state     launcherState
    providers []string
    cursor    int
    // Directory picker
    recentDirs []spawn.RecentDir
    browsePath string
    browseMode bool // false=recent, true=browse
    filterText string
    dirEntries []os.DirEntry
    // Options
    model   int // index into model list
    mode    int
    runtime int
    // Result
    config *spawn.LaunchConfig
}
```

Rendered as an overlay (centered box) on top of the agent list.

Returns `LaunchMsg` when confirmed, `nil` when cancelled.

### app.go wiring

- `:new` / `:n` sets `a.launcherActive = true`, creates LauncherView
- When active, all keys route to LauncherView
- LauncherView emits `LaunchMsg` → app calls `spawn.Spawn(msg)` → status hint
- Agent appears in list on next discovery tick (2s)

## Tests

| File | Tests | Method |
|------|-------|--------|
| spawn_test.go | buildCommand per provider (correct binary, flags, dir) | Verify exec.Cmd.Args |
| spawn_test.go | tmuxSessionName formatting | String comparison |
| spawn_test.go | buildCommand with model/mode options | Verify flags present |
| recent_test.go | Claude recent dirs from temp session files | t.TempDir() with mock structure |
| recent_test.go | Codex recent dirs from temp JSONL with session_meta | t.TempDir() with mock JSONL |
| recent_test.go | Dedup and sort by recency | Multiple files same dir |
| launcher_test.go | State transitions provider→dir→options→launch | Send key messages |
| launcher_test.go | Fuzzy filter on recent dirs | Set filter, verify list |
| launcher_test.go | Esc cancels at each step | Verify no LaunchMsg |
| launcher_test.go | Enter with defaults fast path | Verify LaunchConfig populated |

## Safety

- No changes to discovery, providers, or agent struct
- Spawn is fire-and-forget — aimux doesn't manage the child process lifecycle
- tmux sessions persist independently of aimux
- No credentials or tokens passed through the launcher
- Session names are deterministic and predictable

## Verification

1. `go build ./...` compiles
2. `go test ./... -timeout 30s` all pass
3. Manual: `:new` → pick claude → pick a recent dir → Enter → agent appears in list
4. Manual: `:new` → browse to a new dir → pick codex → launch in iTerm
5. Manual: Esc at each step cancels cleanly
6. Manual: kill aimux, verify tmux session survives (`tmux ls`)

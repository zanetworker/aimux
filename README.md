# agentmux

Multiplex your AI agents.

A k9s-style TUI dashboard for managing multiple AI coding agent sessions -- Claude, Codex, Gemini -- from a single terminal. Discover running agents, view conversation traces, zoom into live PTY sessions, and track costs across projects.

```
┌───────────────────────────────────────────────────────────────────────────────┐
│  ┌──────────────┐ ┌──────────────┐ ┌──────────────┐           agentmux      │
│  │ ● Active   2 │ │ ◐ Waiting  0 │ │ ○ Idle     1 │                         │
│  └──────────────┘ └──────────────┘ └──────────────┘                         │
│  Agents                                                                      │
├────────────────────────────────┬──────────────────────────────────────────────┤
│ NAME          AGENT  MODEL    │  ● claudetopus                              │
│───────────────────────────────│  claude · opus-4.6 · dangerously            │
│▸● claudetopus claude opus-4.6│──────────────────────────────────────────────│
│  dangerously · 14m · $0.82   │ 14:32 USER  add k9s header to the TUI      │
│                               │ 14:32 ASST  I'll redesign the header...    │
│ ● trustyai    claude sonnet  │ 14:33 TOOL  Edit styles.go                  │
│  plan · 8m · $0.31           │ 14:33 ASST  Updated. Build succeeded.      │
│                               │                                             │
│ ○ llama-stack claude haiku   │                                [12/12] ●    │
│  default · 2h · $0.11        │                                             │
├────────────────────────────────┴──────────────────────────────────────────────┤
│ :cmd  j/k:nav  Enter:zoom  l:trace  /:filter  ?:help                       │
└───────────────────────────────────────────────────────────────────────────────┘
```

## Why

If you run multiple AI coding agents -- across terminal tabs, VS Code panels, tmux sessions -- you have no unified way to:

- **See what each agent is doing** -- which project, what model, is it idle or active?
- **Check the conversation trace** -- what prompts were sent, what tools were called?
- **Jump into a session** -- zoom into a running agent's PTY without hunting through tabs
- **Track costs** -- token usage and estimated spend per project, per provider

agentmux solves all of these from a single terminal.

## Install

**Requirements:** Go 1.24+

```bash
# From source
git clone https://github.com/zanetworker/agentmux.git
cd agentmux
make install    # builds and copies to /usr/local/bin

# Or just build locally
make build
./agentmux
```

## Usage

```bash
agentmux     # launch the TUI
```

For the best experience, run inside tmux:

```bash
tmux new -s control-plane
agentmux
```

This enables split-pane session opening -- press Enter on any agent and its PTY session opens in a pane below, with agentmux still visible above.

## Views

### Agents (default)

The main dashboard. Shows all running AI coding agent processes with their status, provider, model, project, permission mode, uptime, and estimated cost.

Split layout: agent list on the left (~35%), preview pane with conversation trace on the right (~65%).

**Status indicators:**
- `●` Active -- recent conversation activity
- `○` Idle -- no activity in the last 30 seconds
- `◐` Waiting -- blocked on a permission prompt
- `?` Unknown -- process found but no session data

### Conversation Trace (`l`)

Press `l` on any agent to see its conversation trace -- a chronological view of user prompts, assistant responses, and tool calls.

```
 17:32:28 USER fix the authentication bug in login.go
 17:32:37 ASST I'll look at the login.go file to understand the issue.
 17:32:38 TOOL Read /src/auth/login.go
 17:32:39 TOOL Grep /pattern: handleAuth/
 17:32:41 ASST Found the issue. The token validation is missing...
 17:32:45 TOOL Edit /src/auth/login.go
 17:32:46 TOOL Bash $ go test ./src/auth/ -v
 17:32:50 ASST Fixed. The tests pass now.
```

### Costs (`:costs`)

Aggregated token usage and estimated cost per project, with provider column.

```
 PROJECT              AGENT      MODEL       TOKENS IN   TOKENS OUT   COST
 demoharness          claude     opus-4.6    245.0K      48.0K        $7.28
 claudetopus          claude     opus-4.6    128.0K      22.0K        $3.57
 app-interface        claude     opus-4.6    45.0K       8.0K         $1.28
 ────────────────────────────────────────────────────────────────────────
 TOTAL                                       418.0K      78.0K        $12.13
```

### Teams (`:teams`)

Shows Claude Code team configurations and their members.

### Help (`?`)

Full keybinding reference and provider support status.

## Key Bindings

### Navigation

| Key | Action |
|-----|--------|
| `j` / `k` | Move cursor down / up |
| `g` / `G` | Jump to top / bottom |
| `Enter` / `J` | Zoom into agent session (interactive PTY) |
| `Ctrl+]` | Zoom out of session (keep session alive) |
| `l` | View conversation trace |
| `Esc` | Go back |
| `/` | Filter agents |
| `?` | Show help |
| `q` | Quit (from agents view) |

### Trace View

| Key | Action |
|-----|--------|
| `j` / `k` | Scroll line by line |
| `d` / `u` | Page down / page up |
| `g` / `G` | Jump to top / bottom (G = follow latest) |
| `Esc` | Back to agents |

### Commands (press `:`)

| Command | Alias | View |
|---------|-------|------|
| `:instances` | `:i` | Agent list |
| `:logs` | `:l` | Conversation trace |
| `:teams` | `:t` | Teams overview |
| `:costs` | `:c` | Cost dashboard |
| `:help` | `:?` | Help |
| `:quit` | `:q` | Exit |

Tab completion works in the command palette.

## Configuration

agentmux looks for a config file at `~/.agentmux/config.yaml`. If the file doesn't exist, all defaults are used.

```yaml
# Enable/disable providers
providers:
  claude:
    enabled: true
  codex:
    enabled: true
  gemini:
    enabled: false

# Discovery refresh interval
refresh_interval: "2s"

# Default runtime for launching new agents: "tmux" or "iterm"
default_runtime: tmux
```

Providers can also specify a custom binary path:

```yaml
providers:
  claude:
    enabled: true
    binary: /opt/homebrew/bin/claude
```

## Provider System

agentmux uses a provider interface to support multiple AI coding agents. Each provider implements discovery, session resumption, conversation parsing, and spawning. Providers can be enabled or disabled via the config file.

| Provider | Discovery | Resume | Trace | Embed | Spawn |
|----------|-----------|--------|-------|-------|-------|
| Claude | Process scanning + session JSONL | PTY embed via `claude --resume` | JSONL parsing | Yes | `claude` CLI |
| Codex | Process scanning + session JSONL | Trace-only (jump out with `J`) | JSONL parsing | No | `codex` CLI |
| Gemini | Stub | Stub | Stub | No | `gemini` CLI |

Adding a new provider requires implementing the `Provider` interface and registering it in `app.go`:

```go
type Provider interface {
    Name() string
    Discover() ([]agent.Agent, error)
    ResumeCommand(a agent.Agent) *exec.Cmd
    ParseConversation(sessionPath string) ([]Segment, error)
    CanEmbed() bool
    FindSessionFile(a agent.Agent) string
    RecentDirs(max int) []RecentDir
    SpawnCommand(dir, model, mode string) *exec.Cmd
    SpawnArgs() SpawnArgs
}
```

## Architecture

### Data Sources

agentmux reads everything from the filesystem. No daemon, no hooks, no modifications to your AI tools required.

| Source | Location | Data |
|--------|----------|------|
| Config | `~/.agentmux/config.yaml` | Provider enable/disable, runtime prefs |
| Process table | `ps aux` | PID, binary path, CLI flags, memory |
| Session logs | `~/.claude/projects/*/`, `~/.codex/sessions/` | Messages, tool calls, token usage |
| Teams | `~/.claude/teams/*/config.json` | Team membership |
| tmux | `tmux list-sessions` | Session names for jump support |

### Session Embedding

When you press Enter on an agent, agentmux opens an embedded PTY session directly in the TUI. The agent's CLI process runs inside a pseudo-terminal, with its output rendered through a VT emulator (charmbracelet/x/vt) into the Bubble Tea view. Press `Ctrl+]` to zoom out without killing the session.

### Discovery Pipeline

On startup, agentmux loads `~/.agentmux/config.yaml` and registers only the enabled providers. The orchestrator then queries all registered providers in parallel. Each provider scans for its agent's processes, enriches them with session data (model, tokens, status), and returns `agent.Agent` structs. The TUI refreshes every 2 seconds.

## Built With

- [Bubble Tea](https://github.com/charmbracelet/bubbletea) -- TUI framework
- [Lip Gloss](https://github.com/charmbracelet/lipgloss) -- Styling
- [charmbracelet/x/vt](https://github.com/charmbracelet/x) -- VT emulator for PTY embedding
- [creack/pty](https://github.com/creack/pty) -- PTY creation
- Go standard library -- process scanning, JSONL parsing

## License

MIT

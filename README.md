<p align="center">
  <img src="assets/logo.png" width="128" alt="agentmux logo">
  <br>
  <strong>agentmux</strong><br>
  <sub>k9s for your AI coding agents</sub>
</p>

<p align="center">
  <a href="https://github.com/zanetworker/agentmux/blob/main/LICENSE"><img src="https://img.shields.io/github/license/zanetworker/agentmux?style=flat-square" alt="License"></a>
  <img src="https://img.shields.io/badge/go-1.24%2B-00ADD8?style=flat-square&logo=go" alt="Go 1.24+">
  <a href="https://github.com/zanetworker/agentmux/releases"><img src="https://img.shields.io/github/v/release/zanetworker/agentmux?style=flat-square&include_prereleases&label=release" alt="Release"></a>
</p>

A single-pane-of-glass TUI for managing multiple AI coding agent sessions -- Claude, Codex, Gemini -- from one terminal. Discover running agents, view conversation traces, zoom into live PTY sessions, launch new agents, and track costs across projects.

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

<!-- TODO: replace with a real terminal recording (GIF or SVG via vhs/asciinema) -->

## Table of Contents

- [Why](#why)
- [Quick Start](#quick-start)
- [Features](#features)
- [Key Bindings](#key-bindings)
- [Configuration](#configuration)
- [Provider System](#provider-system)
- [Architecture](#architecture)
- [Built With](#built-with)
- [License](#license)

## Why

If you run multiple AI coding agents across terminal tabs, VS Code panels, and tmux sessions, you have no unified way to see what they're all doing. agentmux fixes that.

| Problem | agentmux |
|---------|----------|
| Which agents are running? What model? Idle or active? | Live dashboard with status, model, cost per agent |
| What did the agent just do? What tools did it call? | Conversation trace viewer with filtering |
| I need to interact with that agent | Zoom into a live PTY session, or jump out to a split pane |
| How much am I spending? | Cost dashboard aggregated by project |
| I want to launch a new agent from here | Built-in launcher with recent directories |

## Quick Start

```bash
git clone https://github.com/zanetworker/agentmux.git
cd agentmux
make install       # builds and copies to /usr/local/bin
agentmux           # launch the TUI
```

Requires **Go 1.24+**. For the best experience, run inside tmux -- this enables split-pane session embedding.

## Features

**Discovery** -- automatically finds running Claude, Codex, and Gemini processes. Enriches each with session data, model, token usage, git branch, and permission mode. Refreshes every 2 seconds.

**Conversation Trace** -- press `l` on any agent to see a chronological view of user prompts, assistant responses, and tool calls. Filter with `/`, annotate turns with `a`, export with `:export`.

```
 17:32:28 USER fix the authentication bug in login.go
 17:32:37 ASST I'll look at the login.go file to understand the issue.
 17:32:38 TOOL Read /src/auth/login.go
 17:32:41 ASST Found the issue. The token validation is missing...
 17:32:45 TOOL Edit /src/auth/login.go
 17:32:50 ASST Fixed. The tests pass now.
```

**Session Embedding** -- press `Enter` on a Claude agent to open a split view: live trace on the left, interactive PTY session on the right. For providers that can't embed (Codex), press `J` to jump out to a tmux or iTerm split pane.

**Agent Launcher** -- press `:new` to spawn a new Claude, Codex, or Gemini session. Pick from recent project directories, choose model and mode, launch into tmux or iTerm.

**Cost Dashboard** -- aggregated token usage and estimated USD spend per project.

```
 PROJECT              AGENT      MODEL       TOKENS IN   TOKENS OUT   COST
 demoharness          claude     opus-4.6    245.0K      48.0K        $7.28
 claudetopus          claude     opus-4.6    128.0K      22.0K        $3.57
 app-interface        claude     opus-4.6    45.0K       8.0K         $1.28
 ────────────────────────────────────────────────────────────────────────
 TOTAL                                       418.0K      78.0K        $12.13
```

**Teams** -- view Claude Code team configurations and their members.

## Key Bindings

<details>
<summary><strong>Navigation</strong></summary>

| Key | Action |
|-----|--------|
| `j` / `k` | Move cursor down / up |
| `g` / `G` | Jump to top / bottom |
| `Enter` | Zoom into agent session (split view with trace + PTY) |
| `J` | Jump to session in external terminal pane |
| `Ctrl+]` / `Esc` | Zoom out (keep session alive) |
| `l` | View conversation trace |
| `x` | Kill agent process |
| `/` | Filter agents |
| `s` | Cycle sort order |
| `?` | Show help |
| `q` | Quit |

</details>

<details>
<summary><strong>Split View (zoomed session)</strong></summary>

| Key | Action |
|-----|--------|
| `Tab` | Switch focus between trace and session panes |
| `Ctrl+f` | Toggle fullscreen on focused pane |
| `Esc` / `Ctrl+]` | Exit split view |

</details>

<details>
<summary><strong>Trace View</strong></summary>

| Key | Action |
|-----|--------|
| `j` / `k` | Scroll line by line |
| `d` / `u` | Page down / page up |
| `g` / `G` | Jump to top / bottom (G follows latest) |
| `Enter` / `Space` | Expand / collapse turn |
| `/` | Search / filter |
| `a` | Annotate turn (GOOD / BAD / WASTE) |
| `c` | Collapse all turns |

</details>

<details>
<summary><strong>Commands (press <code>:</code>)</strong></summary>

| Command | Alias | Action |
|---------|-------|--------|
| `:instances` | `:i` | Agent list |
| `:logs` | `:l` | Conversation trace |
| `:teams` | `:t` | Teams overview |
| `:costs` | `:c` | Cost dashboard |
| `:new` | | Launch new agent |
| `:export` | | Export trace as JSONL |
| `:help` | `:?` | Help |
| `:quit` | `:q` | Exit |

Tab completion works in the command palette.

</details>

## Configuration

agentmux looks for `~/.agentmux/config.yaml`. All settings are optional -- if the file doesn't exist, all providers are enabled with sensible defaults.

```yaml
providers:
  claude:
    enabled: true
  codex:
    enabled: true
  gemini:
    enabled: false            # disable providers you don't use

refresh_interval: "2s"        # discovery refresh rate
default_runtime: tmux         # "tmux" or "iterm"
```

<details>
<summary><strong>Custom binary paths</strong></summary>

Override the binary location for any provider:

```yaml
providers:
  claude:
    enabled: true
    binary: /opt/homebrew/bin/claude
```

</details>

## Provider System

Each provider implements discovery, session management, and spawning through a common interface. Providers can be enabled or disabled via config.

| Provider | Discovery | Session View | Embed PTY | Spawn |
|----------|-----------|--------------|-----------|-------|
| Claude | Process scan + JSONL | Split: trace + interactive PTY | Yes | `claude` CLI |
| Codex | Process scan + JSONL | Trace-only (jump out with `J`) | No | `codex` CLI |
| Gemini | Stub | Stub | No | `gemini` CLI |

<details>
<summary><strong>Adding a new provider</strong></summary>

Implement the `Provider` interface and register it in `app.go`:

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

The orchestrator and all views pick up new providers automatically. See [CLAUDE.md](CLAUDE.md) for the full guide.

</details>

## Architecture

agentmux reads everything from the filesystem. No daemon, no hooks, no modifications to your AI tools.

| Source | Location | Data |
|--------|----------|------|
| Config | `~/.agentmux/config.yaml` | Provider settings, runtime prefs |
| Process table | `ps aux` | PID, binary path, CLI flags, memory |
| Session logs | `~/.claude/projects/*/`, `~/.codex/sessions/` | Messages, tool calls, token usage |
| Teams | `~/.claude/teams/*/config.json` | Team membership |
| tmux | `tmux list-sessions` | Session names for jump support |

<details>
<summary><strong>Discovery pipeline</strong></summary>

On startup, agentmux loads config and registers only enabled providers. The orchestrator queries all providers in parallel every 2 seconds. Each provider scans for its processes, enriches with session data (model, tokens, status), and returns `agent.Agent` structs that drive all views.

</details>

<details>
<summary><strong>Session embedding</strong></summary>

When you press Enter on an embeddable agent, agentmux opens its CLI process inside a pseudo-terminal (creack/pty). Output is rendered through a VT emulator (charmbracelet/x/vt) into the Bubble Tea view. Press `Ctrl+]` to zoom out without killing the session.

For non-embeddable providers, `J` opens the session in a tmux split pane or iTerm2 split.

</details>

## Built With

- [Bubble Tea](https://github.com/charmbracelet/bubbletea) -- TUI framework
- [Lip Gloss](https://github.com/charmbracelet/lipgloss) -- Styling
- [charmbracelet/x/vt](https://github.com/charmbracelet/x) -- VT emulator for PTY embedding
- [creack/pty](https://github.com/creack/pty) -- PTY creation

## License

[MIT](LICENSE) -- attribution required.

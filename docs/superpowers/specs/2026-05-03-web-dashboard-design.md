# aimux Web Dashboard

## Overview

A browser-based dashboard for aimux that provides Kanban-style multi-repo, multi-session tracking of AI coding agents (Claude, Codex, Gemini) with full visibility into traces and live terminal sessions. Runs alongside the existing TUI in the same Go binary.

## Entry Points

- `aimux` -- TUI only (current, unchanged)
- `aimux --web` -- TUI + web server (both frontends, one process)
- `aimux web` -- web server only (headless, for remote/browser-only use)

Default port: 3000 (auto-assigned if busy). Configurable via `--port`.

## Architecture

### Project Structure

```
aimux/
  internal/
    frontend/
      tui/                  # Bubble Tea TUI (moved from internal/tui/)
      web/
        server.go           # HTTP router, static file serving, SSE
        handlers.go         # REST endpoints
        sse.go              # Server-Sent Events: agent state + trace streaming
        terminal.go         # WebSocket proxy for xterm.js -> tmux
        embed.go            # //go:embed all:../../web/dist
    agent/                  # unchanged
    controller/             # unchanged (already UI-agnostic)
    discovery/              # unchanged
    trace/                  # unchanged
    cost/                   # unchanged
    spawn/                  # unchanged
    evaluation/             # unchanged
    ...                     # all other packages unchanged

  web/                      # React frontend source (repo root)
    src/
      components/
        KanbanBoard.tsx
        AgentCard.tsx
        TracePanel.tsx
        SessionPanel.tsx
        LaunchDialog.tsx
        StatsBar.tsx
      hooks/
        useAgentStream.ts   # SSE hook for agent state
        useTraceStream.ts   # SSE hook for trace updates
      App.tsx
    package.json
    vite.config.ts
    dist/                   # build output, embedded by Go

  cmd/aimux/main.go         # adds --web flag and "web" subcommand
```

### Dependency Graph

```
cmd/aimux/main.go
  +-- internal/frontend/tui/    (imports shared packages)
  +-- internal/frontend/web/    (imports shared packages)

Both consume, neither depends on the other:
  +-- internal/controller/
  +-- internal/discovery/
  +-- internal/trace/
  +-- internal/agent/
  +-- internal/spawn/
  +-- internal/cost/
  +-- internal/evaluation/
  +-- ...
```

`tui/` and `web/` are leaf nodes. Deleting either has zero impact on the other. The React frontend only knows about the SSE/REST/WebSocket contract.

### Tech Stack

- **Go backend**: `net/http` (stdlib), SSE via `http.Flusher`, WebSocket via `gorilla/websocket` (for xterm.js terminal proxy)
- **React frontend**: Vite, TypeScript, dnd-kit (Kanban drag-and-drop), xterm.js (terminal), shadcn/ui (panels, dialogs, cards)
- **Embedding**: `go:embed` for production single-binary. Vite dev server with API proxy for development.

## Design System

DevHub dark-black theme, derived from pure black with lightness steps. No greys.

```
Background:  #000000 (base), #0d0d0d, #141414, #1a1a1a, #242424, #333333
Foreground:  #e6e6e6 (primary), #b3b3b3, #666666, #404040
Accent:      #FF3131 (red, primary), rgba(255,49,49,0.12) (dim)
Secondary:   #49D3B4 (teal)
Semantic:    #69DF73 (green/success), #FFB251 (orange/warning), #A772EF (purple/gemini)
Borders:     #1f1f1f (default), #2e2e2e (hover)
Typography:  Inter (headings + body), SF Mono / Fira Code (mono)
```

Provider color coding:
- Claude: red accent (`#FF3131`)
- Codex: green (`#69DF73`)
- Gemini: purple (`#A772EF`)

## Views and Interactions

### Default View: Kanban Board

Columns by agent status: **Active**, **Idle**, **Waiting Permission**, **Error**, **Completed**.

Toggle button switches to **"By Repo"** mode where columns are repositories, each containing that repo's sessions regardless of status.

Each column has a header with status dot, label, and count badge.

### Agent Card

One card per session. Displays:
- Provider badge (color-coded)
- Repository name (bold)
- Git branch (code styled, red text)
- Last action (italic)
- Model name + session cost (green)
- Time since last activity

Selected card has red border with subtle glow (`box-shadow: 0 0 8px rgba(255,49,49,0.12)`). Waiting Permission cards have orange border.

Drag-and-drop via dnd-kit for manual state changes (e.g., drag to Completed to archive).

### Stats Bar

Top bar spanning full width:
- Logo: "ai" (red) + "mux" (white)
- Aggregate stats: Active count, Repos count, Cost Today, Need Attention count (red when > 0)
- Controls: By Status / By Repo toggle, "+ Launch" button (outline red)

### Right Panel (Trace + Session)

Opens when clicking any card. Resizable via draggable left edge (width persisted in localStorage). Contains:

**Header**: repo name, branch, tab switcher, fullscreen button, close button.

**Stats ribbon**: status, turn count, tokens (in/out), cost, duration.

**Two tabs (full-height, toggling)**:

1. **Trace tab** (default) -- compact conversation history:
   - User turns: dark background block with turn number, role, timestamp, message
   - Agent turns: slightly lighter background with red left border, compressed response text
   - Tool calls as inline pills (checkmark/X icon + tool name + target file)
   - Collapsed diff previews (click to expand)
   - Per-turn stats: token counts, cost, duration
   - G/B/W annotation buttons per agent turn

2. **Session tab** -- full xterm.js terminal connected to the agent's tmux session via WebSocket. Interactive: type commands, respond to permission prompts. Full terminal emulation.

**Fullscreen**: button or Ctrl+F expands whichever tab is active to cover the entire viewport. Press again or Esc to return.

**Close**: collapses the panel entirely, board reclaims full width.

### Launch Dialog

Modal triggered by "+ Launch" button. Steps:
1. Pick provider (Claude, Codex, Gemini)
2. Pick directory (recent dirs + filesystem browser)
3. Pick model
4. Pick mode (auto, plan, etc.)

Matches aimux TUI's existing launcher flow. After launch, new card appears on the board via the next SSE tick.

### Full Trace View

Accessible via "Full Trace" button in the panel header. Navigates to a dedicated page with:
- Back-to-board link
- Session info header with export buttons (JSONL, OTEL)
- Search bar for filtering within the trace
- Full conversation with expandable diffs, bash output blocks, annotations
- Live bar at bottom

## Data Flow

### SSE (Server-Sent Events): `/api/events`

Single SSE endpoint. Server pushes two event types:

**Agent state** (every 2s):
```
event: agents
data: {"agents": [...]}
```

**Trace updates** (when subscribed, as turns arrive):
```
event: trace
data: {"sessionId": "abc-123", "turns": [...]}
```

Client subscribes to trace updates via REST (`POST /api/trace/subscribe/{sessionId}`). The server tracks which sessions each SSE connection is subscribed to (keyed by connection ID, sent as a query param on the SSE endpoint: `/api/events?clientId=xxx`). Trace events are only pushed to connections that have subscribed to that session. Unsubscribe or closing the panel stops trace events for that session.

### REST Endpoints

| Method | Path | Purpose |
|--------|------|---------|
| POST | /api/trace/subscribe/{sessionId} | Start receiving trace events for this session |
| POST | /api/trace/unsubscribe/{sessionId} | Stop receiving trace events |
| POST | /api/agents/launch | Spawn new agent |
| POST | /api/agents/{id}/annotate | Add GOOD/BAD/WASTE annotation |
| POST | /api/agents/{id}/archive | Archive/complete a session |
| GET  | /api/agents/{id}/diff | File diff summary |
| GET  | /api/history | Past sessions list |

### WebSocket: `/api/terminal/{tmuxSession}`

xterm.js connects here for interactive terminal access. The Go server proxies between the WebSocket and the tmux PTY. One WebSocket per attached terminal.

## Build and Dev Workflow

### Development

```bash
# Terminal 1: Go backend with hot reload
cd aimux && air -- web

# Terminal 2: React dev server with HMR
cd web && pnpm dev    # Vite proxies /api/* to Go server
```

### Production

```bash
cd web && pnpm build                        # builds to web/dist/
cd .. && go build -o aimux ./cmd/aimux      # embeds web/dist/
./aimux --web                               # single binary
```

No Docker or Node runtime needed in production.

## Migration

The only change to existing code is moving `internal/tui/` to `internal/frontend/tui/` and updating import paths. All other packages are untouched. The `cmd/aimux/main.go` entry point gains a `--web` flag and `web` subcommand.

## Mockups

Interactive mockups are saved in `.superpowers/brainstorm/` within the aimux repo:
- `dashboard-v4.html` -- final board + tabbed right panel (Trace/Session) with interactive tab switching
- `trace-view.html` -- full trace page with expandable diffs and annotations
- `dashboard-v2.html` -- Technomist palette iteration
- `dashboard-v3.html` -- DevHub dark-black palette with split panel

# UX Restructure — Implementation Plan

**Spec:** `docs/superpowers/specs/2026-09-05-sdk-mode-design.md`
**Branch:** `refactor/managed-sessions`
**Scope:** CLI object-verb pattern, TUI three-tab layout, real claude brain, web parity

## Global Constraints

1. **Object-verb CLI.** Every command is `aimux <object> <verb>`. No floating top-level verbs.
2. **Three objects:** sessions, agents, environments.
3. **Frontend parity.** Every operation reachable from CLI, TUI, and Web.
4. **Layer discipline.** Core packages must not import bubbletea/lipgloss/net/http.
5. **No mocks in tests.** Real execution, real files, real processes.
6. **Backwards compat.** Keep `aimux spawn` as hidden alias for `aimux sessions start`.
7. **Test-first.** Failing test before implementation.

## Current State

- CLI: `aimux spawn <provider>`, `aimux agents list`, `aimux environments list [--check]`
- TUI: agent list home view, launcher overlay, session zoom, trace view
- Web: agent cards, launch dialog, session detail
- SDK: SDKSession, Dispatcher, SubprocessManager, Environment.Exec() all working
- SQLite: session store with events, lifecycle state machine

## Tasks

### Task 9: CLI Object-Verb Restructure

Restructure all CLI commands to `object verb` pattern.

**Before → After:**
- `aimux spawn <name>` → `aimux sessions start <name> [--env <env>]`
- `aimux resume <id>` → `aimux sessions resume <id>`
- (new) → `aimux sessions stop <id>`
- (new) → `aimux sessions list [--status <s>] [--json]`
- (new) → `aimux sessions archive <id>`
- `aimux configs` → `aimux agents list` (already done in Task 3)
- `aimux agents create/update/remove` (already done in Task 3)
- `aimux environments list/add/remove` (already done in Task 4)

**Files to modify:**
- `cmd/aimux/cmd/spawn.go` → rename to `sessions.go`, restructure as `sessions` parent with `start`, `stop`, `resume`, `list`, `archive` subcommands
- `cmd/aimux/cmd/register.go` or `cmd/aimux/main.go` → update command registration
- `cmd/aimux/cmd/spawn_test.go` → update tests

**Files to remove:**
- `cmd/aimux/cmd/spawn.go` (replaced by sessions.go)

**Keep as alias:** `aimux spawn <name>` → hidden alias for `aimux sessions start <name>`

**sessions start** incorporates the SDK launch path:
- If the agent config has a `model` field → managed session (aimux calls claude)
- If the argument is a provider name (claude, codex) → discovered/native session (starts CLI in tmux)

**sessions list** reads from SQLite store + discovered processes:
- `--status running` → active sessions only
- `--status terminated` → history only
- `--json` → JSON output
- Default: all sessions, active first

**sessions stop** → for managed sessions: stop the subprocess. For discovered: send SIGTERM.

**Tests:**
- `sessions start reviewer --env local --dry-run` shows managed session config
- `sessions list --json` returns valid JSON
- `sessions stop` on nonexistent ID returns error
- Alias: `spawn reviewer` works same as `sessions start reviewer`

### Task 10: Wire Real Claude as Brain

Replace mock-sdk with the real `claude` CLI as the SDK subprocess. Use `claude --output-format stream-json --verbose` to get structured events.

**Files to modify:**
- `internal/sdk/subprocess.go` → handle claude's stream-json output format
- `internal/sdk/protocol.go` → add parsing for claude's actual event format
- `internal/sdk/session.go` → use claude binary path, pass model/prompt correctly
- `cmd/aimux/cmd/sessions.go` → `sessions start` resolves claude binary, passes model

**Claude stream-json format** (from `claude --output-format stream-json`):
Each line is a JSON object with a `type` field. Key types:
- `assistant` with `content` array containing `text` blocks and `tool_use` blocks
- `result` with final output
- `system` for system messages

**Integration:**
- `sessions start reviewer` → finds `claude` binary → starts `claude --output-format stream-json --model <model> --prompt <prompt> --allowedTools bash,read,write,edit,glob,grep --permission-mode bypassPermissions`
- Claude's tool_use events get intercepted by the dispatcher
- Tool results go back via stdin

**Tests:**
- Integration test that starts real `claude` with a simple prompt (e.g., "echo hello")
- Verify tool_call received, dispatched, result returned
- Note: this costs API tokens. Use a minimal prompt.

### Task 11: TUI Sessions Tab (Home)

Refactor the TUI home screen from "agent list" to "sessions" with active/history sections.

**Files to modify:**
- `internal/frontend/tui/app.go` → change default view to sessions
- `internal/frontend/tui/views/agents.go` → rename/refactor to sessions view
- `internal/frontend/tui/navigation_ops.go` → tab navigation [1] Sessions [2] Agents [3] Environments
- `internal/frontend/tui/views/header.go` → update tab labels

**Sessions view shows:**
- Active section: running + idle sessions (from discovery + SQLite), sorted by last activity
- History section: terminated + error sessions (from SQLite), sorted by time desc
- Columns: NAME, MODEL, ENV, STATUS, DIR, COST
- Discovered sessions marked with (d)
- Dimmed styling for history section

**Keybindings:**
- Enter → open (tool call stream for managed, live terminal for discovered)
- x → stop
- r → resume
- a → archive
- / → search
- f → filter by status

**Data source:** merge discovered agents (from orchestrator) + managed sessions (from SQLite store). Dedup by sandbox name.

### Task 12: TUI Agents + Environments Tabs

Add Agents (definitions) and Environments tabs to the TUI.

**Agents tab:**
- Shows configured agent templates from ~/.aimux/agents/
- Columns: NAME, MODEL, PROMPT (truncated)
- Enter → start a new session (prompt for env if multiple configured)
- c → create new agent (inline form or overlay)
- e → edit
- d → delete

**Environments tab:**
- Shows environments from config.yaml
- Columns: NAME, TYPE, STATUS, DETAILS
- a → add
- r → remove
- c → check health (runs connectivity test)

**Files to create:**
- `internal/frontend/tui/views/definitions.go` — agent definitions view
- `internal/frontend/tui/views/envs.go` — environments view

**Files to modify:**
- `internal/frontend/tui/app.go` → add tab routing for [2] and [3]
- `internal/frontend/tui/navigation_ops.go` → tab switching logic

### Task 13: TUI Tool Call Stream View

New view for managed sessions: shows structured tool call events instead of a raw terminal.

**Files to create:**
- `internal/frontend/tui/views/toolstream.go` — tool call stream renderer

**Display format:**
Each event is a timestamped line:
- Prompt: `▸` prefix, prompt text
- Tool call: `◆` prefix, tool name, dispatch target (local/sandbox), status (✓/✗), duration
- Expandable detail: input/output of the tool call
- Text: `◇` prefix, model response text
- Completion: `■` prefix, done marker

**Data source:** SessionEvent records from SQLite (populated by SDKSession during Run).

**Keybindings:**
- j/k or arrows → scroll
- Enter → expand/collapse tool call detail
- n/p → next/prev turn
- q → back to sessions list

### Task 14: Web UI Parity

Update the web dashboard to match the three-object model.

**Changes:**
- Rename "Agents" page to "Sessions" (active + history)
- Add "Agents" page (definitions CRUD)
- Add "Environments" page (list + health)
- Session detail: show tool call timeline for managed sessions
- Top nav: Sessions | Agents | Environments

**Files to modify:**
- `web/src/components/` — update components
- `web/src/App.tsx` or router — add new pages
- `internal/frontend/web/handlers.go` → ensure all endpoints exist
- `internal/frontend/web/server.go` → route registration

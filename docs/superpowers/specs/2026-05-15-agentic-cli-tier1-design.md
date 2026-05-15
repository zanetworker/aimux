# Agentic CLI Tier 1 Design

**Date:** 2026-05-15
**Scope:** Tier 1 (Table Stakes) from the agentic-cli-builder framework
**Goal:** Make aimux reliably invocable by shell-based AI agents (Claude Code, Codex, Gemini CLI)

## Current State

- Hand-rolled `os.Args` switch in `cmd/aimux/main.go` (615 lines)
- `sessions --list --json` and `sessions --export` provide partial structured output
- `sessions --limit` provides partial bounded responses
- All errors exit with code 1 (no taxonomy)
- No `--force`, `--dry-run`, `--fields`, or TTY detection
- Interactive picker launches by default when no flags are passed
- MCP server exists (`cmd/mcp/`) but is a separate K8s-focused binary, not a local CLI transport
- Cobra is in go.mod but unused in main.go

## Architecture

Migrate from hand-rolled arg parsing to cobra with command-per-file layout.

```
cmd/aimux/
  main.go              # slim: calls cmd.Execute()
  cmd/
    root.go            # root cobra command, persistent --json flag, TTY detection
    output.go          # OutputWriter: JSON vs table, structured errors, exit codes
    sessions.go        # aimux sessions (list/search/export)
    resume.go          # aimux resume <id>
    spawn.go           # aimux spawn <provider> (NEW)
    web.go             # aimux web [--port]
    agents.go          # aimux agents (NEW: list running agents)
    version.go         # aimux version
```

### Shared Infrastructure (output.go)

Provides:
- Exit code constants (taxonomy below)
- `OutputWriter` struct: takes `--json` flag, writes JSON to stdout or table to stdout
- `WriteError(err, exitCode)`: structured error to stderr, respects `--json`
- `WriteResult(data)`: JSON or table to stdout based on flag
- ANSI stripping when stdout is not a TTY

### Exit Code Taxonomy

| Code | Meaning | Example |
|------|---------|---------|
| 0 | Success | Command completed |
| 1 | General error | Unexpected failure |
| 2 | Usage error | Invalid flag, missing required arg |
| 3 | Not found | Session ID doesn't exist, no agents running |
| 4 | Config error | Can't load config, missing dependency |

Defined as constants in `output.go`. Every command uses these instead of bare `os.Exit(1)`.

### Structured Error Format

When `--json` is set, errors go to stderr as JSON:

```json
{"error": "invalid provider", "code": 2, "valid_values": ["claude", "codex", "gemini"]}
```

When `--json` is not set, errors are human-readable on stderr:

```
Error: invalid provider "gpt" (must be one of: claude, codex, gemini)
```

Both cases include valid values when the error is an enum violation (Principle 3).

## Commands

### aimux sessions

Existing command, retrofitted with agentic features.

```bash
aimux sessions                        # interactive picker (TTY only)
aimux sessions --list                 # table output
aimux sessions --list --json          # JSON output (exists today)
aimux sessions --export               # JSONL output (exists today)
aimux sessions <query>                # search + interactive pick (TTY only)
aimux sessions <query> --list         # search + table output
aimux sessions <query> --list --json  # search + JSON output
```

**New flags:**
- `--fields <comma-separated>`: Select output fields. Valid: id, project, age, turns, cost, annotation, prompt, tags, provider, session_file. Default: all.
- `--dir <path>`: Scope to directory (exists today, move to cobra flag)
- `--limit <n>`: Max results (exists today, move to cobra flag)

**TTY detection (Principle 1):** When stdin is not a TTY, `sessions` without `--list` behaves as `--list` automatically. No interactive picker in non-TTY mode.

**Bounded output (Principle 5):** JSON output includes truncation metadata:

```json
{"sessions": [...], "count": 25, "total": 142, "truncated": true, "hint": "use --limit to control result count"}
```

### aimux resume

Existing command, retrofitted.

```bash
aimux resume <session-id>             # resume session
aimux resume <session-id> --danger    # skip permissions (exists today)
aimux resume <session-id> --dry-run   # show what would be run, don't execute
aimux resume <session-id> --json      # structured output (session metadata + resume command)
```

**New flags:**
- `--dry-run` (Principle 4): Prints the command that would be executed without running it. JSON mode returns `{"command": "claude --resume abc123", "work_dir": "/path/to/project", "dry_run": true}`.
- `--force` alias for `--danger` (Principle 6 vocabulary consistency): Both work, `--danger` kept for backwards compatibility.

### aimux agents (NEW)

Lists discovered agents using `discovery.Orchestrator` from core packages. Read-only.

```bash
aimux agents                          # table output
aimux agents --json                   # JSON output
aimux agents --limit 5                # bounded
aimux agents --fields pid,provider,status,project  # field mask
```

**Output structure:**

```json
{
  "agents": [
    {
      "pid": 1234,
      "provider": "claude",
      "status": "active",
      "project": "aimux",
      "display_name": "claude:aimux",
      "session_id": "abc-123",
      "tmux_session": "claude-aimux-1"
    }
  ],
  "count": 3
}
```

**Exit codes:** 0 if agents found, 3 if no agents running (allows `aimux agents --json || echo "no agents"`).

### aimux spawn (NEW)

Starts an agent session. Reuses `internal/spawn/spawn.go` and provider `SpawnCommand()`.

```bash
aimux spawn claude                              # spawn in current dir
aimux spawn claude --dir ./myproject             # spawn in specific dir
aimux spawn codex --model o4-mini               # with model
aimux spawn gemini --mode plan                  # with mode
aimux spawn claude --prompt "fix the tests"     # with initial prompt
aimux spawn claude --json                       # structured output
aimux spawn claude --dry-run                    # show command without executing
```

**Flags:**
- `--dir <path>`: Working directory (default: cwd)
- `--model <name>`: Model override
- `--mode <name>`: Mode (e.g., plan, auto)
- `--prompt <text>`: Initial prompt
- `--dry-run`: Show spawn command without executing
- `--json`: Structured output

**JSON output on success:**

```json
{"provider": "claude", "pid": 5678, "tmux_session": "claude-myproject-1", "dir": "/path/to/myproject"}
```

**Validation (Principle 3):** Invalid provider returns:

```json
{"error": "invalid provider \"gpt\"", "code": 2, "valid_values": ["claude", "codex", "gemini"]}
```

### aimux web

Existing command, minimal changes.

```bash
aimux web                             # start web dashboard
aimux web --port 8080                 # custom port
```

No `--json` needed (it starts a long-running server). Exit codes: 0 on clean shutdown, 1 on bind failure, 4 on config error.

### aimux version

Replaces the `--version` flag with a proper subcommand. The `--version`/`-v` flags remain as aliases.

```bash
aimux version                         # "aimux v0.3.0"
aimux version --json                  # {"version": "0.3.0", "go": "1.23", "os": "darwin", "arch": "arm64"}
```

## Cross-Cutting Concerns

### Root Command (root.go)

- Persistent `--json` flag available to all subcommands
- TTY detection: sets a `isInteractive` flag based on `os.Stdin` being a terminal
- Version flag (`--version`, `-v`) prints version and exits

### ANSI Handling

When stdout is not a TTY, suppress ANSI escape codes in table output. Cobra's `DisableColors` handles help text. Table rendering checks `isatty(stdout)`.

### Backwards Compatibility

- `aimux` with no args still launches TUI
- `aimux --web` still launches TUI + web. Implemented as a root-level `--web` persistent flag that the root command's `Run` handler checks before launching plain TUI.
- `aimux sessions --list` still works
- `aimux sessions --export` still works
- `--danger`/`-d` still works alongside new `--force`

### Testing Strategy

Each command file gets a corresponding test file:
- `sessions_test.go`: Test JSON output, table output, TTY detection, field masks, search
- `agents_test.go`: Test JSON output, empty state (exit 3), field masks
- `spawn_test.go`: Test dry-run output, provider validation, flag parsing
- `resume_test.go`: Test dry-run output, force flag
- `output_test.go`: Test OutputWriter JSON/table modes, error formatting, exit codes
- `version_test.go`: Test JSON output

Tests use cobra's `ExecuteC()` with captured stdout/stderr. No actual process spawning in tests (mock the spawn layer).

## Out of Scope (Tier 2, future work)

- `agent-context` introspection command (Principle 7)
- Profile management (Principle 9)
- `--deliver` and `feedback` (Principle 10)
- Async `--wait` for spawn (Principle 8)
- Local MCP transport (`aimux mcp` for local agent tools)
- Vocabulary lint CI check (Principle 6)
- Command registry pattern (Phase 2 architecture)

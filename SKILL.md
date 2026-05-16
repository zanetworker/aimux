# aimux — Agent Skill Manifest

aimux is a CLI for managing multiple AI coding agent sessions (Claude, Codex, Gemini). This manifest teaches agents how to compose aimux commands into workflows.

## Introspection

Call once per session to learn the full CLI contract:

```bash
aimux agent-context
```

Returns every command, flag, type, default, and valid enum as JSON.

## Discovering running agents

```bash
aimux agents --json
```

Returns a list of active agent sessions with PID, provider, status, project, and model. Use `--limit` and `--fields` to control output size.

## Searching past sessions

```bash
aimux sessions --list --json --limit 10
aimux sessions "search query" --list --json
aimux sessions --list --json --fields id,project,cost
aimux sessions --export
```

The `--export` flag outputs JSONL (one JSON object per line) for pipeline consumption.

## Resuming a session

```bash
aimux resume <session-id> --dry-run --json
aimux resume <session-id>
```

Use `--dry-run` first to verify the command before executing.

## Spawning agents

```bash
aimux spawn claude --dir ./project --json
aimux spawn claude --dir ./project --model opus --wait --json
aimux spawn claude --dry-run --json
```

Valid providers: `claude`, `codex`, `gemini`. Use `--wait` to block until the session finishes. Use `--dry-run` to preview without executing.

## Delivering output

```bash
aimux spawn claude --json --deliver=file:./result.json
aimux spawn claude --json --deliver=webhook:https://hooks.example.com/agents
```

Valid schemes: `stdout`, `file:<path>`, `webhook:<url>`.

## Using profiles

Save a configuration bundle:

```bash
aimux profile save mysetup --provider claude --model opus --dir ~/project
```

Reuse on every invocation:

```bash
aimux spawn --profile mysetup
```

Explicit flags override profile values. Manage profiles:

```bash
aimux profile list --json
aimux profile get mysetup --json
aimux profile delete mysetup
```

## Reporting feedback

```bash
aimux feedback "the --limit flag silently ignores values over 100"
```

Writes to `~/.aimux/feedback.jsonl` for maintainer review.

## Workflow: parallel agents

1. `aimux spawn claude --dir ./frontend --json` (returns immediately)
2. `aimux spawn codex --dir ./backend --json` (returns immediately)
3. `aimux agents --json` (monitor both)
4. `aimux sessions --list --json --dir ./frontend` (check results)

## Workflow: spawn and wait

1. `aimux spawn claude --dir ./project --wait --json`
2. Command blocks until the agent finishes
3. Returns duration and final status

## Error handling

All errors include valid values when applicable:

```bash
aimux spawn gpt
# Error: invalid provider "gpt" (must be one of: claude, codex, gemini)
```

Exit codes: 0=success, 1=error, 2=usage, 3=not-found, 4=config.

With `--json`, errors go to stderr as structured JSON:

```json
{"error": "...", "code": 2, "valid_values": ["claude", "codex", "gemini"]}
```

## Non-interactive mode

When stdin is not a TTY, `aimux sessions` automatically uses `--list` mode instead of launching the interactive picker.

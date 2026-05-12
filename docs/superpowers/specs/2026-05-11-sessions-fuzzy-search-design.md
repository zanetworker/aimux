# Sessions Fuzzy Search and Resume

## Problem

`aimux sessions` prints a static table. Users must manually copy session IDs and run `aimux resume <id>` separately. There is no way to search sessions by content or resume with `--dangerously-skip-permissions`.

## Solution

Add fuzzy search and interactive picking to `aimux sessions`. When a user selects a session, resume it directly. A `--danger` / `-d` flag passes `--dangerously-skip-permissions` to the underlying `claude --resume` command.

## Commands

| Command | Behavior |
|---------|----------|
| `aimux sessions` | Fuzzy finder over all sessions |
| `aimux sessions <query>` | Pre-filtered fuzzy finder (title/prompt match, content fallback) |
| `aimux sessions <query> -d` | Same, resumes with `--dangerously-skip-permissions` |
| `aimux sessions --list` | Existing table output (unchanged) |
| `aimux sessions --export` | Existing JSONL output (unchanged) |
| `aimux resume <id> -d` | Existing resume with danger flag |

## Search Logic

When a query is provided:

1. Load all sessions via `history.Discover()`
2. Filter sessions where title or first prompt contains the query (case-insensitive)
3. If fewer than 3 matches, also run `history.SearchContent()` for full JSONL content search via ripgrep
4. Merge results (deduplicate by session ID, metadata-matched sessions first)
5. Feed into fuzzy finder

When no query is provided, all sessions are loaded into the fuzzy finder.

## Fuzzy Finder

Two backends, selected automatically:

**fzf (preferred):** Detected via `exec.LookPath("fzf")`. Sessions formatted as tab-separated lines piped to fzf stdin. fzf runs with `--ansi` for colored output. Selected line parsed to extract session ID.

Display format per line:
```
<id>  <project>  <age>  <turns>T  $<cost>  <title-or-prompt>
```

**Bubble Tea fallback:** Used when fzf is not installed. Uses `charmbracelet/bubbles/list` with type-to-filter. Same display columns. Enter to select, Esc/Ctrl+C to cancel.

## Resume Behavior

After selection:
1. Look up the session's project directory from the session metadata
2. Build `claude --resume <id>` command
3. If `--danger` flag is set, append `--dangerously-skip-permissions`
4. Exec the command with stdin/stdout/stderr inherited

## Code Changes

### `internal/history/search.go`
- Add `FilterByPrompt(sessions []Session, query string) []Session` -- case-insensitive substring match on Title and FirstPrompt fields

### `internal/sessions/picker.go` (new file)
- `PickSession(sessions []Session) (Session, error)` -- orchestrates fzf or fallback
- `fzfPick(sessions []Session) (Session, error)` -- pipes to fzf, parses selection
- `bubbletePick(sessions []Session) (Session, error)` -- Bubble Tea list picker
- `hasFzf() bool` -- checks PATH for fzf

This package is UI-aware (imports bubbletea for the fallback) but does not import `tui/`. It is a standalone picker utility.

### `cmd/aimux/main.go`
- `runSessions`: Parse positional query arg and `-d`/`--danger` flag. When not in `--list`/`--export` mode, call search logic then `sessions.PickSession()`. On selection, call updated resume logic.
- `runResume`: Parse `-d`/`--danger` flag, add `--dangerously-skip-permissions` to the claude command.

### Tests
- `internal/history/search_test.go`: Tests for `FilterByPrompt`
- `internal/sessions/picker_test.go`: Tests for fzf line formatting, session ID parsing from fzf output, hasFzf detection

## Non-Goals

- No changes to the TUI dashboard (tui/ package)
- No new config options
- No persistent search index

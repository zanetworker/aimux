# Session History & Resume — Design Document

**Date**: 2026-03-06
**Status**: Draft

## Problem

Aimux discovers and monitors *running* agent sessions, but has no way to browse, resume, or analyze *past* sessions. Users can't easily find a previous conversation, pick up where they left off, or export session data for evaluation workflows.

## Goals

1. Browse past sessions with trace preview and fuzzy search
2. Resume sessions directly from aimux (TUI or CLI)
3. Annotate sessions with verdicts and failure mode tags for eval pipelines
4. Export structured session data for notebooks and eval tools (Braintrust, MLflow)
5. Offer a "resume previous" option when launching new agents

## Non-Goals

- Full eval platform (rubric creation, LLM-as-judge, charts, pivot tables)
- Cross-provider resume for providers that don't support it (Codex, Gemini — future)
- Synthetic data generation or failure mode prediction

## Provider Scope

**Claude: full support** — Rich JSONL in `~/.claude/projects/`, `claude --resume <id>` works, full trace parsing.

**Codex, Gemini: discovery + view only** — Session files can be found and traces displayed, but resume is not available. Shown with a "(view only)" badge. Resume support added per-provider as CLIs mature.

## Architecture

### Discovery Layer: `internal/history/`

New package that scans provider session directories and builds a unified session list.

```
internal/history/
  history.go       # Session struct, Discover(), filtering, sorting
  history_test.go
```

#### Session Struct

```go
type Session struct {
    ID          string
    Provider    string        // "claude", "codex", "gemini"
    Project     string        // decoded directory path
    FilePath    string        // full path to session file
    StartTime   time.Time     // first entry timestamp
    LastActive  time.Time     // last entry timestamp
    TurnCount   int
    TokensIn    int64
    TokensOut   int64
    CostUSD     float64
    FirstPrompt string        // first user message (truncated, for display)
    Resumable   bool          // true if provider supports --resume
    Annotation  string        // "achieved", "partial", "failed", "abandoned", ""
    Note        string        // free-text rationale for the annotation
    Tags        []string      // failure mode tags (free-text with autocomplete)
}
```

#### Discovery Flow

1. Glob `~/.claude/projects/*/*.jsonl` for Claude sessions
2. For each file: **fast-scan** — parse only the first ~5 and last ~5 JSONL lines to extract:
   - `StartTime` from first entry timestamp
   - `LastActive` from last entry timestamp
   - `FirstPrompt` from first user message
   - `TurnCount` approximated from assistant message count in sampled lines, or full count if file is small (<100KB)
   - `TokensIn/Out` from usage entries
3. Decode project directory name to path: `-Users-foo-myproject` to `/Users/foo/myproject`
4. Load annotation, note, and tags from sidecar metadata file (see Annotation Persistence below)
5. Sort by `LastActive` descending
6. Optional: filter to sessions matching a specific working directory

**Performance target**: Discover 200+ sessions in <500ms. The fast-scan approach avoids parsing entire JSONL files (some are 10MB+).

#### Filtering

```go
func Discover(opts DiscoverOpts) ([]Session, error)

type DiscoverOpts struct {
    Dir      string   // scope to this working directory ("" = all)
    Provider string   // filter by provider ("" = all)
    Limit    int      // max results (0 = unlimited)
}
```

### Annotation Model

Three concepts at two levels, unified naming:

| Concept | Turn level | Session level |
|---------|-----------|---------------|
| **Annotation** | good / bad / wasteful (`a` key, existing) | achieved / partial / failed / abandoned (`v` key, new) |
| **Note** | free-text rationale (`N` key, existing) | free-text rationale (`N` key in session browser, new) |
| **Tags** | — | failure mode patterns with autocomplete (`f` key, new) |

- **Annotation** = the quality grade
- **Note** = the explanation ("why did I label it this way")
- **Tags** = the failure diagnosis ("what pattern went wrong")

Turn-level annotations and notes are unchanged (stored in `~/.aimux/annotations/`). Session-level data is stored as sidecar files.

### Session Metadata Persistence

Session-level annotations, notes, and tags stored as sidecar JSON files alongside the JSONL:

```
~/.claude/projects/-Users-foo-myproject/
  abc123.jsonl          # session trace (provider-owned, read-only)
  abc123.meta.json      # aimux metadata (annotation, note, tags)
```

```json
{
  "annotation": "failed",
  "note": "gave up after 40 turns going in circles",
  "tags": ["loop-on-error", "wrong-file"],
  "updated_at": "2026-03-06T14:30:00Z"
}
```

Sidecar files are small, fast to read, and don't modify the provider's session files.

### TUI Session Browser View

New view accessible via `S` from the agent list. Follows the same pattern as costs (`$`) and teams (`T`) views.

```
internal/tui/views/sessions.go
internal/tui/views/sessions_test.go
```

#### Layout

```
┌─ Sessions ─ /Users/azaalouk/go/src/github.com/zanetworker/aimux ──────────────┐
│  12 sessions (press A for all projects)                                         │
│                                                                                 │
│  ▸ 2h ago   feat: rich markdown rendering   16t  $0.42  [ACHIEVED]             │
│    5h ago   fix: process tree dedup          8t  $0.18                         │
│    1d ago   add OTEL export to MLflow       34t  $1.23  [FAILED] loop-on-error │
│    2d ago   refactor provider interface     22t  $0.87                         │
│    ...                                                                          │
│                                                                                 │
│─────────────────────────────────────────────────────────────────────────────────│
│  INPUT ──────────────────────────────────────────────────────────────────────── │
│    fix the markdown rendering in trace output                                   │
│  OUTPUT ─────────────────────────────────────────────────────────────────────── │
│    I'll look at the renderMarkdownLines function...                             │
│                                                                                 │
│  16 turns | $0.42 | 482/1.2k tok | Rd:12 Ed:8 Sh:3                            │
├─────────────────────────────────────────────────────────────────────────────────│
│ j/k nav  Enter resume  / filter  A all  v verdict  f tag  d delete  Esc back  │
└─────────────────────────────────────────────────────────────────────────────────┘
```

#### Key Bindings

| Key | Action |
|-----|--------|
| `j/k`, `up/down` | Navigate session list |
| `Enter` | Resume session (tmux pane / exec fallback) |
| `/` | Fuzzy filter by prompt text, project name, or failure tags |
| `A` | Toggle scope: current directory ↔ all projects |
| `v` | Cycle annotation: achieved → partial → failed → abandoned → (clear) |
| `N` | Add/edit session note (free-text rationale) |
| `f` | Add failure tags (free-text, comma-separated, with autocomplete from global vocabulary) |
| `p` | Expand/collapse trace preview |
| `d` | Delete session (with confirmation prompt) |
| `e` | Export current session (reuses existing JSONL/OTEL export) |
| `Esc` | Back to agent list |

#### Tag Autocomplete

When pressing `f` to add tags, a dropdown shows matching tags from the **global tag vocabulary** — the deduplicated set of all tags across all `.meta.json` files. No separate config needed; the vocabulary builds itself as you use it.

```
Tags: loop-on-error, |
      ┌──────────────────────┐
      │ hallucinated-api     │
      │ missed-context       │
      │ over-engineered      │
      │ test-not-run         │
      │ wrong-file           │
      └──────────────────────┘
```

- Start typing to filter suggestions
- Tab to accept a suggestion
- Comma to finish a tag and start the next
- Enter to save, Esc to cancel

#### Trace Preview

The bottom pane reuses `LogsView` in compact mode. When a session is selected, its JSONL is parsed on demand (not during discovery) and shown as a scrollable trace. Existing per-turn annotations are loaded and displayed.

### Launcher Integration

When launching a new agent via `L`, after selecting provider and directory, a third step offers resume options:

```
┌─ Launch Claude in ~/aimux ──────────────────────┐
│  ● New session                                  │
│  ○ Resume: feat: rich markdown...    (2h ago)   │
│  ○ Resume: fix: process tree...      (5h ago)   │
│  ○ Resume: add table rendering...    (1d ago)   │
└─ ↑↓ select  Enter launch  Esc back ────────────┘
```

- Shows the 5 most recent sessions for the selected directory
- "New session" is the default (pre-selected)
- Only shown for providers that support resume (Claude)
- Skipped entirely if no past sessions exist for the directory

Implementation: Add a `StepResume` phase to `LauncherView` after `StepOptions`. Calls `history.Discover(DiscoverOpts{Dir: selectedDir, Provider: selectedProvider, Limit: 5})`.

### CLI Subcommands

#### `aimux sessions`

Interactive session browser (standalone mini-TUI). Same session list as the main TUI browser but runs independently.

```
$ aimux sessions                          # interactive browser
$ aimux sessions --dir /path/to/project   # scoped to directory
```

Uses bubbletea for the interactive view. Enter resumes, `/` filters, `q` quits.

#### `aimux sessions --list`

Plain table output for scripting:

```
$ aimux sessions --list
ID                  PROJECT              AGE     TURNS  COST    ANNOTATION   TAGS
a1b2c3d4-e5f6-...   /Users/.../aimux     2h ago   16    $0.42   achieved     -
e5f6g7h8-i9j0-...   /Users/.../aimux     5h ago    8    $0.18   -            -
i9j0k1l2-m3n4-...   /Users/.../conductor 1d ago   34    $1.23   failed       loop-on-error

$ aimux sessions --list --dir . --json     # JSON output for piping
```

Flags: `--dir`, `--provider`, `--limit`, `--json`, `--csv`.

#### `aimux sessions --export`

Structured JSONL export for eval pipelines:

```
$ aimux sessions --export > all-sessions.jsonl
$ aimux sessions --export --dir /path/to/project > project.jsonl
```

Each line is a complete session record:

```json
{
  "id": "a1b2c3d4-...",
  "provider": "claude",
  "project": "/Users/.../aimux",
  "start_time": "2026-03-06T10:00:00Z",
  "last_active": "2026-03-06T12:30:00Z",
  "turn_count": 16,
  "tokens_in": 48200,
  "tokens_out": 12400,
  "cost_usd": 0.42,
  "first_prompt": "fix the markdown rendering in trace output",
  "annotation": "achieved",
  "tags": [],
  "turns": [
    {
      "number": 1,
      "user_input": "fix the markdown...",
      "output_preview": "I'll look at the renderMarkdownLines...",
      "actions": [{"name": "Read", "success": true}, {"name": "Edit", "success": true}],
      "annotation": "good",
      "note": "correctly identified the issue",
      "tokens_in": 3200,
      "tokens_out": 800,
      "cost_usd": 0.03
    }
  ],
  "tool_usage": {"Read": 12, "Edit": 8, "Bash": 3, "Grep": 5},
  "error_count": 1
}
```

This format is designed to be directly importable into notebooks, Braintrust datasets, or MLflow.

#### `aimux resume <session-id>`

Direct resume shortcut:

```
$ aimux resume a1b2c3d4
# equivalent to: claude --resume a1b2c3d4
```

Looks up the session to find its working directory, then uses the existing `jump.ResumeCmd()` logic.

## Eval Workflow (How It Fits Together)

Aimux's role in the eval pipeline: **collection + first-pass annotation + structured export**.

```
Agent sessions (live)
    │
    ▼
aimux discovers & monitors ──── existing functionality
    │
    ▼
Session browser (S key) ──────── browse past sessions
    │                              preview traces
    │                              annotate per-turn (good/bad/wasteful)
    │                              set verdict (achieved/failed/...)
    │                              tag failure modes (loop-on-error, wrong-file)
    ▼
aimux sessions --export ──────── structured JSONL
    │
    ▼
Notebook / Braintrust / MLflow ── deeper analysis
                                   rubric creation
                                   failure mode prediction
                                   charts & pivot tables
                                   golden dataset curation
```

What aimux does NOT do: LLM-as-judge scoring, chart generation, pivot tables, synthetic data, or failure mode prediction. These belong in notebooks or dedicated eval tools that consume aimux's export.

## Implementation Order

1. **`internal/history/`** — Session struct, discovery, fast-scan, sidecar metadata read/write
2. **`views/sessions.go`** — TUI session browser with trace preview
3. **App integration** — `S` keybinding in `app.go`, verdict/tag persistence
4. **Launcher resume** — `StepResume` in launcher view
5. **CLI subcommands** — `sessions` (interactive + `--list` + `--export`) and `resume`
6. **Tests** — Unit tests for each package, integration test for discovery + export round-trip

## Dependencies

No new external dependencies. Uses existing:
- `charmbracelet/bubbletea` for interactive CLI
- `charmbracelet/lipgloss` for styling
- Existing `LogsView`, `jump.ResumeInPane()`, provider `ParseTrace()`

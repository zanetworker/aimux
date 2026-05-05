# Plugin Dashboard Extension Model

## Goal

Add a plugin system to aimux that lets external tools register dashboard panels. Plugins declare their layout via a manifest and provide data via a command that outputs JSON. Aimux owns rendering. Both TUI and web frontends consume the same plugin interface.

First plugin: skill effectiveness dashboard (correction rates, invocations, pending learnings, trigger breakdown).

## Plugin Structure

A plugin is a directory in `~/.aimux/plugins/` containing a `plugin.yaml` manifest.

### Manifest Format

```yaml
name: skill-dashboard
tab: Skills
command: python3 ~/.claude/scripts/skill-dashboard.py --format json
cache_seconds: 30
panels:
  - id: metrics
    type: metric-row
    title: Overview
  - id: health
    type: table
    title: Skill Health
    sortable: true
  - id: top-skills
    type: bar-chart
    title: Top Skills
    width: half
  - id: triggers
    type: bar-chart
    title: Trigger Breakdown
    width: half
  - id: pending
    type: list
    title: Pending Learnings
    expandable: true
  - id: never-triggered
    type: list
    title: Never Triggered
```

### Command Output Format

The command prints a single JSON object to stdout. Keys match panel IDs from the manifest. Each value is typed by the panel type:

```json
{
  "metrics": {
    "items": [
      { "label": "Invocations", "value": 240, "color": "teal" },
      { "label": "Correction Rate", "value": "12%", "color": "green" },
      { "label": "Pending", "value": 5, "color": "orange" },
      { "label": "Never Triggered", "value": 8, "color": "accent" }
    ]
  },
  "health": {
    "columns": ["Skill", "Invocations", "Corrections", "Rate"],
    "rows": [
      { "cells": ["crafted-code", 42, 3, "7%"], "color": "green" },
      { "cells": ["brainstorming", 18, 5, "28%"], "color": "orange" },
      { "cells": ["debugging", 12, 6, "50%"], "color": "accent" }
    ]
  },
  "top-skills": {
    "items": [
      { "label": "crafted-code", "value": 42 },
      { "label": "brainstorming", "value": 18 }
    ]
  },
  "triggers": {
    "items": [
      { "label": "crafted-code", "value": 38, "secondary": 4, "legend": ["auto", "user"] }
    ]
  },
  "pending": {
    "items": [
      {
        "title": "Don't rely on self-reported model identity",
        "subtitle": "user_correction | explicit confidence",
        "body": "Claude incorrectly believed it was running on claude-opus-4-6...",
        "tags": ["weekly-report"]
      }
    ]
  },
  "never-triggered": {
    "items": [
      { "title": "some-unused-skill", "subtitle": "Registered but never invoked" }
    ]
  }
}
```

## Panel Types

Four built-in panel renderers. No external chart libraries.

| Type | Description | Data Shape |
|------|-------------|------------|
| `metric-row` | Horizontal row of colored stat chips | `{ items: [{ label, value, color }] }` |
| `table` | Sortable table with colored rows | `{ columns: string[], rows: [{ cells: any[], color?: string }] }` |
| `bar-chart` | Horizontal CSS bar chart | `{ items: [{ label, value, secondary?, legend? }] }` |
| `list` | Expandable item list | `{ items: [{ title, subtitle?, body?, tags? }] }` |

Colors are theme variable names: `green`, `accent`, `orange`, `purple`, `teal`, `fg-3`.

The `width: half` manifest option places two panels side-by-side. Default is full width.

## Architecture

### Core Package: `internal/plugin/`

```
internal/plugin/
  types.go       # Plugin, Panel, PanelType, PanelData structs
  loader.go      # ScanPlugins(dir string) []Plugin - reads ~/.aimux/plugins/*/plugin.yaml
  executor.go    # Executor with Execute(name) (map[string]json.RawMessage, error)
                 # Runs command, parses stdout JSON, caches for cache_seconds
```

No UI imports. Both frontends use this package.

### Caching

`Executor` caches command output per plugin for `cache_seconds` (default 30). Cache key is plugin name. Cache is invalidated on next call after TTL expires. Thread-safe via mutex.

### Web Frontend

**Backend:**
- `GET /api/plugins` - returns list of plugin manifests (name, tab, panels)
- `GET /api/plugins/{name}/data` - runs executor, returns panel data JSON
- Handlers in `internal/frontend/web/handlers.go`
- Server holds `*plugin.Executor` field

**Frontend:**
- `App.tsx` fetches `/api/plugins` on mount, adds tab per plugin
- `PluginView.tsx` - receives manifest + data, renders panels in order
- Panel renderers: `MetricRow.tsx`, `DataTable.tsx`, `BarChart.tsx`, `ExpandableList.tsx`
- All use existing CSS variables and inline styles (matching the rest of the dashboard)

### TUI Frontend

- `tui/views/plugin.go` - calls `plugin.Execute()` directly
- Renders panels as styled lipgloss text:
  - metric-row: colored inline values
  - table: lipgloss table
  - bar-chart: horizontal ASCII bars with labels
  - list: collapsible items with expand/collapse keys

### Layout

One tab per plugin. All panels rendered as scrollable sections within the tab:

```
[Skills] tab
 ┌─ Metric row (colored stat chips) ─────────────────────────────────────────┐
 ├─ Health table (sortable, color-coded rows) ───────────────────────────────┤
 ├─ Top skills chart ──────────────── ┬─ Trigger breakdown ─────────────────┤
 ├─ Pending learnings (expandable) ──┴──────────────────────────────────────┤
 └─ Never-triggered list ───────────────────────────────────────────────────┘
```

## Config Integration

Plugins are discovered by scanning `~/.aimux/plugins/`. No config.yaml changes needed to add a plugin. The plugins directory is created on first use.

Optional override in config:
```yaml
plugins:
  dir: ~/.aimux/plugins  # default, can be overridden
  disabled: []           # list of plugin names to skip
```

## First Plugin: Skill Dashboard

Delivered as a manifest + modifications to the existing `skill-dashboard.py` script.

**Manifest:** `~/.aimux/plugins/skill-dashboard/plugin.yaml`

**Script change:** Add `--format json` flag to `skill-dashboard.py` that outputs the structured JSON format instead of terminal text. The existing terminal output remains the default for CLI usage.

## Error Handling

- If a plugin command fails (non-zero exit), the panel shows an error message with stderr output
- If command times out (10s default), the panel shows "Plugin timed out"
- If JSON output is malformed, the panel shows "Invalid plugin output" with the parse error
- Missing panel IDs in output render as empty panels with "No data" placeholder

## Testing

- `internal/plugin/loader_test.go` - parse valid/invalid manifests, missing files
- `internal/plugin/executor_test.go` - command execution, caching, timeout, error handling
- Web handler tests for `/api/plugins` and `/api/plugins/{name}/data`
- Integration test with a mock plugin (echo command that outputs test JSON)

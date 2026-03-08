# Controller Extraction — Design Doc

Date: 2026-03-08
Status: Planned (not started)

## Problem

`tui/app.go` (2,079 lines) mixes three concerns:
1. **Business logic** — export orchestration, session management, agent discovery coordination, eval workflows
2. **Navigation/state** — view routing, breadcrumbs, zoom/split state machine, navigation stack
3. **TUI rendering** — Bubble Tea message handling, key dispatch, lipgloss styling

A future web UI or API server would need to reimplement all business logic because it's entangled with Bubble Tea types (`tea.Model`, `tea.Cmd`, `tea.Msg`).

## Goal

Extract a `controller/` package that owns the business logic and state, with `tui/app.go` becoming a thin adapter that translates between Bubble Tea events and controller methods.

## Current State (what's in app.go)

### Business logic that should move to controller
- `exportTrace()` — builds ExportTurn list, loads meta, calls WriteExport
- `exportOTEL()` — builds ExportConfig, loads meta, calls otel.ExportTrace
- `activeTraceFilePath()` / `activeTraceSessionID()` — session context resolution
- Agent discovery tick cycle (calls orchestrator, deduplicates, sorts)
- Session resume logic (finds provider, builds command)
- Agent kill logic (sends SIGTERM, manages hidden list)
- OTEL receiver lifecycle (start, enrich spans into traces)
- Eval store management (create/load per session)

### Navigation state that could move to controller
- `currentView` + view stack (navigateTo / navigateBack)
- `zoomed` / `splitMode` / `splitFocus` state machine
- Breadcrumb trail
- Command palette dispatch

### TUI-specific code that stays in app.go
- `Init()` / `Update()` / `View()` — Bubble Tea interface
- Key dispatch (translating `tea.KeyMsg` to controller actions)
- Mouse handling
- View sizing and layout
- Rendering (calling view.View() methods)
- Bubble Tea commands (tea.Cmd wrappers for async work)

## Proposed Architecture

```
cmd/aimux/main.go
    |
    v
internal/controller/
    controller.go        # Controller struct, public methods
    state.go             # Navigation state machine
    export.go            # Export orchestration
    discovery.go         # Agent refresh cycle

internal/tui/
    app.go               # Thin adapter: tea.Model -> controller calls
    views/               # Unchanged
```

### Controller Interface (core methods)

```go
package controller

type Controller struct {
    cfg          config.Config
    orchestrator *discovery.Orchestrator
    providers    []provider.Provider
    otelReceiver *otel.Receiver
    otelStore    *otel.SpanStore
    evalStore    *evaluation.Store

    // State
    agents       []agent.Agent
    teams        []team.TeamConfig
    currentView  ViewType
    viewStack    []ViewType
    zoomed       bool
    splitMode    bool
    splitFocus   string
    sessionID    string   // active session context
    sessionFile  string   // active session file path
}

// Discovery
func (c *Controller) RefreshAgents() []agent.Agent
func (c *Controller) Agents() []agent.Agent

// Navigation
func (c *Controller) NavigateTo(view ViewType)
func (c *Controller) NavigateBack() ViewType
func (c *Controller) EnterZoom(sessionID string)
func (c *Controller) ExitZoom() ViewType  // returns new state
func (c *Controller) ToggleSplit()

// Session management
func (c *Controller) ResumeSession(id string) (*exec.Cmd, error)
func (c *Controller) KillAgent(pid int) error

// Export
func (c *Controller) ExportJSONL() (path string, count int, err error)
func (c *Controller) ExportOTEL() (endpoint string, count int, err error)

// Evaluation
func (c *Controller) SetEvalContext(sessionID, filePath string)
func (c *Controller) Annotate(turn int, label, note string) error

// Sessions (history)
func (c *Controller) DiscoverSessions(opts history.DiscoverOpts) ([]history.Session, error)
func (c *Controller) DeleteSession(s history.Session) error
func (c *Controller) BulkDelete(sessions []history.Session) (int, error)
func (c *Controller) AnnotateSession(s history.Session, annotation string) error
func (c *Controller) TagSession(s history.Session, tags []string) error

// OTEL
func (c *Controller) StartOTELReceiver() error
func (c *Controller) EnrichTraces(turns []trace.Turn) []trace.Turn
```

### How app.go Changes

Before (current):
```go
func (a App) exportTrace() (tea.Model, tea.Cmd) {
    turns := a.activeTraceTurns()
    sessionID := a.activeTraceSessionID()
    // ... 40 lines of business logic ...
    path := evaluation.ExportPath(sessionID)
    if err := evaluation.WriteExport(path, exportTurns, sessionMeta); err != nil {
        a.statusHint = fmt.Sprintf("Export failed: %v", err)
        return a, nil
    }
    a.statusHint = fmt.Sprintf("Exported %d turns to %s", len(exportTurns), path)
    return a, nil
}
```

After (with controller):
```go
func (a App) exportTrace() (tea.Model, tea.Cmd) {
    path, count, err := a.ctrl.ExportJSONL()
    if err != nil {
        a.statusHint = fmt.Sprintf("Export failed: %v", err)
        return a, nil
    }
    a.statusHint = fmt.Sprintf("Exported %d turns to %s", count, path)
    return a, nil
}
```

## Implementation Plan

### Phase 1: Extract export logic (low risk, high value)
1. Create `internal/controller/controller.go` with struct and constructor
2. Move `ExportJSONL` and `ExportOTEL` methods
3. `app.go` calls `a.ctrl.ExportJSONL()` instead of inline logic
4. Tests: unit test controller export methods independently

### Phase 2: Extract discovery cycle
1. Move agent refresh, deduplication, sorting to controller
2. Move OTEL receiver lifecycle to controller
3. `app.go` calls `a.ctrl.RefreshAgents()` on tick

### Phase 3: Extract navigation state machine
1. Move view stack, zoom/split state to controller
2. Controller returns "what view to show" — app.go renders it
3. Enables testing navigation logic without Bubble Tea

### Phase 4: Extract session management
1. Move resume, kill, eval store management to controller
2. Move session annotation/tag/delete to controller

## What NOT to Extract

- Key dispatch stays in app.go (TUI-specific mapping)
- View sizing and layout stays in app.go
- Rendering stays in views/
- Bubble Tea message types stay in tui/

## Future: Web UI Architecture

With controller extracted, a web UI would look like:

```
cmd/aimux-web/main.go
    |
    v
internal/controller/     # Same controller, shared
internal/web/
    server.go            # HTTP/WebSocket server
    handlers.go          # REST endpoints calling controller
    ws.go                # WebSocket for real-time agent updates
frontend/
    src/                 # React/Svelte app
```

Both `tui/app.go` and `web/server.go` would import and use the same `controller.Controller`.

## Estimated Effort

| Phase | Lines moved | Risk | Dependencies |
|-------|------------|------|-------------|
| Phase 1 (exports) | ~120 | Low | None |
| Phase 2 (discovery) | ~150 | Medium | Tick timing |
| Phase 3 (navigation) | ~200 | Medium | View state coupling |
| Phase 4 (sessions) | ~150 | Low | None |

Total: ~620 lines move from app.go to controller/, reducing app.go to ~1,400 lines of pure TUI wiring.

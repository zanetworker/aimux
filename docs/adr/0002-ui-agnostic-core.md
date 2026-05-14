# ADR-0002: UI-Agnostic Core Architecture

## Status

Accepted

## Context

aimux started as a TUI application using Bubble Tea. As the web UI was added, business logic mixed with TUI code caused duplication and made the web frontend reimplement Go logic in TypeScript.

## Decision

Split the codebase into core packages (UI-agnostic) and TUI packages (Bubble Tea specific). Core packages under `internal/` must not import `bubbletea`, `lipgloss`, or anything from `tui/`. The TUI layer is a thin adapter: key handling, rendering, navigation. The web frontend calls Go API endpoints backed by the same core packages.

## Consequences

- Business logic is testable without TUI dependencies
- Web UI and TUI share the same data layer
- New frontends (API server, desktop app) can reuse core packages
- Trade-off: requires discipline to keep the boundary clean; easy to accidentally import TUI types in core

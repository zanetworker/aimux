# Design Intent

## Purpose

aimux is a unified dashboard for managing multiple AI coding agent sessions. It solves the problem of context-switching between agent terminals by providing a single pane of glass with live status, trace inspection, cost tracking, and session management.

## Preconditions

- Users run multiple AI coding agents (Claude, Codex, Gemini) simultaneously
- Agents run in tmux sessions or as direct processes
- Users need visibility into what agents are doing without switching terminals

## Invariants

- Core packages never depend on TUI framework imports
- Provider interface is the only coupling point between core and agent backends
- Trace parsing is provider-owned; shared types live in `internal/trace/`
- Session discovery works without agent cooperation (process scanning, file matching)

## Key Trade-offs

- **Single binary vs plugin system**: Providers are compiled in, not loaded as plugins. Simpler distribution at the cost of recompilation for new providers.
- **Process scanning vs agent registration**: aimux discovers agents by scanning processes rather than requiring agents to register. More resilient but less precise.
- **PTY embedding vs tmux mirroring**: Claude gets direct PTY for lower latency; Codex/Gemini use tmux mirroring for compatibility. Two backends to maintain.

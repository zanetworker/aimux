# ADR-0001: Provider Interface Design

## Status

Accepted

## Context

aimux needs to support multiple AI coding agent backends (Claude, Codex) with different process models, session formats, and terminal embedding strategies. The system should allow adding new providers without modifying core packages.

## Decision

Define a `Provider` interface with 11 methods covering discovery, session management, trace parsing, and OTEL configuration. Each provider is a single Go file implementing this interface. Providers are registered at startup; the orchestrator calls `Discover()` on each and merges results.

Two `SessionBackend` implementations handle terminal I/O: direct PTY embedding (Claude) and tmux mirroring (Codex). The provider signals its capability via `CanEmbed()`.

## Consequences

- Adding a provider requires one file and one registration call
- Core packages remain provider-agnostic
- Remote providers (Kubernetes, SSH) can extend the interface without breaking existing providers
- Trade-off: the interface has 11 methods, which is large; but splitting into smaller interfaces would complicate the orchestrator

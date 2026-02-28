# Observability Roadmap: MLflow Export + OTEL Tracing

## Context

agentmux traces agent behavior by parsing provider-specific session files (Claude JSONL, Codex JSONL, Gemini JSON). Users can annotate turns (GOOD/BAD/WASTE) and export with `:export`. This roadmap adds two capabilities without replacing the existing file-based approach.

## Step 1: MLflow-Compatible Export

**Goal**: `:export` produces a format MLflow can import, connecting agentmux's annotation workflow to MLflow's evaluation pipeline.

**Status**: Not started

**Scope**:
- Research MLflow trace import schema (spans, events, attributes)
- Add `:export mlflow` command variant (or make default export MLflow-compatible)
- Map `trace.Turn` fields to MLflow span attributes:
  - Turn -> Span with `gen_ai.operation.name`
  - UserLines -> `gen_ai.input.messages`
  - OutputLines -> `gen_ai.output.messages`
  - Actions -> child spans with tool names
  - Annotations (GOOD/BAD/WASTE) -> MLflow feedback/tags
  - TokensIn/Out -> `gen_ai.usage.input_tokens` / `gen_ai.usage.output_tokens`
  - CostUSD -> `gen_ai.usage.cost`
  - Model -> `gen_ai.request.model`
- Output format: MLflow-compatible JSONL or direct API upload
- Tests: export round-trip, field mapping, empty traces

**Files likely touched**:
- `internal/evaluation/export.go` -- new MLflow export format
- `internal/tui/app.go` -- `:export mlflow` command
- `internal/tui/views/help.go` -- document new command

**Non-goals**:
- No MLflow server dependency at runtime
- No changes to file-based trace parsing
- No changes to annotation workflow

## Step 2: OTEL Tracing (Optional Enhancement)

**Goal**: agentmux can receive OTEL traces from Claude/Codex/Gemini alongside file-based parsing, providing richer real-time data when available.

**Status**: Not started

**Scope**:
- Embed lightweight OTEL gRPC receiver (localhost only)
- Config: `otel.enabled: true`, `otel.port: 4317` in `~/.agentmux/config.yaml`
- Convert OTEL spans to `trace.Turn` format
- OTEL data supplements file-based parsing (does not replace it)
- When OTEL span arrives, merge with file-based turn data (prefer OTEL for timing/tokens)
- Graceful fallback: if no OTEL data, file parsing works exactly as before

**Architecture**:
```
Claude Code --OTLP--> agentmux OTEL receiver --> trace.Turn --> LogsView
Codex CLI   --OTLP--> agentmux OTEL receiver --> trace.Turn --> LogsView
Gemini CLI  --OTLP--> agentmux OTEL receiver --> trace.Turn --> LogsView
                                                    ^
Session files ---------> Provider.ParseTrace -------+  (fallback)
```

**Files likely touched**:
- `internal/otel/receiver.go` -- new: OTEL gRPC receiver
- `internal/otel/converter.go` -- new: OTEL span -> trace.Turn
- `internal/config/config.go` -- OTEL config fields
- `internal/tui/app.go` -- start/stop receiver, merge data

**Dependencies**:
- `go.opentelemetry.io/otel` and related packages
- `google.golang.org/grpc` for OTLP receiver

**Non-goals**:
- Not a full OTEL collector (no forwarding, no processing pipelines)
- No mandatory OTEL dependency (feature-flagged in config)
- No changes to provider interface or file-based parsing

## Decision Log

| Date | Decision | Rationale |
|------|----------|-----------|
| 2026-02-28 | File-based parsing stays as default | Zero-config, works immediately, no external dependencies |
| 2026-02-28 | OTEL as optional enhancement | Not all users have OTEL configured on their CLIs |
| 2026-02-28 | MLflow via export, not runtime integration | agentmux is a terminal tool, MLflow is a web platform -- connect via data, not coupling |
| 2026-02-28 | Step 1 before Step 2 | Smaller scope, immediate value, validates the annotation->evaluation pipeline |

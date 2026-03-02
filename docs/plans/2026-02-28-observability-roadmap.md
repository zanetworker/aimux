# Observability Roadmap: OTEL Export + OTEL Receiver

## Context

aimux traces agent behavior by parsing provider-specific session files (Claude JSONL, Codex JSONL, Gemini JSON). Users can annotate turns (GOOD/BAD/WASTE) and export with `:export`. This roadmap adds two capabilities without replacing the existing file-based approach.

aimux's unique value in the evaluation pipeline: **human annotations made in context**. You label turns while watching the agent work, capturing ground truth that automated judges can't produce. The annotations feed into evaluation platforms (MLflow, Braintrust, Langfuse, or any OTEL backend) for offline analysis, regression detection, and judge calibration.

## Step 1a: Annotation Notes

**Goal**: Add the ability to attach a free-text note to an annotation, explaining WHY a turn is GOOD/BAD/WASTE.

**Status**: Not started

**Current state**: `a` key cycles labels (good -> bad -> waste -> clear). The `Annotation.Note` field exists in the data model but the UI doesn't expose it.

**Scope**:
- Add `n` key in trace view to open a text input for the current turn's note
- Store note alongside label in evaluation store (already supported)
- Show note in trace view next to the label
- Include note in export

**Files**:
- `internal/tui/views/logs.go` -- add `n` key handler, note input mode, note display
- `internal/tui/app.go` -- handle note msg, persist to eval store

## Step 1b: OTEL Export

**Goal**: `:export otel` sends traces + annotations as OTLP/HTTP to a configurable endpoint. Works with MLflow, Jaeger, Grafana, or any OTEL backend.

**Status**: Not started

**Why OTEL, not a custom format**:
- MLflow accepts OTLP/HTTP at `/v1/traces` natively (3.6+)
- Jaeger, Grafana Tempo, Datadog all accept OTLP
- One export format, many backends
- No Python script needed

**Scope**:
- Config: `export.endpoint: http://localhost:5000/v1/traces` in `~/.aimux/config.yaml`
- `:export otel` command sends current trace as OTLP/HTTP POST
- Mapping:
  - Session -> root span
  - Turn -> child span with `gen_ai.operation.name: "turn"`
  - Turn.UserLines -> span attribute `gen_ai.input.messages`
  - Turn.OutputLines -> span attribute `gen_ai.output.messages`
  - Turn.Actions -> grandchild spans (tool calls)
  - Turn.TokensIn/Out -> `gen_ai.usage.input_tokens` / `gen_ai.usage.output_tokens`
  - Turn.CostUSD -> `gen_ai.usage.cost`
  - Turn.Model -> `gen_ai.request.model`
  - Annotation label -> span attribute `aimux.label`
  - Annotation note -> span attribute `aimux.note`
- Existing `:export` (JSONL to file) remains unchanged
- OTLP/HTTP only (MLflow doesn't support gRPC yet)

**Dependencies**:
- `go.opentelemetry.io/otel` -- span construction
- `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp` -- HTTP exporter
- No runtime dependency on MLflow or any specific backend

**Files**:
- `internal/otel/exporter.go` -- new: builds OTLP spans from trace.Turn + annotations, sends via HTTP
- `internal/config/config.go` -- export endpoint config
- `internal/tui/app.go` -- `:export otel` command handler
- `internal/tui/views/help.go` -- document command

## Step 2: OTEL Receiver (Optional Enhancement)

**Goal**: aimux can receive OTEL traces from Claude/Codex/Gemini alongside file-based parsing, providing richer real-time data when available.

**Status**: Not started (depends on Step 1b for OTEL library familiarity)

**Architecture**:
```
Claude Code --OTLP--> aimux OTEL receiver --> trace.Turn --> LogsView
Codex CLI   --OTLP--> aimux OTEL receiver --> trace.Turn --> LogsView
Gemini CLI  --OTLP--> aimux OTEL receiver --> trace.Turn --> LogsView
                                                    ^
Session files ---------> Provider.ParseTrace -------+  (fallback)
```

**Scope**:
- Embed lightweight OTLP/HTTP receiver (localhost only)
- Config: `otel.receiver.enabled: true`, `otel.receiver.port: 4318`
- Convert incoming OTEL spans to `trace.Turn` format
- OTEL data supplements file-based parsing (does not replace it)
- Graceful fallback: if no OTEL data, file parsing works exactly as before

**Files**:
- `internal/otel/receiver.go` -- new: OTLP/HTTP receiver
- `internal/otel/converter.go` -- new: OTEL span -> trace.Turn
- `internal/config/config.go` -- receiver config fields
- `internal/tui/app.go` -- start/stop receiver, merge data

## Evaluation Pipeline Flow

```
aimux (real-time, in-terminal)          Evaluation platform (offline, web)
─────────────────────────────────          ────────────────────────────────────
Watch agent work in split view
Agent deletes wrong file
Press 'a' → BAD
Press 'n' → "deleted prod config"

:export otel ──── OTLP/HTTP ──────────→  MLflow / Jaeger / Braintrust
                                          │
                                          ├─ Store traces with annotations
                                          ├─ Run automated LLM judges
                                          ├─ Compare models (opus vs sonnet)
                                          ├─ Detect regressions over time
                                          └─ Use annotations as judge calibration data
```

## Decision Log

| Date | Decision | Rationale |
|------|----------|-----------|
| 2026-02-28 | File-based parsing stays as default | Zero-config, works immediately, no external dependencies |
| 2026-02-28 | OTEL as the export wire format | MLflow, Jaeger, Grafana all accept OTLP -- one format, many backends |
| 2026-02-28 | Export backend is configurable (not MLflow-specific) | MLflow is first target but OTEL is vendor-neutral |
| 2026-02-28 | Add annotation notes before export | "BAD" alone isn't useful for judges; "BAD: deleted prod config" is |
| 2026-02-28 | Step 1a (notes) before 1b (export) | Notes make the exported data actually useful for evaluation |
| 2026-02-28 | OTLP/HTTP only, no gRPC | MLflow only supports HTTP; simpler implementation |
| 2026-02-28 | Step 2 (receiver) after Step 1 | Validates OTEL library usage first, bigger architectural change |

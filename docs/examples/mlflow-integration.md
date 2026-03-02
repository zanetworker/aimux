# aimux + MLflow Integration

Export agent traces with human annotations from aimux to MLflow for evaluation, regression detection, and judge calibration.

## Prerequisites

- aimux installed
- MLflow 3.6+ (`pip install mlflow>=3.6`)
- Python 3.10+

## Quick Start

### 1. Start MLflow

```bash
mlflow server --host 127.0.0.1 --port 5000
```

### 2. Configure aimux

Add the MLflow endpoint to `~/.aimux/config.yaml`:

```yaml
export:
  endpoint: "localhost:5000"
  insecure: true
```

### 3. Annotate and Export

```
# In aimux, open a trace (l on any agent)
# Annotate turns as you watch:
#   a    → cycle label: GOOD → BAD → WASTE → clear
#   N    → add a note explaining why (e.g., "deleted prod config")
#
# Export to MLflow:
#   :export-otel
```

### 4. View in MLflow

Open http://localhost:5000 in your browser. Navigate to your experiment's Traces tab. You'll see:

- A **session span** containing child spans for each turn
- Each turn span has attributes:
  - `gen_ai.input.messages` -- what the user asked
  - `gen_ai.output.messages` -- what the agent responded
  - `gen_ai.usage.input_tokens` / `gen_ai.usage.output_tokens`
  - `gen_ai.usage.cost`
  - `gen_ai.request.model`
- Annotated turns have additional attributes:
  - `aimux.feedback.value` -- GOOD, BAD, or WASTE
  - `aimux.feedback.rationale` -- your note explaining why

## Evaluation Workflow

### Build an Evaluation Dataset

1. In MLflow UI, go to Traces
2. Select traces that have aimux annotations
3. Click "Add to evaluation dataset"
4. Name it (e.g., `coding-agent-quality-v1`)

### Run Automated Evaluation

Use your annotated traces as ground truth to calibrate LLM judges:

```python
import mlflow

# Load your annotated dataset
dataset = mlflow.data.load_delta(
    table_name="coding_agent_quality_v1"
)

# Define a custom scorer based on your annotation patterns
@mlflow.scorer
def quality_scorer(inputs, outputs, trace):
    """Score based on patterns learned from human annotations."""
    feedback = trace.get_attribute("aimux.feedback.value")
    # Use as ground truth for calibrating automated judges
    return {"human_label": feedback}

# Evaluate
results = mlflow.genai.evaluate(
    data=dataset,
    scorers=[quality_scorer],
)
```

### Compare Models

Export traces from different model sessions and compare:

```python
# In aimux: run the same task with opus, then sonnet
# Annotate both sessions
# :export-otel for each

# In MLflow:
import mlflow

opus_traces = mlflow.search_traces(
    filter_string="attributes.`gen_ai.request.model` = 'claude-opus-4-6'"
)
sonnet_traces = mlflow.search_traces(
    filter_string="attributes.`gen_ai.request.model` = 'claude-sonnet-4-5'"
)

# Compare BAD rates, cost, token usage...
```

### Detect Regressions

Set up continuous monitoring with MLflow's online evaluation:

```python
import mlflow

# Auto-score incoming traces
mlflow.genai.monitor(
    scorers=[quality_scorer],
    experiment_name="agent-quality-monitoring",
)
```

## Span Attribute Reference

| Attribute | Type | Description |
|-----------|------|-------------|
| `aimux.session_id` | string | Session identifier |
| `aimux.provider` | string | Provider name (claude, codex, gemini) |
| `aimux.turn.number` | int | Turn number within the session |
| `aimux.turn.action_count` | int | Number of tool calls in this turn |
| `aimux.turn.error_count` | int | Number of failed tool calls |
| `aimux.feedback.value` | string | Human annotation: good, bad, wasteful |
| `aimux.feedback.rationale` | string | Free-text note explaining the label |
| `gen_ai.input.messages` | string | User prompt text |
| `gen_ai.output.messages` | string | Agent response text |
| `gen_ai.request.model` | string | Model used for this turn |
| `gen_ai.usage.input_tokens` | int64 | Input token count |
| `gen_ai.usage.output_tokens` | int64 | Output token count |
| `gen_ai.usage.cost` | float64 | Estimated cost in USD |
| `tool.name` | string | Tool/function name (on tool call spans) |
| `tool.input` | string | Tool input snippet |
| `tool.success` | bool | Whether the tool call succeeded |
| `tool.error` | string | Error message if tool call failed |

## Other OTEL Backends

The `:export-otel` command works with any OTLP/HTTP-compatible backend, not just MLflow:

```yaml
# Jaeger
export:
  endpoint: "localhost:4318"
  insecure: true

# Grafana Tempo
export:
  endpoint: "tempo.example.com:4318"
  insecure: false

# Datadog (via OTEL collector)
export:
  endpoint: "localhost:4318"
  insecure: true
```

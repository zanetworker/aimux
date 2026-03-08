package otel

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/zanetworker/aimux/internal/evaluation"
	"github.com/zanetworker/aimux/internal/trace"
)

// ExportConfig holds the configuration for OTEL export.
type ExportConfig struct {
	Endpoint     string            // e.g., "localhost:5001" (MLflow) or "localhost:4318" (collector)
	Insecure     bool              // true for HTTP (no TLS)
	SessionID    string            // session identifier
	Provider     string            // "claude", "codex", "gemini"
	ExperimentID string            // MLflow experiment ID (required by MLflow OTLP endpoint)
	Headers      map[string]string // extra HTTP headers

	// Session-level evaluation metadata (from .meta.json sidecar)
	Annotation   string   // achieved/partial/failed/abandoned
	FailureModes []string // failure-mode tags
	Note         string   // free-text rationale
}

// ExportTrace sends trace turns and annotations as OTLP/HTTP spans to the
// configured endpoint. Each turn becomes a child span under a session root
// span. Tool calls become grandchild spans. Annotations become span attributes.
func ExportTrace(cfg ExportConfig, turns []trace.Turn, store *evaluation.Store) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Build exporter options
	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(cfg.Endpoint),
	}
	if cfg.Insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}

	// Add headers (MLflow requires x-mlflow-experiment-id)
	headers := make(map[string]string)
	for k, v := range cfg.Headers {
		headers[k] = v
	}
	if cfg.ExperimentID != "" {
		headers["x-mlflow-experiment-id"] = cfg.ExperimentID
	}
	if len(headers) > 0 {
		opts = append(opts, otlptracehttp.WithHeaders(headers))
	}

	exporter, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return fmt.Errorf("otel: create exporter: %w", err)
	}
	defer exporter.Shutdown(ctx)

	// Create trace provider
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName("aimux"),
			attribute.String("aimux.provider", cfg.Provider),
			attribute.String("aimux.session_id", cfg.SessionID),
		),
	)
	if err != nil {
		return fmt.Errorf("otel: create resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	defer tp.Shutdown(ctx)

	tracer := tp.Tracer("aimux")

	// Determine session time bounds
	var sessionStart, sessionEnd time.Time
	if len(turns) > 0 {
		sessionStart = turns[0].Timestamp
		sessionEnd = turns[len(turns)-1].EndTime
		if sessionEnd.IsZero() {
			sessionEnd = turns[len(turns)-1].Timestamp
		}
	}
	if sessionStart.IsZero() {
		sessionStart = time.Now().Add(-1 * time.Hour)
	}
	if sessionEnd.IsZero() {
		sessionEnd = time.Now()
	}

	// Root span: the session
	sessionAttrs := []attribute.KeyValue{
		attribute.String("aimux.session_id", cfg.SessionID),
		attribute.String("aimux.provider", cfg.Provider),
		attribute.Int("aimux.turn_count", len(turns)),
	}
	if cfg.Annotation != "" {
		sessionAttrs = append(sessionAttrs, attribute.String("aimux.session.annotation", cfg.Annotation))
	}
	if len(cfg.FailureModes) > 0 {
		sessionAttrs = append(sessionAttrs, attribute.StringSlice("aimux.session.failure_modes", cfg.FailureModes))
	}
	if cfg.Note != "" {
		sessionAttrs = append(sessionAttrs, attribute.String("aimux.session.note", cfg.Note))
	}
	sessionCtx, sessionSpan := tracer.Start(ctx, "session",
		oteltrace.WithTimestamp(sessionStart),
		oteltrace.WithAttributes(sessionAttrs...),
	)

	// Child spans: each turn
	for _, t := range turns {
		turnStart := t.Timestamp
		if turnStart.IsZero() {
			turnStart = sessionStart
		}
		turnEnd := t.EndTime
		if turnEnd.IsZero() {
			turnEnd = turnStart.Add(1 * time.Second)
		}

		turnAttrs := []attribute.KeyValue{
			attribute.Int("aimux.turn.number", t.Number),
			attribute.String("gen_ai.input.messages", strings.Join(t.UserLines, "\n")),
			attribute.String("gen_ai.output.messages", strings.Join(t.OutputLines, "\n")),
			attribute.Int64("gen_ai.usage.input_tokens", t.TokensIn),
			attribute.Int64("gen_ai.usage.output_tokens", t.TokensOut),
			attribute.Float64("gen_ai.usage.cost", t.CostUSD),
			attribute.Int("aimux.turn.action_count", len(t.Actions)),
			attribute.Int("aimux.turn.error_count", t.ErrorCount()),
		}

		if t.Model != "" {
			turnAttrs = append(turnAttrs, attribute.String("gen_ai.request.model", t.Model))
		}

		// Add annotation as span attributes
		if store != nil {
			if ann := store.GetForTurn(t.Number); ann != nil {
				turnAttrs = append(turnAttrs,
					attribute.String("aimux.feedback.value", ann.Label),
				)
				if ann.Note != "" {
					turnAttrs = append(turnAttrs,
						attribute.String("aimux.feedback.rationale", ann.Note),
					)
				}
			}
		}

		turnCtx, turnSpan := tracer.Start(sessionCtx,
			fmt.Sprintf("turn-%d", t.Number),
			oteltrace.WithTimestamp(turnStart),
			oteltrace.WithAttributes(turnAttrs...),
		)

		// Grandchild spans: tool calls
		for i, action := range t.Actions {
			actionStart := turnStart.Add(time.Duration(i+1) * 100 * time.Millisecond)
			actionEnd := actionStart.Add(50 * time.Millisecond)

			actionAttrs := []attribute.KeyValue{
				attribute.String("tool.name", action.Name),
				attribute.String("tool.input", action.Snippet),
				attribute.Bool("tool.success", action.Success),
			}
			if action.ErrorMsg != "" {
				actionAttrs = append(actionAttrs, attribute.String("tool.error", action.ErrorMsg))
			}

			_, actionSpan := tracer.Start(turnCtx,
				action.Name,
				oteltrace.WithTimestamp(actionStart),
				oteltrace.WithAttributes(actionAttrs...),
			)
			actionSpan.End(oteltrace.WithTimestamp(actionEnd))
		}

		turnSpan.End(oteltrace.WithTimestamp(turnEnd))
	}

	sessionSpan.End(oteltrace.WithTimestamp(sessionEnd))

	// Force flush to ensure all spans are sent
	if err := tp.ForceFlush(ctx); err != nil {
		return fmt.Errorf("otel: flush: %w", err)
	}

	return nil
}

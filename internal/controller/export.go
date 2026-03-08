package controller

import (
	"fmt"

	"github.com/zanetworker/aimux/internal/evaluation"
	"github.com/zanetworker/aimux/internal/history"
	aimuxotel "github.com/zanetworker/aimux/internal/otel"
)

// ExportResult holds the outcome of an export operation.
type ExportResult struct {
	Path     string // file path (JSONL) or endpoint URL (OTEL)
	Count    int    // number of turns exported
}

// ExportJSONL exports trace turns and session metadata to a JSONL file.
// Returns the file path and turn count on success.
func (c *Controller) ExportJSONL(ctx ExportContext) (ExportResult, error) {
	if len(ctx.Turns) == 0 || ctx.SessionID == "" {
		return ExportResult{}, fmt.Errorf("no trace data to export")
	}

	exportTurns := buildExportTurns(ctx)

	var sessionMeta *evaluation.ExportSessionMeta
	if ctx.SessionFile != "" {
		meta := history.LoadMeta(ctx.SessionFile)
		if meta.Annotation != "" || len(meta.Tags) > 0 || meta.Note != "" || meta.Title != "" {
			sessionMeta = &evaluation.ExportSessionMeta{
				SessionID:    ctx.SessionID,
				Annotation:   meta.Annotation,
				FailureModes: meta.Tags,
				Note:         meta.Note,
				Title:        meta.Title,
			}
		}
	}

	path := evaluation.ExportPath(ctx.SessionID)
	if err := evaluation.WriteExport(path, exportTurns, sessionMeta); err != nil {
		return ExportResult{}, fmt.Errorf("write JSONL export: %w", err)
	}

	return ExportResult{Path: path, Count: len(exportTurns)}, nil
}

// ExportOTEL exports trace turns and session metadata as OTLP/HTTP spans.
// Returns the endpoint URL and turn count on success.
func (c *Controller) ExportOTEL(ctx ExportContext) (ExportResult, error) {
	if len(ctx.Turns) == 0 || ctx.SessionID == "" {
		return ExportResult{}, fmt.Errorf("no trace data to export")
	}

	endpoint := c.cfg.Export.Endpoint
	if endpoint == "" {
		return ExportResult{}, fmt.Errorf("set export.endpoint in ~/.aimux/config.yaml first")
	}

	cfg := aimuxotel.ExportConfig{
		Endpoint:     endpoint,
		Insecure:     c.cfg.Export.Insecure,
		SessionID:    ctx.SessionID,
		Provider:     ctx.ProviderName,
		ExperimentID: c.cfg.Export.MLflow.ExperimentID,
		Headers:      c.cfg.Export.Headers,
	}

	// Load session-level metadata
	if ctx.SessionFile != "" {
		meta := history.LoadMeta(ctx.SessionFile)
		cfg.Annotation = meta.Annotation
		cfg.FailureModes = meta.Tags
		cfg.Note = meta.Note
	}

	// Convert TraceInput back to trace.Turn for the OTEL exporter
	turns := inputsToTraceTurns(ctx.Turns)

	if err := aimuxotel.ExportTrace(cfg, turns, ctx.EvalStore); err != nil {
		return ExportResult{}, fmt.Errorf("OTEL export: %w", err)
	}

	return ExportResult{Path: fmt.Sprintf("http://%s", endpoint), Count: len(turns)}, nil
}

// buildExportTurns converts TraceInput slices to evaluation.ExportTurn slices,
// enriching with per-turn annotations from the eval store.
func buildExportTurns(ctx ExportContext) []evaluation.ExportTurn {
	var result []evaluation.ExportTurn
	for _, t := range ctx.Turns {
		et := evaluation.ExportTurn{
			Turn:       t.Number,
			Timestamp:  t.Timestamp,
			Input:      t.UserText,
			Output:     t.OutputText,
			TokensIn:   t.TokensIn,
			TokensOut:  t.TokensOut,
			CostUSD:    t.CostUSD,
			DurationMs: t.DurationMs,
		}
		for _, action := range t.Actions {
			et.Actions = append(et.Actions, evaluation.ExportAction{
				Tool:    action.Tool,
				Input:   action.Input,
				Success: action.Success,
				Error:   action.Error,
			})
		}
		if ctx.EvalStore != nil {
			if ann := ctx.EvalStore.GetForTurn(t.Number); ann != nil {
				et.Label = ann.Label
				et.Note = ann.Note
			}
		}
		result = append(result, et)
	}
	return result
}

package controller

import (
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/trace"
)

// inputsToTraceTurns converts the UI-agnostic TraceInput slice back to
// trace.Turn for consumers that expect the full type (e.g., OTEL exporter).
func inputsToTraceTurns(inputs []TraceInput) []trace.Turn {
	turns := make([]trace.Turn, len(inputs))
	for i, in := range inputs {
		ts, _ := time.Parse(time.RFC3339, in.Timestamp)
		t := trace.Turn{
			Number:    in.Number,
			Timestamp: ts,
			UserLines: strings.Split(in.UserText, "\n"),
			TokensIn:  in.TokensIn,
			TokensOut: in.TokensOut,
			CostUSD:   in.CostUSD,
			Model:     in.Model,
		}
		if in.OutputText != "" {
			t.OutputLines = strings.Split(in.OutputText, "\n")
		}
		if in.DurationMs > 0 {
			t.EndTime = ts.Add(time.Duration(in.DurationMs) * time.Millisecond)
		}
		for _, a := range in.Actions {
			t.Actions = append(t.Actions, trace.ToolSpan{
				Name:     a.Tool,
				Snippet:  a.Input,
				Success:  a.Success,
				ErrorMsg: a.Error,
			})
		}
		turns[i] = t
	}
	return turns
}

// TurnsToInputs converts trace.Turn slices to the UI-agnostic TraceInput
// format. Called by the UI layer before passing data to the controller.
func TurnsToInputs(turns []trace.Turn) []TraceInput {
	inputs := make([]TraceInput, len(turns))
	for i, t := range turns {
		in := TraceInput{
			Number:    t.Number,
			Timestamp: t.Timestamp.Format(time.RFC3339),
			UserText:  strings.Join(t.UserLines, "\n"),
			TokensIn:  t.TokensIn,
			TokensOut: t.TokensOut,
			CostUSD:   t.CostUSD,
			Model:     t.Model,
		}
		if len(t.OutputLines) > 0 {
			in.OutputText = strings.Join(t.OutputLines, "\n")
		}
		if dur := t.Duration(); dur > 0 {
			in.DurationMs = dur.Milliseconds()
		}
		for _, a := range t.Actions {
			in.Actions = append(in.Actions, ActionInput{
				Tool:    a.Name,
				Input:   a.Snippet,
				Success: a.Success,
				Error:   a.ErrorMsg,
			})
		}
		inputs[i] = in
	}
	return inputs
}

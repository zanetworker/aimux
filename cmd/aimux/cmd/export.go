package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/history"
	"github.com/zanetworker/aimux/internal/trace"
)

// traceParserFn parses a session file into structured turns.
type traceParserFn func(filePath string) ([]trace.Turn, error)

func newExportCmd(discoverSessions sessionsDiscoverFn, parsers map[string]traceParserFn) *cobra.Command {
	var exportType string

	cmd := &cobra.Command{
		Use:   "export <session-id>",
		Short: "Export a session trace",
		Long:  "Export a session trace to JSONL or OTEL format. Session ID can be a prefix.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			idPrefix := args[0]

			if exportType != "jsonl" && exportType != "otel" {
				return fmt.Errorf("invalid export type %q (must be one of: jsonl, otel)", exportType)
			}

			allSessions, err := discoverSessions(history.DiscoverOpts{}, "")
			if err != nil {
				return fmt.Errorf("session discovery failed: %w", err)
			}

			var matched *history.Session
			for i := range allSessions {
				if strings.HasPrefix(allSessions[i].ID, idPrefix) {
					if matched != nil {
						return fmt.Errorf("ambiguous session ID prefix %q matches multiple sessions", idPrefix)
					}
					s := allSessions[i]
					matched = &s
				}
			}
			if matched == nil {
				return fmt.Errorf("no session found matching %q", idPrefix)
			}

			// Find the appropriate trace parser.
			provider := matched.Provider
			if provider == "" {
				provider = "claude" // default
			}
			parser, ok := parsers[provider]
			if !ok {
				return fmt.Errorf("no trace parser available for provider %q", provider)
			}

			if matched.FilePath == "" {
				return fmt.Errorf("session %s has no file path for trace export", matched.ID)
			}

			turns, err := parser(matched.FilePath)
			if err != nil {
				return fmt.Errorf("parse trace: %w", err)
			}

			// Convert trace.Turn to controller.TraceInput.
			inputs := turnsToInputs(turns)

			cfg := config.Default()
			ctrl := controller.New(cfg)
			ctx := controller.ExportContext{
				SessionID:    matched.ID,
				SessionFile:  matched.FilePath,
				ProviderName: provider,
				Turns:        inputs,
			}

			var result controller.ExportResult
			switch exportType {
			case "jsonl":
				result, err = ctrl.ExportJSONL(ctx)
			case "otel":
				result, err = ctrl.ExportOTEL(ctx)
			}
			if err != nil {
				return fmt.Errorf("export failed: %w", err)
			}

			if jsonOutput {
				out := map[string]any{
					"session_id": matched.ID,
					"type":       exportType,
					"path":       result.Path,
					"turns":      result.Count,
				}
				b, _ := json.MarshalIndent(out, "", "  ")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Exported %d turns to %s\n", result.Count, result.Path)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&exportType, "type", "jsonl", "Export type: jsonl or otel")
	return cmd
}

// turnsToInputs converts trace.Turn slices to controller.TraceInput slices
// for use with the controller export methods.
func turnsToInputs(turns []trace.Turn) []controller.TraceInput {
	inputs := make([]controller.TraceInput, len(turns))
	for i, t := range turns {
		ts := ""
		if !t.Timestamp.IsZero() {
			ts = t.Timestamp.Format(time.RFC3339)
		}
		inputs[i] = controller.TraceInput{
			Number:     t.Number,
			Timestamp:  ts,
			UserText:   strings.Join(t.UserLines, "\n"),
			OutputText: strings.Join(t.OutputLines, "\n"),
			TokensIn:   t.TokensIn,
			TokensOut:  t.TokensOut,
			CostUSD:    t.CostUSD,
			DurationMs: t.Duration().Milliseconds(),
			Model:      t.Model,
		}
		for _, a := range t.Actions {
			inputs[i].Actions = append(inputs[i].Actions, controller.ActionInput{
				Tool:    a.Name,
				Input:   a.Snippet,
				Success: a.Success,
				Error:   a.ErrorMsg,
			})
		}
	}
	return inputs
}

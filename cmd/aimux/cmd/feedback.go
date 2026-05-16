package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"
)

type feedbackEntry struct {
	Timestamp  string `json:"timestamp"`
	Text       string `json:"text"`
	CLIVersion string `json:"cli_version"`
	OS         string `json:"os"`
}

func newFeedbackCmd(feedbackPath string) *cobra.Command {
	return &cobra.Command{
		Use:   "feedback <text>",
		Short: "Send feedback about the CLI",
		Long:  "Record feedback about CLI friction, bugs, or suggestions. Saved to ~/.aimux/feedback.jsonl.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			entry := feedbackEntry{
				Timestamp:  time.Now().UTC().Format(time.RFC3339),
				Text:       args[0],
				CLIVersion: rootCmd.Version,
				OS:         runtime.GOOS,
			}

			if feedbackPath == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("cannot determine home directory: %w", err)
				}
				feedbackPath = filepath.Join(home, ".aimux", "feedback.jsonl")
			}

			dir := filepath.Dir(feedbackPath)
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return fmt.Errorf("create feedback dir: %w", err)
			}

			line, err := json.Marshal(entry)
			if err != nil {
				return fmt.Errorf("marshal feedback: %w", err)
			}
			line = append(line, '\n')

			f, err := os.OpenFile(feedbackPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304
			if err != nil {
				return fmt.Errorf("open feedback file: %w", err)
			}
			defer func() { _ = f.Close() }()

			if _, err := f.Write(line); err != nil {
				return fmt.Errorf("write feedback: %w", err)
			}

			if jsonOutput {
				result := map[string]any{
					"status": "recorded",
					"file":   feedbackPath,
				}
				b, _ := json.MarshalIndent(result, "", "  ")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
			} else {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Feedback recorded.")
			}
			return nil
		},
	}
}

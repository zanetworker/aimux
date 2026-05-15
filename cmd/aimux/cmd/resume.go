package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

type resumeBuilderFn func(sessionID string, danger bool) (command, workDir string, err error)

func newResumeCmd(builder resumeBuilderFn, execFn func(sessionID string, danger bool)) *cobra.Command {
	var danger, dryRun bool

	cmd := &cobra.Command{
		Use:   "resume <session-id>",
		Short: "Resume a past session",
		Long:  "Resume an AI agent session by its ID. Use --dry-run to preview the command without executing.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID := args[0]

			if builder == nil {
				return fmt.Errorf("resume not configured")
			}

			command, workDir, err := builder(sessionID, danger)
			if err != nil {
				return err
			}

			if dryRun {
				if jsonOutput {
					result := map[string]any{
						"command":  command,
						"work_dir": workDir,
						"dry_run":  true,
					}
					b, _ := json.MarshalIndent(result, "", "  ")
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
				} else {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Would run: %s\n", command)
					if workDir != "" {
						_, _ = fmt.Fprintf(cmd.OutOrStdout(), "In: %s\n", workDir)
					}
				}
				return nil
			}

			if execFn != nil {
				execFn(sessionID, danger)
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&danger, "danger", "d", false, "Resume with --dangerously-skip-permissions")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show the resume command without executing")
	return cmd
}

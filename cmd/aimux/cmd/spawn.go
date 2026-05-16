package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/deliver"
)

type spawnFn func(provider, dir, model, mode, prompt string) (pid int, tmuxSession string, err error)

func newSpawnCmd(validProviders []string, spawn spawnFn) *cobra.Command {
	var dir, model, mode, prompt string
	var dryRun bool
	var deliverTarget string

	cmd := &cobra.Command{
		Use:   "spawn <provider>",
		Short: "Start a new AI agent session",
		Long:  fmt.Sprintf("Launch a new AI agent session. Provider must be one of: %s", strings.Join(validProviders, ", ")),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			provider := args[0]

			valid := false
			for _, vp := range validProviders {
				if provider == vp {
					valid = true
					break
				}
			}
			if !valid {
				return fmt.Errorf("invalid provider %q (must be one of: %s)", provider, strings.Join(validProviders, ", "))
			}

			if dir == "" {
				dir, _ = os.Getwd()
			}

			if dryRun {
				if jsonOutput {
					result := map[string]any{
						"provider": provider,
						"dir":      dir,
						"model":    model,
						"mode":     mode,
						"prompt":   prompt,
						"dry_run":  true,
					}
					b, _ := json.MarshalIndent(result, "", "  ")
					if deliverTarget != "" && deliverTarget != "stdout" {
						if err := deliver.Deliver(b, deliverTarget); err != nil {
							return err
						}
						_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Delivered to %s\n", deliverTarget)
					} else {
						_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
					}
				} else {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Would spawn %s in %s\n", provider, dir)
					if model != "" {
						_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Model: %s\n", model)
					}
				}
				return nil
			}

			if spawn == nil {
				return fmt.Errorf("spawn not configured")
			}

			pid, tmuxSession, err := spawn(provider, dir, model, mode, prompt)
			if err != nil {
				return fmt.Errorf("spawn failed: %w", err)
			}

			if jsonOutput {
				result := map[string]any{
					"provider":     provider,
					"pid":          pid,
					"tmux_session": tmuxSession,
					"dir":          dir,
				}
				b, _ := json.MarshalIndent(result, "", "  ")
				if deliverTarget != "" && deliverTarget != "stdout" {
					if err := deliver.Deliver(b, deliverTarget); err != nil {
						return err
					}
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Delivered to %s\n", deliverTarget)
				} else {
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
				}
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Spawned %s (tmux: %s)\n", provider, tmuxSession)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&dir, "dir", "", "Working directory (default: current)")
	cmd.Flags().StringVar(&model, "model", "", "Model override")
	cmd.Flags().StringVar(&mode, "mode", "", "Mode (e.g., plan, auto)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Initial prompt")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show spawn command without executing")
	cmd.Flags().StringVar(&deliverTarget, "deliver", "", "Delivery target: stdout (default), file:<path>, webhook:<url>")
	return cmd
}

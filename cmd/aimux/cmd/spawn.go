package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/deliver"
	"github.com/zanetworker/aimux/internal/spawn"
)

type spawnFn func(opts spawn.LaunchOpts) (pid int, tmuxSession string, err error)

func newSpawnCmd(validProviders []string, spawnAgent spawnFn, defaultMode string) *cobra.Command {
	var dir, model, mode, prompt string
	var runtime, execution, shell, sessionMgr string
	var otel bool
	var dryRun bool
	var wait bool
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
			if mode == "" && defaultMode != "" {
				mode = defaultMode
			}

			if dryRun {
				if jsonOutput {
					result := map[string]any{
						"provider":        provider,
						"dir":             dir,
						"model":           model,
						"mode":            mode,
						"prompt":          prompt,
						"runtime":         runtime,
						"execution":       execution,
						"shell":           shell,
						"session_manager": sessionMgr,
						"otel":            otel,
						"dry_run":         true,
						"wait":            wait,
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

			if spawnAgent == nil {
				return fmt.Errorf("spawn not configured")
			}

			opts := spawn.LaunchOpts{
				Provider:       provider,
				Dir:            dir,
				Model:          model,
				Mode:           mode,
				Prompt:         prompt,
				Runtime:        runtime,
				Execution:      execution,
				Shell:          shell,
				SessionManager: sessionMgr,
				OTELEnabled:    otel,
			}
			pid, tmuxSession, err := spawnAgent(opts)
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

			if wait && tmuxSession != "" {
				start := time.Now()
				for {
					time.Sleep(5 * time.Second)
					// #nosec G204
					checkCmd := exec.Command("tmux", "has-session", "-t", tmuxSession)
					if err := checkCmd.Run(); err != nil {
						break
					}
				}
				duration := time.Since(start)
				if jsonOutput {
					waitResult := map[string]any{
						"provider":     provider,
						"tmux_session": tmuxSession,
						"waited":       true,
						"duration_s":   int(duration.Seconds()),
					}
					b, _ := json.MarshalIndent(waitResult, "", "  ")
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
				} else {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Session %s exited after %s\n", tmuxSession, duration.Round(time.Second))
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&dir, "dir", "", "Working directory (default: current)")
	cmd.Flags().StringVar(&model, "model", "", "Model override")
	cmd.Flags().StringVar(&mode, "mode", "", "Mode (e.g., plan, auto)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Initial prompt")
	cmd.Flags().StringVar(&runtime, "runtime", "", "Runtime: local (default) or container")
	cmd.Flags().StringVar(&execution, "execution", "", "Execution: local (default) or hybrid")
	cmd.Flags().StringVar(&shell, "shell", "", "Login shell (default: $SHELL)")
	cmd.Flags().StringVar(&sessionMgr, "session", "", "Session manager: tmux (default) or direct")
	cmd.Flags().BoolVar(&otel, "otel", false, "Enable OTEL telemetry")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show spawn command without executing")
	cmd.Flags().BoolVar(&wait, "wait", false, "Block until the spawned session exits")
	cmd.Flags().StringVar(&deliverTarget, "deliver", "", "Delivery target: stdout (default), file:<path>, webhook:<url>")
	return cmd
}

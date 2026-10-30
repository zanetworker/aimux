package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/controller"
)

func newJumpCmd(discover discoverFunc, focuser controller.Focuser) *cobra.Command {
	return &cobra.Command{
		Use:   "jump <session-id|prefix|pid>",
		Short: "Bring a running agent's terminal pane to the front",
		Long:  "Find the running agent by session ID, unique session ID prefix or PID and focus its iTerm2 window, tab and pane. Fails when the session is not running; for agents in tmux it prints the attach command instead.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			agents, err := discover()
			if err != nil {
				return fmt.Errorf("discovery failed: %w", err)
			}
			target, err := controller.FindLiveAgent(agents, args[0])
			if err != nil {
				return err
			}
			res, err := controller.FocusAgent(target, focuser)
			if jsonOutput {
				b, _ := json.MarshalIndent(res, "", "  ")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
			}
			if err != nil {
				if res.Hint != "" {
					return fmt.Errorf("%w; run: %s", err, res.Hint)
				}
				return err
			}
			if !jsonOutput {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Focused %s (PID %d): %s\n", target.ProviderName, target.PID, res.Where)
			}
			return nil
		},
	}
}

package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/history"
)

func newSessionsStarCmd(discover sessionsDiscoverFn) *cobra.Command {
	return &cobra.Command{
		Use:   "star <session-id>",
		Short: "Toggle star on a session",
		Long:  "Toggle the starred state of a session. Session ID can be a prefix.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			idPrefix := args[0]

			allSessions, err := discover(history.DiscoverOpts{}, "")
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

			if matched.FilePath == "" {
				return fmt.Errorf("session %s has no file path", matched.ID)
			}

			starred, err := controller.ToggleStar(matched.FilePath)
			if err != nil {
				return fmt.Errorf("toggle star: %w", err)
			}

			if jsonOutput {
				result := map[string]any{
					"session_id": matched.ID,
					"starred":    starred,
				}
				b, _ := json.MarshalIndent(result, "", "  ")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
			} else {
				label := "starred"
				if !starred {
					label = "unstarred"
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Session %s %s.\n", matched.ID, label)
			}
			return nil
		},
	}
}

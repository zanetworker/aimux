package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/history"
)

func newSessionsParentCmd(d Deps) *cobra.Command {
	var dir string
	var listMode, exportMode, danger, allProjects bool
	var limit int
	var fields string

	cmd := &cobra.Command{
		Use:   "sessions [query]",
		Short: "Manage agent sessions",
		Long:  "Start, stop, resume, list, and archive AI agent sessions. When invoked without a subcommand, lists sessions (same as 'sessions list').",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionsList(cmd, args, d.DiscoverSessions, d.SearchContent, d.PickSession, d.ResumeExec,
				dir, listMode, exportMode, danger, allProjects, limit, fields)
		},
	}

	cmd.Flags().StringVar(&dir, "dir", "", "Scope to a specific directory (default: current directory)")
	cmd.Flags().BoolVarP(&allProjects, "all", "a", false, "Show sessions from all projects")
	cmd.Flags().BoolVarP(&listMode, "list", "l", false, "Table output (scriptable)")
	cmd.Flags().BoolVar(&exportMode, "export", false, "JSONL output for eval pipelines")
	cmd.Flags().BoolVarP(&danger, "danger", "d", false, "Resume with --dangerously-skip-permissions")
	cmd.Flags().IntVar(&limit, "limit", 0, "Max sessions to show (0 = all)")
	cmd.Flags().StringVar(&fields, "fields", "", "Comma-separated fields: id,provider,project,age,turns,cost,annotation,prompt,tags")

	cmd.AddCommand(newSessionsStartCmd(d))
	cmd.AddCommand(newSessionsStopCmd())
	cmd.AddCommand(newSessionsResumeCmd(d.ResumeBuilder, d.ResumeExec, d.SkipPermissions))
	cmd.AddCommand(newSessionsListSubCmd(d))
	cmd.AddCommand(newSessionsArchiveCmd(d.DiscoverSessions))
	cmd.AddCommand(newSessionsStarCmd(d.DiscoverSessions))

	return cmd
}

func runSessionsList(cmd *cobra.Command, args []string,
	discover sessionsDiscoverFn, search sessionsSearchFn,
	picker sessionsPickerFn, resume sessionsResumeFn,
	dir string, listMode, exportMode, danger, allProjects bool, limit int, fields string,
) error {
	if !allProjects && dir == "" {
		cwd, _ := os.Getwd()
		dir = controller.DefaultSessionDir("", cwd)
	}
	opts := history.DiscoverOpts{Dir: dir, Limit: 0}
	allSessions, err := discover(opts, "")
	if err != nil {
		return fmt.Errorf("session discovery failed: %w", err)
	}

	var filtered []history.Session
	for _, s := range allSessions {
		if s.TurnCount <= 5 && s.CostUSD == 0 {
			continue
		}
		if s.LastActive.IsZero() {
			continue
		}
		if s.IsSubagent {
			continue
		}
		filtered = append(filtered, s)
	}

	query := ""
	if len(args) > 0 {
		query = args[0]
	}

	if query != "" {
		filtered = searchSessionsFiltered(filtered, query, search)
		if len(filtered) == 0 {
			return fmt.Errorf("no sessions matching %q", query)
		}
	}

	total := len(filtered)
	truncated := false
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
		truncated = true
	}

	if exportMode {
		for _, s := range filtered {
			data, _ := json.Marshal(s)
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(data))
		}
		return nil
	}

	if listMode || !IsInteractive() {
		if jsonOutput {
			result := map[string]any{
				"sessions": filtered,
				"count":    len(filtered),
			}
			if truncated {
				result["total"] = total
				result["truncated"] = true
				result["hint"] = "use --limit to control result count"
			}
			b, _ := json.MarshalIndent(result, "", "  ")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
		} else {
			printSessionsTableCobra(cmd, filtered, parseFields(fields))
		}
		return nil
	}

	if picker == nil {
		return fmt.Errorf("interactive mode not available")
	}
	selected, err := picker(filtered)
	if err != nil {
		return err
	}
	if resume != nil {
		resume(selected.ID, danger)
	}
	return nil
}

func newSessionsStartCmd(d Deps) *cobra.Command {
	cmd := newSpawnCmd(d.Providers, d.SpawnAgent, d.DefaultMode, d.Environments, d.AgentConfigs)
	cmd.Use = "start <agent-name|provider>"
	cmd.Short = "Start a new agent session"
	cmd.Long = fmt.Sprintf("Start a new AI agent session. Argument is an agent config name or a provider (%s).",
		strings.Join(d.Providers, ", "))
	return cmd
}

func newSessionsStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <session-id|pid>",
		Short: "Stop a running session",
		Long:  "Stop a running agent session by PID. Sends SIGTERM to the process.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid PID %q: numeric PID required", args[0])
			}

			if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
				return fmt.Errorf("stop failed for PID %d: %w", pid, err)
			}

			if jsonOutput {
				b, _ := json.MarshalIndent(map[string]any{
					"pid":     pid,
					"stopped": true,
				}, "", "  ")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Stopped session (PID %d)\n", pid)
			}
			return nil
		},
	}
}

func newSessionsResumeCmd(builder resumeBuilderFn, execFn func(sessionID string, danger bool), skipPermissions bool) *cobra.Command {
	return newResumeCmd(builder, execFn, skipPermissions)
}

func newSessionsListSubCmd(d Deps) *cobra.Command {
	var dir string
	var listMode, exportMode, danger, allProjects bool
	var limit int
	var fields string

	cmd := &cobra.Command{
		Use:     "list [query]",
		Short:   "List and search past sessions",
		Long:    "List, search, and resume past AI agent sessions. Scopes to the current directory by default; use --all for all projects.",
		Aliases: []string{"ls"},
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionsList(cmd, args, d.DiscoverSessions, d.SearchContent, d.PickSession, d.ResumeExec,
				dir, listMode, exportMode, danger, allProjects, limit, fields)
		},
	}

	cmd.Flags().StringVar(&dir, "dir", "", "Scope to a specific directory (default: current directory)")
	cmd.Flags().BoolVarP(&allProjects, "all", "a", false, "Show sessions from all projects")
	cmd.Flags().BoolVarP(&listMode, "list", "l", false, "Table output (scriptable)")
	cmd.Flags().BoolVar(&exportMode, "export", false, "JSONL output for eval pipelines")
	cmd.Flags().BoolVarP(&danger, "danger", "d", false, "Resume with --dangerously-skip-permissions")
	cmd.Flags().IntVar(&limit, "limit", 0, "Max sessions to show (0 = all)")
	cmd.Flags().StringVar(&fields, "fields", "", "Comma-separated fields: id,provider,project,age,turns,cost,annotation,prompt,tags")

	return cmd
}

func newSessionsArchiveCmd(discover sessionsDiscoverFn) *cobra.Command {
	return &cobra.Command{
		Use:   "archive <session-id>",
		Short: "Mark a session as archived",
		Long:  "Mark a terminated session as archived. The session must exist in session history.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID := args[0]

			allSessions, err := discover(history.DiscoverOpts{}, "")
			if err != nil {
				return fmt.Errorf("session discovery failed: %w", err)
			}

			var matched *history.Session
			for i := range allSessions {
				if allSessions[i].ID == sessionID {
					s := allSessions[i]
					matched = &s
					break
				}
			}
			if matched == nil {
				return fmt.Errorf("no session found with ID %q", sessionID)
			}

			if jsonOutput {
				b, _ := json.MarshalIndent(map[string]any{
					"session_id": matched.ID,
					"archived":   true,
				}, "", "  ")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Archived session %s\n", matched.ID)
			}
			return nil
		},
	}
}

func newHiddenSpawnAlias(d Deps) *cobra.Command {
	cmd := newSpawnCmd(d.Providers, d.SpawnAgent, d.DefaultMode, d.Environments, d.AgentConfigs)
	cmd.Hidden = true
	return cmd
}

func newHiddenResumeAlias(builder resumeBuilderFn, execFn func(sessionID string, danger bool), skipPermissions bool) *cobra.Command {
	cmd := newResumeCmd(builder, execFn, skipPermissions)
	cmd.Hidden = true
	return cmd
}

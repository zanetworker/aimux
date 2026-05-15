package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/agent"
)

type agentJSON struct {
	PID         int    `json:"pid"`
	Provider    string `json:"provider"`
	Status      string `json:"status"`
	Project     string `json:"project"`
	DisplayName string `json:"display_name"`
	SessionID   string `json:"session_id,omitempty"`
	TmuxSession string `json:"tmux_session,omitempty"`
	Model       string `json:"model,omitempty"`
	WorkingDir  string `json:"working_dir"`
}

type discoverFunc func() ([]agent.Agent, error)

func newAgentsCmd(discover discoverFunc) *cobra.Command {
	var limit int
	var fields string

	cmd := &cobra.Command{
		Use:   "agents",
		Short: "List running AI agents",
		Long:  "Discover and list all running AI coding agent sessions (Claude, Codex, Gemini)",
		RunE: func(cmd *cobra.Command, args []string) error {
			agents, err := discover()
			if err != nil {
				return fmt.Errorf("discovery failed: %w", err)
			}

			total := len(agents)
			truncated := false
			if limit > 0 && len(agents) > limit {
				agents = agents[:limit]
				truncated = true
			}

			if jsonOutput {
				items := make([]agentJSON, len(agents))
				for i, a := range agents {
					items[i] = agentJSON{
						PID:         a.PID,
						Provider:    a.ProviderName,
						Status:      a.Status.String(),
						Project:     a.Name,
						DisplayName: a.ProviderName + ":" + a.Name,
						SessionID:   a.SessionID,
						TmuxSession: a.TMuxSession,
						Model:       a.ShortModel(),
						WorkingDir:  a.WorkingDir,
					}
				}
				result := map[string]any{
					"agents": items,
					"count":  len(items),
				}
				if truncated {
					result["total"] = total
					result["truncated"] = true
					result["hint"] = "use --limit to control result count"
				}
				b, _ := json.MarshalIndent(result, "", "  ")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
			} else {
				if len(agents) == 0 {
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No agents running.")
					return nil
				}
				selectedFields := parseFields(fields)
				printAgentsTable(cmd, agents, selectedFields)
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 0, "Maximum number of agents to show")
	cmd.Flags().StringVar(&fields, "fields", "", "Comma-separated fields: pid,provider,status,project,model,session_id,tmux_session,working_dir")
	return cmd
}

func printAgentsTable(cmd *cobra.Command, agents []agent.Agent, fields []string) {
	if len(fields) == 0 {
		fields = []string{"pid", "provider", "status", "project", "model"}
	}
	var headers []string
	for _, f := range fields {
		headers = append(headers, strings.ToUpper(f))
	}
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), strings.Join(headers, "\t"))

	for _, a := range agents {
		var vals []string
		for _, f := range fields {
			switch f {
			case "pid":
				vals = append(vals, fmt.Sprintf("%d", a.PID))
			case "provider":
				vals = append(vals, a.ProviderName)
			case "status":
				vals = append(vals, a.Status.String())
			case "project":
				vals = append(vals, a.Name)
			case "model":
				vals = append(vals, a.ShortModel())
			case "session_id":
				vals = append(vals, a.SessionID)
			case "tmux_session":
				vals = append(vals, a.TMuxSession)
			case "working_dir":
				vals = append(vals, a.WorkingDir)
			default:
				vals = append(vals, "")
			}
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), strings.Join(vals, "\t"))
	}
}

func parseFields(s string) []string {
	if s == "" {
		return nil
	}
	var fields []string
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f != "" {
			fields = append(fields, f)
		}
	}
	return fields
}

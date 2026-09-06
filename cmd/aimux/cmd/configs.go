package cmd

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/config"
)

func newConfigsCmd(agentConfigs []config.AgentConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "configs",
		Short: "List named agent configurations",
		Long:  "Show agent configurations from agents.yaml (global and project-local).",
		Aliases: []string{"agent-configs"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(agentConfigs) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No agent configs found. Create ~/.aimux/agents.yaml or .aimux/agents.yaml")
				return nil
			}

			if jsonOutput {
				type entry struct {
					Name      string   `json:"name"`
					Runtime   string   `json:"runtime"`
					Inference string   `json:"inference,omitempty"`
					Model     string   `json:"model,omitempty"`
					Prompt    string   `json:"prompt,omitempty"`
					MCP       []string `json:"mcp,omitempty"`
					Skills    []string `json:"skills,omitempty"`
					Policy    string   `json:"policy,omitempty"`
				}
				entries := make([]entry, len(agentConfigs))
				for i, ac := range agentConfigs {
					entries[i] = entry{
						Name: ac.Name, Runtime: ac.Runtime, Inference: ac.Inference,
						Model: ac.Model, Prompt: ac.Prompt, MCP: ac.MCP,
						Skills: ac.Skills, Policy: ac.Policy,
					}
				}
				b, _ := json.MarshalIndent(map[string]any{"configs": entries}, "", "  ")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "NAME\tRUNTIME\tMODEL\tPROMPT")
			for _, ac := range agentConfigs {
				prompt := ac.Prompt
				if len(prompt) > 50 {
					prompt = prompt[:47] + "..."
				}
				model := ac.Model
				if model == "" {
					model = "default"
				}
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", ac.Name, ac.Runtime, model, prompt)
			}
			_ = w.Flush()
			return nil
		},
	}
}

package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/controller"
)

func defaultAgentsDir() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".aimux", "agents")
}

func newConfigsCmd(agentConfigs []config.AgentConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "agents",
		Short:   "Manage named agent configurations",
		Long:    "List, create, update, and remove agent configurations from per-file YAML.",
		Aliases: []string{"configs", "agent-configs"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(agentConfigs) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No agent configs found. Create files in ~/.aimux/agents/ or .aimux/agents/")
				return nil
			}

			if jsonOutput {
				type entry struct {
					Name      string   `json:"name"`
					Harness   string   `json:"harness"`
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
						Name: ac.Name, Harness: ac.Harness, Inference: ac.Inference,
						Model: ac.Model, Prompt: ac.Prompt, MCP: ac.MCP,
						Skills: ac.Skills, Policy: ac.Policy,
					}
				}
				b, _ := json.MarshalIndent(map[string]any{"agents": entries}, "", "  ")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "NAME\tHARNESS\tMODEL\tPROMPT")
			for _, ac := range agentConfigs {
				prompt := ac.Prompt
				if len(prompt) > 50 {
					prompt = prompt[:47] + "..."
				}
				model := ac.Model
				if model == "" {
					model = "default"
				}
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", ac.Name, ac.Harness, model, prompt)
			}
			_ = w.Flush()
			return nil
		},
	}

	cmd.AddCommand(newAgentCreateCmd())
	cmd.AddCommand(newAgentUpdateCmd())
	cmd.AddCommand(newAgentRemoveCmd())

	return cmd
}

func newAgentCreateCmd() *cobra.Command {
	var harness, model, prompt string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new agent configuration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			dir := defaultAgentsDir()
			if dir == "" {
				return fmt.Errorf("cannot determine home directory")
			}

			ac := config.AgentConfig{
				Name:    name,
				Harness: harness,
				Model:   model,
				Prompt:  prompt,
			}
			if err := controller.CreateAgent(dir, ac); err != nil {
				return err
			}

			if jsonOutput {
				b, _ := json.MarshalIndent(map[string]any{"created": ac.Name, "path": filepath.Join(dir, config.AgentFileName(name))}, "", "  ")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Created agent %q in %s\n", name, dir)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&harness, "harness", "", "Agent harness (e.g. claude-sdk, claude-native)")
	cmd.Flags().StringVar(&model, "model", "", "Model to use (e.g. opus, sonnet)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "System prompt")
	return cmd
}

func newAgentUpdateCmd() *cobra.Command {
	var harness, model, prompt string
	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "Update an existing agent configuration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			dir := defaultAgentsDir()
			if dir == "" {
				return fmt.Errorf("cannot determine home directory")
			}

			updates := config.AgentConfig{
				Harness: harness,
				Model:   model,
				Prompt:  prompt,
			}
			if err := controller.UpdateAgent(dir, name, updates); err != nil {
				return err
			}

			if jsonOutput {
				b, _ := json.MarshalIndent(map[string]any{"updated": name}, "", "  ")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Updated agent %q\n", name)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&harness, "harness", "", "Agent harness")
	cmd.Flags().StringVar(&model, "model", "", "Model")
	cmd.Flags().StringVar(&prompt, "prompt", "", "System prompt")
	return cmd
}

func newAgentRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm", "delete"},
		Short:   "Remove an agent configuration",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			dir := defaultAgentsDir()
			if dir == "" {
				return fmt.Errorf("cannot determine home directory")
			}

			if err := controller.RemoveAgent(dir, name); err != nil {
				return err
			}

			if jsonOutput {
				b, _ := json.MarshalIndent(map[string]any{"removed": name}, "", "  ")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Removed agent %q\n", name)
			}
			return nil
		},
	}
}

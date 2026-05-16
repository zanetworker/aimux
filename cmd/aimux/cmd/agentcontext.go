package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type flagInfo struct {
	Type    string   `json:"type"`
	Default string   `json:"default,omitempty"`
	Values  []string `json:"values,omitempty"`
}

type argInfo struct {
	Name     string   `json:"name"`
	Required bool     `json:"required"`
	Values   []string `json:"values,omitempty"`
}

type commandInfo struct {
	Description string              `json:"description"`
	Args        []argInfo           `json:"args,omitempty"`
	Flags       map[string]flagInfo `json:"flags,omitempty"`
}

func newAgentContextCmd(providers []string) *cobra.Command {
	return &cobra.Command{
		Use:   "agent-context",
		Short: "Machine-readable CLI surface for agents",
		Long:  "Outputs the full command tree, flags, types, and valid values as JSON. Agents call this to learn the CLI contract.",
		RunE: func(cmd *cobra.Command, args []string) error {
			commands := make(map[string]commandInfo)

			for _, sub := range rootCmd.Commands() {
				if sub.Name() == "help" || sub.Name() == "completion" || sub.Name() == "agent-context" {
					continue
				}

				info := commandInfo{
					Description: sub.Short,
					Flags:       make(map[string]flagInfo),
				}

				// Extract positional args from Use string
				use := sub.Use
				if idx := strings.IndexByte(use, ' '); idx != -1 {
					argPart := use[idx+1:]
					argPart = strings.TrimSpace(argPart)
					if argPart != "" {
						required := strings.HasPrefix(argPart, "<")
						argName := strings.Trim(argPart, "<>[]")

						ai := argInfo{Name: argName, Required: required}
						if sub.Name() == "spawn" {
							ai.Values = providers
						}
						info.Args = append(info.Args, ai)
					}
				}

				// Extract flags (skip inherited --json which is on root)
				sub.Flags().VisitAll(func(f *pflag.Flag) {
					if f.Name == "help" {
						return
					}
					fi := flagInfo{
						Type:    f.Value.Type(),
						Default: f.DefValue,
					}
					info.Flags["--"+f.Name] = fi
				})

				// Add inherited --json flag
				info.Flags["--json"] = flagInfo{Type: "bool", Default: "false"}

				commands[sub.Name()] = info
			}

			result := map[string]any{
				"schema_version": "1",
				"cli_version":    rootCmd.Version,
				"commands":       commands,
				"providers":      providers,
			}

			b, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				return fmt.Errorf("failed to marshal agent context: %w", err)
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
			return nil
		},
	}
}

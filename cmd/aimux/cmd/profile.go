package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/profile"
)

func newProfileCmd(store *profile.Store) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage saved configuration profiles",
		Long:  "Save and reuse named bundles of flags (provider, dir, model, mode).",
	}

	cmd.AddCommand(newProfileSaveCmd(store))
	cmd.AddCommand(newProfileListCmd(store))
	cmd.AddCommand(newProfileGetCmd(store))
	cmd.AddCommand(newProfileDeleteCmd(store))
	return cmd
}

func newProfileSaveCmd(store *profile.Store) *cobra.Command {
	var provider, dir, model, mode string

	cmd := &cobra.Command{
		Use:   "save <name>",
		Short: "Save a named profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := profile.Profile{
				Name:     args[0],
				Provider: provider,
				Dir:      dir,
				Model:    model,
				Mode:     mode,
			}
			if err := store.Save(p); err != nil {
				return fmt.Errorf("save profile: %w", err)
			}
			if jsonOutput {
				b, _ := json.MarshalIndent(p, "", "  ")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Profile %q saved.\n", args[0])
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "", "Provider (claude, codex, gemini)")
	cmd.Flags().StringVar(&dir, "dir", "", "Working directory")
	cmd.Flags().StringVar(&model, "model", "", "Model override")
	cmd.Flags().StringVar(&mode, "mode", "", "Mode (plan, auto)")
	return cmd
}

func newProfileListCmd(store *profile.Store) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all saved profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			profiles := store.List()
			if jsonOutput {
				result := map[string]any{"profiles": profiles, "count": len(profiles)}
				b, _ := json.MarshalIndent(result, "", "  ")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
			} else {
				if len(profiles) == 0 {
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No profiles saved.")
					return nil
				}
				for _, p := range profiles {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s", p.Name)
					if p.Provider != "" {
						_, _ = fmt.Fprintf(cmd.OutOrStdout(), " (provider=%s", p.Provider)
						if p.Model != "" {
							_, _ = fmt.Fprintf(cmd.OutOrStdout(), ", model=%s", p.Model)
						}
						_, _ = fmt.Fprint(cmd.OutOrStdout(), ")")
					}
					_, _ = fmt.Fprintln(cmd.OutOrStdout())
				}
			}
			return nil
		},
	}
}

func newProfileGetCmd(store *profile.Store) *cobra.Command {
	return &cobra.Command{
		Use:   "get <name>",
		Short: "Show a profile's details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, ok := store.Get(args[0])
			if !ok {
				return fmt.Errorf("profile %q not found (available: %v)", args[0], store.Names())
			}
			if jsonOutput {
				b, _ := json.MarshalIndent(p, "", "  ")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Name:     %s\n", p.Name)
				if p.Provider != "" {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Provider: %s\n", p.Provider)
				}
				if p.Dir != "" {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Dir:      %s\n", p.Dir)
				}
				if p.Model != "" {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Model:    %s\n", p.Model)
				}
				if p.Mode != "" {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Mode:     %s\n", p.Mode)
				}
			}
			return nil
		},
	}
}

func newProfileDeleteCmd(store *profile.Store) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a saved profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := store.Delete(args[0]); err != nil {
				return err
			}
			if jsonOutput {
				result := map[string]any{"deleted": args[0]}
				b, _ := json.MarshalIndent(result, "", "  ")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Profile %q deleted.\n", args[0])
			}
			return nil
		},
	}
}

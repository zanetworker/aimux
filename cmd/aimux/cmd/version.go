package cmd

import (
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show aimux version",
		RunE: func(cmd *cobra.Command, args []string) error {
			ver := rootCmd.Version
			if ver == "" {
				ver = "dev"
			}
			if jsonOutput {
				data := map[string]string{
					"version": ver,
					"go":      runtime.Version(),
					"os":      runtime.GOOS,
					"arch":    runtime.GOARCH,
				}
				b, _ := json.MarshalIndent(data, "", "  ")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "aimux %s\n", ver)
			}
			return nil
		},
	}
}

package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var jsonOutput bool

var rootCmd = &cobra.Command{
	Use:   "aimux",
	Short: "AI agent multiplexer",
	Long:  "aimux — TUI dashboard for managing multiple AI coding agent sessions",
	RunE: func(cmd *cobra.Command, args []string) error {
		webFlag, _ := cmd.Flags().GetBool("web")
		if webFlag {
			if runBothFn != nil {
				return runBothFn(cmd, args)
			}
			return nil
		}
		if runTUIFn != nil {
			return runTUIFn(cmd, args)
		}
		return nil
	},
	SilenceUsage:  true,
	SilenceErrors: true,
}

var (
	runTUIFn  func(cmd *cobra.Command, args []string) error
	runBothFn func(cmd *cobra.Command, args []string) error
)

func init() {
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	rootCmd.Flags().Bool("web", false, "Launch TUI + web dashboard")
}

func Execute(version string) {
	rootCmd.Version = version
	rootCmd.SetVersionTemplate("aimux {{.Version}}\n")
	if err := rootCmd.Execute(); err != nil {
		out := NewOutputWriter(jsonOutput)
		code := out.WriteError(err.Error(), ExitError, nil)
		os.Exit(code)
	}
}

func SetRunTUI(fn func(cmd *cobra.Command, args []string) error) {
	runTUIFn = fn
}

func SetRunBoth(fn func(cmd *cobra.Command, args []string) error) {
	runBothFn = fn
}

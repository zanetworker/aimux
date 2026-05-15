package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

type webServerFn func(port int) error

func newWebCmd(startServer webServerFn) *cobra.Command {
	var port int

	cmd := &cobra.Command{
		Use:   "web",
		Short: "Launch web dashboard",
		Long:  "Start the aimux web dashboard for browser-based agent monitoring",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "aimux web dashboard: http://127.0.0.1:%d\n", port)
			if startServer == nil {
				return fmt.Errorf("web server not configured")
			}
			return startServer(port)
		},
	}

	cmd.Flags().IntVar(&port, "port", 3000, "Port to listen on")
	return cmd
}

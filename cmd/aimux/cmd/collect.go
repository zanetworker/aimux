package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/config"
	aimuxotel "github.com/zanetworker/aimux/internal/otel"
)

func newCollectCmd() *cobra.Command {
	var port int
	var flushInterval time.Duration

	cmd := &cobra.Command{
		Use:   "collect",
		Short: "Run headless OTEL collector",
		Long:  "Listen for OTLP/HTTP spans from Claude Code and persist per-tool token breakdowns to ~/.aimux/data/context/. Designed to run as a launchd agent.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _ := config.Load(config.DefaultPath())

			if port == 0 {
				port = cfg.OTELReceiverPort()
			}

			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("home dir: %w", err)
			}
			dataDir := filepath.Join(home, ".aimux", "data", "context")

			store := aimuxotel.NewSpanStore()
			receiver := aimuxotel.NewReceiver(store, port)
			if err := receiver.Start(); err != nil {
				return fmt.Errorf("start receiver on :%d: %w", port, err)
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "aimux collect: listening on :%d, flushing every %s to %s\n", port, flushInterval, dataDir)

			ticker := time.NewTicker(flushInterval)
			defer ticker.Stop()

			sig := make(chan os.Signal, 1)
			signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

			for {
				select {
				case <-ticker.C:
					if store.HasData() {
						_ = aimuxotel.FlushTracker(store, dataDir)
					}
				case <-sig:
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "shutting down...")
					if store.HasData() {
						_ = aimuxotel.FlushTracker(store, dataDir)
					}
					receiver.Stop()
					return nil
				}
			}
		},
	}

	cmd.Flags().IntVar(&port, "port", 0, "OTEL receiver port (default: from config or 4318)")
	cmd.Flags().DurationVar(&flushInterval, "flush-interval", 10*time.Second, "How often to write tracker JSON")

	return cmd
}

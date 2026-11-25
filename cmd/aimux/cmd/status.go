package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/controller"
	"github.com/zanetworker/aimux/internal/mcpproxy"
	"github.com/zanetworker/aimux/internal/modelproxy"
	"github.com/zanetworker/aimux/internal/toolgateway"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the health of aimux background services",
		Long: "Check the model proxy, MCP proxy, tool gateway, and collector without starting them.\n" +
			"Use --json for machine-readable output. The exit code is 1 if a service needs attention.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("find the home directory for service status: %w", err)
			}
			cfg, _ := config.Load(config.DefaultPath())
			version := cmd.Root().Version
			probes := defaultStatusProbes(defaultStatusEnv(home, version, cfg))
			o := controller.ServicesOverview(ctx, version, probes, 2*time.Second)
			return runStatus(ctx, o, cmd.OutOrStdout(), time.Now(), jsonOutput)
		},
	}
}

// defaultStatusEnv is where the four services are found on this machine.
func defaultStatusEnv(home, version string, cfg config.Config) statusEnv {
	gateway := toolgateway.ControlClient{Path: toolgateway.ControlSocketPath(home)}
	return statusEnv{
		modelAddr:     fmt.Sprintf("127.0.0.1:%d", modelproxy.StablePort),
		mcpAddr:       fmt.Sprintf("127.0.0.1:%d", restartPort(home, defaultPort, false)),
		gatewayAddr:   toolgateway.ReadonlyAddr,
		collectorAddr: fmt.Sprintf("127.0.0.1:%d", cfg.OTELReceiverPort()),
		plistDir:      filepath.Join(home, "Library", "LaunchAgents"),
		cliVersion:    version,
		gatewayStatus: gateway.Status,
	}
}

func runStatus(_ context.Context, o controller.StatusOverview, out io.Writer, now time.Time, asJSON bool) error {
	items, count := statusAttention(o)
	if asJSON {
		// The JSON carries what the table shows: the same problems with their fixes, and times in UTC.
		wire := struct {
			controller.StatusOverview
			Attention []controller.Attention `json:"attention"`
		}{StatusOverview: controller.StatusOverview{CLIBuild: o.CLIBuild, Services: slices.Clone(o.Services)}, Attention: items}
		if wire.Attention == nil {
			wire.Attention = []controller.Attention{}
		}
		for i := range wire.Services {
			wire.Services[i].StartedAt = utcTime(wire.Services[i].StartedAt)
			wire.Services[i].LastActivity = utcTime(wire.Services[i].LastActivity)
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(wire); err != nil {
			return fmt.Errorf("write status JSON: %w", err)
		}
	} else if err := writeStatusTable(o, out, now, items, count); err != nil {
		return fmt.Errorf("write status table: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("%s", plural(count, "service needs attention", "services need attention"))
	}
	return nil
}

// statusAttention lists unhealthy services' items first. The controller has already given every down or degraded
// row its reason as an item.
func statusAttention(o controller.StatusOverview) ([]controller.Attention, int) {
	var unhealthy, other controller.StatusOverview
	for _, service := range o.Services {
		switch service.State {
		case controller.ServiceDown, controller.ServiceDegraded:
			unhealthy.Services = append(unhealthy.Services, service)
		case controller.ServiceOK:
			if len(service.Attention) > 0 {
				other.Services = append(other.Services, service)
			}
		}
	}
	return append(unhealthy.Attention(), other.Attention()...), len(unhealthy.Services) + len(other.Services)
}

func writeStatusTable(o controller.StatusOverview, out io.Writer, now time.Time, items []controller.Attention, count int) error {
	var text strings.Builder
	version := o.CLIBuild
	if controller.BuildNumber(version) != version {
		version = version[:strings.LastIndex(version, "-g")]
	}
	summary := "all ok"
	if count > 0 {
		summary = plural(count, "needs attention", "need attention")
	}
	_, _ = fmt.Fprintf(&text, "aimux %s    %s, %s\n\n", statusCell(shortVersion(version), 48),
		plural(len(o.Services), "service", "services"), summary)

	table := tabwriter.NewWriter(&text, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "SERVICE\tSTATE\tBUILD\tUP\tADDRESS\tLAST ACTIVITY")
	for _, service := range o.Services {
		build, up, addr, activity := statusColumns(service, now)
		_, _ = fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n", statusCell(service.Name, 12), service.State,
			statusCell(build, 24), statusCell(up, 7), statusCell(addr, 15), statusCell(activity, 18))
	}
	_ = table.Flush()

	if len(items) > 0 {
		text.WriteByte('\n')
		const maxFixWidth = 70
		fixWidth := len("FIX")
		for _, item := range items {
			fixWidth = max(fixWidth, runewidth.StringWidth(statusCell(item.Fix, maxFixWidth)))
		}
		_, _ = fmt.Fprintln(table, "NEEDS ATTENTION\tFIX")
		for _, item := range items {
			_, _ = fmt.Fprintf(table, "%s\t%s\n", statusCell(item.Problem, 99-2-fixWidth), statusCell(item.Fix, maxFixWidth))
		}
		_ = table.Flush()
	}
	_, err := io.WriteString(out, text.String())
	return err
}

func statusColumns(service controller.ServiceStatus, now time.Time) (build, up, addr, activity string) {
	if service.State == controller.ServiceNotInstalled {
		return "-", "-", "-", "-"
	}
	build = shortVersion(controller.BuildNumber(service.Build))
	if build == "" {
		build = "-"
	}
	switch {
	case service.BuildNote != "":
		build += " (" + service.BuildNote + ")"
	case service.Build != "" && len(service.Attention) > 0:
		build += " !"
	}
	if service.StartedAt != nil {
		up = strings.TrimSuffix(mcpproxy.Ago(now, *service.StartedAt), " ago")
	}
	activity = service.ActivityNote
	switch {
	// For an unhealthy row the reason matters more than when it last worked.
	case service.State == controller.ServiceDown,
		service.State == controller.ServiceDegraded && service.Reason != "":
		activity = service.Reason
	case service.LastActivity != nil:
		activity = strings.TrimSpace(activity + " " + mcpproxy.Ago(now, *service.LastActivity))
	case activity == "turn" || activity == "call" || activity == "span":
		activity = ""
	}
	return build, up, service.Address, activity
}

func statusCell(value string, width int) string {
	if value == "" {
		return "-"
	}
	return cut(printable(value), width)
}

func utcTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

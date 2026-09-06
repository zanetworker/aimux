package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/controller"
)

func newEnvironmentsCmd(environments map[string]config.EnvironmentConfig) *cobra.Command {
	var check bool

	cmd := &cobra.Command{
		Use:   "environments",
		Short: "List configured environments",
		Long:  "Show all named environments where agents can execute (local, OpenShell, Kubernetes).\nUse --check to test connectivity.",
		Aliases: []string{"envs", "env"},
		RunE: func(cmd *cobra.Command, args []string) error {
			names := controller.EnvironmentNames(environments)
			if len(names) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No environments configured.")
				return nil
			}

			type envEntry struct {
				Name      string `json:"name"`
				Type      string `json:"type"`
				Gateway   string `json:"gateway,omitempty"`
				Namespace string `json:"namespace,omitempty"`
				Runtime   string `json:"runtime"`
				Status    string `json:"status,omitempty"`
			}

			entries := make([]envEntry, len(names))
			for i, name := range names {
				ec := environments[name]
				entries[i] = envEntry{
					Name:      name,
					Type:      ec.Type,
					Gateway:   ec.Gateway,
					Namespace: ec.Namespace,
					Runtime:   controller.ResolveLaunchRuntime(ec),
				}
				if check {
					entries[i].Status = checkEnvironmentHealth(ec)
				}
			}

			if jsonOutput {
				b, _ := json.MarshalIndent(map[string]any{"environments": entries}, "", "  ")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			if check {
				_, _ = fmt.Fprintln(w, "NAME\tTYPE\tRUNTIME\tSTATUS\tDETAILS")
			} else {
				_, _ = fmt.Fprintln(w, "NAME\tTYPE\tRUNTIME\tDETAILS")
			}
			for _, e := range entries {
				var parts []string
				ec := environments[e.Name]
				if ec.Gateway != "" {
					parts = append(parts, "gateway="+ec.Gateway)
				}
				if ec.Namespace != "" {
					parts = append(parts, "ns="+ec.Namespace)
				}
				if ec.Image != "" {
					parts = append(parts, "image="+ec.Image)
				}
				details := strings.Join(parts, ", ")
				if check {
					_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.Name, e.Type, e.Runtime, e.Status, details)
				} else {
					_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", e.Name, e.Type, e.Runtime, details)
				}
			}
			_ = w.Flush()
			return nil
		},
	}

	cmd.Flags().BoolVar(&check, "check", false, "Test connectivity to each environment")
	return cmd
}

func checkEnvironmentHealth(ec config.EnvironmentConfig) string {
	switch ec.Type {
	case "local":
		return "ready"
	case "openshell":
		if ec.Gateway == "" {
			return "no gateway configured"
		}
		url := strings.TrimRight(ec.Gateway, "/") + "/api/v1/health"
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get(url) // #nosec G107
		if err != nil {
			return fmt.Sprintf("unreachable (%s)", firstWord(err.Error()))
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == 200 {
			return "ready"
		}
		return fmt.Sprintf("unhealthy (HTTP %d)", resp.StatusCode)
	case "k8s":
		if ec.RedisURL == "" {
			return "no redis_url configured"
		}
		return "configured (run aimux agents to verify)"
	default:
		return "unknown type"
	}
}

func firstWord(s string) string {
	parts := strings.Fields(s)
	if len(parts) == 0 {
		return s
	}
	w := parts[len(parts)-1]
	if len(w) > 30 {
		w = w[:27] + "..."
	}
	return w
}

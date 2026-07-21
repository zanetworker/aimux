package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"
	aimuxcompose "github.com/zanetworker/aimux/internal/compose"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/mcpserver"
)

// mcpConfigPath overrides the config file path for testing. Empty uses the default.
var mcpConfigPath string

func newMCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "MCP server for remote agent orchestration",
		Long:  "MCP server commands for orchestrating AI coding agents on remote infrastructure.",
	}
	cmd.AddCommand(newMCPServeCmd())
	cmd.AddCommand(newMCPRegisterCmd())
	cmd.AddCommand(newMCPUnregisterCmd())
	return cmd
}

func newMCPServeCmd() *cobra.Command {
	var (
		backend    string
		gateway    string
		image      string
		warmPool   int
		redisURL   string
		kubeconfig string
		namespace  string
		teamID     string
		maxAgents  int
		maxCost    float64
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the MCP stdio server",
		Long:  "Start the remote agent MCP server over stdio. Reads config from ~/.aimux/config.yaml with flag overrides.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath := mcpConfigPath
			if cfgPath == "" {
				cfgPath = config.DefaultPath()
			}
			cfg, _ := config.Load(cfgPath)

			resolvedBackend := firstNonEmpty(backend, cfg.Remote.Backend, "k8s")

			opts := mcpserver.Options{
				Backend:         resolvedBackend,
				GatewayEndpoint: firstNonEmpty(gateway, cfg.Remote.Gateway),
				Image:           firstNonEmpty(image, cfg.Remote.Image),
				WarmPool:        max(warmPool, cfg.Remote.WarmPool),
				RedisURL:        firstNonEmpty(redisURL, cfg.Kubernetes.RedisURL),
				Kubeconfig:      firstNonEmpty(kubeconfig, cfg.Kubernetes.Kubeconfig),
				Namespace:       firstNonEmpty(namespace, cfg.Kubernetes.Namespace),
				TeamID:          firstNonEmpty(teamID, cfg.Kubernetes.TeamID),
				MaxAgents:       maxAgents,
				MaxCost:         maxCost,
			}

			if resolvedBackend == "openshell" {
				engine, err := aimuxcompose.New(aimuxcompose.Options{
					Binary:   "openshell",
					Gateway:  opts.GatewayEndpoint,
					Insecure: false,
					Image:    opts.Image,
				})
				if err != nil {
					return fmt.Errorf("compose engine: %w", err)
				}
				opts.ExternalBackend = aimuxcompose.NewBackend(engine)
			}

			// K8s backend requires Redis URL
			if resolvedBackend == "k8s" && opts.RedisURL == "" {
				return fmt.Errorf("redis URL is required for k8s backend: set --redis-url flag or kubernetes.redis_url in config")
			}

			s, err := mcpserver.NewServer(opts)
			if err != nil {
				return fmt.Errorf("create MCP server: %w", err)
			}
			return s.Serve()
		},
	}

	cmd.Flags().StringVar(&backend, "backend", "", "Backend type: openshell or k8s (default from config or k8s)")
	cmd.Flags().StringVar(&gateway, "gateway", "", "OpenShell gateway endpoint URL")
	cmd.Flags().StringVar(&image, "image", "", "Default sandbox image")
	cmd.Flags().IntVar(&warmPool, "warm-pool", 0, "Number of sandboxes to pre-create on startup")
	cmd.Flags().StringVar(&redisURL, "redis-url", "", "Redis URL (K8s backend, e.g. redis://localhost:6379)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig file (K8s backend)")
	cmd.Flags().StringVar(&namespace, "namespace", "", "Kubernetes namespace for agent deployments (K8s backend)")
	cmd.Flags().StringVar(&teamID, "team-id", "", "Team ID for Redis key scoping (K8s backend)")
	cmd.Flags().IntVar(&maxAgents, "max-agents", 0, "Maximum number of concurrent agents (default 20)")
	cmd.Flags().Float64Var(&maxCost, "max-cost", 0, "Maximum cost limit in USD (default 100)")

	return cmd
}

// firstNonEmpty returns the first non-empty string from the given values.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func newMCPRegisterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "register",
		Short: "Register aimux MCP server in Claude Code settings",
		Long:  "Write the aimux-k8s-agents MCP server entry to ~/.claude/settings.json so Claude Code can discover it.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath := mcpConfigPath
			if cfgPath == "" {
				cfgPath = config.DefaultPath()
			}
			cfg, _ := config.Load(cfgPath)

			if cfg.Kubernetes.RedisURL == "" {
				return fmt.Errorf("kubernetes.redis_url must be set in %s", cfgPath)
			}

			aimuxBin, err := os.Executable()
			if err != nil {
				return fmt.Errorf("resolve aimux binary path: %w", err)
			}

			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("resolve home directory: %w", err)
			}
			settingsPath := filepath.Join(home, ".claude", "settings.json")

			// Use defaults that match the serve command defaults.
			namespace := cfg.Kubernetes.Namespace
			if namespace == "" {
				namespace = "agents"
			}
			teamID := cfg.Kubernetes.TeamID
			maxAgents := 20
			maxCost := 100.0

			if err := registerMCPServer(settingsPath, aimuxBin, cfg.Kubernetes.RedisURL, namespace, teamID, maxAgents, maxCost); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Registered aimux-k8s-agents in %s\n", settingsPath)
			return nil
		},
	}
	return cmd
}

func newMCPUnregisterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unregister",
		Short: "Remove aimux MCP server from Claude Code settings",
		Long:  "Remove the aimux-k8s-agents entry from ~/.claude/settings.json.",
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("resolve home directory: %w", err)
			}
			settingsPath := filepath.Join(home, ".claude", "settings.json")

			if err := unregisterMCPServer(settingsPath); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Unregistered aimux-k8s-agents from %s\n", settingsPath)
			return nil
		},
	}
	return cmd
}

// AutoRegisterMCP checks if auto-registration is enabled in config and
// registers the MCP server in Claude Code settings if not already there.
func AutoRegisterMCP(cfg config.Config) {
	if !cfg.Kubernetes.IsActive() || !cfg.Kubernetes.MCP.AutoRegister {
		return
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")

	// Check if already registered
	if data, err := os.ReadFile(settingsPath); err == nil { // #nosec G304 -- settings path from user home
		var settings map[string]interface{}
		if json.Unmarshal(data, &settings) == nil {
			if servers, ok := settings["mcpServers"].(map[string]interface{}); ok {
				if _, exists := servers["aimux-k8s-agents"]; exists {
					return
				}
			}
		}
	}

	aimuxBin, err := os.Executable()
	if err != nil {
		return
	}

	maxAgents := cfg.Kubernetes.MaxAgents
	if maxAgents == 0 {
		maxAgents = 20
	}
	maxCost := cfg.Kubernetes.MaxCostUSD
	if maxCost == 0 {
		maxCost = 100
	}

	_ = registerMCPServer(
		settingsPath, aimuxBin, cfg.Kubernetes.RedisURL,
		cfg.Kubernetes.Namespace, cfg.Kubernetes.TeamID,
		maxAgents, maxCost,
	)
}

// registerMCPServer adds the aimux-k8s-agents entry to a Claude Code settings.json file.
func registerMCPServer(settingsPath, aimuxBin, redisURL, namespace, teamID string, maxAgents int, maxCost float64) error {
	settings := make(map[string]interface{})

	data, err := os.ReadFile(settingsPath) // #nosec G304 -- user-controlled path
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read settings: %w", err)
	}
	if err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("parse settings: %w", err)
		}
	}

	mcpServers, _ := settings["mcpServers"].(map[string]interface{})
	if mcpServers == nil {
		mcpServers = make(map[string]interface{})
	}

	mcpServers["aimux-k8s-agents"] = map[string]interface{}{
		"command": aimuxBin,
		"args":    []string{"mcp", "serve"},
		"env": map[string]string{
			"REDIS_URL":     redisURL,
			"K8S_NAMESPACE": namespace,
			"TEAM_ID":       teamID,
			"MAX_AGENTS":    strconv.Itoa(maxAgents),
			"MAX_COST_USD":  strconv.FormatFloat(maxCost, 'f', -1, 64),
		},
	}

	settings["mcpServers"] = mcpServers

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o750); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}

	return os.WriteFile(settingsPath, out, 0o600)
}

// unregisterMCPServer removes the aimux-k8s-agents entry from a Claude Code settings.json file.
func unregisterMCPServer(settingsPath string) error {
	data, err := os.ReadFile(settingsPath) // #nosec G304 -- user-controlled path
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to remove
		}
		return fmt.Errorf("read settings: %w", err)
	}

	settings := make(map[string]interface{})
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("parse settings: %w", err)
	}

	mcpServers, ok := settings["mcpServers"].(map[string]interface{})
	if !ok {
		return nil // no mcpServers section
	}

	if _, exists := mcpServers["aimux-k8s-agents"]; !exists {
		return nil // entry not present
	}

	delete(mcpServers, "aimux-k8s-agents")
	settings["mcpServers"] = mcpServers

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}

	return os.WriteFile(settingsPath, out, 0o600)
}

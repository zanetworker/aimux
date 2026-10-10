package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	mcplib "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
	pkgcompose "github.com/zanetworker/agent-compose/pkg/compose"
	aimuxcompose "github.com/zanetworker/aimux/internal/compose"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/coordination"
	"github.com/zanetworker/aimux/internal/mcpserver"
	"github.com/zanetworker/aimux/internal/search"
	"github.com/zanetworker/aimux/internal/sessionmcp"
)

// mcpConfigPath overrides the config file path for testing. Empty uses the default.
var mcpConfigPath string

func newMCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "MCP server for session search and remote agents",
		Long:  "MCP server commands: session search and resume tools, plus opt-in remote agent orchestration.",
	}
	cmd.AddCommand(newMCPServeCmd())
	cmd.AddCommand(newMCPRegisterCmd())
	cmd.AddCommand(newMCPUnregisterCmd())
	return cmd
}

// agentsFlags holds the remote-agent overrides of `aimux mcp serve`.
type agentsFlags struct {
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
}

func newMCPServeCmd() *cobra.Command {
	var (
		f      agentsFlags
		agents bool
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the MCP stdio server",
		Long: "Start the aimux MCP server over stdio. Session tools (search, get, list, continue and merge " +
			"Claude Code sessions) are always on. --agents adds the remote agent tools, configured from " +
			"~/.aimux/config.yaml with flag overrides; if they fail to start, the session tools keep serving.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath := mcpConfigPath
			if cfgPath == "" {
				cfgPath = config.DefaultPath()
			}
			cfg, _ := config.Load(cfgPath)
			notes := cmd.ErrOrStderr()
			return mcplib.ServeStdio(newServeMCP(agents, f, &cfg, search.DefaultService(notes), notes))
		},
	}

	cmd.Flags().BoolVar(&agents, "agents", false, "Also serve the remote agent tools (needs a k8s or openshell backend)")
	cmd.Flags().StringVar(&f.backend, "backend", "", "Backend type: openshell or k8s (default from config or k8s)")
	cmd.Flags().StringVar(&f.gateway, "gateway", "", "OpenShell gateway endpoint URL")
	cmd.Flags().StringVar(&f.image, "image", "", "Default sandbox image")
	cmd.Flags().IntVar(&f.warmPool, "warm-pool", 0, "Number of sandboxes to pre-create on startup")
	cmd.Flags().StringVar(&f.redisURL, "redis-url", "", "Redis URL (K8s backend, e.g. redis://localhost:6379)")
	cmd.Flags().StringVar(&f.kubeconfig, "kubeconfig", "", "Path to kubeconfig file (K8s backend)")
	cmd.Flags().StringVar(&f.namespace, "namespace", "", "Kubernetes namespace for agent deployments (K8s backend)")
	cmd.Flags().StringVar(&f.teamID, "team-id", "", "Team ID for Redis key scoping (K8s backend)")
	cmd.Flags().IntVar(&f.maxAgents, "max-agents", 0, "Maximum number of concurrent agents (default 20)")
	cmd.Flags().Float64Var(&f.maxCost, "max-cost", 0, "Maximum cost limit in USD (default 100)")

	return cmd
}

// newServeMCP builds the server: session tools always, agent tools when
// asked and they start; an agents failure is reported to notes, not fatal.
func newServeMCP(agents bool, f agentsFlags, cfg *config.Config, svc *search.Service, notes io.Writer) *mcplib.MCPServer {
	srv := mcplib.NewMCPServer("aimux", rootCmd.Version)
	sessionmcp.New(svc).Register(srv)
	if agents {
		s, err := buildAgentsServer(f, cfg)
		if err != nil {
			_, _ = fmt.Fprintf(notes, "agents tools disabled: %v\n", err)
		} else {
			s.Register(srv)
		}
	}
	return srv
}

// mcpExecutor runs openshell with no stdin and all output on stderr: under
// `mcp serve` the process's stdin and stdout carry JSON-RPC.
func mcpExecutor(binary string, stderr io.Writer) pkgcompose.Executor {
	return pkgcompose.NewCLIExecutor(binary, nil, stderr, stderr)
}

// buildAgentsServer resolves the remote-agent backend from flags and config.
func buildAgentsServer(f agentsFlags, cfg *config.Config) (*mcpserver.Server, error) {
	resolvedBackend := firstNonEmpty(f.backend, cfg.Remote.Backend, "k8s")

	opts := mcpserver.Options{
		Backend:         resolvedBackend,
		GatewayEndpoint: firstNonEmpty(f.gateway, cfg.Remote.Gateway),
		Image:           firstNonEmpty(f.image, cfg.Remote.Image),
		WarmPool:        max(f.warmPool, cfg.Remote.WarmPool),
		RedisURL:        firstNonEmpty(f.redisURL, cfg.Kubernetes.RedisURL),
		Kubeconfig:      firstNonEmpty(f.kubeconfig, cfg.Kubernetes.Kubeconfig),
		Namespace:       firstNonEmpty(f.namespace, cfg.Kubernetes.Namespace),
		TeamID:          firstNonEmpty(f.teamID, cfg.Kubernetes.TeamID),
		MaxAgents:       f.maxAgents,
		MaxCost:         f.maxCost,
	}

	if resolvedBackend == "openshell" {
		engine, err := aimuxcompose.New(aimuxcompose.Options{
			Binary:   "openshell",
			Gateway:  opts.GatewayEndpoint,
			Insecure: false,
			Image:    opts.Image,
			Executor: mcpExecutor("openshell", os.Stderr),
		})
		if err != nil {
			return nil, fmt.Errorf("compose engine: %w", err)
		}
		opts.ExternalBackend = aimuxcompose.NewBackend(engine)
	}

	// K8s backend requires Redis URL
	if resolvedBackend == "k8s" && opts.RedisURL == "" {
		return nil, fmt.Errorf("redis URL is required for k8s backend: set --redis-url flag or kubernetes.redis_url in config")
	}

	// Create coordinator from config
	var coord coordination.Coordinator
	coordURL := firstNonEmpty(cfg.Coordination.RedisURL, opts.RedisURL)
	coordTeam := firstNonEmpty(cfg.Coordination.TeamID, opts.TeamID)
	if resolvedBackend != "openshell" && coordURL != "" {
		var coordErr error
		coord, coordErr = coordination.NewRedisCoordinator(coordURL, coordTeam)
		if coordErr != nil {
			coord = coordination.NewLocalCoordinator()
		}
	} else {
		coord = coordination.NewLocalCoordinator()
	}
	opts.Coordinator = coord

	s, err := mcpserver.NewServer(opts)
	if err != nil {
		return nil, fmt.Errorf("create MCP server: %w", err)
	}
	return s, nil
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
		"args":    []string{"mcp", "serve", "--agents"},
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

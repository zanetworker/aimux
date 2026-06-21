# MCP Server CLI Wiring Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire the existing K8s MCP server into the aimux CLI as `aimux mcp serve`, and auto-register it in Claude Code settings when kubernetes is enabled.

**Architecture:** Extract the MCP server logic from `cmd/mcp/main.go` into `internal/mcpserver/` as a reusable package with a `Server` struct. Add a cobra subcommand `mcp serve` that instantiates and runs it. Add an `aimux mcp register` command that writes the MCP server entry into Claude Code's `~/.claude/settings.json`. Config already has `K8sProviderConfig` with all needed fields.

**Tech Stack:** Go, cobra, mcp-go, redis, k8s client-go

---

### Task 1: Extract MCP server into internal package

Move the server logic from `cmd/mcp/main.go` (which uses package-level globals) into a clean `internal/mcpserver/` package with a `Server` struct that holds all state.

**Files:**
- Create: `internal/mcpserver/server.go`
- Create: `internal/mcpserver/server_test.go`
- Modify: `cmd/mcp/main.go` (slim down to call the new package)

- [ ] **Step 1: Write the failing test**

Create `internal/mcpserver/server_test.go`:

```go
package mcpserver

import "testing"

func TestNewServer(t *testing.T) {
	opts := Options{
		RedisURL:    "redis://localhost:6379",
		Kubeconfig:  "",
		Namespace:   "agents",
		TeamID:      "test-team",
		MaxAgents:   20,
		MaxCostUSD:  100,
		GithubToken: "",
		GithubRepo:  "",
	}
	s, err := NewServer(opts)
	if err != nil {
		// Expected to fail without Redis/K8s, but should not panic
		t.Logf("NewServer returned error (expected without infra): %v", err)
		return
	}
	if s == nil {
		t.Fatal("NewServer returned nil without error")
	}
}

func TestOptionsFromConfig(t *testing.T) {
	opts := Options{
		Namespace: "",
		TeamID:    "",
		MaxAgents: 0,
	}
	opts = opts.withDefaults()
	if opts.Namespace != "agents" {
		t.Errorf("expected default namespace 'agents', got %q", opts.Namespace)
	}
	if opts.TeamID != "default" {
		t.Errorf("expected default team 'default', got %q", opts.TeamID)
	}
	if opts.MaxAgents != 20 {
		t.Errorf("expected default max agents 20, got %d", opts.MaxAgents)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/mcpserver/ -timeout 30s -v`
Expected: FAIL — package does not exist

- [ ] **Step 3: Create the server package**

Create `internal/mcpserver/server.go`. Extract all handler functions and the `Server` struct from `cmd/mcp/main.go`. Replace package-level globals with struct fields. Key changes:
- `rdb`, `k8s`, `namespace`, `teamID`, `maxAgents`, `maxCost`, `githubToken`, `githubRepo` become `Server` fields
- `teamKey()` becomes a method on `Server`
- All `handle*` functions become methods on `Server`
- `Options` struct replaces env var reads
- `withDefaults()` fills zero values

```go
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/redis/go-redis/v9"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

var validTaskID = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// Options configures a Server. Zero values are replaced by withDefaults().
type Options struct {
	RedisURL    string
	Kubeconfig  string
	Namespace   string
	TeamID      string
	MaxAgents   int
	MaxCostUSD  float64
	GithubToken string
	GithubRepo  string
}

func (o Options) withDefaults() Options {
	if o.Namespace == "" {
		o.Namespace = "agents"
	}
	if o.TeamID == "" {
		o.TeamID = "default"
	}
	if o.MaxAgents == 0 {
		o.MaxAgents = 20
	}
	if o.MaxCostUSD == 0 {
		o.MaxCostUSD = 100
	}
	return o
}

// Server holds all state for the K8s agents MCP server.
type Server struct {
	rdb         *redis.Client
	k8s         *kubernetes.Clientset
	namespace   string
	teamID      string
	maxAgents   int
	maxCost     float64
	githubToken string
	githubRepo  string
}

// NewServer creates a Server from options, connecting to Redis and K8s.
func NewServer(opts Options) (*Server, error) {
	opts = opts.withDefaults()

	redisOpt, err := redis.ParseURL(opts.RedisURL)
	if err != nil {
		return nil, fmt.Errorf("invalid redis URL: %w", err)
	}
	rdb := redis.NewClient(redisOpt)

	k8sConfig, err := clientcmd.BuildConfigFromFlags("", opts.Kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("k8s config: %w", err)
	}
	k8sClient, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		return nil, fmt.Errorf("k8s client: %w", err)
	}

	return &Server{
		rdb:         rdb,
		k8s:         k8sClient,
		namespace:   opts.Namespace,
		teamID:      opts.TeamID,
		maxAgents:   opts.MaxAgents,
		maxCost:     opts.MaxCostUSD,
		githubToken: opts.GithubToken,
		githubRepo:  opts.GithubRepo,
	}, nil
}

func (s *Server) teamKey(suffix string) string {
	return fmt.Sprintf("team:%s:%s", s.teamID, suffix)
}

// Serve registers all MCP tools and starts the stdio server. Blocks until exit.
func (s *Server) Serve() error {
	srv := mcpserver.NewMCPServer("k8s-agents", "1.0.0")
	srv.AddTool(s.spawnAgentTool(), s.handleSpawnAgent)
	srv.AddTool(s.createTaskTool(), s.handleCreateTask)
	srv.AddTool(s.listTasksTool(), s.handleListTasks)
	srv.AddTool(s.getTaskTool(), s.handleGetTask)
	srv.AddTool(s.getTaskResultTool(), s.handleGetTaskResult)
	srv.AddTool(s.waitForTaskTool(), s.handleWaitForTask)
	srv.AddTool(s.listAgentsTool(), s.handleListAgents)
	srv.AddTool(s.sendMessageTool(), s.handleSendMessage)
	srv.AddTool(s.scaleDownTool(), s.handleScaleDown)
	srv.AddTool(s.getCostsTool(), s.handleGetCosts)
	srv.AddTool(s.cleanupBranchesTool(), s.handleCleanupBranches)
	return mcpserver.ServeStdio(srv)
}
```

Then move all the tool definition and handler methods (spawnAgentTool, handleSpawnAgent, etc.) from `cmd/mcp/main.go` onto `*Server`, replacing global variable references with `s.rdb`, `s.k8s`, `s.namespace`, etc. The helper functions `joinLines` and `splitComma` stay as package-level functions.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/mcpserver/ -timeout 30s -v`
Expected: PASS

- [ ] **Step 5: Update cmd/mcp/main.go to use the new package**

Slim `cmd/mcp/main.go` to:

```go
package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/zanetworker/aimux/internal/mcpserver"
)

func main() {
	opts := mcpserver.Options{
		RedisURL:    envOr("REDIS_URL", "redis://localhost:6379"),
		Kubeconfig:  os.Getenv("KUBECONFIG"),
		Namespace:   envOr("K8S_NAMESPACE", "agents"),
		TeamID:      envOr("TEAM_ID", "default"),
		GithubToken: os.Getenv("GITHUB_TOKEN"),
		GithubRepo:  os.Getenv("GITHUB_REPO"),
	}
	opts.MaxAgents, _ = strconv.Atoi(envOr("MAX_AGENTS", "20"))
	opts.MaxCostUSD, _ = strconv.ParseFloat(envOr("MAX_COST_USD", "100"), 64)

	s, err := mcpserver.NewServer(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if err := s.Serve(); err != nil {
		fmt.Fprintf(os.Stderr, "MCP server error: %v\n", err)
		os.Exit(1)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
```

- [ ] **Step 6: Run all tests**

Run: `go test ./internal/mcpserver/ ./cmd/mcp/ -timeout 30s -v`
Expected: PASS — existing cmd/mcp tests still pass, new package tests pass

- [ ] **Step 7: Build both binaries**

Run: `go build ./cmd/aimux && go build ./cmd/mcp/`
Expected: Both compile

- [ ] **Step 8: Commit**

```bash
git add internal/mcpserver/ cmd/mcp/main.go
git commit -m "refactor: extract MCP server into internal/mcpserver package"
```

---

### Task 2: Add `aimux mcp serve` cobra subcommand

**Files:**
- Create: `cmd/aimux/cmd/mcp.go`
- Create: `cmd/aimux/cmd/mcp_test.go`
- Modify: `cmd/aimux/cmd/register.go` (add mcp command)

- [ ] **Step 1: Write the failing test**

Create `cmd/aimux/cmd/mcp_test.go`:

```go
package cmd

import (
	"strings"
	"testing"
)

func TestMCPServeCmd_MissingRedisURL(t *testing.T) {
	cmd := newMCPCmd()
	// Run without required flags — should error about redis URL
	cmd.SetArgs([]string{"serve"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when redis URL is not configured")
	}
	if !strings.Contains(err.Error(), "redis") {
		t.Errorf("expected error about redis, got: %v", err)
	}
}

func TestMCPServeCmd_Flags(t *testing.T) {
	cmd := newMCPCmd()
	serve := cmd.Commands()[0] // "serve" subcommand

	flags := []string{"redis-url", "kubeconfig", "namespace", "team-id", "max-agents", "max-cost"}
	for _, name := range flags {
		if serve.Flags().Lookup(name) == nil {
			t.Errorf("missing flag: --%s", name)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/aimux/cmd/ -run TestMCP -timeout 30s -v`
Expected: FAIL — `newMCPCmd` does not exist

- [ ] **Step 3: Create the mcp subcommand**

Create `cmd/aimux/cmd/mcp.go`:

```go
package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/config"
	"github.com/zanetworker/aimux/internal/mcpserver"
)

func newMCPCmd() *cobra.Command {
	mcpCmd := &cobra.Command{
		Use:   "mcp",
		Short: "MCP server for K8s agent orchestration",
		Long:  "Manage the MCP server that lets Claude Code spawn and coordinate agents on Kubernetes.",
	}
	mcpCmd.AddCommand(newMCPServeCmd())
	return mcpCmd
}

func newMCPServeCmd() *cobra.Command {
	var (
		redisURL   string
		kubeconfig string
		namespace  string
		teamID     string
		maxAgents  int
		maxCost    float64
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the K8s agents MCP server (stdio)",
		Long: "Start the MCP server that provides spawn_agent, create_task, list_agents, " +
			"and other tools for orchestrating AI agents on Kubernetes. " +
			"Communicates via stdio (MCP protocol). " +
			"Configure in ~/.aimux/config.yaml under the kubernetes section.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _ := config.Load(config.DefaultPath())

			opts := mcpserver.Options{
				RedisURL:   firstNonEmpty(redisURL, cfg.Kubernetes.RedisURL),
				Kubeconfig: firstNonEmpty(kubeconfig, cfg.Kubernetes.Kubeconfig),
				Namespace:  firstNonEmpty(namespace, cfg.Kubernetes.Namespace),
				TeamID:     firstNonEmpty(teamID, cfg.Kubernetes.TeamID),
				MaxAgents:  maxAgents,
			}
			if maxCost > 0 {
				opts.MaxCostUSD = maxCost
			}

			if opts.RedisURL == "" {
				return fmt.Errorf("redis URL required: set --redis-url flag or kubernetes.redis_url in config")
			}

			s, err := mcpserver.NewServer(opts)
			if err != nil {
				return fmt.Errorf("failed to start MCP server: %w", err)
			}
			return s.Serve()
		},
	}

	cmd.Flags().StringVar(&redisURL, "redis-url", "", "Redis connection URL (overrides config)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig (overrides config)")
	cmd.Flags().StringVar(&namespace, "namespace", "", "K8s namespace (default: from config or 'agents')")
	cmd.Flags().StringVar(&teamID, "team-id", "", "Team ID for Redis key prefix (default: from config or 'default')")
	cmd.Flags().IntVar(&maxAgents, "max-agents", 0, "Max concurrent agent pods (default: 20)")
	cmd.Flags().Float64Var(&maxCost, "max-cost", 0, "Cost warning threshold in USD (default: 100)")

	return cmd
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/aimux/cmd/ -run TestMCP -timeout 30s -v`
Expected: PASS

- [ ] **Step 5: Register the mcp command**

Add to `cmd/aimux/cmd/register.go` in the `RegisterAll` function:

```go
rootCmd.AddCommand(newMCPCmd())
```

Add it after the `newCollectCmd()` line.

- [ ] **Step 6: Build and verify**

Run: `go build -o aimux ./cmd/aimux && ./aimux mcp serve --help`
Expected: Shows help with all flags

- [ ] **Step 7: Commit**

```bash
git add cmd/aimux/cmd/mcp.go cmd/aimux/cmd/mcp_test.go cmd/aimux/cmd/register.go
git commit -m "feat: add aimux mcp serve subcommand"
```

---

### Task 3: Add `aimux mcp register` command

Writes the MCP server entry into Claude Code's `~/.claude/settings.json` so Claude Code can use the K8s agent tools. Also supports `aimux mcp unregister` to remove it.

**Files:**
- Modify: `cmd/aimux/cmd/mcp.go` (add register/unregister subcommands)
- Create: `cmd/aimux/cmd/mcp_register_test.go`

- [ ] **Step 1: Write the failing test**

Create `cmd/aimux/cmd/mcp_register_test.go`:

```go
package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMCPRegister_CreatesEntry(t *testing.T) {
	// Use a temp dir as fake Claude settings
	tmpDir := t.TempDir()
	settingsPath := filepath.Join(tmpDir, "settings.json")

	// Write an empty settings file
	initial := map[string]interface{}{"model": "test"}
	data, _ := json.MarshalIndent(initial, "", "  ")
	os.WriteFile(settingsPath, data, 0644)

	aimuxBin := "/usr/local/bin/aimux"
	redisURL := "redis://:pass@localhost:6379"

	err := registerMCPServer(settingsPath, aimuxBin, redisURL, "agents", "my-team", 20, 100)
	if err != nil {
		t.Fatalf("registerMCPServer failed: %v", err)
	}

	// Read back and verify
	raw, _ := os.ReadFile(settingsPath)
	var result map[string]interface{}
	json.Unmarshal(raw, &result)

	servers, ok := result["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatal("mcpServers not found in settings")
	}
	entry, ok := servers["aimux-k8s-agents"]
	if !ok {
		t.Fatal("aimux-k8s-agents entry not found")
	}
	entryMap := entry.(map[string]interface{})
	if entryMap["command"] != aimuxBin {
		t.Errorf("expected command %q, got %q", aimuxBin, entryMap["command"])
	}
	args := entryMap["args"].([]interface{})
	if len(args) < 2 || args[0] != "mcp" || args[1] != "serve" {
		t.Errorf("expected args [mcp, serve, ...], got %v", args)
	}
}

func TestMCPUnregister_RemovesEntry(t *testing.T) {
	tmpDir := t.TempDir()
	settingsPath := filepath.Join(tmpDir, "settings.json")

	initial := map[string]interface{}{
		"model": "test",
		"mcpServers": map[string]interface{}{
			"aimux-k8s-agents": map[string]interface{}{
				"command": "/usr/local/bin/aimux",
			},
		},
	}
	data, _ := json.MarshalIndent(initial, "", "  ")
	os.WriteFile(settingsPath, data, 0644)

	err := unregisterMCPServer(settingsPath)
	if err != nil {
		t.Fatalf("unregisterMCPServer failed: %v", err)
	}

	raw, _ := os.ReadFile(settingsPath)
	var result map[string]interface{}
	json.Unmarshal(raw, &result)

	servers, ok := result["mcpServers"].(map[string]interface{})
	if !ok {
		// mcpServers removed entirely, that's fine
		return
	}
	if _, exists := servers["aimux-k8s-agents"]; exists {
		t.Error("aimux-k8s-agents entry should have been removed")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/aimux/cmd/ -run TestMCPRegister -timeout 30s -v`
Expected: FAIL — `registerMCPServer` does not exist

- [ ] **Step 3: Implement register/unregister**

Add to `cmd/aimux/cmd/mcp.go`:

```go
func newMCPRegisterCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "register",
		Short: "Register aimux MCP server in Claude Code settings",
		Long: "Add the aimux K8s agents MCP server to Claude Code's settings.json " +
			"so Claude can use spawn_agent, create_task, and other K8s tools. " +
			"Reads connection details from ~/.aimux/config.yaml kubernetes section.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _ := config.Load(config.DefaultPath())

			if cfg.Kubernetes.RedisURL == "" {
				return fmt.Errorf("kubernetes.redis_url must be set in ~/.aimux/config.yaml")
			}

			aimuxBin, err := os.Executable()
			if err != nil {
				return fmt.Errorf("cannot find aimux binary path: %w", err)
			}

			home, _ := os.UserHomeDir()
			settingsPath := filepath.Join(home, ".claude", "settings.json")

			err = registerMCPServer(
				settingsPath, aimuxBin, cfg.Kubernetes.RedisURL,
				cfg.Kubernetes.Namespace, cfg.Kubernetes.TeamID,
				20, 100,
			)
			if err != nil {
				return err
			}
			fmt.Println("Registered aimux-k8s-agents MCP server in Claude Code settings.")
			fmt.Println("Restart Claude Code to pick up the change.")
			return nil
		},
	}
}

func newMCPUnregisterCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unregister",
		Short: "Remove aimux MCP server from Claude Code settings",
		RunE: func(cmd *cobra.Command, args []string) error {
			home, _ := os.UserHomeDir()
			settingsPath := filepath.Join(home, ".claude", "settings.json")
			if err := unregisterMCPServer(settingsPath); err != nil {
				return err
			}
			fmt.Println("Removed aimux-k8s-agents from Claude Code settings.")
			return nil
		},
	}
}

func registerMCPServer(settingsPath, aimuxBin, redisURL, namespace, teamID string, maxAgents int, maxCost float64) error {
	settings := make(map[string]interface{})
	if data, err := os.ReadFile(settingsPath); err == nil {
		_ = json.Unmarshal(data, &settings)
	}

	servers, ok := settings["mcpServers"].(map[string]interface{})
	if !ok {
		servers = make(map[string]interface{})
	}

	servers["aimux-k8s-agents"] = map[string]interface{}{
		"command": aimuxBin,
		"args":    []string{"mcp", "serve"},
		"env": map[string]string{
			"REDIS_URL":     redisURL,
			"K8S_NAMESPACE": namespace,
			"TEAM_ID":       teamID,
			"MAX_AGENTS":    fmt.Sprintf("%d", maxAgents),
			"MAX_COST_USD":  fmt.Sprintf("%.0f", maxCost),
		},
	}
	settings["mcpServers"] = servers

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	return os.WriteFile(settingsPath, data, 0644)
}

func unregisterMCPServer(settingsPath string) error {
	settings := make(map[string]interface{})
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return nil // no settings file, nothing to remove
	}
	_ = json.Unmarshal(data, &settings)

	servers, ok := settings["mcpServers"].(map[string]interface{})
	if !ok {
		return nil
	}
	delete(servers, "aimux-k8s-agents")
	settings["mcpServers"] = servers

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	return os.WriteFile(settingsPath, out, 0644)
}
```

Add the subcommands in `newMCPCmd()`:

```go
mcpCmd.AddCommand(newMCPRegisterCmd())
mcpCmd.AddCommand(newMCPUnregisterCmd())
```

Add the `os` and `path/filepath` imports to the import block.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/aimux/cmd/ -run TestMCP -timeout 30s -v`
Expected: PASS

- [ ] **Step 5: Build and verify**

Run: `go build -o aimux ./cmd/aimux && ./aimux mcp register --help`
Expected: Shows help text

- [ ] **Step 6: Commit**

```bash
git add cmd/aimux/cmd/mcp.go cmd/aimux/cmd/mcp_register_test.go
git commit -m "feat: add aimux mcp register/unregister commands"
```

---

### Task 4: Add K8s MCP config fields for auto-registration

Add `MaxAgents` and `MaxCostUSD` to `K8sProviderConfig` so they can be set in `~/.aimux/config.yaml` instead of only via flags/env vars. Also add the `MCP` sub-section for controlling auto-registration.

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/config/config_test.go`:

```go
func TestK8sConfigMCPFields(t *testing.T) {
	yaml := `
kubernetes:
  enabled: true
  redis_url: "redis://:pass@host:6379"
  team_id: "my-team"
  namespace: "agents"
  max_agents: 10
  max_cost_usd: 50
  mcp:
    auto_register: true
`
	tmpFile := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(tmpFile, []byte(yaml), 0644)

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Kubernetes.MaxAgents != 10 {
		t.Errorf("expected max_agents 10, got %d", cfg.Kubernetes.MaxAgents)
	}
	if cfg.Kubernetes.MaxCostUSD != 50 {
		t.Errorf("expected max_cost_usd 50, got %f", cfg.Kubernetes.MaxCostUSD)
	}
	if !cfg.Kubernetes.MCP.AutoRegister {
		t.Error("expected mcp.auto_register to be true")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestK8sConfigMCPFields -timeout 30s -v`
Expected: FAIL — fields don't exist

- [ ] **Step 3: Add fields to K8sProviderConfig**

In `internal/config/config.go`, add to `K8sProviderConfig`:

```go
type K8sProviderConfig struct {
	Enabled      bool           `yaml:"enabled"`
	RedisURL     string         `yaml:"redis_url"`
	TeamID       string         `yaml:"team_id"`
	Namespace    string         `yaml:"namespace"`
	Kubeconfig   string         `yaml:"kubeconfig"`
	OTELEndpoint string         `yaml:"otel_endpoint"`
	MaxAgents    int            `yaml:"max_agents"`
	MaxCostUSD   float64        `yaml:"max_cost_usd"`
	MCP          K8sMCPConfig   `yaml:"mcp"`
}

type K8sMCPConfig struct {
	AutoRegister bool `yaml:"auto_register"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -timeout 30s -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: add max_agents, max_cost_usd, mcp.auto_register to K8s config"
```

---

### Task 5: Auto-register MCP server on `aimux` startup

When `kubernetes.enabled` and `kubernetes.mcp.auto_register` are both true, aimux checks if the MCP server is registered in Claude Code settings and registers it if not. This runs once at startup, not as a background process.

**Files:**
- Modify: `cmd/aimux/main.go` (add auto-registration call after config load)
- Modify: `cmd/aimux/cmd/mcp.go` (export `registerMCPServer` for use from main.go, or move to internal package)

- [ ] **Step 1: Create an internal function for auto-registration**

Add to `cmd/aimux/cmd/mcp.go`:

```go
// AutoRegisterMCP checks if auto-registration is enabled in config and
// registers the MCP server in Claude Code settings if it's not already there.
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
	if data, err := os.ReadFile(settingsPath); err == nil {
		var settings map[string]interface{}
		if json.Unmarshal(data, &settings) == nil {
			if servers, ok := settings["mcpServers"].(map[string]interface{}); ok {
				if _, exists := servers["aimux-k8s-agents"]; exists {
					return // already registered
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
```

- [ ] **Step 2: Call from main.go**

In `cmd/aimux/main.go`, after the config load block (around line 44), add:

```go
cmd.AutoRegisterMCP(cfg)
```

- [ ] **Step 3: Build and verify**

Run: `go build -o aimux ./cmd/aimux`
Expected: Compiles

- [ ] **Step 4: Run all tests**

Run: `go test ./... -timeout 30s`
Expected: All pass

- [ ] **Step 5: Commit**

```bash
git add cmd/aimux/cmd/mcp.go cmd/aimux/main.go
git commit -m "feat: auto-register MCP server in Claude Code when kubernetes is enabled"
```

---

### Task 6: Update docs and CLAUDE.md

**Files:**
- Modify: `/Users/azaalouk/go/src/github.com/zanetworker/aimux/.claude/CLAUDE.md`

- [ ] **Step 1: Add MCP server to project structure**

In the project structure tree in `.claude/CLAUDE.md`, add after the `spawn/` entry:

```
  mcpserver/                    # K8s agents MCP server (spawn, tasks, messaging, costs)
```

- [ ] **Step 2: Add MCP section to Key Patterns**

Add a bullet after "Spawn & Runtime":

```
- **K8s MCP server**: `internal/mcpserver/` provides an MCP stdio server with tools for K8s agent orchestration (spawn_agent, create_task, send_message, scale_down, get_costs). CLI: `aimux mcp serve`. Auto-registers in Claude Code settings when `kubernetes.mcp.auto_register` is true. Agents run as K8s Deployments (scale-to-zero), coordinate via Redis, execute tasks using `claude-code-sdk`.
```

- [ ] **Step 3: Update config example**

Add to the Key Config yaml block:

```yaml
kubernetes:
  enabled: true
  redis_url: "redis://:pass@redis-host:6379"
  namespace: "agents"
  team_id: "my-team"
  max_agents: 20
  max_cost_usd: 100
  mcp:
    auto_register: true
```

- [ ] **Step 4: Commit**

```bash
git add .claude/CLAUDE.md
git commit -m "docs: add K8s MCP server to project guide"
```

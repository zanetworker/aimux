package main

import (
	"fmt"
	"os"
	"strconv"

	aimuxcompose "github.com/zanetworker/aimux/internal/compose"
	"github.com/zanetworker/aimux/internal/coordination"
	"github.com/zanetworker/aimux/internal/mcpserver"
)

func main() {
	maxAgents, _ := strconv.Atoi(envOr("MAX_AGENTS", "20"))
	maxCost, _ := strconv.ParseFloat(envOr("MAX_COST_USD", "100"), 64)

	opts := mcpserver.Options{
		Backend:         envOr("AIMUX_BACKEND", "k8s"),
		GatewayEndpoint: os.Getenv("OPENSHELL_GATEWAY"),
		Image:           os.Getenv("AIMUX_IMAGE"),
		RedisURL:        envOr("REDIS_URL", "redis://localhost:6379"),
		Kubeconfig:      os.Getenv("KUBECONFIG"),
		Namespace:       envOr("K8S_NAMESPACE", "agents"),
		TeamID:          envOr("TEAM_ID", "default"),
		MaxAgents:       maxAgents,
		MaxCost:         maxCost,
		GithubToken:     os.Getenv("GITHUB_TOKEN"),
		GithubRepo:      os.Getenv("GITHUB_REPO"),
	}

	if opts.Backend == "openshell" {
		engine, err := aimuxcompose.New(aimuxcompose.Options{
			Gateway: opts.GatewayEndpoint,
			Image:   opts.Image,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "compose engine: %v\n", err)
			os.Exit(1)
		}
		opts.ExternalBackend = aimuxcompose.NewBackend(engine)
	}

	// Create coordinator from config
	var coord coordination.Coordinator
	if opts.Backend != "openshell" && opts.RedisURL != "" {
		var coordErr error
		coord, coordErr = coordination.NewRedisCoordinator(opts.RedisURL, opts.TeamID)
		if coordErr != nil {
			coord = coordination.NewLocalCoordinator()
		}
	} else {
		coord = coordination.NewLocalCoordinator()
	}
	opts.Coordinator = coord

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

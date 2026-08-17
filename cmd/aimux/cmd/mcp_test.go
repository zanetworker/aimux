package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestMCPServeCmd_MissingRedisURL(t *testing.T) {
	// Point to a nonexistent config so defaults are used (no redis URL).
	mcpConfigPath = "/nonexistent/config.yaml"
	defer func() { mcpConfigPath = "" }()

	cmd := newMCPCmd()
	rootCmd.AddCommand(cmd)
	defer rootCmd.RemoveCommand(cmd)

	rootCmd.SetArgs([]string{"mcp", "serve"})
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when redis URL is missing, got nil")
	}
	if got := err.Error(); !strings.Contains(got, "redis") {
		t.Errorf("error should mention redis, got %q", got)
	}
}

func TestMCPServeCmd_Flags(t *testing.T) {
	cmd := newMCPServeCmd()

	flags := []string{
		"redis-url",
		"kubeconfig",
		"namespace",
		"team-id",
		"max-agents",
		"max-cost",
	}

	for _, name := range flags {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("missing flag --%s on serve subcommand", name)
		}
	}
}

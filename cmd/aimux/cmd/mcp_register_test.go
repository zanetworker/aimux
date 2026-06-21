package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMCPRegister_CreatesEntry(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	// Start with a minimal settings file.
	initial := map[string]interface{}{"model": "test"}
	data, err := json.Marshal(initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	err = registerMCPServer(settingsPath, "/usr/local/bin/aimux", "redis://:pass@localhost:6379", "agents", "my-team", 20, 100)
	if err != nil {
		t.Fatalf("registerMCPServer returned error: %v", err)
	}

	// Read back and verify.
	out, err := os.ReadFile(settingsPath) // #nosec G304 -- test temp path
	if err != nil {
		t.Fatal(err)
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(out, &settings); err != nil {
		t.Fatal(err)
	}

	mcpServers, ok := settings["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatal("mcpServers key missing or not a map")
	}

	entry, ok := mcpServers["aimux-k8s-agents"].(map[string]interface{})
	if !ok {
		t.Fatal("aimux-k8s-agents entry missing or not a map")
	}

	if entry["command"] != "/usr/local/bin/aimux" {
		t.Errorf("command = %v, want /usr/local/bin/aimux", entry["command"])
	}

	args, ok := entry["args"].([]interface{})
	if !ok || len(args) != 2 || args[0] != "mcp" || args[1] != "serve" {
		t.Errorf("args = %v, want [mcp serve]", entry["args"])
	}

	env, ok := entry["env"].(map[string]interface{})
	if !ok {
		t.Fatal("env missing or not a map")
	}
	if env["REDIS_URL"] != "redis://:pass@localhost:6379" {
		t.Errorf("REDIS_URL = %v, want redis://:pass@localhost:6379", env["REDIS_URL"])
	}
	if env["K8S_NAMESPACE"] != "agents" {
		t.Errorf("K8S_NAMESPACE = %v, want agents", env["K8S_NAMESPACE"])
	}
	if env["TEAM_ID"] != "my-team" {
		t.Errorf("TEAM_ID = %v, want my-team", env["TEAM_ID"])
	}
	if env["MAX_AGENTS"] != "20" {
		t.Errorf("MAX_AGENTS = %v, want 20", env["MAX_AGENTS"])
	}
	if env["MAX_COST_USD"] != "100" {
		t.Errorf("MAX_COST_USD = %v, want 100", env["MAX_COST_USD"])
	}
}

func TestMCPUnregister_RemovesEntry(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	// Write settings with an existing aimux-k8s-agents entry.
	initial := map[string]interface{}{
		"model": "test",
		"mcpServers": map[string]interface{}{
			"aimux-k8s-agents": map[string]interface{}{
				"command": "/usr/local/bin/aimux",
				"args":    []string{"mcp", "serve"},
			},
			"other-server": map[string]interface{}{
				"command": "/usr/bin/other",
			},
		},
	}
	data, err := json.Marshal(initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	err = unregisterMCPServer(settingsPath)
	if err != nil {
		t.Fatalf("unregisterMCPServer returned error: %v", err)
	}

	out, err := os.ReadFile(settingsPath) // #nosec G304 -- test temp path
	if err != nil {
		t.Fatal(err)
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(out, &settings); err != nil {
		t.Fatal(err)
	}

	mcpServers, ok := settings["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatal("mcpServers key missing")
	}

	if _, exists := mcpServers["aimux-k8s-agents"]; exists {
		t.Error("aimux-k8s-agents entry still present after unregister")
	}

	// other-server should still be there.
	if _, exists := mcpServers["other-server"]; !exists {
		t.Error("other-server entry was removed; should be preserved")
	}
}

func TestMCPRegister_PreservesExistingSettings(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	initial := map[string]interface{}{
		"model":          "claude-sonnet-4-20250514",
		"enabledPlugins": []string{"plugin-a", "plugin-b"},
	}
	data, err := json.Marshal(initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	err = registerMCPServer(settingsPath, "/usr/local/bin/aimux", "redis://localhost:6379", "default", "", 10, 50)
	if err != nil {
		t.Fatalf("registerMCPServer returned error: %v", err)
	}

	out, err := os.ReadFile(settingsPath) // #nosec G304 -- test temp path
	if err != nil {
		t.Fatal(err)
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(out, &settings); err != nil {
		t.Fatal(err)
	}

	if settings["model"] != "claude-sonnet-4-20250514" {
		t.Errorf("model = %v, want claude-sonnet-4-20250514", settings["model"])
	}

	plugins, ok := settings["enabledPlugins"].([]interface{})
	if !ok || len(plugins) != 2 {
		t.Errorf("enabledPlugins = %v, want [plugin-a plugin-b]", settings["enabledPlugins"])
	}

	// mcpServers should also be present.
	mcpServers, ok := settings["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatal("mcpServers key missing")
	}
	if _, exists := mcpServers["aimux-k8s-agents"]; !exists {
		t.Error("aimux-k8s-agents entry missing")
	}
}

func TestMCPUnregister_NoFileIsNoOp(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "nonexistent.json")

	err := unregisterMCPServer(settingsPath)
	if err != nil {
		t.Fatalf("unregisterMCPServer on missing file returned error: %v", err)
	}
}

func TestMCPUnregister_NoEntryIsNoOp(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	initial := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"other-server": map[string]interface{}{"command": "/usr/bin/other"},
		},
	}
	data, err := json.Marshal(initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	err = unregisterMCPServer(settingsPath)
	if err != nil {
		t.Fatalf("unregisterMCPServer returned error: %v", err)
	}

	// other-server should still be present.
	out, err := os.ReadFile(settingsPath) // #nosec G304 -- test temp path
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(out, &settings); err != nil {
		t.Fatal(err)
	}
	mcpServers := settings["mcpServers"].(map[string]interface{})
	if _, exists := mcpServers["other-server"]; !exists {
		t.Error("other-server was removed unexpectedly")
	}
}

func TestMCPRegister_CreatesFileIfMissing(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "subdir", "settings.json")

	err := registerMCPServer(settingsPath, "/usr/local/bin/aimux", "redis://localhost:6379", "agents", "team", 5, 25)
	if err != nil {
		t.Fatalf("registerMCPServer returned error: %v", err)
	}

	out, err := os.ReadFile(settingsPath) // #nosec G304 -- test temp path
	if err != nil {
		t.Fatalf("settings file not created: %v", err)
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(out, &settings); err != nil {
		t.Fatal(err)
	}

	mcpServers, ok := settings["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatal("mcpServers key missing")
	}
	if _, exists := mcpServers["aimux-k8s-agents"]; !exists {
		t.Error("aimux-k8s-agents entry missing")
	}
}

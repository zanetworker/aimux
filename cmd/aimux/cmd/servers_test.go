package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func runServers(t *testing.T, env serversEnv, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	root := &cobra.Command{Use: "aimux", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().BoolVar(&jsonOutput, "json", false, "JSON output")
	defer func() { jsonOutput = false }()
	root.AddCommand(newServersCmdWith(env))
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"servers"}, args...))
	err := root.Execute()
	return out.String(), err
}

func TestServersDiscover_SuggestsFileRefWhenDotenv(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("JIRA_TOKEN=sentinel-value-123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "server.py"), []byte("import os\nu = os.getenv(\"JIRA_SERVER\")\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runServers(t, serversEnv{Home: t.TempDir()}, "discover", "JirAIa", "--cwd", dir)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if strings.Contains(out, "sentinel-value-123") {
		t.Fatalf("a value reached the output:\n%s", out)
	}
	for _, want := range []string{
		"JIRA_TOKEN", "dotenv_key",
		`"JIRA_TOKEN": {file: {path: "` + filepath.Join(dir, ".env") + `", key: "JIRA_TOKEN"}}`,
		"JIRA_SERVER", "source_read",
		`"JIRA_SERVER": {keychain: {item: "aimux/JirAIa", field: "env.JIRA_SERVER"}}`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestServersDiscover_NeedsCwdOrCommand(t *testing.T) {
	_, err := runServers(t, serversEnv{Home: t.TempDir()}, "discover", "nope")
	if err == nil || !strings.Contains(err.Error(), "--cwd") {
		t.Fatalf("want an error naming --cwd, got %v", err)
	}
}

func TestServersDiscover_BadCwdFails(t *testing.T) {
	_, err := runServers(t, serversEnv{Home: t.TempDir()}, "discover", "x", "--cwd", filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatal("want an error for a missing --cwd")
	}
}

func TestServersDiscover_JSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\nimport \"os\"\nvar _ = os.Getenv(\"GO_TOKEN\")\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runServers(t, serversEnv{Home: t.TempDir()}, "discover", "srv", "--cwd", dir, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Server      string `json:"server"`
		Findings    []struct{ Name, Kind string }
		Suggestions map[string]string `json:"suggestions"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.Server != "srv" || len(got.Findings) != 1 || got.Findings[0].Name != "GO_TOKEN" || got.Suggestions["GO_TOKEN"] == "" {
		t.Fatalf("json: %+v", got)
	}
}

func TestServersMigrate_WritesSplitFiles(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "aimux")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := "version: 1\nsources:\n  jira:\n    type: mcp-http\n    url: http://127.0.0.1:8001/mcp\nsets:\n  jira:readonly:\n    source: jira\n    tools: [search]\n"
	if err := os.WriteFile(filepath.Join(dir, "tool-sources.yaml"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runServers(t, serversEnv{Home: home}, "migrate")
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, f := range []string{"servers.yaml", "tool-sets.yaml", "tool-sources.yaml.migrated"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s missing: %v", f, err)
		}
	}
	if !strings.Contains(out, "servers.yaml") {
		t.Errorf("output: %s", out)
	}
	if _, err := runServers(t, serversEnv{Home: home}, "migrate"); err == nil {
		t.Error("a second migrate must refuse")
	}
}

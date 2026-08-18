package cmd

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
)

func TestAgentContextCmd_Output(t *testing.T) {
	var stdout bytes.Buffer

	// Register a few commands so the tree has content
	agentsCmd := newAgentsCmd(func() ([]agent.Agent, error) { return nil, nil })
	spawnCmd := newSpawnCmd([]string{"claude", "codex"}, nil, "")
	versionCmd := newVersionCmd()
	rootCmd.AddCommand(agentsCmd)
	rootCmd.AddCommand(spawnCmd)
	rootCmd.AddCommand(versionCmd)
	defer rootCmd.RemoveCommand(agentsCmd)
	defer rootCmd.RemoveCommand(spawnCmd)
	defer rootCmd.RemoveCommand(versionCmd)

	c := newAgentContextCmd([]string{"claude", "codex"})
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"agent-context"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v\nraw: %s", err, stdout.String())
	}

	if result["schema_version"] != "1" {
		t.Errorf("schema_version=%v, want \"1\"", result["schema_version"])
	}

	commands, ok := result["commands"].(map[string]any)
	if !ok {
		t.Fatal("commands is not a map")
	}

	for _, expected := range []string{"agents", "spawn", "version"} {
		if _, ok := commands[expected]; !ok {
			t.Errorf("missing command %q in agent-context output", expected)
		}
	}

	// Verify spawn has provider values
	spawnInfo, ok := commands["spawn"].(map[string]any)
	if !ok {
		t.Fatal("spawn is not a map")
	}
	args, ok := spawnInfo["args"].([]any)
	if !ok || len(args) == 0 {
		t.Fatal("spawn should have args")
	}
	firstArg := args[0].(map[string]any)
	vals, ok := firstArg["values"].([]any)
	if !ok || len(vals) != 2 {
		t.Errorf("spawn provider arg should have 2 values, got %v", vals)
	} else {
		wantVals := []string{"claude", "codex"}
		for i, want := range wantVals {
			if got, _ := vals[i].(string); got != want {
				t.Errorf("spawn provider values[%d] = %q, want %q", i, got, want)
			}
		}
		for _, v := range vals {
			if s, _ := v.(string); s == "gemini" {
				t.Error("spawn provider values must not contain gemini")
			}
		}
	}

	// Verify providers list
	providers, ok := result["providers"].([]any)
	if !ok || len(providers) != 2 {
		t.Errorf("providers should have 2 entries, got %v", providers)
	} else {
		wantProviders := []string{"claude", "codex"}
		for i, want := range wantProviders {
			if got, _ := providers[i].(string); got != want {
				t.Errorf("providers[%d] = %q, want %q", i, got, want)
			}
		}
		for _, p := range providers {
			if s, _ := p.(string); s == "gemini" {
				t.Error("providers must not contain gemini")
			}
		}
	}
}

func TestAgentContextCmd_ExcludesHelp(t *testing.T) {
	var stdout bytes.Buffer
	c := newAgentContextCmd([]string{"claude"})
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"agent-context"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result struct {
		Commands map[string]any `json:"commands"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}

	if _, ok := result.Commands["help"]; ok {
		t.Error("agent-context should exclude 'help' command")
	}
	if _, ok := result.Commands["completion"]; ok {
		t.Error("agent-context should exclude 'completion' command")
	}
	if _, ok := result.Commands["agent-context"]; ok {
		t.Error("agent-context should exclude itself")
	}
}

func TestAgentContextCmd_FlagTypes(t *testing.T) {
	var stdout bytes.Buffer

	spawnCmd := newSpawnCmd([]string{"claude"}, nil, "")
	rootCmd.AddCommand(spawnCmd)
	defer rootCmd.RemoveCommand(spawnCmd)

	c := newAgentContextCmd([]string{"claude"})
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"agent-context"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result struct {
		Commands map[string]struct {
			Flags map[string]struct {
				Type    string `json:"type"`
				Default string `json:"default"`
			} `json:"flags"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("not valid JSON: %v\nraw: %s", err, stdout.String())
	}

	spawnFlags := result.Commands["spawn"].Flags
	if spawnFlags == nil {
		t.Fatal("spawn should have flags")
	}

	// spawn has --dir (string), --dry-run (bool), --model (string)
	dirFlag, ok := spawnFlags["--dir"]
	if !ok {
		t.Error("spawn missing --dir flag")
	} else if dirFlag.Type != "string" {
		t.Errorf("--dir type=%q, want \"string\"", dirFlag.Type)
	}

	dryRunFlag, ok := spawnFlags["--dry-run"]
	if !ok {
		t.Error("spawn missing --dry-run flag")
	} else if dryRunFlag.Type != "bool" {
		t.Errorf("--dry-run type=%q, want \"bool\"", dryRunFlag.Type)
	}

	// Inherited --json flag should appear
	jsonFlag, ok := spawnFlags["--json"]
	if !ok {
		t.Error("spawn missing inherited --json flag")
	} else if jsonFlag.Type != "bool" {
		t.Errorf("--json type=%q, want \"bool\"", jsonFlag.Type)
	}
}

package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
)

func TestAgentContextCmd_Output(t *testing.T) {
	var stdout bytes.Buffer

	// Register a few commands so the tree has content
	agentsCmd := newAgentsCmd(func() ([]agent.Agent, error) { return nil, nil })
	spawnCmd := newSpawnCmd([]string{"claude", "codex"}, nil, "", nil)
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

	spawnCmd := newSpawnCmd([]string{"claude"}, nil, "", nil)
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

// Subcommands are listed by their full path, so agents see `tools status`,
// `tools diff` and `tools approve` with their flags and args.
func TestAgentContextCmd_ListsToolsSubcommands(t *testing.T) {
	var stdout bytes.Buffer
	tools := newToolsCmdWith(toolsEnv{})
	rootCmd.AddCommand(tools)
	defer rootCmd.RemoveCommand(tools)
	c := newAgentContextCmd([]string{"claude"})
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"agent-context"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)
	if err := rootCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Commands map[string]commandInfo `json:"commands"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tools", "tools status", "tools diff", "tools approve", "tools add", "tools remove"} {
		if _, ok := result.Commands[name]; !ok {
			t.Errorf("agent-context lacks %q", name)
		}
	}
	ap := result.Commands["tools approve"]
	if _, ok := ap.Flags["--all"]; !ok || len(ap.Args) != 1 || ap.Args[0].Name != "set|set/tool" || !ap.Args[0].Required {
		t.Errorf("tools approve = %+v", ap)
	}
	if _, ok := ap.Flags["--expect"]; !ok {
		t.Errorf("tools approve lacks --expect: %+v", ap)
	}
	add := result.Commands["tools add"]
	for _, f := range []string{"--folder", "--name", "--url", "--tools"} {
		if _, ok := add.Flags[f]; !ok {
			t.Errorf("tools add lacks %s: %+v", f, add)
		}
	}
	if len(add.Args) != 1 || !add.Args[0].Required || strings.Join(add.Args[0].Values, ",") != "notes,mcp,cli,http" {
		t.Errorf("tools add args = %+v, want one required arg with values notes, mcp, cli, http", add.Args)
	}
	rm := result.Commands["tools remove"]
	if _, ok := rm.Flags["--yes"]; !ok || len(rm.Args) != 1 || rm.Args[0].Name != "source" || !rm.Args[0].Required {
		t.Errorf("tools remove = %+v", rm)
	}
}

// The sandbox noun and browser status are listed with their full paths.
func TestAgentContextCmd_ListsSandboxSubcommands(t *testing.T) {
	var stdout bytes.Buffer
	sb, br := newSandboxCmdWith(browserEnv{}), newBrowserCmdWith(browserEnv{})
	rootCmd.AddCommand(sb, br)
	defer rootCmd.RemoveCommand(sb, br)
	c := newAgentContextCmd([]string{"claude"})
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"agent-context"})
	rootCmd.AddCommand(c)
	defer rootCmd.RemoveCommand(c)
	if err := rootCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Commands map[string]commandInfo `json:"commands"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sandbox", "sandbox status", "sandbox setup", "sandbox verify", "browser status", "browser verify"} {
		if _, ok := result.Commands[name]; !ok {
			t.Errorf("agent-context lacks %q", name)
		}
	}
	if _, ok := result.Commands["sandbox setup"].Flags["--sandbox-dir"]; !ok {
		t.Errorf("sandbox setup = %+v", result.Commands["sandbox setup"])
	}
}

package cmd

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/spf13/cobra"

	"github.com/zanetworker/aimux/internal/parts"
)

// completions asks cmd's subcommand at path what it offers for the next word after args.
func completions(t *testing.T, root *cobra.Command, path []string, args ...string) []string {
	t.Helper()
	cmd, _, err := root.Find(path)
	if err != nil || cmd.ValidArgsFunction == nil {
		t.Fatalf("%v: no completion (%v)", path, err)
	}
	got, directive := cmd.ValidArgsFunction(cmd, args, "")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("%v: file names would be offered too (directive %d)", path, directive)
	}
	slices.Sort(got)
	return got
}

// Pressing Tab after a command that takes a name offers the names the config file holds: the engines that can
// be switched, the entries of models, the roles. Which names an edit would take is parts.EditCandidates' to say;
// this checks each command asks for the right list at the right word.
func TestCompletion_NamesFromTheConfigFile(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".aimux"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := "parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      anthropic:\n        enabled: true\n      enmaas:\n        enabled: false\n        type: messages\n        url: https://gateway.example\n" +
		"    models:\n      fast: codex:gpt-6.2-sol\n      deep: codex:default\n    roles:\n      subagent: fast\n"
	if err := os.WriteFile(filepath.Join(home, ".aimux", "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	env := frontEnv{}
	env.Home = home
	engine := newEngineCmdWith(env)
	for _, c := range []struct {
		path []string
		args []string
		want []string
	}{
		{[]string{"enable"}, nil, []string{"enmaas"}},                // the one that is off
		{[]string{"disable"}, nil, []string{"anthropic"}},            // the one that is on; codex is not switched here
		{[]string{"remove"}, nil, []string{"enmaas"}},                // only one that was added
		{[]string{"enable"}, []string{"enmaas"}, nil},                // one name, then nothing more
		{[]string{"model", "update"}, nil, []string{"deep", "fast"}}, // an entry to give another model
		{[]string{"model", "update"}, []string{"fast"}, nil},         // the model is typed, never offered
		{[]string{"model", "delete"}, nil, []string{"deep"}},         // fast is used by a role, and would be refused
		{[]string{"role", "update"}, nil, []string{"subagent"}},
		{[]string{"role", "update"}, []string{"subagent"}, []string{"deep", "fast"}},
		{[]string{"role", "delete"}, nil, []string{"subagent"}}, // the role that is set
	} {
		if got := completions(t, engine, c.path, c.args...); !slices.Equal(got, c.want) {
			t.Errorf("engine %v %v: %v, want %v", c.path, c.args, got, c.want)
		}
	}

	// A config file that is not there or cannot be read offers nothing, and never an error on the command line.
	empty := frontEnv{}
	empty.Home = t.TempDir()
	if got := completions(t, newEngineCmdWith(empty), []string{"disable"}); len(got) != 0 {
		t.Errorf("no config file: %v", got)
	}
	if got := completions(t, newEngineCmdWith(empty), []string{"enable"}); !slices.Equal(got, []string{"anthropic"}) {
		t.Errorf("no config file, and Anthropic can be switched on: %v", got)
	}
	if got := completions(t, newEngineCmdWith(empty), []string{"role", "update"}); !slices.Equal(got, []string{"subagent"}) {
		t.Errorf("the role names do not depend on the file: %v", got)
	}
	if err := os.WriteFile(filepath.Join(home, ".aimux", "config.yaml"), []byte("parts: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := completions(t, engine, []string{"model", "delete"}); len(got) != 0 {
		t.Errorf("a broken config file: %v", got)
	}
}

// The names that do not come from a file: the parts, the services an upgrade handles, and how the meter groups.
func TestCompletion_FixedNames(t *testing.T) {
	var switchable []string
	for _, part := range parts.Parts() {
		if !part.Always {
			switchable = append(switchable, part.ID)
		}
	}
	slices.Sort(switchable)
	if got := completions(t, newPartSwitchCmd(frontEnv{}, true), nil); len(got) == 0 || !slices.Equal(got, switchable) {
		t.Errorf("parts: %v, want %v", got, switchable)
	}
	upgrade := newUpgradeCmd()
	if got := completions(t, upgrade, nil); !slices.Equal(got, []string{"collector", "mcp-proxy", "model-proxy"}) {
		t.Errorf("upgrade: %v", got)
	}
	// A service already named is not offered again.
	if got := completions(t, upgrade, nil, "collector"); !slices.Equal(got, []string{"mcp-proxy", "model-proxy"}) {
		t.Errorf("upgrade after collector: %v", got)
	}
	by, found := newCostCmd().GetFlagCompletionFunc("by")
	if !found {
		t.Fatal("cost --by has no completion")
	}
	got, _ := by(nil, nil, "")
	slices.Sort(got)
	if !slices.Equal(got, []string{"day", "model", "role", "session"}) {
		t.Errorf("cost --by: %v", got)
	}
}

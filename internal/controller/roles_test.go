package controller

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zanetworker/aimux/internal/parts"
)

func proxySettings(t *testing.T, yaml string) parts.PartSettings {
	t.Helper()
	s, err := parts.Parse([]byte(yaml))
	if err != nil || len(s.Validate()) != 0 {
		t.Fatalf("%v %v", err, s.Validate())
	}
	return s.Parts["inference-proxy"]
}

const rolesYAML = "parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      anthropic: {}\n    models:\n      implement: codex:gpt-6.2-sol\n      follow: codex:default\n      strong: claude-opus-5-5\n    roles:\n      subagent: implement\n"

func TestRolesView(t *testing.T) {
	rows := RolesView(proxySettings(t, rolesYAML), []string{"codex", "anthropic"})
	if len(rows) != 2 || rows[0].Role != "main" || rows[0].Model != "" || rows[0].Engine != "" {
		t.Fatalf("rows = %+v", rows)
	}
	sub := rows[1]
	if sub.Role != "subagent" || sub.Entry != "implement" || sub.Model != "codex:gpt-6.2-sol" || sub.Engine != "codex" || sub.Note != "" {
		t.Errorf("subagent row = %+v", sub)
	}
	// An engine the running proxy does not answer with is said: every call of that role would be refused.
	rows = RolesView(proxySettings(t, "parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      anthropic: {}\n    models:\n      strong: claude-opus-5-5\n    roles:\n      subagent: strong\n"), []string{"codex"})
	if rows[1].Engine != "anthropic" || rows[1].Note == "" {
		t.Errorf("an engine that is not running: %+v", rows[1])
	}
	// No role set: only the main conversation's line.
	if rows := RolesView(proxySettings(t, "parts:\n  inference-proxy:\n    enabled: true\n"), nil); len(rows) != 1 {
		t.Errorf("nothing set: %+v", rows)
	}
}

func TestSubagentModels(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	write := func(dir, name, text string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(home, "reviewer.md", "---\nname: reviewer\ndescription: Reviews.\nmodel: codex:gpt-6.2-astra\n---\nReview it.\n")
	write(home, "helper.md", "---\nname: helper\ndescription: Helps.\n---\nHelp.\n")
	write(project, "fast.md", "---\nname: fast\nmodel: haiku\n---\nBe quick.\n")
	write(project, "inherits.md", "---\nname: inherits\nmodel: \"inherit\"\n---\nSame as the session.\n")
	write(project, "notes.txt", "not a definition")
	write(project, "broken.md", "no front matter here\n")
	dirs := []SubagentDir{{Path: home, Label: "~/.claude/agents"}, {Path: project, Label: ".claude/agents"}, {Path: filepath.Join(home, "missing"), Label: "nowhere"}}

	rows := SubagentModels(dirs, proxySettings(t, rolesYAML))
	got := map[string]SubagentRow{}
	for _, r := range rows {
		got[r.Name] = r
	}
	if len(rows) != 4 {
		t.Fatalf("rows = %+v", rows)
	}
	if r := got["reviewer"]; r.Model != "codex:gpt-6.2-astra" || r.RunsOn != "codex" || r.DefinedIn != "~/.claude/agents" || r.FromRole {
		t.Errorf("reviewer = %+v", r)
	}
	// No model of its own: the role's, when one is set.
	if r := got["helper"]; r.Model != "" || r.RunsOn != "codex" || !r.FromRole {
		t.Errorf("helper = %+v", r)
	}
	// A Claude alias is Anthropic's; "inherit" is the session's own model, whichever engine that is.
	if r := got["fast"]; r.Model != "haiku" || r.RunsOn != "anthropic" {
		t.Errorf("fast = %+v", r)
	}
	if r := got["inherits"]; r.Model != "inherit" || r.RunsOn != "" || r.FromRole {
		t.Errorf("inherits = %+v", r)
	}
	// With no role set, a definition without a model uses the session's.
	bare := SubagentModels(dirs, proxySettings(t, "parts:\n  inference-proxy:\n    enabled: true\n"))
	for _, r := range bare {
		if r.Name == "helper" && (r.RunsOn != "" || r.FromRole) {
			t.Errorf("helper with no role = %+v", r)
		}
	}
}

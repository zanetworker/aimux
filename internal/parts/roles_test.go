package parts

import (
	"os"
	"slices"
	"strings"
	"testing"
)

const rolesConfig = `parts:
  inference-proxy:
    enabled: true
    engines:
      anthropic: {}
      lab:
        type: messages
        url: http://127.0.0.1:9000
    models:
      implement: codex:gpt-6.2-sol
      second-opinion: codex:gpt-6.2-astra
      follow: codex:default
      strong: claude-opus-5-5
      local: lab:deepseek-v4
    roles:
      subagent: implement
    on_limit: report
`

func TestLoad_ModelsRolesAndOnLimit(t *testing.T) {
	s, err := Parse([]byte(rolesConfig))
	if err != nil || len(s.Validate()) != 0 {
		t.Fatalf("%v %v", err, s.Validate())
	}
	p := s.Parts["inference-proxy"]
	if len(p.Models) != 5 || p.Models["implement"] != "codex:gpt-6.2-sol" || p.Models["follow"] != "codex:default" || p.Models["local"] != "lab:deepseek-v4" {
		t.Errorf("models = %v", p.Models)
	}
	// A provider's own model id may hold a colon after the engine's.
	versioned, _ := Parse([]byte(strings.Replace(rolesConfig, "local: lab:deepseek-v4", "local: lab:deepseek-v4:2026-11", 1)))
	if got := versioned.Parts["inference-proxy"].Models["local"]; got != "lab:deepseek-v4:2026-11" || len(versioned.Validate()) != 0 {
		t.Errorf("a versioned model id: %q %v", got, versioned.Validate())
	}
	if len(p.Roles) != 1 || p.Roles["subagent"] != "implement" || p.OnLimit != "report" {
		t.Errorf("roles = %v, on_limit = %q", p.Roles, p.OnLimit)
	}
	// The model a role stands for, through its entry.
	if model, ok := p.RoleModel("subagent"); !ok || model != "codex:gpt-6.2-sol" {
		t.Errorf("RoleModel(subagent) = %q, %v", model, ok)
	}
	if _, ok := p.RoleModel("main"); ok {
		t.Error("main has a role model")
	}
	// Left out, nothing is stated.
	bare, _ := Parse([]byte("parts:\n  inference-proxy:\n    enabled: true\n"))
	if q := bare.Parts["inference-proxy"]; q.Models != nil || q.Roles != nil || q.OnLimit != "" {
		t.Errorf("left out: %+v", q)
	}
}

func TestValidate_ModelsRolesAndOnLimitProblems(t *testing.T) {
	head := "parts:\n  inference-proxy:\n    enabled: true\n"
	for name, tc := range map[string]struct {
		yaml, path, says string
	}{
		"main is not a role to set":            {head + "    models:\n      a: codex:gpt-6.2-sol\n    roles:\n      main: a\n", "parts.inference-proxy.roles.main", "chosen in the session"},
		"a kind of subagent is not set here":   {head + "    models:\n      a: codex:gpt-6.2-sol\n    roles:\n      subagent.review: a\n", "parts.inference-proxy.roles.subagent.review", "its own definition"},
		"a role nobody knows":                  {head + "    models:\n      a: codex:gpt-6.2-sol\n    roles:\n      planner: a\n", "parts.inference-proxy.roles.planner", "subagent"},
		"a role naming no entry":               {head + "    models:\n      a: codex:gpt-6.2-sol\n    roles:\n      subagent: fast\n", "parts.inference-proxy.roles.subagent", "fast"},
		"a role naming a model directly":       {head + "    roles:\n      subagent: codex:gpt-6.2-sol\n", "parts.inference-proxy.roles.subagent", "under models"},
		"an entry on an engine that is not on": {head + "    models:\n      a: lab:deepseek-v4\n", "parts.inference-proxy.models.a", "lab"},
		"an entry on an engine turned off":     {head + "    engines:\n      lab:\n        type: messages\n        url: http://127.0.0.1:9000\n        enabled: false\n    models:\n      a: lab:deepseek-v4\n", "parts.inference-proxy.models.a", "lab"},
		"a Claude model without anthropic":     {head + "    models:\n      a: claude-opus-5-5\n", "parts.inference-proxy.models.a", "anthropic"},
		"an entry's name":                      {head + "    models:\n      Not_A_Name: codex:gpt-6.2-sol\n", "parts.inference-proxy.models.Not_A_Name", "lower-case"},
		"an entry with no model":               {head + "    models:\n      a: \"\"\n", "parts.inference-proxy.models.a", "model"},
		"a model that is not a name":           {head + "    models:\n      a: \"codex:gpt 6; rm -rf ~\"\n", "parts.inference-proxy.models.a", "model"},
		"anthropic written as a prefix":        {head + "    engines:\n      anthropic: {}\n    models:\n      a: anthropic:claude-opus-5-5\n", "parts.inference-proxy.models.a", "without an engine"},
		"an entry that is a number":            {head + "    models:\n      a: 5\n", "parts.inference-proxy.models.a", "models gives names"},
		"a role that is true":                  {head + "    models:\n      a: codex:gpt-6.2-sol\n    roles:\n      subagent: true\n", "parts.inference-proxy.roles.subagent", "roles says"},
		"on_limit names an engine":             {head + "    on_limit: [codex]\n", "parts.inference-proxy.on_limit", "report"},
		"on_limit another word":                {head + "    on_limit: always\n", "parts.inference-proxy.on_limit", "report"},
		"roles on another part":                {"parts:\n  mcp-proxy:\n    enabled: true\n    roles:\n      subagent: a\n", "parts.mcp-proxy.roles", "not a setting of this part"},
	} {
		s, err := Parse([]byte(tc.yaml))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		found := false
		for _, p := range s.Validate() {
			if p.Path == tc.path && strings.Contains(p.Message, tc.says) && p.Line > 0 {
				found = true
			}
			// A problem never repeats what was written where a value belongs.
			if strings.Contains(p.Message, "rm -rf") {
				t.Errorf("%s: the message repeats the value: %s", name, p.Message)
			}
		}
		if !found {
			t.Errorf("%s: no problem at %s saying %q in %v", name, tc.path, tc.says, s.Validate())
		}
	}
}

// Naming a model and giving it a role are two small edits; the rest of the file stays as it was written.
func TestSetModelAndRole(t *testing.T) {
	const before = "shell: /bin/zsh   # mine\nparts:\n  inference-proxy:\n    enabled: true   # keep\n"
	file := writeConfig(t, before)
	if err := SetModel(file, "implement", "codex:gpt-6.2-sol"); err != nil {
		t.Fatal(err)
	}
	if err := SetRole(file, RoleSubagent, "implement"); err != nil {
		t.Fatal(err)
	}
	s, err := Load(file)
	if err != nil || len(s.Validate()) != 0 {
		t.Fatalf("%v %v", err, s.Validate())
	}
	if model, ok := s.Parts["inference-proxy"].RoleModel(RoleSubagent); !ok || model != "codex:gpt-6.2-sol" {
		t.Fatalf("role model = %q, %v", model, ok)
	}
	text := readFile(t, file)
	if !strings.HasPrefix(text, before) {
		t.Errorf("what was there changed:\n%s", text)
	}
	// A new model is one changed line: the role follows it.
	if err := SetModel(file, "implement", "codex:gpt-6.2-astra"); err != nil {
		t.Fatal(err)
	}
	s, _ = Load(file)
	if model, _ := s.Parts["inference-proxy"].RoleModel(RoleSubagent); model != "codex:gpt-6.2-astra" {
		t.Errorf("after the model changed, the role stands for %q", model)
	}
	// Clearing puts the file back: the role first, then the entry nothing uses any more.
	if err := ClearRole(file, RoleSubagent); err != nil {
		t.Fatal(err)
	}
	if err := ClearModel(file, "implement"); err != nil {
		t.Fatal(err)
	}
	s, _ = Load(file)
	if p := s.Parts["inference-proxy"]; len(p.Roles) != 0 || len(p.Models) != 0 || len(s.Validate()) != 0 {
		t.Errorf("after clearing: %+v %v", p, s.Validate())
	}
}

func TestSetModelAndRole_Refusals(t *testing.T) {
	base := "parts:\n  inference-proxy:\n    enabled: true\n    models:\n      implement: codex:gpt-6.2-sol\n    roles:\n      subagent: implement\n"
	for name, tc := range map[string]struct {
		do   func(file string) error
		says string
	}{
		"a role that cannot be set":             {func(f string) error { return SetRole(f, "main", "implement") }, "chosen in the session"},
		"a role for an entry that is not there": {func(f string) error { return SetRole(f, RoleSubagent, "fast") }, "fast"},
		"a model on an engine that is off":      {func(f string) error { return SetModel(f, "strong", "claude-opus-5-5") }, "anthropic"},
		"an entry's name":                       {func(f string) error { return SetModel(f, "Bad Name", "codex:gpt-6.2-sol") }, "lower-case"},
		"clearing an entry a role still uses":   {func(f string) error { return ClearModel(f, "implement") }, "subagent"},
		"clearing a role that is not set":       {func(f string) error { return ClearRole(f, "background") }, "not set"},
		"clearing an entry that is not there":   {func(f string) error { return ClearModel(f, "fast") }, "fast"},
	} {
		file := writeConfig(t, base)
		err := tc.do(file)
		if err == nil || !strings.Contains(err.Error(), tc.says) {
			t.Errorf("%s: err = %v", name, err)
		}
		if readFile(t, file) != base {
			t.Errorf("%s: a refused edit changed the file", name)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- a file this test wrote
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// on_limit in the file becomes an argument of the proxy's start command, and is taken out when the file drops it
// only by a file that states it otherwise: a setting left out keeps what is installed.
func TestOnLimit_ReachesTheJob(t *testing.T) {
	service := MustService("model-proxy")
	stated, _ := Parse([]byte("parts:\n  inference-proxy:\n    enabled: true\n    on_limit: report\n"))
	p := stated.Parts["inference-proxy"]
	if args := p.ProxyArgs(); !slices.Equal(args, []string{"--on-limit", "report"}) {
		t.Fatalf("ProxyArgs = %q", args)
	}
	current := []string{"/x/aimux", "model", "proxy", "serve", "--anthropic", "--verbose"}
	wanted, err := WantedArgs(service, current, p)
	if err != nil || !slices.Equal(wanted, []string{"/x/aimux", "model", "proxy", "serve", "--verbose", "--anthropic", "--on-limit", "report"}) {
		t.Fatalf("wanted = %q, %v", wanted, err)
	}
	// A job that has it already is not rewritten.
	if !SameArgs(service, wanted, append(slices.Clone(current[:4]), "--on-limit=report", "--anthropic", "--verbose")) {
		t.Error("the same arguments in another order and spelling are not the same")
	}
	// Left out of the file, what is installed stays.
	bare, _ := Parse([]byte("parts:\n  inference-proxy:\n    enabled: true\n"))
	kept, _ := WantedArgs(service, wanted, bare.Parts["inference-proxy"])
	if !slices.Equal(kept, wanted) {
		t.Errorf("left out, the job changed: %q", kept)
	}
}

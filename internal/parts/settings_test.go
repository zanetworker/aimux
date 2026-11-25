package parts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const fullConfig = `shell: /bin/zsh   # an older key, left alone
parts:
  inference-proxy:
    enabled: true
    port: 7400
    serves: [messages]
    engines:
      codex:
        service_tier: standard
        binary: codex-lean
      anthropic:
        enabled: true
      enmaas:
        type: messages
        url: https://api.enmaas.devshift.net
        key: {keychain: {item: enmaas-key}}
      lab:
        type: messages
        url: http://127.0.0.1:9000
        enabled: false
  mcp-proxy:
    enabled: false
`

func TestLoad_NothingStated(t *testing.T) {
	for name, text := range map[string]string{
		"an old file with none of it": "shell: /bin/zsh\notel:\n  enabled: true\n",
		"an empty file":               "",
		"parts with nothing under it": "parts:\n",
		"parts as an empty map":       "parts: {}\n",
	} {
		s, err := Load(writeConfig(t, text))
		if err != nil || s.Stated || len(s.Parts) != 0 || len(s.Validate()) != 0 {
			t.Errorf("%s: Load = %+v, %v; problems %v", name, s, err, s.Validate())
		}
	}
	if s, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err != nil || s.Stated {
		t.Errorf("a missing file: %+v, %v", s, err)
	}
}

func TestLoad_Full(t *testing.T) {
	s, err := Load(writeConfig(t, fullConfig))
	if err != nil {
		t.Fatal(err)
	}
	if problems := s.Validate(); len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	proxy := s.Parts["inference-proxy"]
	if !s.Stated || proxy.Enabled == nil || !*proxy.Enabled || proxy.Port != 7400 || len(proxy.Engines) != 4 {
		t.Fatalf("inference proxy = %+v", proxy)
	}
	if mcp := s.Parts["mcp-proxy"]; mcp.Enabled == nil || *mcp.Enabled {
		t.Errorf("mcp proxy = %+v; want listed and off", mcp)
	}
	if _, listed := s.Parts["collector"]; listed {
		t.Error("a part the file does not name is not in Parts")
	}
	for name, want := range map[string]EngineView{
		"codex":     {Name: "codex", Type: TypeCodexAppServer, On: true, ServiceTier: "standard", Binary: "codex-lean"},
		"anthropic": {Name: "anthropic", Type: TypeMessages, Auth: AuthCaller, URL: AnthropicURL, On: true},
		"enmaas":    {Name: "enmaas", Type: TypeMessages, Auth: AuthKey, URL: "https://api.enmaas.devshift.net", On: true, KeyItem: "enmaas-key"},
		"lab":       {Name: "lab", Type: TypeMessages, Auth: AuthKey, URL: "http://127.0.0.1:9000", On: false, KeyItem: "lab-key"},
	} {
		got := proxy.Engine(name)
		got.Key = nil
		if got != want {
			t.Errorf("engine %s = %+v, want %+v", name, got, want)
		}
	}
}

// What is left out is not a setting: the proxy part alone says nothing about its port or its engines.
func TestLoad_LeftOutIsNotStated(t *testing.T) {
	s, err := Load(writeConfig(t, "parts:\n  inference-proxy:\n    enabled: true\n"))
	if err != nil || len(s.Validate()) != 0 {
		t.Fatalf("%v, %v", err, s.Validate())
	}
	proxy := s.Parts["inference-proxy"]
	if proxy.Port != 0 || proxy.Engines != nil || proxy.Serves != nil {
		t.Errorf("proxy = %+v; a setting left out stays empty, so an installed job keeps what it has", proxy)
	}
}

func TestValidate_Problems(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"an unknown part":                    {"parts:\n  inferense-proxy:\n    enabled: true\n", "inferense-proxy"},
		"a part listed without enabled":      {"parts:\n  inference-proxy:\n    port: 9000\n", "enabled"},
		"a part that is not built yet":       {"parts:\n  evals:\n    enabled: true\n", "not available"},
		"a key that does not exist":          {"parts:\n  inference-proxy:\n    enabled: true\n    prot: 9000\n", "prot"},
		"a port out of range":                {"parts:\n  inference-proxy:\n    enabled: true\n    port: 70000\n", "port"},
		"a port on a part without one":       {"parts:\n  mcp-proxy:\n    enabled: true\n    port: 7381\n", "port"},
		"an API the proxy cannot serve":      {"parts:\n  inference-proxy:\n    enabled: true\n    serves: [responses]\n", "responses"},
		"codex turned off":                   {"parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      codex:\n        enabled: false\n", "codex"},
		"codex given an address":             {"parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      codex:\n        url: https://x.example\n", "url"},
		"anthropic sent somewhere else":      {"parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      anthropic:\n        url: https://x.example\n", "anthropic"},
		"anthropic with the owner's key":     {"parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      anthropic:\n        auth: key\n", "auth"},
		"a named engine with no type":        {"parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      lab:\n        url: https://x.example\n", "type"},
		"a type this build does not know":    {"parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      lab:\n        type: responses\n        url: https://x.example\n", "responses"},
		"a named engine with no address":     {"parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      lab:\n        type: messages\n", "url"},
		"plain http to another machine":      {"parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      lab:\n        type: messages\n        url: http://gw.example\n", "https"},
		"an address with a path":             {"parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      lab:\n        type: messages\n        url: https://gw.example/v1\n", "url"},
		"a login sent to a gateway":          {"parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      lab:\n        type: messages\n        url: https://gw.example\n        auth: caller\n", "auth"},
		"a name that cannot be typed":        {"parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      My Lab:\n        type: messages\n        url: https://gw.example\n", "My Lab"},
		"a tier on an engine that has none":  {"parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      lab:\n        type: messages\n        url: https://gw.example\n        service_tier: priority\n", "service_tier"},
		"a tier that does not exist":         {"parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      codex:\n        service_tier: fastest\n", "fastest"},
		"a key kept in two places at once":   {"parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      lab:\n        type: messages\n        url: https://gw.example\n        key: {keychain: {item: a}, env: {name: B}}\n", "key"},
		"a part named twice":                 {"parts:\n  mcp-proxy:\n    enabled: true\n  mcp-proxy:\n    enabled: false\n", "twice"},
		"a setting written twice":            {"parts:\n  mcp-proxy:\n    enabled: true\n    enabled: false\n", "twice"},
		"an engine named twice":              {"parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      anthropic: {}\n      anthropic: {enabled: false}\n", "twice"},
		"a port with a fraction":             {"parts:\n  inference-proxy:\n    enabled: true\n    port: 7385.9\n", "port"},
		"an address with no host":            {"parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      lab:\n        type: messages\n        url: https://:443\n", "url"},
		"an address with an impossible port": {"parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      lab:\n        type: messages\n        url: https://gw.example:99999\n", "url"},
		"a key reference with a misspelling": {"parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      lab:\n        type: messages\n        url: https://gw.example\n        key: {keychain: {item: a, feild: b}}\n", "key"},
		"a secret written into the file":     {"parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      lab:\n        type: messages\n        url: https://gw.example\n        key: sk-live-not-a-reference\n", "key"},
	} {
		t.Run(name, func(t *testing.T) {
			s, err := Load(writeConfig(t, tc.yaml))
			problems := s.Validate()
			if err != nil {
				problems = append(problems, Problem{Message: err.Error()})
			}
			if len(problems) == 0 {
				t.Fatalf("no problem reported for:\n%s", tc.yaml)
			}
			var all []string
			for _, p := range problems {
				all = append(all, p.String())
			}
			if joined := strings.Join(all, "\n"); !strings.Contains(joined, tc.want) {
				t.Errorf("problems do not name %q:\n%s", tc.want, joined)
			}
			if strings.Contains(strings.Join(all, "\n"), "sk-live-not-a-reference") {
				t.Error("a problem repeats what was written where a key reference belongs")
			}
		})
	}
}

func TestValidate_SaysWhere(t *testing.T) {
	s, err := Load(writeConfig(t, "shell: /bin/zsh\nparts:\n  inference-proxy:\n    enabled: true\n    port: 70000\n"))
	if err != nil {
		t.Fatal(err)
	}
	problems := s.Validate()
	if len(problems) != 1 || problems[0].Line != 5 || problems[0].Path != "parts.inference-proxy.port" {
		t.Fatalf("problems = %+v; want one, at line 5, for parts.inference-proxy.port", problems)
	}
}

func TestLoad_NotYAML(t *testing.T) {
	if _, err := Load(writeConfig(t, "parts: [\n")); err == nil {
		t.Error("a file that is not YAML loaded")
	}
	if _, err := Load(writeConfig(t, "parts: yes please\n")); err == nil {
		t.Error("parts as a sentence loaded")
	}
	if _, err := Load(writeConfig(t, "parts: {}\n---\nother: 1\n")); err == nil {
		t.Error("a file of two documents loaded")
	}
	if _, err := Load(writeConfig(t, "parts: {}\nparts: {}\n")); err == nil {
		t.Error("a file with two parts sections loaded")
	}
	if _, err := Load(writeConfig(t, "a: 1\na: 2\nparts: {}\nparts:\n  mcp-proxy:\n    enabled: false\n")); err == nil {
		t.Error("two parts sections after another repeated key loaded")
	}
	if _, err := Load(writeConfig(t, "base: &b\n  parts:\n    mcp-proxy:\n      enabled: false\n<<: *b\n")); err == nil {
		t.Error("a parts section that comes from a merge loaded as nothing stated")
	}
	// engines with nothing under it says "none"; engines left out says nothing.
	s, err := Load(writeConfig(t, "parts:\n  inference-proxy:\n    enabled: true\n    engines:\n"))
	if err != nil || s.Parts["inference-proxy"].Engines == nil || len(s.Validate()) != 0 {
		t.Errorf("engines with nothing under it: %+v, %v", s.Parts["inference-proxy"], err)
	}
}

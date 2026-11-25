package parts

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func configWith(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if text != "" {
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func fileText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- the test's own file
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func proxyOf(t *testing.T, path string) PartSettings {
	t.Helper()
	s, err := Load(path)
	if err != nil || len(s.Validate()) != 0 {
		t.Fatalf("%v %v\n%s", err, s.Validate(), fileText(t, path))
	}
	return s.Parts["inference-proxy"]
}

func TestEnableEngine(t *testing.T) {
	// From nothing: the part is listed as on (it runs already; this takes it under management) and the engine added.
	path := configWith(t, "shell: /bin/zsh\n")
	if err := EnableEngine(path, "anthropic"); err != nil {
		t.Fatal(err)
	}
	if want := "shell: /bin/zsh\nparts:\n  inference-proxy:\n    enabled: true\n    engines:\n      anthropic:\n        enabled: true\n"; fileText(t, path) != want {
		t.Errorf("file:\n%s", fileText(t, path))
	}
	// Again: nothing changes.
	before := fileText(t, path)
	if err := EnableEngine(path, "anthropic"); err != nil || fileText(t, path) != before {
		t.Errorf("enabling what is on changed the file: %v", err)
	}
	// One that was turned off keeps everything else it had.
	path = configWith(t, "parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      enmaas:\n        type: messages\n        url: https://gw.example   # the gateway\n        enabled: false\n")
	if err := EnableEngine(path, "enmaas"); err != nil {
		t.Fatal(err)
	}
	if got := fileText(t, path); !strings.Contains(got, "url: https://gw.example   # the gateway\n        enabled: true\n") {
		t.Errorf("file:\n%s", got)
	}
	// Listed with no switch: already on.
	path = configWith(t, "parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      anthropic: {}\n")
	before = fileText(t, path)
	if err := EnableEngine(path, "anthropic"); err != nil || fileText(t, path) != before {
		t.Errorf("an engine listed with no switch is on already: %v\n%s", err, fileText(t, path))
	}
}

func TestEnableEngine_Refusals(t *testing.T) {
	for name, tc := range map[string]struct{ config, engine, want string }{
		"an upstream that was never added": {"", "lab", "aimux engine add lab="},
		"codex, which is always on":        {"", "codex", "always on"},
		"a name that cannot be one":        {"", "My Lab", "name"},

		"the proxy turned off":  {"parts:\n  inference-proxy:\n    enabled: false\n", "anthropic", "aimux enable inference-proxy"},
		"a file with a problem": {"parts:\n  inference-proxy:\n    port: 9000\n", "anthropic", "line"},
	} {
		path := configWith(t, tc.config)
		err := EnableEngine(path, tc.engine)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v; want one naming %q", name, err, tc.want)
		}
		if tc.config == "" {
			if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("%s: a refused change made the file", name)
			}
		} else if fileText(t, path) != tc.config {
			t.Errorf("%s: a refused change edited the file", name)
		}
	}
}

func TestDisableEngine(t *testing.T) {
	path := configWith(t, "parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      anthropic: {}\n      enmaas:\n        type: messages\n        url: https://gw.example\n")
	if err := DisableEngine(path, "enmaas"); err != nil {
		t.Fatal(err)
	}
	proxy := proxyOf(t, path)
	if proxy.Engine("enmaas").On || proxy.Engine("enmaas").URL != "https://gw.example" || !proxy.Engine("anthropic").On {
		t.Errorf("engines = %+v\n%s", proxy.Engines, fileText(t, path))
	}
	// An entry written as {} is opened up to take the switch.
	if err := DisableEngine(path, "anthropic"); err != nil || proxyOf(t, path).Engine("anthropic").On {
		t.Errorf("anthropic written as {}: %v\n%s", err, fileText(t, path))
	}
	// A one-line entry with settings in it cannot take a new key as text; the owner is told, and the file is as it was.
	oneLine := configWith(t, "parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      lab: {type: messages, url: https://gw.example}\n")
	before := fileText(t, oneLine)
	if err := DisableEngine(oneLine, "lab"); err == nil || !strings.Contains(err.Error(), "line 5") || fileText(t, oneLine) != before {
		t.Errorf("a one-line engine: err = %v", err)
	}
	// Anthropic not listed and turned off: said outright, so it stays off whatever the job had.
	path = configWith(t, "parts:\n  inference-proxy:\n    enabled: true\n")
	if err := DisableEngine(path, "anthropic"); err != nil {
		t.Fatal(err)
	}
	if proxy := proxyOf(t, path); proxy.Engine("anthropic").On || proxy.Engines == nil {
		t.Errorf("after disabling anthropic: %+v", proxy.Engines)
	}
	for name, engine := range map[string]string{"codex": "codex", "an upstream that is not there": "lab"} {
		if err := DisableEngine(path, engine); err == nil {
			t.Errorf("%s: disabled without complaint", name)
		}
	}
}

func TestAddAndRemoveEngine(t *testing.T) {
	path := configWith(t, "# mine\nshell: /bin/zsh\n")
	if err := AddEngine(path, "enmaas", "https://api.enmaas.devshift.net"); err != nil {
		t.Fatal(err)
	}
	want := "# mine\nshell: /bin/zsh\nparts:\n  inference-proxy:\n    enabled: true\n    engines:\n      enmaas:\n        type: messages\n        url: https://api.enmaas.devshift.net\n        key: {keychain: {item: enmaas-key}}\n"
	if fileText(t, path) != want {
		t.Errorf("file:\n%s", fileText(t, path))
	}
	if view := proxyOf(t, path).Engine("enmaas"); !view.On || view.Auth != AuthKey || view.KeyItem != "enmaas-key" {
		t.Errorf("view = %+v", view)
	}
	for name, tc := range map[string]struct{ engine, url, want string }{
		"the same name again":                    {"enmaas", "https://other.example", "already"},
		"codex":                                  {"codex", "https://x.example", "built in"},
		"anthropic":                              {"anthropic", "https://x.example", "aimux engine enable anthropic"},
		"plain http to another host":             {"lab", "http://gw.example", "https"},
		"an address with a path":                 {"lab", "https://gw.example/v1", "address"},
		"a name that cannot be one":              {"Lab One", "https://gw.example", "name"},
		"a name a config file reads as a switch": {"true", "https://gw.example", "something other than a name"},
	} {
		before := fileText(t, path)
		if err := AddEngine(path, tc.engine, tc.url); err == nil || !strings.Contains(err.Error(), tc.want) || fileText(t, path) != before {
			t.Errorf("%s: err = %v; want one naming %q and the file untouched", name, err, tc.want)
		}
	}
	if err := AddEngine(path, "lab", "http://127.0.0.1:9000"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveEngine(path, "enmaas"); err != nil {
		t.Fatal(err)
	}
	proxy := proxyOf(t, path)
	if _, still := proxy.Engines["enmaas"]; still || !proxy.Engine("lab").On {
		t.Errorf("after removing enmaas: %+v", proxy.Engines)
	}
	for name, engine := range map[string]string{"codex": "codex", "one that is not there": "enmaas"} {
		if err := RemoveEngine(path, engine); err == nil {
			t.Errorf("%s: removed without complaint", name)
		}
	}
	// Removing the last one leaves "no engines beyond Codex", said outright.
	if err := RemoveEngine(path, "lab"); err != nil {
		t.Fatal(err)
	}
	if proxy := proxyOf(t, path); proxy.Engines == nil || len(proxy.Engines) != 0 {
		t.Errorf("after removing the last engine: %+v", proxy.Engines)
	}
}

func TestEnableAndDisablePart(t *testing.T) {
	path := configWith(t, "")
	if err := EnablePart(path, "mcp-proxy"); err != nil {
		t.Fatal(err)
	}
	if fileText(t, path) != "parts:\n  mcp-proxy:\n    enabled: true\n" {
		t.Errorf("file:\n%s", fileText(t, path))
	}
	if err := DisablePart(path, "mcp-proxy"); err != nil || !strings.Contains(fileText(t, path), "enabled: false") {
		t.Errorf("disable: %v\n%s", err, fileText(t, path))
	}
	for name, id := range map[string]string{"a part that does not exist": "nope", "the dashboard, which is always on": "dashboard", "a part not built yet": "evals"} {
		before := fileText(t, path)
		if err := EnablePart(path, id); err == nil || fileText(t, path) != before {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// A part that is not built can be turned off, which changes nothing about what runs.
	if err := DisablePart(path, "nope"); err == nil {
		t.Error("a part that does not exist was turned off")
	}
}

func TestListEngines(t *testing.T) {
	path := configWith(t, "parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      zeta: {type: messages, url: https://z.example, enabled: false}\n      anthropic: {}\n      alpha: {type: messages, url: https://a.example}\n")
	views, stated, err := ListEngines(path)
	if err != nil || !stated {
		t.Fatal(err, stated)
	}
	var got []string
	for _, v := range views {
		state := "off"
		if v.On {
			state = "on"
		}
		got = append(got, v.Name+" "+state)
	}
	// The two that are built in first, then the owner's by name.
	if want := "codex on, anthropic on, alpha on, zeta off"; strings.Join(got, ", ") != want {
		t.Errorf("engines = %q, want %q", strings.Join(got, ", "), want)
	}
	// With nothing in the file: Codex on, Anthropic off, as the proxy runs without settings.
	views, stated, err = ListEngines(configWith(t, ""))
	if err != nil || stated || len(views) != 2 || !views[0].On || views[1].On {
		t.Errorf("with no file: %+v, stated %v, %v", views, stated, err)
	}
	// The proxy listed and its engines left out: still nothing stated about engines.
	if _, stated, _ = ListEngines(configWith(t, "parts:\n  inference-proxy:\n    enabled: true\n")); stated {
		t.Error("engines left out count as stated")
	}
}

package parts

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func jobFixture(t *testing.T, label string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", label+".plist")) // #nosec G304 -- a fixture named by the test
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func proxySettings(t *testing.T, yaml string) PartSettings {
	t.Helper()
	s, err := Parse([]byte(yaml))
	if err != nil || len(s.Validate()) != 0 {
		t.Fatalf("%v %v", err, s.Validate())
	}
	return s.Parts["inference-proxy"]
}

func TestJobArgs_ReadsTheJobFilesAsTheyAre(t *testing.T) {
	for label, want := range map[string][]string{
		"com.aimux.model-proxy":      {"/Users/someone/.aimux/services/model-proxy/aimux", "model", "proxy", "serve"},
		"com.aimux.mcp-proxy":        {"/Users/someone/.aimux/services/mcp-proxy/aimux", "mcp", "proxy", "serve", "--port", "7381"},
		"com.azaalouk.aimux-collect": {"/Users/someone/.aimux/services/collector/aimux", "collect"},
	} {
		got, err := JobArgs(jobFixture(t, label))
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("%s: %q, %v", label, got, err)
		}
	}
}

func TestSetJobArgs_ChangesTheArgumentsAndNothingElse(t *testing.T) {
	for _, label := range []string{"com.aimux.model-proxy", "com.azaalouk.aimux-collect"} {
		original := string(jobFixture(t, label))
		args := []string{"/a path/aimux", "model", "proxy", "serve", "--anthropic", "--messages-upstream-key", `enmaas={"env":{"name":"A&B<C>"}}`}
		out, err := SetJobArgs([]byte(original), args)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := JobArgs(out); err != nil || !slices.Equal(got, args) {
			t.Fatalf("%s: read back %q, %v", label, got, err)
		}
		// Everything before the list of arguments and everything after it is the same bytes.
		start, end := strings.Index(original, "<array>"), strings.Index(original, "</array>")
		newStart, newEnd := strings.Index(string(out), "<array>"), strings.Index(string(out), "</array>")
		if original[:start] != string(out)[:newStart] || original[end:] != string(out)[newEnd:] {
			t.Errorf("%s: bytes outside the arguments changed", label)
		}
		// The arguments are indented as the file indents them.
		indent := original[strings.LastIndex(original[:strings.Index(original, "<string>/Users")], "\n")+1 : strings.Index(original, "<string>/Users")]
		if !strings.Contains(string(out), "\n"+indent+"<string>--anthropic</string>\n") {
			t.Errorf("%s: a new argument is not indented like the others (%q)", label, indent)
		}
		// The same arguments leave the file exactly as it was.
		same, _ := JobArgs([]byte(original))
		if unchanged, err := SetJobArgs([]byte(original), same); err != nil || string(unchanged) != original {
			t.Errorf("%s: writing the same arguments changed the file", label)
		}
	}
}

func TestJobArgs_RefusesWhatItCannotSplit(t *testing.T) {
	model := string(jobFixture(t, "com.aimux.model-proxy"))
	for name, text := range map[string]string{
		"a separate Program key":                     strings.Replace(model, "<key>ProgramArguments</key>", "<key>Program</key>\n\t<string>/bin/x</string>\n\t<key>ProgramArguments</key>", 1),
		"no arguments at all":                        strings.Replace(model, "ProgramArguments", "SomethingElse", 1),
		"an argument not a string":                   strings.Replace(model, "<string>serve</string>", "<integer>5</integer>", 1),
		"a comment in the list":                      strings.Replace(model, "<string>serve</string>", "<!-- note --><string>serve</string>", 1),
		"not a job file":                             "hello",
		"a CDATA section":          strings.Replace(model, "<string>/Users/someone</string>", "<string><![CDATA[<key>ProgramArguments</key>]]></string>", 1),
		"a carriage return inside an argument": strings.Replace(model, "<string>serve</string>", "<string>ser\rve</string>", 1),
		"a comment anywhere":                         strings.Replace(model, "<dict>", "<dict>\n\t<!-- <key>ProgramArguments</key><array><string>/old</string></array> -->", 1),
		"the key a second time, in the environment":  strings.Replace(model, "<key>HOME</key>", "<key>ProgramArguments</key>\n\t\t<string>x</string>\n\t\t<key>HOME</key>", 1),
		"Program spelled with a character reference": strings.Replace(model, "<key>ProgramArguments</key>", "<key>Pro&#103;ram</key>\n\t<string>/bin/x</string>\n\t<key>ProgramArguments</key>", 1),
	} {
		if _, err := JobArgs([]byte(text)); err == nil {
			t.Errorf("%s: read without complaint", name)
		}
		if _, err := SetJobArgs([]byte(text), []string{"/x", "model", "proxy", "serve"}); err == nil {
			t.Errorf("%s: written without complaint", name)
		}
	}
}

func TestSetJobArgs_OddButValidFiles(t *testing.T) {
	// The key and the list on one line: no markup is taken for indentation.
	oneLine := strings.Replace(string(jobFixture(t, "com.aimux.model-proxy")), "<key>ProgramArguments</key>\n\t<array>", "<key>ProgramArguments</key><array>", 1)
	args := []string{"/p/aimux", "model", "proxy", "serve", "--anthropic"}
	out, err := SetJobArgs([]byte(oneLine), args)
	if err != nil || strings.Count(string(out), "<key>ProgramArguments</key>") != 1 {
		t.Fatalf("%v\n%s", err, out)
	}
	if got, err := JobArgs(out); err != nil || !slices.Equal(got, args) {
		t.Errorf("read back %q, %v", got, err)
	}
	// An argument XML cannot hold is refused, not written as something else.
	if _, err := SetJobArgs(jobFixture(t, "com.aimux.model-proxy"), []string{"/p/aimux", "model", "proxy", "serve", "--codex-command", "bad\x01name"}); err == nil {
		t.Error("an argument with a control character was written")
	}
}

func TestWantedArgs_JobsItCannotFollow(t *testing.T) {
	proxy := MustService("model-proxy")
	on := true
	base := []string{"/p/aimux", "model", "proxy", "serve"}
	for name, extra := range map[string][]string{
		"an upstream named twice":         {"--messages-upstream", "a=https://x.example", "--messages-upstream", "a=https://y.example"},
		"a key for no upstream":           {"--messages-upstream-key", `a={"env":{"name":"K"}}`},
		"a key given twice":               {"--messages-upstream", "a=https://x.example", "--messages-upstream-key", `a={"env":{"name":"K"}}`, "--messages-upstream-key", `a={"env":{"name":"L"}}`},
		"arguments after a bare --":       {"--", "something"},
		"a switch with a value it is not": {"--anthropic=maybe"},
	} {
		if _, err := WantedArgs(proxy, append(slices.Clone(base), extra...), PartSettings{Enabled: &on}); err == nil {
			t.Errorf("%s: followed without complaint", name)
		}
	}
	// The switch written with a value is the same switch.
	off := proxySettings(t, "parts:\n  inference-proxy:\n    enabled: true\n    engines:\n")
	if got, err := WantedArgs(proxy, append(slices.Clone(base), "--anthropic=true"), off); err != nil || !slices.Equal(got, base) {
		t.Errorf("--anthropic=true with the engine turned off: %q, %v", got, err)
	}
	if got, err := WantedArgs(proxy, append(slices.Clone(base), "--anthropic=false"), PartSettings{Enabled: &on}); err != nil || !slices.Equal(got, base) {
		t.Errorf("--anthropic=false: %q, %v", got, err)
	}
	if !SameArgs(proxy, append(slices.Clone(base), "--anthropic=true"), append(slices.Clone(base), "--anthropic")) {
		t.Error("--anthropic=true and --anthropic are a change")
	}
}

func TestWantedArgs(t *testing.T) {
	proxy := MustService("model-proxy")
	installed := []string{"/p/aimux", "model", "proxy", "serve"}
	custom := []string{"/p/aimux", "model", "proxy", "serve", "--port", "7400", "--codex-command=codex-lean", "--verbose", "--messages-upstream", "old=https://old.example"}
	on := "parts:\n  inference-proxy:\n    enabled: true\n"
	for name, tc := range map[string]struct {
		current []string
		yaml    string
		want    []string
	}{
		"taken under management, nothing stated: as installed": {installed, on, installed},
		"a custom job, nothing stated: every argument kept":    {custom, on, []string{"/p/aimux", "model", "proxy", "serve", "--verbose", "--port", "7400", "--codex-command", "codex-lean", "--messages-upstream", "old=https://old.example"}},
		"anthropic turned on":                                  {installed, on + "    engines:\n      anthropic: {}\n", append(slices.Clone(installed), "--anthropic")},
		"engines stated replace the installed ones": {custom, on + "    engines:\n      anthropic: {}\n      enmaas: {type: messages, url: https://gw.example}\n",
			[]string{"/p/aimux", "model", "proxy", "serve", "--verbose", "--port", "7400", "--codex-command", "codex-lean", "--anthropic", "--messages-upstream", "enmaas=https://gw.example"}},
		"engines stated as none take the installed ones away": {append(slices.Clone(installed), "--anthropic", "--messages-upstream", "old=https://old.example"), on + "    engines:\n", installed},
		"a port stated replaces the installed one":            {custom, on + "    port: 7385\n", []string{"/p/aimux", "model", "proxy", "serve", "--verbose", "--port", "7385", "--codex-command", "codex-lean", "--messages-upstream", "old=https://old.example"}},
		"codex settings stated one at a time":                 {custom, on + "    engines:\n      codex: {service_tier: standard}\n", []string{"/p/aimux", "model", "proxy", "serve", "--verbose", "--port", "7400", "--service-tier", "standard", "--codex-command", "codex-lean"}},
	} {
		got, err := WantedArgs(proxy, tc.current, proxySettings(t, tc.yaml))
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("%s:\ngot  %q, %v\nwant %q", name, got, err, tc.want)
		}
	}
	// A job that starts something else is not this service's job.
	if _, err := WantedArgs(proxy, []string{"/p/aimux", "mcp", "proxy", "serve"}, proxySettings(t, on)); err == nil {
		t.Error("a job that starts another command was accepted as the model proxy's")
	}
	if _, err := WantedArgs(proxy, nil, proxySettings(t, on)); err == nil {
		t.Error("a job with no arguments was accepted")
	}
	// A service with no settings of its own keeps every argument, its port among them.
	mcp := []string{"/p/aimux", "mcp", "proxy", "serve", "--port", "7381"}
	if got, err := WantedArgs(MustService("mcp-proxy"), mcp, PartSettings{}); err != nil || !slices.Equal(got, mcp) {
		t.Errorf("mcp proxy: %q, %v", got, err)
	}
}

func TestSameArgs(t *testing.T) {
	proxy := MustService("model-proxy")
	a := []string{"/p/aimux", "model", "proxy", "serve", "--anthropic", "--port=7385"}
	b := []string{"/p/aimux", "model", "proxy", "serve", "--port", "7385", "--anthropic"}
	c := []string{"/p/aimux", "model", "proxy", "serve", "--port", "7386", "--anthropic"}
	if !SameArgs(proxy, a, b) {
		t.Error("the same settings written in another order are a change")
	}
	if SameArgs(proxy, a, c) || SameArgs(proxy, a, append(slices.Clone(a), "--verbose")) || SameArgs(proxy, a, []string{"/other/aimux", "model", "proxy", "serve", "--anthropic", "--port", "7385"}) {
		t.Error("different arguments are the same")
	}
}

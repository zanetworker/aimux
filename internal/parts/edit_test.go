package parts

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const commented = `# my aimux settings
shell: /bin/zsh   # the shell I use

# what is on
parts:
  # the proxy in front of my sessions
  inference-proxy:
    enabled: true     # do not turn off
    port: 7385
    engines:
      # my login
      anthropic:
        enabled: false   # off for now
      enmaas:
        type: messages
        url: https://api.enmaas.devshift.net
  mcp-proxy:
    enabled: true

otel:
  enabled: true
`

func edit(t *testing.T, text string, path []string, value any) string {
	t.Helper()
	out, err := Edit([]byte(text), path, value)
	if err != nil {
		t.Fatalf("Edit(%v, %v): %v", path, value, err)
	}
	return string(out)
}

// diffLines is the lines of b that are not the same line of a, for edits that change lines in place.
func changedLines(a, b string) []string {
	as, bs := strings.Split(a, "\n"), strings.Split(b, "\n")
	var changed []string
	for i := range bs {
		if i >= len(as) || as[i] != bs[i] {
			changed = append(changed, bs[i])
		}
	}
	return changed
}

func TestEdit_ChangesOneValueAndNothingElse(t *testing.T) {
	for name, tc := range map[string]struct {
		path  []string
		value any
		was   string
		now   string
	}{
		"a switch with a comment beside it": {[]string{"parts", "inference-proxy", "engines", "anthropic", "enabled"}, true,
			"        enabled: false   # off for now", "        enabled: true   # off for now"},
		"a number":       {[]string{"parts", "inference-proxy", "port"}, 7400, "    port: 7385", "    port: 7400"},
		"a text value":   {[]string{"parts", "inference-proxy", "engines", "enmaas", "url"}, "https://lab.example", "        url: https://api.enmaas.devshift.net", "        url: https://lab.example"},
		"the first part": {[]string{"parts", "inference-proxy", "enabled"}, false, "    enabled: true     # do not turn off", "    enabled: false     # do not turn off"},
	} {
		t.Run(name, func(t *testing.T) {
			got := edit(t, commented, tc.path, tc.value)
			if want := strings.Replace(commented, tc.was, tc.now, 1); got != want {
				t.Errorf("got:\n%s\nwant only this line changed:\n%s", got, tc.now)
			}
		})
	}
}

func TestEdit_KeepsTheFileAsItWasWritten(t *testing.T) {
	path := []string{"parts", "inference-proxy", "enabled"}
	for name, text := range map[string]string{
		"windows line endings":  strings.ReplaceAll(commented, "\n", "\r\n"),
		"no newline at the end": strings.TrimRight(commented, "\n"),
		"tabs in a comment":     strings.Replace(commented, "# what is on", "#\twhat\tis on", 1),
		"quoted values":         strings.Replace(commented, "enabled: true     # do not turn off", `enabled: "true"     # do not turn off`, 1),
		"a one-line engine":     strings.Replace(commented, "      enmaas:\n        type: messages\n        url: https://api.enmaas.devshift.net\n", "      enmaas: {type: messages, url: \"https://api.enmaas.devshift.net\"}\n", 1),
	} {
		t.Run(name, func(t *testing.T) {
			got := edit(t, text, path, false)
			changed := changedLines(text, got)
			if len(changed) != 1 || !strings.Contains(changed[0], "enabled: false") || len(strings.Split(got, "\n")) != len(strings.Split(text, "\n")) {
				t.Errorf("changed lines: %q; want exactly the one line", changed)
			}
			if strings.HasSuffix(text, "\n") != strings.HasSuffix(got, "\n") || strings.Contains(text, "\r\n") != strings.Contains(got, "\r\n") {
				t.Error("the end of the file or its line endings changed")
			}
		})
	}
	// A value inside a one-line engine changes in place.
	oneLine := strings.Replace(commented, "      enmaas:\n        type: messages\n        url: https://api.enmaas.devshift.net\n", "      enmaas: {type: messages, url: https://a.example, enabled: true}   # one line\n", 1)
	got := edit(t, oneLine, []string{"parts", "inference-proxy", "engines", "enmaas", "enabled"}, false)
	if want := strings.Replace(oneLine, "enabled: true}   # one line", "enabled: false}   # one line", 1); got != want {
		t.Errorf("a value inside a one-line engine:\n%s", got)
	}
}

func TestEdit_AddsWhatIsNotThere(t *testing.T) {
	// A new engine goes at the end of the engines, at their indentation, and nothing above or below moves.
	got := edit(t, commented, []string{"parts", "inference-proxy", "engines", "lab"},
		Block{{"type", "messages"}, {"url", "https://lab.example"}, {"key", Flow{{"keychain", Flow{{"item", "lab-key"}}}}}})
	want := strings.Replace(commented, "        url: https://api.enmaas.devshift.net\n",
		"        url: https://api.enmaas.devshift.net\n      lab:\n        type: messages\n        url: https://lab.example\n        key: {keychain: {item: lab-key}}\n", 1)
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	// A new key in an engine that has settings.
	got = edit(t, commented, []string{"parts", "inference-proxy", "engines", "enmaas", "enabled"}, false)
	if want := strings.Replace(commented, "        url: https://api.enmaas.devshift.net\n", "        url: https://api.enmaas.devshift.net\n        enabled: false\n", 1); got != want {
		t.Errorf("a new key:\n%s", got)
	}
	// Parents that are missing are made: an engine's switch where there are no engines yet.
	got = edit(t, "parts:\n  inference-proxy:\n    enabled: true\n", []string{"parts", "inference-proxy", "engines", "anthropic", "enabled"}, true)
	if want := "parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      anthropic:\n        enabled: true\n"; got != want {
		t.Errorf("missing parents:\n%s", got)
	}
}

func TestEdit_CreatesTheSection(t *testing.T) {
	for name, tc := range map[string]struct{ text, want string }{
		"at the end of a file that has none": {"shell: /bin/zsh\n", "shell: /bin/zsh\nparts:\n  inference-proxy:\n    enabled: true\n"},
		"in an empty file":                   {"", "parts:\n  inference-proxy:\n    enabled: true\n"},
		"after a last line with no newline":  {"shell: /bin/zsh", "shell: /bin/zsh\nparts:\n  inference-proxy:\n    enabled: true\n"},
		"under a parts with nothing in it":   {"parts:\nshell: /bin/zsh\n", "parts:\n  inference-proxy:\n    enabled: true\nshell: /bin/zsh\n"},
		"in a parts written as {}":           {"parts: {}   # none yet\nshell: /bin/zsh\n", "parts:   # none yet\n  inference-proxy:\n    enabled: true\nshell: /bin/zsh\n"},
		"in a file of comments only":         {"# nothing yet\n", "# nothing yet\nparts:\n  inference-proxy:\n    enabled: true\n"},
	} {
		if got := edit(t, tc.text, []string{"parts", "inference-proxy", "enabled"}, true); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", name, got, tc.want)
		}
	}
}

func TestEdit_Removes(t *testing.T) {
	// An engine goes with its settings; its neighbours stay. The comment above the first of several engines
	// stays too, since it may be about all of them.
	got := edit(t, commented, []string{"parts", "inference-proxy", "engines", "anthropic"}, nil)
	if want := strings.Replace(commented, "      anthropic:\n        enabled: false   # off for now\n", "", 1); got != want {
		t.Errorf("got:\n%s", got)
	}
	// The comment above a later engine is about that engine, and goes with it.
	withNote := strings.Replace(commented, "      enmaas:\n", "      # the gateway\n      enmaas:\n", 1)
	if got := edit(t, withNote, []string{"parts", "inference-proxy", "engines", "enmaas"}, nil); strings.Contains(got, "# the gateway") || strings.Contains(got, "enmaas") {
		t.Errorf("a later engine and its comment:\n%s", got)
	}
	got = edit(t, commented, []string{"parts", "inference-proxy", "engines", "enmaas"}, nil)
	if want := strings.Replace(commented, "      enmaas:\n        type: messages\n        url: https://api.enmaas.devshift.net\n", "", 1); got != want {
		t.Errorf("the last engine:\n%s", got)
	}
	// Removing the only engine leaves "engines:" with nothing under it: no engines beyond Codex, said outright,
	// which is not the same as the key left out ("as installed").
	only := "parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      anthropic:\n        enabled: true\n"
	got = edit(t, only, []string{"parts", "inference-proxy", "engines", "anthropic"}, nil)
	if s, err := Parse([]byte(got)); err != nil || len(s.Validate()) != 0 || s.Parts["inference-proxy"].Engines == nil || len(s.Parts["inference-proxy"].Engines) != 0 {
		t.Errorf("after removing the only engine: %v, %v\n%s", err, s.Validate(), got)
	}
	// Removing what is not there changes nothing and is not an error.
	if got := edit(t, commented, []string{"parts", "inference-proxy", "engines", "nope"}, nil); got != commented {
		t.Error("removing an engine that is not there changed the file")
	}
}

func TestEdit_RefusesWhatItCannotDoSafely(t *testing.T) {
	for name, tc := range map[string]struct {
		text  string
		path  []string
		value any
		want  string
	}{
		"a value behind an anchor": {"defaults: &d\n  enabled: true\nparts:\n  inference-proxy: *d\n", []string{"parts", "inference-proxy", "enabled"}, false, "line 4"},
		"a value merged in":        {"defaults: &d\n  enabled: true\nparts:\n  inference-proxy:\n    <<: *d\n", []string{"parts", "inference-proxy", "enabled"}, false, "line"},
		"a new key in a one-line engine": {"parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      lab: {type: messages, url: https://a.example}\n",
			[]string{"parts", "inference-proxy", "engines", "lab", "enabled"}, false, "line 5"},
		"a value that runs over lines": {"parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      lab:\n        url: >\n          https://a.example\n",
			[]string{"parts", "inference-proxy", "engines", "lab", "url"}, "https://b.example", "line"},
		"parts that is not a list of parts": {"parts: yes\n", []string{"parts", "inference-proxy", "enabled"}, true, "line 1"},
		"a file that is not YAML":           {"parts: [\n", []string{"parts", "inference-proxy", "enabled"}, true, "YAML"},
		"two documents":                     {"a: 1\n---\nparts: {}\n", []string{"parts", "inference-proxy", "enabled"}, true, "document"},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := Edit([]byte(tc.text), tc.path, tc.value)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v; want one naming %q", err, tc.want)
			}
			if string(out) != tc.text {
				t.Error("a refused edit returned changed text")
			}
		})
	}
}

func TestEdit_TheResultSaysWhatWasAsked(t *testing.T) {
	text := edit(t, commented, []string{"parts", "inference-proxy", "engines", "lab"}, Block{{"type", "messages"}, {"url", "https://lab.example:8443"}, {"enabled", false}})
	text = edit(t, text, []string{"parts", "inference-proxy", "engines", "anthropic", "enabled"}, true)
	text = edit(t, text, []string{"parts", "inference-proxy", "engines", "enmaas"}, nil)
	s, err := Parse([]byte(text))
	if err != nil || len(s.Validate()) != 0 {
		t.Fatalf("%v, %v\n%s", err, s.Validate(), text)
	}
	proxy := s.Parts["inference-proxy"]
	if !proxy.Engine("anthropic").On || proxy.Engine("lab").On || proxy.Engine("lab").URL != "https://lab.example:8443" || len(proxy.Engines) != 2 {
		t.Errorf("engines = %+v", proxy.Engines)
	}
	// Text that would read as something else is quoted.
	text = edit(t, "parts:\n  inference-proxy:\n    enabled: true\n", []string{"parts", "inference-proxy", "engines", "codex", "binary"}, "true")
	if !strings.Contains(text, `binary: "true"`) {
		t.Errorf("a word YAML would read as a switch was not quoted:\n%s", text)
	}
}

func TestSet_WritesTheFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(file, []byte(commented), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o400); err != nil { // a mode that is not the default, to see it kept
		t.Fatal(err)
	}
	if err := Set(file, []string{"parts", "mcp-proxy", "enabled"}, false); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(file) // #nosec G304 -- the test's own file
	if want := strings.Replace(commented, "  mcp-proxy:\n    enabled: true", "  mcp-proxy:\n    enabled: false", 1); string(got) != want {
		t.Errorf("file:\n%s", got)
	}
	if info, _ := os.Stat(file); info.Mode().Perm() != 0o400 {
		t.Errorf("mode = %v; the file keeps its mode", info.Mode().Perm())
	}
	// A refused edit leaves the file as it was.
	if err := os.Chmod(file, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("parts: yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Set(file, []string{"parts", "mcp-proxy", "enabled"}, false); err == nil {
		t.Error("an edit of a broken section succeeded")
	}
	if got, _ := os.ReadFile(file); string(got) != "parts: yes\n" { // #nosec G304 -- the test's own file
		t.Error("a refused edit changed the file")
	}
	// A file that is not there is made, readable by its owner only.
	fresh := filepath.Join(t.TempDir(), "sub", "config.yaml")
	if err := Set(fresh, []string{"parts", "inference-proxy", "enabled"}, true); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(fresh); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("a new file: %v, %v", err, info)
	}
}

func TestEdit_MoreShapesOfFile(t *testing.T) {
	enabled := []string{"parts", "inference-proxy", "enabled"}
	// A comment set off by a tab stays.
	if got := edit(t, "parts:\n  inference-proxy:\n    enabled: true\t# keep\n", enabled, false); got != "parts:\n  inference-proxy:\n    enabled: false\t# keep\n" {
		t.Errorf("a tab before a comment: %q", got)
	}
	// Empty sections, however they are spelled, can be added to, and a comment beside them stays.
	for text, want := range map[string]string{
		"parts: null\n":            "parts:\n  inference-proxy:\n    enabled: true\n",
		"parts: ~   # none\n":      "parts:   # none\n  inference-proxy:\n    enabled: true\n",
		"parts: { }\nshell: zsh\n": "parts:\n  inference-proxy:\n    enabled: true\nshell: zsh\n",
		"parts: {}\t# none\n":      "parts:\t# none\n  inference-proxy:\n    enabled: true\n",
	} {
		if got := edit(t, text, enabled, true); got != want {
			t.Errorf("%q:\ngot  %q\nwant %q", text, got, want)
		}
	}
	// A heading above the first engine is about the engines, not about that one: it stays when the engine goes.
	headed := "parts:\n  inference-proxy:\n    enabled: true\n    engines:\n      # my engines\n      anthropic:\n        enabled: true\n      # the gateway\n      enmaas:\n        type: messages\n        url: https://a.example\n"
	if got := edit(t, headed, []string{"parts", "inference-proxy", "engines", "anthropic"}, nil); !strings.Contains(got, "# my engines") || strings.Contains(got, "anthropic") {
		t.Errorf("the first engine removed:\n%s", got)
	}
	if got := edit(t, headed, []string{"parts", "inference-proxy", "engines", "enmaas"}, nil); strings.Contains(got, "# the gateway") || !strings.Contains(got, "# my engines") {
		t.Errorf("a later engine removed:\n%s", got)
	}
	// Line endings the editor cannot follow are refused, not guessed at.
	for name, text := range map[string]string{"a Unicode line separator": "shell: zsh\u2028parts:\n  inference-proxy:\n    enabled: true\n", "old Mac line endings": "shell: zsh\rparts:\r  inference-proxy:\r    enabled: true\r", "a broken second document": "parts: {}\n---\n[\n"} {
		if _, err := Edit([]byte(text), enabled, false); err == nil {
			t.Errorf("%s: the edit went ahead", name)
		}
	}
}

func TestSet_FollowsALinkAndRefusesAFileThatChanged(t *testing.T) {
	dir := t.TempDir()
	target, link := filepath.Join(dir, "real.yaml"), filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(target, []byte("parts:\n  mcp-proxy:\n    enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := Set(link, []string{"parts", "mcp-proxy", "enabled"}, false); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced by a file")
	}
	if got, _ := os.ReadFile(target); !strings.Contains(string(got), "enabled: false") { // #nosec G304 -- the test's own file
		t.Errorf("the file the link points at was not changed: %s", got)
	}
	// A link to a file that is not there yet: the file is made where the link points, and the link stays.
	missing, dangling := filepath.Join(dir, "later.yaml"), filepath.Join(dir, "dangling.yaml")
	if err := os.Symlink("later.yaml", dangling); err != nil {
		t.Fatal(err)
	}
	if err := Set(dangling, []string{"parts", "mcp-proxy", "enabled"}, true); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(dangling); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Error("a link to a missing file was replaced by a file")
	}
	if _, err := os.Stat(missing); err != nil {
		t.Errorf("the file the link points at was not made: %v", err)
	}
	// Many edits at once: each one lands.
	done := make(chan error, 8)
	for i := range 8 {
		go func() {
			done <- Set(target, []string{"parts", "inference-proxy", "engines", "e" + strconv.Itoa(i)}, Block{{"type", "messages"}, {"url", "https://a.example"}})
		}()
	}
	for range 8 {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
	s, err := Load(target)
	if err != nil || len(s.Parts["inference-proxy"].Engines) != 8 {
		t.Errorf("after eight edits at once: %v, %d engines; want all eight", err, len(s.Parts["inference-proxy"].Engines))
	}
}

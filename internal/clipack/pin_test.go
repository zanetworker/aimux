package clipack_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/clipack"
	"github.com/zanetworker/aimux/internal/toolgateway"
)

// External tests: toolgateway imports mcpproxy, which imports clipack, so
// only an external test package can pin tools with the real PinTool.

func pinNamed(t *testing.T, p clipack.Pack, version, name string) string {
	t.Helper()
	tools, err := p.Tools(version)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range tools {
		var s struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		if s.Name == name {
			pin, err := toolgateway.PinTool(raw)
			if err != nil {
				t.Fatal(err)
			}
			return pin
		}
	}
	t.Fatalf("no tool %s", name)
	return ""
}

func builtinGH(t *testing.T) clipack.Pack {
	t.Helper()
	p, err := clipack.Builtin("gh")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTools_PinFollowsBinaryVersion(t *testing.T) {
	p := builtinGH(t)
	pinA, pinAgain := pinNamed(t, p, "2.103.3", "gh_issue_list"), pinNamed(t, p, "2.103.3", "gh_issue_list")
	if pinA != pinAgain {
		t.Errorf("same input, different pins %s %s", pinA, pinAgain)
	}
	if pinA == pinNamed(t, p, "2.104.0", "gh_issue_list") {
		t.Error("pin did not change with the binary version")
	}
}

func TestTools_PinFollowsBinaryPath(t *testing.T) {
	p := builtinGH(t)
	pinA := pinNamed(t, p, "2.103.3", "gh_issue_list")
	p.Binary = "/opt/gh"
	if pinA == pinNamed(t, p, "2.103.3", "gh_issue_list") {
		t.Error("pin did not change with the binary path")
	}
}

func pinOf(t *testing.T, doc string) string {
	t.Helper()
	p, err := clipack.Load([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return pinNamed(t, p, "1", "t_c")
}

func cmdPack(cmd string) string {
	return "name: t\nbinary: /usr/bin/gh\nversion: \"1.0\"\npolicy:\n  - {host: api.github.com, port: 443, protocol: rest, enforcement: enforce, access: read-only}\ncommands:\n" + cmd
}

func TestTools_BindingsInPin(t *testing.T) {
	ab := cmdPack("  - {name: c, argv: [issue, view], flags: {repo: {type: string, pattern: '[a-z]+'}}, args: [{name: a, type: int, max: 5}, {name: b, type: int, max: 5}]}\n")
	ba := cmdPack("  - {name: c, argv: [issue, view], flags: {repo: {type: string, pattern: '[a-z]+'}}, args: [{name: b, type: int, max: 5}, {name: a, type: int, max: 5}]}\n")
	if first, second := pinOf(t, ab), pinOf(t, ab); first != second {
		t.Error("same pack, different pins")
	}
	if pinOf(t, ab) == pinOf(t, ba) {
		t.Error("swapping positional args did not change the pin")
	}
	renamed := strings.Replace(ab, "flags: {repo:", "flags: {owner:", 1)
	if pinOf(t, ab) == pinOf(t, renamed) {
		t.Error("renaming a flag did not change the pin")
	}
}

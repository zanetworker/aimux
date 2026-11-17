package profilerun

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/sandboxlaunch"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// goldenPins and goldenReg are a fixed install: four reviewed sets, one
// listed tool left unpinned so the set and pins intersection shows.
var goldenPins = toolgateway.Pins{
	"wiki":   {"wiki_read": "a", "wiki_search": "b"},
	"jira":   {"search_issues_jql": "c", "get_issue_details": "d"},
	"slack":  {"search_messages": "e"},
	"google": {"get_doc_as_markdown": "f"},
}

var goldenReg = toolreg.Registry{Version: 1, Sets: map[string]toolreg.Set{
	"wiki:readonly":   {Source: "wiki", Tools: []string{"wiki_search", "wiki_read"}},
	"jira:readonly":   {Source: "jira", Tools: []string{"search_issues_jql", "get_issue_details", "get_comments"}},
	"slack:readonly":  {Source: "slack", Tools: []string{"search_messages"}},
	"google:readonly": {Source: "google", Tools: []string{"get_doc_as_markdown"}},
}}

// goldenTurn is what one claude launch looked like, with the random session
// id and token replaced by placeholders.
type goldenTurn struct {
	Argv      []string            `json:"argv"`
	MCPConfig string              `json:"mcp_config"`
	Scope     map[string][]string `json:"token_scope"`
	Env       map[string]string   `json:"env"`
}

type goldenProfile struct {
	PolicyDigest string       `json:"policy_digest"`
	Turns        []goldenTurn `json:"turns"`
}

type harnessGolden struct {
	Page     map[string]json.RawMessage `json:"page_acp_meta"` // by model
	Profiles map[string]goldenProfile   `json:"profiles"`
}

func compileGolden(t *testing.T, name string) profile.Compiled {
	t.Helper()
	m, err := profile.Resolve(name, nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := profile.Compile(m, profile.RegistrySets(goldenReg, goldenPins), profile.GatewayAddr{})
	if err != nil {
		t.Fatalf("compile %s: %v", name, err)
	}
	return c
}

// runGoldenTurns runs a first turn, a resumed turn and a resumed turn with
// the opus model through the real SandboxEngine on a fake backend.
func runGoldenTurns(t *testing.T, c profile.Compiled) []goldenTurn {
	t.Helper()
	tokens := &toolgateway.Tokens{}
	b := &sbBackend{tokens: tokens, script: func(s string) (string, error, error) { return okStream(s), nil, nil }}
	e := NewSandboxEngine(&sandboxlaunch.Launcher{Backend: b},
		func(string) (profile.Compiled, error) { return c, nil },
		func() (string, Leases, error) {
			return "http://host.containers.internal:7390", toolgateway.LocalLeases{T: tokens}, nil
		})
	e.ArchiveDir = filepath.Join(t.TempDir(), "archive")
	sid, err := e.New(context.Background(), Meta{Profile: c.Profile, URL: "https://a", Title: "T"}, "PAGE")
	if err != nil {
		t.Fatal(err)
	}
	for _, ctx := range []context.Context{context.Background(), context.Background(), WithModel(context.Background(), OpusPageModel)} {
		if _, err := e.Send(ctx, sid, "q", func(acprun.Event) {}); err != nil {
			t.Fatal(err)
		}
	}
	var out []goldenTurn
	for _, x := range b.execs {
		argv := make([]string, len(x.args))
		for i, a := range x.args {
			argv[i] = strings.ReplaceAll(a, sid, "<SESSION>")
		}
		cfg := x.cfg
		if x.token != "" {
			cfg = strings.ReplaceAll(cfg, x.token, "<TOKEN>")
		}
		out = append(out, goldenTurn{Argv: argv, MCPConfig: cfg, Scope: x.allow, Env: x.env})
	}
	return out
}

// The harness launch config and the compiled policy for page, research and
// work-readonly are pinned byte for byte: refactors of how the grant and
// intent are carried must not change what claude is started with.
func TestHarnessLaunch_MatchesGolden(t *testing.T) {
	got := harnessGolden{Page: map[string]json.RawMessage{}, Profiles: map[string]goldenProfile{}}
	for _, model := range []string{DefaultPageModel, OpusPageModel} {
		spec, err := PageSpec("/adapter", "/cwd", model)
		if err != nil {
			t.Fatal(err)
		}
		meta, err := json.Marshal(spec.Meta)
		if err != nil {
			t.Fatal(err)
		}
		got.Page[model] = meta
	}
	for _, name := range []string{"research", "work-readonly"} {
		c := compileGolden(t, name)
		got.Profiles[name] = goldenProfile{PolicyDigest: c.PolicyDigest, Turns: runGoldenTurns(t, c)}
	}
	b, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	golden := filepath.Join("testdata", "harness-launch.golden.json")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, b, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("rewrote %s; rerun without UPDATE_GOLDEN to check it", golden)
	}
	want, err := os.ReadFile(golden) //nolint:gosec // G304: golden is a fixed testdata path
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(want) {
		t.Errorf("harness launch differs from golden\n--- got ---\n%s\n--- want ---\n%s", b, want)
	}
}

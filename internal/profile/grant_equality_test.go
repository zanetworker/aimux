package profile

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/harnessconfig"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// viewScope reads a harness grant view back into a map; nil is empty.
func viewScope(v harnessconfig.ToolGrantView) map[string][]string {
	out := map[string][]string{}
	if v == nil {
		return out
	}
	for _, s := range v.Servers() {
		out[s] = v.Tools(s)
	}
	return out
}

// pairs flattens server -> tools into sorted "server/tool" entries.
func pairs(m map[string][]string) []string {
	out := []string{}
	for s, tools := range m {
		for _, t := range tools {
			out = append(out, s+"/"+t)
		}
	}
	slices.Sort(out)
	return out
}

// One compiled grant is the only source of what a session may call: the
// policy's tools/call rules, the gateway token's scope, the harness's
// --allowedTools and its MCP config servers are the same set in every case.
func TestCompile_OneGrantForPolicyTokenAndHarness(t *testing.T) {
	intersect := RegistrySets(toolreg.Registry{Version: 1, Sets: map[string]toolreg.Set{
		"wiki:readonly": {Source: "wiki", Tools: []string{"read_page", "never_pinned"}},
		"jira:readonly": {Source: "jira", Tools: []string{"search"}},
	}}, toolgateway.Pins{"wiki": {"read_page": "a", "search": "b"}, "jira": {"search": "c", "create_issue": "d"}})
	cases := []struct {
		name    string
		profile string
		sets    ToolSets
		want    []string
	}{
		{"all pinned", "work-readonly", testSets(), []string{"google/get_doc", "jira/get_issue", "jira/search", "slack/history", "wiki/read_page", "wiki/search"}},
		{"slack unpinned", "work-readonly", pinnedSets(toolgateway.Pins{"wiki": {"search": "b"}, "google": {"get_doc": "f"}}), []string{"google/get_doc", "wiki/search"}},
		{"set and pins intersect", "work-readonly", intersect, []string{"jira/search", "wiki/read_page"}},
		{"no tools", "research", testSets(), []string{}},
	}
	adapter, err := harnessconfig.For("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := Resolve(tc.profile, nil)
			if err != nil {
				t.Fatal(err)
			}
			c, err := Compile(m, tc.sets, testGW)
			if err != nil {
				t.Fatal(err)
			}

			policy := map[string][]string{}
			for _, np := range parsePolicy(t, c.PolicyYAML).NetworkPolicies {
				for _, e := range np.Endpoints {
					for _, r := range e.Rules {
						if r.Allow.Method == "tools/call" {
							s := strings.TrimPrefix(e.Path, "/mcp/")
							policy[s] = append(policy[s], r.Allow.Tool.Any...)
						}
					}
				}
			}

			var tokens toolgateway.Tokens
			tok, err := tokens.Issue("s", c.Grant.Scope(), c.Snapshot)
			if err != nil {
				t.Fatal(err)
			}
			_, scope, ok := tokens.Lookup(tok)
			if !ok {
				t.Fatal("token not found")
			}

			spec, err := adapter.Config(c.Capabilities(), harnessconfig.Launch{Mode: harnessconfig.ModeHeadless})
			if err != nil {
				t.Fatal(err)
			}
			allowed := map[string][]string{}
			for i, a := range spec.Argv {
				if a != "--allowedTools" || i+1 == len(spec.Argv) {
					continue
				}
				for _, n := range strings.Split(spec.Argv[i+1], ",") {
					if rest, ok := strings.CutPrefix(n, "mcp__"); ok {
						s, tool, _ := strings.Cut(rest, "__")
						allowed[s] = append(allowed[s], tool)
					}
				}
			}
			var cfgServers []string
			for _, s := range c.Grant.Servers() {
				if strings.Contains(spec.MCPConfig("http://h", "t"), `"http://h/mcp/`+s+`"`) {
					cfgServers = append(cfgServers, s)
				}
			}

			for what, got := range map[string][]string{
				"grant": pairs(c.Grant.Scope()), "policy": pairs(policy), "token": pairs(scope),
				"allowedTools": pairs(allowed), "capabilities": pairs(viewScope(c.Capabilities().Grant)),
			} {
				if !reflect.DeepEqual(got, tc.want) {
					t.Errorf("%s = %v, want %v", what, got, tc.want)
				}
			}
			if len(cfgServers) != len(c.Grant.Servers()) || strings.Count(spec.MCPConfig("http://h", "t"), `"type":"http"`) != len(c.Grant.Servers()) {
				t.Errorf("MCP config %s, want exactly servers %v", spec.MCPConfig("http://h", "t"), c.Grant.Servers())
			}
			if g, ok := c.Capabilities().Grant.(ToolGrant); !ok || g.Digest() != c.Grant.Digest() {
				t.Errorf("Capabilities().Grant is not Compiled.Grant: %#v", c.Capabilities().Grant)
			}
		})
	}
}

package profile

import (
	"errors"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/harnessconfig"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
	"gopkg.in/yaml.v3"
)

// pinnedSets is a registry with one <server>:readonly set per pinned
// server listing exactly its pinned tools, as aimux browser setup leaves
// them, resolved against p.
func pinnedSets(p toolgateway.Pins) ToolSets {
	reg := toolreg.Registry{Version: 1, Sets: map[string]toolreg.Set{}}
	for server, tools := range p {
		reg.Sets[server+":readonly"] = toolreg.Set{Source: server, Tools: slices.Collect(maps.Keys(tools))}
	}
	return RegistrySets(reg, p)
}

func TestRegistrySets_ResolvesSortedPinnedNames(t *testing.T) {
	sets := pinnedSets(toolgateway.Pins{"jira": {"search": "p1", "get_issue": "p2"}})
	server, tools, err := sets("jira:readonly")
	if err != nil {
		t.Fatal(err)
	}
	if server != "jira" || !reflect.DeepEqual(tools, []string{"get_issue", "search"}) {
		t.Fatalf("got %q %v", server, tools)
	}
}

// A set yields only the tools it lists that are also pinned: a pin for a
// tool the set does not list grants nothing.
func TestRegistrySets_SetAndPinsIntersect(t *testing.T) {
	reg := toolreg.Registry{Version: 1, Sets: map[string]toolreg.Set{
		"jira:readonly": {Source: "jira", Tools: []string{"search", "get_issue", "never_pinned"}},
	}}
	sets := RegistrySets(reg, toolgateway.Pins{"jira": {"search": "a", "get_issue": "b", "create_issue": "c"}})
	server, tools, err := sets("jira:readonly")
	if err != nil || server != "jira" || !reflect.DeepEqual(tools, []string{"get_issue", "search"}) {
		t.Fatalf("got %q %v %v", server, tools, err)
	}
}

// Built-in profiles ask for wiki:readonly: a registry without it resolves
// it to notes:readonly, served (and pinned) under the notes source's name.
func TestRegistrySets_WikiAliasesNotes(t *testing.T) {
	reg := toolreg.Registry{Version: 1, Sets: map[string]toolreg.Set{
		"notes:readonly": {Source: "notes", Tools: []string{"wiki_read", "wiki_search"}},
	}}
	sets := RegistrySets(reg, toolgateway.Pins{"notes": {"wiki_read": "a", "wiki_search": "b"}})
	server, tools, err := sets("wiki:readonly")
	if err != nil || server != "notes" || !reflect.DeepEqual(tools, []string{"wiki_read", "wiki_search"}) {
		t.Fatalf("got %q %v %v", server, tools, err)
	}
}

func TestRegistrySets_MalformedNameIsError(t *testing.T) {
	sets := pinnedSets(toolgateway.Pins{"jira": {"search": "p"}})
	for _, s := range []string{"jira", ":readonly", "jira:", "jira:readonly:x"} {
		_, _, err := sets(s)
		if !errors.Is(err, ErrUnknownSet) || errors.Is(err, ErrUnavailable) {
			t.Errorf("%q: err = %v, want ErrUnknownSet and not ErrUnavailable", s, err)
		}
	}
}

func testSets() ToolSets {
	return pinnedSets(toolgateway.Pins{
		"wiki":   {"read_page": "a", "search": "b"},
		"jira":   {"search": "c", "get_issue": "d"},
		"slack":  {"history": "e"},
		"google": {"get_doc": "f"},
	})
}

func workReadonly(t *testing.T) Manifest {
	t.Helper()
	m, err := Resolve("work-readonly", nil)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

var testGW = GatewayAddr{}

func TestCompile_RefusesHostProfile(t *testing.T) {
	for _, runs := range []string{"host", "acp", "", "sandbx"} {
		m := workReadonly(t)
		m.Runs = runs
		if _, err := Compile(m, testSets(), testGW); !errors.Is(err, ErrNotSandboxed) {
			t.Errorf("runs=%s: err = %v, want ErrNotSandboxed", runs, err)
		}
	}
}

func TestCompile_RefusesUnavailable(t *testing.T) {
	m := workReadonly(t)
	m.Available = false
	if _, err := Compile(m, testSets(), testGW); !errors.Is(err, ErrNotAvailable) {
		t.Fatalf("err = %v, want ErrNotAvailable", err)
	}
}

func TestCompile_SandboxNameAndDigest(t *testing.T) {
	m := workReadonly(t)
	c, err := Compile(m, testSets(), testGW)
	if err != nil {
		t.Fatal(err)
	}
	if c.Profile != "work-readonly" || c.Sandbox != "aimux-work-readonly" {
		t.Errorf("profile/sandbox = %q/%q", c.Profile, c.Sandbox)
	}
	if c.Digest != m.Digest() {
		t.Errorf("digest = %q, want %q", c.Digest, m.Digest())
	}
	if c.Image != "localhost/wingman-sandbox:2.1.319" || c.Provider != "wingman-claude" {
		t.Errorf("image/provider = %q/%q", c.Image, c.Provider)
	}
	want := map[string][]string{
		"wiki": {"read_page", "search"}, "jira": {"get_issue", "search"},
		"slack": {"history"}, "google": {"get_doc"},
	}
	if !reflect.DeepEqual(c.Grant.Scope(), want) {
		t.Errorf("grant = %v", c.Grant.Scope())
	}
}

// A set the registry lacks, or whose server has no pinned tools, is
// unavailable: Compile leaves it out.
func TestRegistrySets_UnavailableSets(t *testing.T) {
	sets := pinnedSets(toolgateway.Pins{"jira": {"search": "p"}, "slack": {}})
	for _, s := range []string{"slack:readonly", "confluence:readonly", "jira:write"} {
		if _, _, err := sets(s); !errors.Is(err, ErrUnavailable) || !errors.Is(err, ErrUnknownSet) {
			t.Errorf("%q: err = %v, want ErrUnavailable", s, err)
		}
	}
	if _, _, err := sets("slack:readonly"); !errors.Is(err, ErrUnpinned) {
		t.Errorf("slack:readonly: err = %v, want ErrUnpinned", err)
	}
}

func TestCompile_MalformedSetIsError(t *testing.T) {
	m := workReadonly(t)
	m.Tools = []string{"jira:readonly", "jira"}
	if _, err := Compile(m, testSets(), testGW); !errors.Is(err, ErrUnknownSet) {
		t.Fatalf("err = %v, want ErrUnknownSet", err)
	}
}

// A backend left unpinned by aimux browser setup drops out of the profile:
// no endpoint, no tools, no token entry; the pinned sets still run.
func TestCompile_UnpinnedSetIsOmitted(t *testing.T) {
	m := workReadonly(t) // wiki, jira, slack, google
	sets := pinnedSets(toolgateway.Pins{
		"wiki":   {"read_page": "a", "search": "b"},
		"jira":   {"search": "c", "get_issue": "d"},
		"google": {"get_doc": "f"},
	})
	c, err := Compile(m, sets, testGW)
	if err != nil {
		t.Fatalf("compile with slack unpinned: %v", err)
	}
	want := map[string][]string{"wiki": {"read_page", "search"}, "jira": {"get_issue", "search"}, "google": {"get_doc"}}
	if !reflect.DeepEqual(c.Grant.Scope(), want) {
		t.Errorf("grant = %v, want %v", c.Grant.Scope(), want)
	}
	if !reflect.DeepEqual(viewScope(c.Capabilities().Grant), want) {
		t.Errorf("capabilities grant = %v, want %v", viewScope(c.Capabilities().Grant), want)
	}
	if strings.Contains(string(c.PolicyYAML), "slack") {
		t.Errorf("policy still mentions slack:\n%s", c.PolicyYAML)
	}
	for _, s := range []string{"/mcp/wiki", "/mcp/jira", "/mcp/google"} {
		if !strings.Contains(string(c.PolicyYAML), s) {
			t.Errorf("policy lacks %s:\n%s", s, c.PolicyYAML)
		}
	}
}

func TestCompile_AllSetsUnpinnedFailsClosed(t *testing.T) {
	_, err := Compile(workReadonly(t), pinnedSets(toolgateway.Pins{}), testGW)
	if !errors.Is(err, ErrUnpinned) {
		t.Fatalf("err = %v, want ErrUnpinned", err)
	}
	if err != nil && !strings.Contains(err.Error(), "work-readonly") {
		t.Errorf("error should name the profile: %v", err)
	}
}

// policyDoc is the subset of the OpenShell policy the tests inspect.
type policyDoc struct {
	FilesystemPolicy struct {
		ReadOnly  []string `yaml:"read_only"`
		ReadWrite []string `yaml:"read_write"`
	} `yaml:"filesystem_policy"`
	NetworkPolicies map[string]struct {
		Endpoints []struct {
			Host       string   `yaml:"host"`
			Port       int      `yaml:"port"`
			Path       string   `yaml:"path"`
			Protocol   string   `yaml:"protocol"`
			AllowedIPs []string `yaml:"allowed_ips"`
			Rules      []struct {
				Allow struct {
					Method string `yaml:"method"`
					Tool   struct {
						Any []string `yaml:"any"`
					} `yaml:"tool"`
				} `yaml:"allow"`
			} `yaml:"rules"`
		} `yaml:"endpoints"`
	} `yaml:"network_policies"`
}

func parsePolicy(t *testing.T, b []byte) policyDoc {
	t.Helper()
	var d policyDoc
	if err := yaml.Unmarshal(b, &d); err != nil {
		t.Fatalf("policy is not valid yaml: %v\n%s", err, b)
	}
	return d
}

func TestCompile_NoWebUnlessAllowed(t *testing.T) {
	c, err := Compile(workReadonly(t), testSets(), testGW)
	if err != nil {
		t.Fatal(err)
	}
	d := parsePolicy(t, c.PolicyYAML)
	if len(d.NetworkPolicies) == 0 {
		t.Fatal("no network policies emitted")
	}
	for name, np := range d.NetworkPolicies {
		for _, e := range np.Endpoints {
			if e.Host != "host.containers.internal" || e.Port != 7390 {
				t.Errorf("%s: endpoint %s:%d is not the gateway", name, e.Host, e.Port)
			}
		}
	}
}

func TestCompile_WorkReadonlyMatchesGolden(t *testing.T) {
	c, err := Compile(workReadonly(t), testSets(), testGW)
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "compile", "work-readonly.policy.yaml")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, c.PolicyYAML, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden) //nolint:gosec // G304: golden is a fixed testdata path
	if err != nil {
		t.Fatal(err)
	}
	if string(c.PolicyYAML) != string(want) {
		t.Errorf("policy differs from golden\n--- got ---\n%s\n--- want ---\n%s", c.PolicyYAML, want)
	}
}

func TestCompile_CapabilitiesMirrorPolicy(t *testing.T) {
	c, err := Compile(workReadonly(t), testSets(), testGW)
	if err != nil {
		t.Fatal(err)
	}
	d := parsePolicy(t, c.PolicyYAML)
	fromPolicy := map[string][]string{}
	for _, np := range d.NetworkPolicies {
		for _, e := range np.Endpoints {
			server := strings.TrimPrefix(e.Path, "/mcp/")
			for _, r := range e.Rules {
				if r.Allow.Method == "tools/call" {
					fromPolicy[server] = r.Allow.Tool.Any
				}
			}
		}
	}
	if len(fromPolicy) != 4 || !reflect.DeepEqual(viewScope(c.Capabilities().Grant), fromPolicy) {
		t.Errorf("capabilities tools %v != policy tools %v", viewScope(c.Capabilities().Grant), fromPolicy)
	}
	if caps := c.Capabilities(); caps.InheritUser || caps.Web || len(caps.Files.Read)+len(caps.Files.Write) > 0 {
		t.Errorf("capabilities must be closed: %+v", caps)
	}

	m := workReadonly(t)
	m.Files.Read = []string{"/data/notes"}
	m.Files.Write = []string{"/data/out"}
	m.Model.Name = "opus"
	c2, err := Compile(m, testSets(), testGW)
	if err != nil {
		t.Fatal(err)
	}
	want := harnessconfig.FileGrant{Read: []string{"/data/notes"}, Write: []string{"/data/out"}}
	if !reflect.DeepEqual(c2.Capabilities().Files, want) || c2.Capabilities().Model != "opus" {
		t.Errorf("files/model not carried: %+v", c2.Capabilities())
	}
	// Capabilities() is a copy: editing it cannot change a later one.
	caps := c2.Capabilities()
	caps.Files.Read[0], caps.Files.Write[0] = "/", "/"
	if got := c2.Capabilities().Files; !reflect.DeepEqual(got, want) {
		t.Errorf("Capabilities() shares its file grant: %+v", got)
	}
	if got := parsePolicy(t, c2.PolicyYAML).FilesystemPolicy.ReadOnly; !slices.Contains(got, "/data/notes") {
		t.Errorf("policy read_only %v lacks /data/notes", got)
	}
}

func TestCompile_RejectsUnsafeNames(t *testing.T) {
	bad := func(set string) (string, []string, error) { return "wiki", []string{"read page"}, nil }
	if _, err := Compile(workReadonly(t), bad, testGW); !errors.Is(err, ErrUnsupported) {
		t.Errorf("tool name: err = %v", err)
	}
	badSrv := func(set string) (string, []string, error) { return "../x", []string{"ok"}, nil }
	if _, err := Compile(workReadonly(t), badSrv, testGW); !errors.Is(err, ErrUnsupported) {
		t.Errorf("server name: err = %v", err)
	}
}

func TestCompile_ToolsNeedGatewayAllow(t *testing.T) {
	m := workReadonly(t)
	m.Network.Allow = []string{"model"}
	if _, err := Compile(m, testSets(), testGW); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	m.Tools = nil // no tools, no gateway needed
	c, err := Compile(m, testSets(), testGW)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsePolicy(t, c.PolicyYAML).NetworkPolicies) != 0 {
		t.Error("tool-less profile must have no network policies")
	}
}

func TestCompile_ModelProviderOnlyWhenModelAllowed(t *testing.T) {
	m := workReadonly(t)
	m.Network.Allow = []string{"gateway"}
	if _, err := Compile(m, testSets(), testGW); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestCompile_EmptyToolSetRefused(t *testing.T) {
	empty := func(set string) (string, []string, error) { return "wiki", nil, nil }
	_, err := Compile(workReadonly(t), empty, testGW)
	if !errors.Is(err, ErrUnknownSet) {
		t.Fatalf("err = %v, want ErrUnknownSet", err)
	}
	if err != nil && !strings.Contains(err.Error(), "wiki:readonly") {
		t.Errorf("error should name the set: %v", err)
	}
}

// The engines pick the harness adapter from the compiled profile.
func TestCompile_CarriesHarness(t *testing.T) {
	c, err := Compile(workReadonly(t), testSets(), testGW)
	if err != nil {
		t.Fatal(err)
	}
	if c.Harness != "claude-code" {
		t.Fatalf("harness = %q", c.Harness)
	}
}

// webEndpoint is one endpoint of the public_web_readonly policy as the tests read it.
type webEndpoint struct {
	Host        string   `yaml:"host"`
	Port        int      `yaml:"port"`
	Path        string   `yaml:"path"`
	Protocol    string   `yaml:"protocol"`
	Enforcement string   `yaml:"enforcement"`
	Access      string   `yaml:"access"`
	AllowedIPs  []string `yaml:"allowed_ips"`
}

type webDoc struct {
	NetworkPolicies map[string]struct {
		Name      string        `yaml:"name"`
		Endpoints []webEndpoint `yaml:"endpoints"`
		Binaries  []struct {
			Path string `yaml:"path"`
		} `yaml:"binaries"`
	} `yaml:"network_policies"`
}

func compileResearch(t *testing.T) Compiled {
	t.Helper()
	m, err := Resolve("research", nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Compile(m, testSets(), testGW)
	if err != nil {
		t.Fatalf("compile research: %v", err)
	}
	return c
}

func webPolicy(t *testing.T, c Compiled) []webEndpoint {
	t.Helper()
	var d webDoc
	if err := yaml.Unmarshal(c.PolicyYAML, &d); err != nil {
		t.Fatalf("policy is not valid yaml: %v", err)
	}
	np, ok := d.NetworkPolicies["public_web_readonly"]
	if !ok {
		return nil
	}
	if np.Name != "public_web_readonly" {
		t.Errorf("policy name = %q", np.Name)
	}
	var bins []string
	for _, b := range np.Binaries {
		bins = append(bins, b.Path)
	}
	if !reflect.DeepEqual(bins, sandboxBinaries) {
		t.Errorf("binaries = %v, want %v", bins, sandboxBinaries)
	}
	return np.Endpoints
}

var reviewedTLDs = []string{"com", "org", "net", "io", "dev", "ai", "app", "edu", "gov", "co", "me", "info", "uk", "de", "eu"}

func TestCompile_ResearchWebPolicy(t *testing.T) {
	c := compileResearch(t)
	golden := filepath.Join("testdata", "compile", "research.golden.yaml")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, c.PolicyYAML, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden) //nolint:gosec // G304: golden is a fixed testdata path
	if err != nil {
		t.Fatal(err)
	}
	if string(c.PolicyYAML) != string(want) {
		t.Errorf("policy differs from golden\n--- got ---\n%s", c.PolicyYAML)
	}
	if !c.Grant.Empty() || len(viewScope(c.Capabilities().Grant)) != 0 {
		t.Errorf("research must have no tool servers: %v / %v", c.Grant.Scope(), viewScope(c.Capabilities().Grant))
	}
	if !c.Capabilities().Web {
		t.Error("Capabilities.Web = false for a profile allowing web")
	}
	if strings.Contains(string(c.PolicyYAML), "/mcp/") || strings.Contains(string(c.PolicyYAML), "aimux_tool_gateway") {
		t.Error("research policy has a tool gateway endpoint")
	}
	eps := webPolicy(t, c)
	var tlds []string
	exact := 0
	for _, e := range eps {
		if e.Port != 443 || e.Protocol != "rest" || e.Enforcement != "enforce" || e.Access != "read-only" || e.Path != "" {
			t.Errorf("endpoint %s: %+v is not read-only rest on 443", e.Host, e)
		}
		if tld, ok := strings.CutPrefix(e.Host, "**.*."); ok {
			if len(e.AllowedIPs) != 0 {
				t.Errorf("wildcard %s has allowed_ips %v", e.Host, e.AllowedIPs)
			}
			tlds = append(tlds, tld)
			continue
		}
		exact++
		if len(e.AllowedIPs) == 0 {
			t.Errorf("apex %s has no allowed_ips", e.Host)
		}
	}
	slices.Sort(tlds)
	wantTLDs := slices.Sorted(slices.Values(reviewedTLDs))
	if !reflect.DeepEqual(tlds, wantTLDs) {
		t.Errorf("wildcard TLDs = %v, want %v", tlds, wantTLDs)
	}
	if !reflect.DeepEqual(WebTLDs(), wantTLDs) {
		t.Errorf("WebTLDs() = %v, want %v", WebTLDs(), wantTLDs)
	}
	if exact != len(WebApexHosts()) || exact == 0 {
		t.Errorf("exact endpoints = %d, apex hosts = %d", exact, len(WebApexHosts()))
	}
}

func TestCompile_WebDroppedByChild(t *testing.T) {
	m := loadOne(t, "name: x\nextends: builtin:research\nnetwork:\n  allow: [model]\n")
	c, err := Compile(m, testSets(), testGW)
	if err != nil {
		t.Fatal(err)
	}
	if eps := webPolicy(t, c); eps != nil || strings.Contains(string(c.PolicyYAML), "public_web_readonly") {
		t.Errorf("child without web still has the web policy:\n%s", c.PolicyYAML)
	}
	if c.Capabilities().Web {
		t.Error("Capabilities.Web = true after the child dropped web")
	}
}

func TestResolve_AddingWebWidens(t *testing.T) {
	user := map[string]Manifest{"x": {Name: "x", Extends: "builtin:work-readonly", Network: NetworkSpec{Allow: []string{"model", "gateway", "web"}}}}
	if _, err := Resolve("x", user); !errors.Is(err, ErrWidens) {
		t.Fatalf("err = %v, want ErrWidens", err)
	}
}

func TestCompile_ApexHostsExact(t *testing.T) {
	// An independent table, not internalIPv4: removing a range from the
	// generator must fail this test.
	var blocked []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15",
		"224.0.0.0/4", "240.0.0.0/4", "255.255.255.255/32",
		"::1/128", "fe80::/10", "fc00::/7",
	} {
		blocked = append(blocked, netip.MustParsePrefix(s))
	}
	eps := webPolicy(t, compileResearch(t))
	seen := map[string]int{}
	for _, e := range eps {
		seen[e.Host]++
	}
	for _, h := range WebApexHosts() {
		if seen[h] != 1 {
			t.Errorf("apex %s appears %d times as an exact host", h, seen[h])
		}
		if strings.Contains(h, "*") {
			t.Errorf("apex %s has a wildcard", h)
		}
	}
	for _, e := range eps {
		if strings.Contains(e.Host, "*") {
			continue
		}
		for _, s := range e.AllowedIPs {
			p, err := netip.ParsePrefix(s)
			if err != nil {
				t.Fatalf("%s: allowed_ips %q: %v", e.Host, s, err)
			}
			for _, b := range blocked {
				if p.Overlaps(b) {
					t.Errorf("%s: allowed_ips %s overlaps %s", e.Host, p, b)
				}
			}
		}
	}
}

// OpenShell rejects two endpoints that can match the same host on the same
// port with different allowed_ips. A wildcard **.*.<tld> matches every host
// of three or more labels under a listed TLD, so such a host cannot also be
// an exact apex entry (it is reachable through the wildcard already).
func TestWebApex_NotCoveredByWildcard(t *testing.T) {
	tlds := WebTLDs()
	for _, h := range WebApexHosts() {
		labels := strings.Split(h, ".")
		if len(labels) >= 3 && slices.Contains(tlds, labels[len(labels)-1]) {
			t.Errorf("apex %s overlaps wildcard **.*.%s", h, labels[len(labels)-1])
		}
	}
}

func TestPolicyDigest_ChangesWithTLDs(t *testing.T) {
	orig := webTLDs
	t.Cleanup(func() { webTLDs = orig })
	webTLDs = []string{"com"}
	a := compileResearch(t)
	webTLDs = []string{"com", "org"}
	b := compileResearch(t)
	if a.PolicyDigest == b.PolicyDigest {
		t.Fatal("policy digest unchanged when the TLD list changed")
	}
	if a.Digest != b.Digest {
		t.Errorf("manifest digest moved with the TLD list: %q vs %q", a.Digest, b.Digest)
	}
}

func TestCompile_SandboxNameTooLong(t *testing.T) {
	m := loadOne(t, "name: x\nextends: builtin:research\n")
	m.Name = "research-with-work2"
	_, err := Compile(m, testSets(), testGW)
	if !errors.Is(err, ErrNameTooLong) || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrNameTooLong wrapping ErrUnsupported", err)
	}
	if !strings.Contains(err.Error(), "aimux-research-with-work2") {
		t.Errorf("error should name the sandbox: %v", err)
	}
	c, err := Compile(workReadonly(t), testSets(), testGW)
	if err != nil || len(c.Sandbox) != 19 {
		t.Fatalf("work-readonly (19 chars) must compile: %v, sandbox %q", err, c.Sandbox)
	}
}

func TestBuiltins_ResearchAvailable(t *testing.T) {
	r, err := Resolve("research", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Available || len(r.Tools) != 0 || !reflect.DeepEqual(r.Network.Allow, []string{"model", "web"}) {
		t.Errorf("research = %+v, want available, no tools, allow [model web]", r)
	}
	rww, err := Resolve("research-with-work", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rww.Available {
		t.Error("research-with-work must stay unavailable")
	}
}

func TestParseList(t *testing.T) {
	got, err := parseList("# comment\n\n  org \ncom\n# x\n", tldRE)
	if err != nil || !reflect.DeepEqual(got, []string{"com", "org"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, bad := range []string{"co m\n", "*.com\n", "com\ncom\n", "COM\n", "# only comments\n"} {
		if _, err := parseList(bad, tldRE); err == nil {
			t.Errorf("parseList(%q) accepted", bad)
		}
	}
	if _, err := parseList("go.dev\nendoflife.date\n", apexRE); err != nil {
		t.Errorf("apex hosts rejected: %v", err)
	}
	for _, bad := range []string{"*.github.com\n", "github\n", ".github.com\n", "github..com\n"} {
		if _, err := parseList(bad, apexRE); err == nil {
			t.Errorf("parseList(%q, apex) accepted", bad)
		}
	}
}

func TestPublicIPv4_ComplementsInternal(t *testing.T) {
	pub := publicIPv4()
	var total uint64
	for _, p := range pub {
		total += 1 << (32 - p.Bits())
		for _, in := range internalIPv4 {
			if p.Overlaps(in) {
				t.Errorf("public %s overlaps internal %s", p, in)
			}
		}
	}
	var internal uint64
	for _, in := range internalIPv4 {
		internal += 1 << (32 - in.Bits())
	}
	if total+internal != 1<<32 {
		t.Errorf("public %d + internal %d addresses != 2^32", total, internal)
	}
	if !slices.IsSortedFunc(pub, func(a, b netip.Prefix) int { return a.Addr().Compare(b.Addr()) }) {
		t.Error("public ranges not sorted")
	}
}

// hostPatternMatches mirrors OpenShell's host glob: "**" spans labels, "*"
// stays inside one label, matching is case-insensitive.
func hostPatternMatches(pattern, host string) bool {
	var re strings.Builder
	re.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch {
		case strings.HasPrefix(pattern[i:], "**"):
			re.WriteString(".+")
			i++
		case pattern[i] == '*':
			re.WriteString("[^.]+")
		default:
			re.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
		}
	}
	re.WriteString("$")
	return regexp.MustCompile(re.String()).MatchString(strings.ToLower(host))
}

// OpenShell authorizes its host-gateway aliases as trusted gateways, which
// skips the private-address block wildcards otherwise get: a pattern like
// "**.*.*" made host.containers.internal resolve to the Mac host on 443
// (live on 0.1.7). No web endpoint may match these names.
func TestCompile_WebNeverMatchesHostGateway(t *testing.T) {
	aliases := []string{"host.containers.internal", "host.openshell.internal", "host.docker.internal"}
	for _, a := range aliases {
		if !hostPatternMatches("**.*.*", a) || hostPatternMatches("**.*.com", a) {
			t.Fatalf("matcher self-check failed for %s", a)
		}
	}
	for _, e := range webPolicy(t, compileResearch(t)) {
		for _, a := range aliases {
			if hostPatternMatches(e.Host, a) {
				t.Errorf("web endpoint %q matches host-gateway alias %s", e.Host, a)
			}
		}
	}
}

func TestCheckSandboxName(t *testing.T) {
	cases := []struct {
		name, runs string
		tooLong    bool
	}{
		{"research-1234", "sandbox", false}, // 19 with the prefix
		{"research-12345", "sandbox", true}, // 20
		{"research-with-work", "run", true}, // 24
		{"page-with-a-long-name", "acp", false},
		{"coding-with-a-long-name", "host", false},
	}
	for _, c := range cases {
		err := CheckSandboxName(Manifest{Name: c.name, Runs: c.runs})
		if got := errors.Is(err, ErrNameTooLong); got != c.tooLong {
			t.Errorf("%s (runs %s): err = %v, want too long %v", c.name, c.runs, err, c.tooLong)
		}
	}
}

// A cli source's set is granted like any other: the server is the source
// name, its pinned tools are the grant, and the policy has its /mcp rule.
func TestCompile_CLISetInGrant(t *testing.T) {
	reg := toolreg.Registry{Version: 1,
		Sources: map[string]toolreg.Source{"gh": {Type: toolreg.TypeCLI, Pack: "gh"}},
		Sets:    map[string]toolreg.Set{"gh:readonly": {Source: "gh", Tools: []string{"issue_list", "pr_view", "unpinned"}}},
	}
	sets := RegistrySets(reg, toolgateway.Pins{"gh": {"issue_list": "a", "pr_view": "b"}})
	m := workReadonly(t)
	m.Tools = []string{"gh:readonly"}
	c, err := Compile(m, sets, testGW)
	if err != nil {
		t.Fatal(err)
	}
	if got := viewScope(c.Capabilities().Grant); !reflect.DeepEqual(got, map[string][]string{"gh": {"issue_list", "pr_view"}}) {
		t.Errorf("grant = %v", got)
	}
	if !strings.Contains(string(c.PolicyYAML), "/mcp/gh") {
		t.Errorf("policy has no /mcp/gh rule:\n%s", c.PolicyYAML)
	}
}

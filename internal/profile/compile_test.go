package profile

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/toolgateway"
	"gopkg.in/yaml.v3"
)

func TestPinnedSets_ResolvesSortedPinnedNames(t *testing.T) {
	sets := PinnedSets(toolgateway.Pins{"jira": {"search": "p1", "get_issue": "p2"}})
	server, tools, err := sets("jira:readonly")
	if err != nil {
		t.Fatal(err)
	}
	if server != "jira" || !reflect.DeepEqual(tools, []string{"get_issue", "search"}) {
		t.Fatalf("got %q %v", server, tools)
	}
}

func TestPinnedSets_UnknownIsError(t *testing.T) {
	sets := PinnedSets(toolgateway.Pins{"jira": {"search": "p"}})
	for _, s := range []string{"slack:readonly", "jira:write", "jira", ":readonly", "jira:readonly:x"} {
		if _, _, err := sets(s); !errors.Is(err, ErrUnknownSet) {
			t.Errorf("%q: err = %v, want ErrUnknownSet", s, err)
		}
	}
}

func testSets() ToolSets {
	return PinnedSets(toolgateway.Pins{
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
	if !reflect.DeepEqual(c.Servers, want) {
		t.Errorf("servers = %v", c.Servers)
	}
}

func TestPinnedSets_UnpinnedServerIsErrUnpinned(t *testing.T) {
	sets := PinnedSets(toolgateway.Pins{"jira": {"search": "p"}, "slack": {}})
	for _, s := range []string{"slack:readonly", "confluence:readonly"} {
		if _, _, err := sets(s); !errors.Is(err, ErrUnpinned) {
			t.Errorf("%q: err = %v, want ErrUnpinned", s, err)
		}
	}
	for _, s := range []string{"jira:write", "jira", ":readonly"} {
		if _, _, err := sets(s); errors.Is(err, ErrUnpinned) {
			t.Errorf("%q: malformed set reported as unpinned: %v", s, err)
		}
	}
}

func TestCompile_MalformedSetIsError(t *testing.T) {
	m := workReadonly(t)
	m.Tools = []string{"jira:readonly", "jira:write"}
	if _, err := Compile(m, testSets(), testGW); !errors.Is(err, ErrUnknownSet) {
		t.Fatalf("err = %v, want ErrUnknownSet", err)
	}
}

// A backend left unpinned by aimux browser setup drops out of the profile:
// no endpoint, no tools, no token entry; the pinned sets still run.
func TestCompile_UnpinnedSetIsOmitted(t *testing.T) {
	m := workReadonly(t) // wiki, jira, slack, google
	sets := PinnedSets(toolgateway.Pins{
		"wiki":   {"read_page": "a", "search": "b"},
		"jira":   {"search": "c", "get_issue": "d"},
		"google": {"get_doc": "f"},
	})
	c, err := Compile(m, sets, testGW)
	if err != nil {
		t.Fatalf("compile with slack unpinned: %v", err)
	}
	want := map[string][]string{"wiki": {"read_page", "search"}, "jira": {"get_issue", "search"}, "google": {"get_doc"}}
	if !reflect.DeepEqual(c.Servers, want) {
		t.Errorf("servers = %v, want %v", c.Servers, want)
	}
	if !reflect.DeepEqual(c.Intent.Tools, want) {
		t.Errorf("intent tools = %v, want %v", c.Intent.Tools, want)
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
	_, err := Compile(workReadonly(t), PinnedSets(toolgateway.Pins{}), testGW)
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

func TestCompile_IntentMirrorsPolicy(t *testing.T) {
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
	if len(fromPolicy) != 4 || !reflect.DeepEqual(c.Intent.Tools, fromPolicy) {
		t.Errorf("intent tools %v != policy tools %v", c.Intent.Tools, fromPolicy)
	}
	if c.Intent.InheritUser || c.Intent.Web || c.Intent.Files {
		t.Errorf("intent must be closed: %+v", c.Intent)
	}
	if c.Intent.Mode != "print" {
		t.Errorf("mode = %q", c.Intent.Mode)
	}

	m := workReadonly(t)
	m.Files.Read = []string{"/data/notes"}
	m.Model.Name = "opus"
	c2, err := Compile(m, testSets(), testGW)
	if err != nil {
		t.Fatal(err)
	}
	if !c2.Intent.Files || c2.Intent.Model != "opus" {
		t.Errorf("files/model not carried: %+v", c2.Intent)
	}
	if got := parsePolicy(t, c2.PolicyYAML).FilesystemPolicy.ReadOnly; !slices.Contains(got, "/data/notes") {
		t.Errorf("policy read_only %v lacks /data/notes", got)
	}
}

func TestCompile_WebAllowIsRefused(t *testing.T) {
	m := workReadonly(t)
	m.Network.Allow = append(m.Network.Allow, "web")
	if _, err := Compile(m, testSets(), testGW); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
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

package profile

import (
	"errors"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/clipack"
	"github.com/zanetworker/aimux/internal/httpsource"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
	"gopkg.in/yaml.v3"
)

func noSpecs(toolreg.Source) ([]httpsource.Operation, error) { return nil, nil }

func runnerSnap(sources map[string]toolreg.Source, pins toolreg.PinTable) toolreg.Snapshot {
	return toolreg.NewSnapshot(toolreg.Registry{Version: 1, Sources: sources}, pins, nil)
}

func ghSources() map[string]toolreg.Source {
	return map[string]toolreg.Source{"github": {Type: toolreg.TypeCLI, Pack: "gh"}}
}

func ghPins(tools ...string) toolreg.PinTable {
	out := toolreg.PinTable{"github": {}}
	for _, t := range tools {
		out["github"][t] = toolreg.Pin{Hash: "sha256:" + t, Source: "pack:gh"}
	}
	return out
}

func decodePolicy(t *testing.T, b []byte) policyFile {
	t.Helper()
	var p policyFile
	if err := yaml.Unmarshal(b, &p); err != nil {
		t.Fatalf("policy is not YAML: %v\n%s", err, b)
	}
	return p
}

func TestCompileRunner_GHPolicy(t *testing.T) {
	snap := runnerSnap(ghSources(), ghPins("gh_issue_list"))
	c, err := CompileRunner(snap, clipack.Builtin, noSpecs)
	if err != nil {
		t.Fatal(err)
	}
	if c.Sandbox != RunnerSandbox || c.Image != RunnerImage || RunnerSandbox != "aimux-tools" || RunnerImage != "localhost/aimux-tools:2.103.3" {
		t.Fatalf("sandbox %q image %q", c.Sandbox, c.Image)
	}
	if strings.Join(c.Providers, ",") != "aimux-github" {
		t.Fatalf("providers = %v", c.Providers)
	}
	if c.Snapshot.Version() != snap.Version() {
		t.Fatal("snapshot not recorded")
	}
	p := decodePolicy(t, c.PolicyYAML)
	np, ok := p.NetworkPolicies["tools_github"]
	if !ok || len(p.NetworkPolicies) != 1 {
		t.Fatalf("network policies = %v", p.NetworkPolicies)
	}
	if len(np.Binaries) != 1 || np.Binaries[0].Path != "/usr/bin/gh" {
		t.Fatalf("binaries = %v", np.Binaries)
	}
	if len(np.Endpoints) != 2 {
		t.Fatalf("endpoints = %+v", np.Endpoints)
	}
	rest, gql := np.Endpoints[0], np.Endpoints[1]
	if rest.Host != "api.github.com" || rest.Port != 443 || rest.Protocol != "rest" || rest.Access != "read-only" || rest.Enforcement != "enforce" || len(rest.Rules) != 0 || rest.Path != "" {
		t.Fatalf("rest endpoint = %+v", rest)
	}
	if gql.Host != "api.github.com" || gql.Path != "/graphql" || gql.Protocol != "graphql" || gql.Access != "" || gql.Enforcement != "enforce" ||
		len(gql.Rules) != 1 || gql.Rules[0].Allow.OperationType != "query" || gql.Rules[0].Allow.Method != "" {
		t.Fatalf("graphql endpoint = %+v", gql)
	}
	for _, want := range []string{"operation_type: query", "access: read-only", "protocol: graphql"} {
		if !strings.Contains(string(c.PolicyYAML), want) {
			t.Errorf("policy lacks %q:\n%s", want, c.PolicyYAML)
		}
	}
	if strings.Contains(string(c.PolicyYAML), `method: ""`) || strings.Contains(string(c.PolicyYAML), "tool:") {
		t.Errorf("empty rule fields leaked:\n%s", c.PolicyYAML)
	}
	// Writes only under /tmp (and /dev/null); the workdir is not writable.
	if p.FilesystemPolicy.IncludeWorkdir || strings.Join(p.FilesystemPolicy.ReadWrite, ",") != "/dev/null,/tmp" {
		t.Fatalf("filesystem = %+v", p.FilesystemPolicy)
	}
}

func TestCompileRunner_OnlyApprovedSources(t *testing.T) {
	cases := map[string]toolreg.PinTable{
		"no pins":        {},
		"empty pin list": ghPins(),
		// A pin approved from another endpoint is not active.
		"source changed": {"github": {"gh_issue_list": {Hash: "h", Source: "pack:other"}}},
	}
	for name, pins := range cases {
		t.Run(name, func(t *testing.T) {
			c, err := CompileRunner(runnerSnap(ghSources(), pins), clipack.Builtin, noSpecs)
			if err != nil {
				t.Fatal(err)
			}
			if p := decodePolicy(t, c.PolicyYAML); len(p.NetworkPolicies) != 0 {
				t.Fatalf("unapproved source compiled: %v", p.NetworkPolicies)
			}
			if len(c.Providers) != 0 {
				t.Fatalf("providers = %v", c.Providers)
			}
		})
	}
}

func TestCompileRunner_DigestStable(t *testing.T) {
	a, err := CompileRunner(runnerSnap(ghSources(), ghPins("gh_issue_list")), clipack.Builtin, noSpecs)
	if err != nil {
		t.Fatal(err)
	}
	b, err := CompileRunner(runnerSnap(ghSources(), ghPins("gh_issue_list")), clipack.Builtin, noSpecs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a.PolicyDigest, "sha256:") || a.PolicyDigest != b.PolicyDigest {
		t.Fatalf("digests %q %q", a.PolicyDigest, b.PolicyDigest)
	}
	// A first pin on a second source opens its policy: a new digest.
	sources := ghSources()
	sources["gh2"] = toolreg.Source{Type: toolreg.TypeCLI, Pack: "gh"}
	pins := ghPins("gh_issue_list")
	pins["gh2"] = map[string]toolreg.Pin{"gh_pr_list": {Hash: "h", Source: "pack:gh"}}
	c, err := CompileRunner(runnerSnap(sources, pins), clipack.Builtin, noSpecs)
	if err != nil {
		t.Fatal(err)
	}
	if c.PolicyDigest == a.PolicyDigest {
		t.Fatal("a new pin kept the digest")
	}
	// Two sources on one credential: the provider is listed once.
	if strings.Join(c.Providers, ",") != "aimux-github" {
		t.Fatalf("providers = %v", c.Providers)
	}
}

func TestCompileRunner_NoModelOrGateway(t *testing.T) {
	c, err := CompileRunner(runnerSnap(ghSources(), ghPins("gh_issue_list")), clipack.Builtin, noSpecs)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"anthropic", defaultGatewayHost, "192.168.127.254", "/mcp/", "node", SandboxProvider} {
		if strings.Contains(string(c.PolicyYAML), bad) {
			t.Errorf("runner policy has %q:\n%s", bad, c.PolicyYAML)
		}
	}
	for _, p := range c.Providers {
		if p == SandboxProvider {
			t.Fatal("runner gets the model provider")
		}
	}
}

// httpPins pins ops of source at base as the gateway would on approval.
func httpPins(t *testing.T, source, base string, ops ...httpsource.Operation) map[string]toolreg.Pin {
	t.Helper()
	out := map[string]toolreg.Pin{}
	for _, op := range ops {
		raws, err := httpsource.Tools(source, base, []httpsource.Operation{op})
		if err != nil || len(raws) != 1 {
			t.Fatalf("tools: %v", err)
		}
		h, err := toolgateway.PinTool(raws[0])
		if err != nil {
			t.Fatal(err)
		}
		out[httpsource.ToolName(source, op)] = toolreg.Pin{Hash: h, Source: base}
	}
	return out
}

func fixedOps(ops ...httpsource.Operation) func(toolreg.Source) ([]httpsource.Operation, error) {
	return func(toolreg.Source) ([]httpsource.Operation, error) { return ops, nil }
}

var (
	opIssue  = httpsource.Operation{ID: "get_issue", Path: "/issue/{key}", Params: []httpsource.Param{{Name: "key", In: "path", Type: "string", Required: true}}}
	opSearch = httpsource.Operation{ID: "search", Path: "/search"}
	opDup    = httpsource.Operation{ID: "dup", Path: "/search"}
	opAdmin  = httpsource.Operation{ID: "admin", Path: "/admin/{x}", Params: []httpsource.Param{{Name: "x", In: "path", Type: "string", Required: true}}}
)

// An http-openapi source compiles each approved GET operation to a rest
// rule on its base URL's host ({param} segments as *), with curl as the
// binary.
func TestCompileRunner_HTTPOpenAPI(t *testing.T) {
	const base = "https://jira.example.com/rest/api/2"
	sources := map[string]toolreg.Source{"jira": {Type: toolreg.TypeHTTPOpenAPI, Spec: "/s.yaml", BaseURL: base}}
	pins := toolreg.PinTable{"jira": httpPins(t, "jira", base, opIssue, opSearch, opDup)}
	c, err := CompileRunner(runnerSnap(sources, pins), clipack.Builtin, fixedOps(opIssue, opSearch, opDup))
	if err != nil {
		t.Fatal(err)
	}
	np := decodePolicy(t, c.PolicyYAML).NetworkPolicies["tools_jira"]
	if len(np.Binaries) != 1 || np.Binaries[0].Path != "/usr/bin/curl" || len(np.Endpoints) != 1 {
		t.Fatalf("policy = %+v", np)
	}
	e := np.Endpoints[0]
	if e.Host != "jira.example.com" || e.Port != 443 || e.Protocol != "rest" || e.Enforcement != "enforce" || e.Access != "" || len(e.Rules) != 2 {
		t.Fatalf("endpoint = %+v", e)
	}
	var got []string
	for _, r := range e.Rules {
		got = append(got, r.Allow.Method+" "+r.Allow.Path)
	}
	if strings.Join(got, ",") != "GET /rest/api/2/issue/*,GET /rest/api/2/search" {
		t.Fatalf("rules = %v", got)
	}
	if len(c.Providers) != 0 {
		t.Fatalf("providers = %v", c.Providers)
	}

	// No operations: no endpoints, so no network policy.
	c, err = CompileRunner(runnerSnap(sources, pins), clipack.Builtin, noSpecs)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(decodePolicy(t, c.PolicyYAML).NetworkPolicies); n != 0 {
		t.Fatalf("%d policies from no operations", n)
	}
}

// Only operations with an active pin whose definition still matches it
// reach the policy: an unpinned operation, and a pinned one whose path
// template changed since approval, get no rule. Path params are one *
// segment each, never **.
func TestCompileRunner_HTTPPathRules(t *testing.T) {
	const base = "https://api.example.com"
	sources := map[string]toolreg.Source{"api": {Type: toolreg.TypeHTTPOpenAPI, Spec: "/s.yaml", BaseURL: base}}
	moved := opSearch
	moved.Path = "/search/{all}"
	moved.Params = []httpsource.Param{{Name: "all", In: "path", Type: "string", Required: true}}
	pins := toolreg.PinTable{"api": httpPins(t, "api", base, opIssue, opSearch)}
	// The spec now has search at another path, and admin nobody approved.
	c, err := CompileRunner(runnerSnap(sources, pins), clipack.Builtin, fixedOps(opIssue, moved, opAdmin))
	if err != nil {
		t.Fatal(err)
	}
	eps := decodePolicy(t, c.PolicyYAML).NetworkPolicies["tools_api"].Endpoints
	if len(eps) != 1 || len(eps[0].Rules) != 1 {
		t.Fatalf("endpoints = %+v", eps)
	}
	if r := eps[0].Rules[0].Allow; r.Method != "GET" || r.Path != "/issue/*" {
		t.Fatalf("rule = %+v", r)
	}
	if strings.Contains(string(c.PolicyYAML), "**") || strings.Contains(string(c.PolicyYAML), "/admin") {
		t.Fatalf("policy widened:\n%s", c.PolicyYAML)
	}
	// Nothing pinned matches: no policy at all.
	stale := toolreg.PinTable{"api": {"api_get_issue": {Hash: "sha256:old", Source: base}}}
	c, err = CompileRunner(runnerSnap(sources, stale), clipack.Builtin, fixedOps(opIssue))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(decodePolicy(t, c.PolicyYAML).NetworkPolicies); n != 0 {
		t.Fatalf("%d policies from a stale pin", n)
	}
}

// A source whose spec cannot be read is left out with its error (naming
// the source), and the other sources still compile.
func TestCompileRunnerAvailable_SpecErrorStaysOnSource(t *testing.T) {
	const base = "https://api.example.com"
	sources := map[string]toolreg.Source{
		"api":    {Type: toolreg.TypeHTTPOpenAPI, Spec: "/missing.yaml", BaseURL: base},
		"github": {Type: toolreg.TypeCLI, Pack: "gh"},
	}
	pins := ghPins("gh_issue_list")
	pins["api"] = httpPins(t, "api", base, opSearch)
	bad := func(toolreg.Source) ([]httpsource.Operation, error) {
		return nil, errors.New("spec /missing.yaml: open: no such file or directory")
	}
	c, errs, err := CompileRunnerAvailable(runnerSnap(sources, pins), clipack.Builtin, bad, nil)
	if err != nil {
		t.Fatal(err)
	}
	if e := errs["api"]; e == nil || !strings.Contains(e.Error(), "source api") || !strings.Contains(e.Error(), "/missing.yaml") || len(errs) != 1 {
		t.Fatalf("errs = %v", errs)
	}
	np := decodePolicy(t, c.PolicyYAML).NetworkPolicies
	if _, ok := np["tools_api"]; ok {
		t.Fatal("unreadable source has a policy")
	}
	if _, ok := np["tools_github"]; !ok {
		t.Fatal("the other source was dropped too")
	}
}

// http-openapi sources do not send credentials yet: one that names a
// provider is left out (no policy, no provider) with an error saying so.
func TestCompileRunnerAvailable_HTTPCredentialRefused(t *testing.T) {
	const base = "https://jira.example.com"
	sources := map[string]toolreg.Source{"jira": {Type: toolreg.TypeHTTPOpenAPI, Spec: "/s.yaml", BaseURL: base, Credential: "aimux-jira"}} // #nosec G101 -- a provider name, not a credential
	pins := toolreg.PinTable{"jira": httpPins(t, "jira", base, opIssue)}
	read := false
	specs := func(toolreg.Source) ([]httpsource.Operation, error) {
		read = true
		return []httpsource.Operation{opIssue}, nil
	}
	c, errs, err := CompileRunnerAvailable(runnerSnap(sources, pins), clipack.Builtin, specs, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if e := errs["jira"]; e == nil || !strings.Contains(e.Error(), "credential") || !strings.Contains(e.Error(), "source jira") {
		t.Fatalf("errs = %v", errs)
	}
	if len(decodePolicy(t, c.PolicyYAML).NetworkPolicies) != 0 || len(c.Providers) != 0 || read {
		t.Fatalf("credentialed source compiled: providers %v, spec read %v\n%s", c.Providers, read, c.PolicyYAML)
	}
}

func TestCompileRunner_Errors(t *testing.T) {
	snap := runnerSnap(ghSources(), ghPins("gh_issue_list"))
	noPack := func(string) (clipack.Pack, error) { return clipack.Pack{}, errors.New("no pack") }
	if _, err := CompileRunner(snap, noPack, noSpecs); err == nil || !strings.Contains(err.Error(), "github") {
		t.Fatalf("pack error = %v", err)
	}
	// A path that is not an absolute template is refused, not passed
	// through; literal segments are checked here too, not only by the
	// parser. The source is left out, the others still compile.
	for _, path := range []string{"search", "/a%2F..%2Fadmin", "/files/[a-z]", `/a\..\b`, "/a//b", "/sp ace"} {
		op := httpsource.Operation{ID: "x", Path: path}
		sources := ghSources()
		sources["jira"] = toolreg.Source{Type: toolreg.TypeHTTPOpenAPI, Spec: "/s.yaml", BaseURL: "https://jira.example.com"}
		pins := ghPins("gh_issue_list")
		pins["jira"] = httpPins(t, "jira", "https://jira.example.com", op)
		_ = requireDropped(t, runnerSnap(sources, pins), fixedOps(op), "jira")
	}
}

// requireDropped compiles snap and checks that source is left out with an
// error naming it while the github cli source (when approved) still
// compiles: one bad source never takes the runner down.
func requireDropped(t *testing.T, snap toolreg.Snapshot, specs func(toolreg.Source) ([]httpsource.Operation, error), source string) error {
	t.Helper()
	c, errs, err := CompileRunnerAvailable(snap, clipack.Builtin, specs, nil)
	if err != nil {
		t.Fatalf("compile failed for every source: %v", err)
	}
	e := errs[source]
	if e == nil || !strings.Contains(e.Error(), "source "+source) {
		t.Fatalf("errs = %v, want %s left out", errs, source)
	}
	np := decodePolicy(t, c.PolicyYAML).NetworkPolicies
	if _, ok := np["tools_"+source]; ok {
		t.Fatalf("%s has a policy:\n%s", source, c.PolicyYAML)
	}
	if _, ok := np["tools_github"]; !ok {
		t.Fatalf("the github source was dropped too:\n%s", c.PolicyYAML)
	}
	return e
}

// An http-openapi base URL host must be a plain public hostname: no IP
// literals, localhost, .internal or .local names (host.containers.internal
// is the tool gateway) and no model host.
func TestCompileRunner_HTTPHostRefused(t *testing.T) {
	op := httpsource.Operation{ID: "get", Path: "/x"}
	for _, base := range []string{
		"https://127.0.0.1/api", "https://[::1]/", "https://10.0.0.5", "https://2130706433/", "https://localhost:8443",
		"https://a.localhost", "https://host.containers.internal:7390", "https://metadata.google.internal",
		"https://printer.local", "https://api.anthropic.com", "https://API.Anthropic.com/v1", "https://anthropic.com",
	} {
		t.Run(base, func(t *testing.T) {
			sources := ghSources()
			sources["api"] = toolreg.Source{Type: toolreg.TypeHTTPOpenAPI, Spec: "/s.yaml", BaseURL: base}
			pins := ghPins("gh_issue_list")
			pins["api"] = httpPins(t, "api", base, op)
			_ = requireDropped(t, runnerSnap(sources, pins), fixedOps(op), "api")
		})
	}
	// A bad port is left out the same way.
	for _, base := range []string{"https://api.example.com:0", "https://api.example.com:70000"} {
		sources := ghSources()
		sources["api"] = toolreg.Source{Type: toolreg.TypeHTTPOpenAPI, Spec: "/s.yaml", BaseURL: base}
		pins := ghPins("gh_issue_list")
		pins["api"] = httpPins(t, "api", base, op)
		_ = requireDropped(t, runnerSnap(sources, pins), fixedOps(op), "api")
	}
	// A plain public host still compiles.
	const base = "https://api.example.com"
	sources := map[string]toolreg.Source{"api": {Type: toolreg.TypeHTTPOpenAPI, Spec: "/s.yaml", BaseURL: base}}
	if _, err := CompileRunner(runnerSnap(sources, toolreg.PinTable{"api": httpPins(t, "api", base, op)}), clipack.Builtin, fixedOps(op)); err != nil {
		t.Fatal(err)
	}
}

// A host a credentialed builtin pack reaches (api.github.com for gh) is
// bound to that pack's provider: an http-openapi source on it is left out
// with a reason, and the gh source keeps its policy and provider.
func TestCompileRunnerAvailable_HTTPOnCredentialedPackHost(t *testing.T) {
	op := httpsource.Operation{ID: "get", Path: "/x"}
	for _, base := range []string{"https://api.github.com", "https://API.GitHub.com/v3"} {
		sources := ghSources()
		sources["api"] = toolreg.Source{Type: toolreg.TypeHTTPOpenAPI, Spec: "/s.yaml", BaseURL: base}
		pins := ghPins("gh_issue_list")
		pins["api"] = httpPins(t, "api", base, op)
		e := requireDropped(t, runnerSnap(sources, pins), fixedOps(op), "api")
		if !strings.Contains(e.Error(), "gh") || strings.Contains(e.Error(), base) {
			t.Fatalf("%s: reason = %v", base, e)
		}
	}
}

// Profile sandboxes list their one provider in Providers too.
func TestCompile_ProvidersHasSandboxProvider(t *testing.T) {
	c, err := Compile(workReadonly(t), testSets(), testGW)
	if err != nil {
		t.Fatal(err)
	}
	if c.Provider != SandboxProvider || strings.Join(c.Providers, ",") != SandboxProvider {
		t.Fatalf("provider %q providers %v", c.Provider, c.Providers)
	}
}

// Providers attach outside the policy, so a provider change alone must
// still change the recorded digest (Ensure updates the sandbox on it).
func TestCompileRunner_DigestCoversProviders(t *testing.T) {
	compile := func(provider string) Compiled {
		packs := func(name string) (clipack.Pack, error) {
			p, err := clipack.Builtin(name)
			p.Credential = &clipack.Cred{Provider: provider, Keychain: "item"}
			return p, err
		}
		c, err := CompileRunner(runnerSnap(ghSources(), ghPins("gh_issue_list")), packs, noSpecs)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	a, b := compile("aimux-github"), compile("aimux-github2")
	if string(a.PolicyYAML) != string(b.PolicyYAML) {
		t.Fatalf("policies differ; the test needs equal policies:\n%s\n%s", a.PolicyYAML, b.PolicyYAML)
	}
	if a.PolicyDigest == b.PolicyDigest {
		t.Fatal("a provider change kept the digest")
	}
	if c := compile("aimux-github"); c.PolicyDigest != a.PolicyDigest {
		t.Fatal("same providers, different digest")
	}
}

func TestCompileRunnerAvailable_DropsSourceWithoutProvider(t *testing.T) {
	const base = "https://jira.example.com"
	sources := map[string]toolreg.Source{
		"github": {Type: toolreg.TypeCLI, Pack: "gh"},
		"jira":   {Type: toolreg.TypeHTTPOpenAPI, Spec: "/s.yaml", BaseURL: base},
	}
	pins := ghPins("gh_issue_list")
	pins["jira"] = httpPins(t, "jira", base, opIssue)
	snap := runnerSnap(sources, pins)
	specs := fixedOps(opIssue)

	have := func(string) bool { return false }
	c, errs, err := CompileRunnerAvailable(snap, clipack.Builtin, specs, have)
	if err != nil {
		t.Fatal(err)
	}
	np := decodePolicy(t, c.PolicyYAML).NetworkPolicies
	if _, ok := np["tools_github"]; ok {
		t.Fatal("source without its provider still has a policy")
	}
	if _, ok := np["tools_jira"]; !ok {
		t.Fatal("the other source was dropped too")
	}
	if len(c.Providers) != 0 {
		t.Fatalf("providers = %v", c.Providers)
	}
	gh := errs["github"]
	if gh == nil || len(errs) != 1 {
		t.Fatalf("errs = %v", errs)
	}
	for _, want := range []string{"aimux-github", "aimux sandbox setup", "Keychain item aimux-github"} {
		if !strings.Contains(gh.Error(), want) {
			t.Errorf("error %q lacks %q", gh, want)
		}
	}

	// Every provider present: the same compile as CompileRunner.
	all, errs, err := CompileRunnerAvailable(snap, clipack.Builtin, specs, func(string) bool { return true })
	if err != nil || len(errs) != 0 {
		t.Fatalf("%v %v", err, errs)
	}
	plain, err := CompileRunner(snap, clipack.Builtin, specs)
	if err != nil {
		t.Fatal(err)
	}
	if all.PolicyDigest != plain.PolicyDigest || c.PolicyDigest == plain.PolicyDigest {
		t.Fatalf("digests: all %s plain %s dropped %s", all.PolicyDigest, plain.PolicyDigest, c.PolicyDigest)
	}

	// An unapproved source needs no provider: nothing to report.
	_, errs, err = CompileRunnerAvailable(runnerSnap(ghSources(), nil), clipack.Builtin, noSpecs, func(string) bool { return false })
	if err != nil || len(errs) != 0 {
		t.Fatalf("unapproved: %v %v", err, errs)
	}
}

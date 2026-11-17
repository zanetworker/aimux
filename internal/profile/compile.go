package profile

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	binenc "encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/zanetworker/aimux/internal/harnessconfig"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
	"gopkg.in/yaml.v3"
)

var (
	ErrNotSandboxed = errors.New("profile does not run in a sandbox")
	ErrNotAvailable = errors.New("profile is not available yet")
	ErrUnsupported  = errors.New("not supported by the compiler yet")
	ErrUnknownSet   = errors.New("unknown tool set")
	// ErrUnavailable is a well-formed set that cannot be granted now: not
	// in tool-sources.yaml (or dropped there as malformed), or with no
	// pinned tools. Compile omits such sets; it is still an ErrUnknownSet
	// for callers that resolve one.
	ErrUnavailable = fmt.Errorf("%w: unavailable", ErrUnknownSet)
	// ErrUnpinned is an available set whose tools are none of them pinned
	// (its backend was unavailable at aimux browser setup).
	ErrUnpinned = fmt.Errorf("%w: no pinned tools", ErrUnavailable)
	// ErrNameTooLong is a profile whose sandbox name exceeds OpenShell's limit.
	ErrNameTooLong = fmt.Errorf("%w: sandbox name too long", ErrUnsupported)
)

const (
	SandboxImage    = "localhost/wingman-sandbox:2.1.319"
	SandboxProvider = "wingman-claude"
	// maxSandboxName is OpenShell's limit (MAX_ROUTABLE_NAME_LEN in
	// crates/openshell-server/src/grpc/mod.rs).
	maxSandboxName = 19
)

// ToolSets resolves a reviewed tool set ("jira:readonly") to its server and tools.
type ToolSets func(set string) (server string, tools []string, err error)

// GatewayAddr is where the sandbox reaches the aimux tool gateway.
type GatewayAddr struct {
	Host       string
	Port       int
	AllowedIPs []string
}

// Compiled is everything needed to create and run the sandbox for a profile.
type Compiled struct {
	Profile, Digest, Sandbox string
	// PolicyDigest is sha256 over PolicyYAML: it keys the sandbox's policy
	// (ensure cache, .digest file) and changes with the pins; Digest is the
	// manifest digest sessions and audit record.
	PolicyDigest    string
	Image, Provider string
	// Providers are the OpenShell providers attached at sandbox create:
	// [Provider] for a profile sandbox, the sources' credentials for the
	// tool runner.
	Providers  []string
	Harness    string // Manifest.Harness: the adapter the engines launch
	PolicyYAML []byte
	// Grant is the MCP tools the policy allows, the gateway token scopes
	// and the harness lists (through Capabilities).
	Grant       ToolGrant
	Web         bool
	Files       harnessconfig.FileGrant
	InheritUser bool
	Model       string
	// Snapshot is the registry and pins CompileAt compiled against (zero
	// from Compile); the turn's gateway token is issued under it.
	Snapshot toolreg.Snapshot
}

// Capabilities is what the harness adapter gets: Grant as its tool grant,
// plus web, files, inherit user and model. Its file lists are copies.
func (c Compiled) Capabilities() harnessconfig.Capabilities {
	return harnessconfig.Capabilities{
		Grant:       c.Grant,
		Web:         c.Web,
		Files:       harnessconfig.FileGrant{Read: slices.Clone(c.Files.Read), Write: slices.Clone(c.Files.Write)},
		InheritUser: c.InheritUser,
		Model:       c.Model,
	}
}

// RegistrySets resolves a profile's set through reg (wiki:readonly falls
// back to notes:readonly, see toolreg.Registry.Resolve) to its source,
// which is the server name, and the tools the set lists that are also
// pinned for that server, sorted. A name that is not <name>:<kind> is
// ErrUnknownSet; a set reg lacks or has none pinned is ErrUnavailable.
func RegistrySets(reg toolreg.Registry, p toolgateway.Pins) ToolSets {
	return func(name string) (string, []string, error) {
		parts := strings.Split(name, ":")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", nil, fmt.Errorf("%w: %q", ErrUnknownSet, name)
		}
		set, err := reg.Resolve(name)
		if err != nil {
			return "", nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		var tools []string
		for _, t := range set.Tools {
			if _, ok := p[set.Source][t]; ok {
				tools = append(tools, t)
			}
		}
		if len(tools) == 0 {
			return "", nil, fmt.Errorf("%w: %q", ErrUnpinned, name)
		}
		sort.Strings(tools)
		return set.Source, tools, nil
	}
}

// CompileAt compiles m against snap (its registry, and its pins less the
// source_changed ones, which the gateway does not serve) and records snap
// in the result.
func CompileAt(m Manifest, snap toolreg.Snapshot, gw GatewayAddr) (Compiled, error) {
	c, err := Compile(m, RegistrySets(snap.Registry(), toolgateway.ServedPins(snap)), gw)
	if err != nil {
		return Compiled{}, err
	}
	c.Snapshot = snap
	return c, nil
}

// CheckSandboxName fails a profile that runs in a sandbox ("sandbox" or
// "run") whose sandbox name, "aimux-" + name, is longer than OpenShell
// allows; profiles that run elsewhere pass.
func CheckSandboxName(m Manifest) error {
	if m.Runs != "sandbox" && m.Runs != "run" {
		return nil
	}
	sandbox := "aimux-" + m.Name
	if len(sandbox) > maxSandboxName {
		return fmt.Errorf("%w: %q is %d characters, OpenShell allows %d", ErrNameTooLong, sandbox, len(sandbox), maxSandboxName)
	}
	return nil
}

func Compile(m Manifest, sets ToolSets, gw GatewayAddr) (Compiled, error) {
	if m.Runs != "sandbox" && m.Runs != "run" {
		return Compiled{}, fmt.Errorf("%w: %s runs %q", ErrNotSandboxed, m.Name, m.Runs)
	}
	if !m.Available {
		return Compiled{}, fmt.Errorf("%w: %s", ErrNotAvailable, m.Name)
	}
	if !slices.Contains(m.Network.Allow, "model") {
		return Compiled{}, fmt.Errorf("%w: the sandbox provider grants model egress, so network.allow must include model", ErrUnsupported)
	}
	if err := CheckSandboxName(m); err != nil {
		return Compiled{}, err
	}
	sandbox := "aimux-" + m.Name
	web := slices.Contains(m.Network.Allow, "web")
	servers := map[string][]string{}
	var unpinned []string
	for _, set := range m.Tools {
		server, tools, err := sets(set)
		if errors.Is(err, ErrUnavailable) {
			unpinned = append(unpinned, set) // omitted: no endpoint, no tools, no token entry
			continue
		}
		if err != nil {
			return Compiled{}, err
		}
		if len(tools) == 0 {
			return Compiled{}, fmt.Errorf("%w: %q yields no tools", ErrUnknownSet, set)
		}
		for _, n := range append([]string{server}, tools...) {
			if !safeName.MatchString(n) {
				return Compiled{}, fmt.Errorf("%w: name %q must match [A-Za-z0-9_-]+", ErrUnsupported, n)
			}
		}
		servers[server] = mergeSorted(servers[server], tools)
	}
	if len(m.Tools) > 0 && len(servers) == 0 {
		return Compiled{}, fmt.Errorf("%w: %s requests %s and none is configured in tool-sources.yaml with pinned tools; run aimux tools status, then aimux tools approve", ErrUnpinned, m.Name, strings.Join(unpinned, ", "))
	}
	if len(servers) > 0 && !slices.Contains(m.Network.Allow, "gateway") {
		return Compiled{}, fmt.Errorf("%w: tools need network.allow gateway", ErrUnsupported)
	}
	grant := NewToolGrant(servers)
	policy, err := marshalPolicy(buildPolicy(m, grant, gw, web))
	if err != nil {
		return Compiled{}, err
	}
	sum := sha256.Sum256(policy)
	return Compiled{
		PolicyYAML:   policy,
		PolicyDigest: "sha256:" + hex.EncodeToString(sum[:]),
		Profile:      m.Name,
		Digest:       m.Digest(),
		Sandbox:      sandbox,
		Image:        SandboxImage,
		Provider:     SandboxProvider,
		Providers:    []string{SandboxProvider},
		Harness:      m.Harness,
		Grant:        grant,
		Web:          web,
		Files:        harnessconfig.FileGrant{Read: slices.Clone(m.Files.Read), Write: slices.Clone(m.Files.Write)},
		InheritUser:  m.Settings.InheritUser,
		Model:        m.Model.Name,
	}, nil
}

// mergeSorted returns the sorted union of a and b.
func mergeSorted(a, b []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, t := range append(append([]string{}, a...), b...) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// Defaults for the aimux tool gateway as seen from inside the sandbox
// (podman machine host alias, verified on OpenShell 0.1.7).
const (
	defaultGatewayHost = "host.containers.internal"
	defaultGatewayPort = 7390
	defaultGatewayIP   = "192.168.127.254/32"
)

var safeName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

var mcpHandshakeMethods = []string{"initialize", "notifications/initialized", "tools/list", "ping"}

var sandboxBinaries = []string{"/usr/bin/node", "/usr/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe"}

type policyFile struct {
	Version          int                      `yaml:"version"`
	FilesystemPolicy filesystemPolicy         `yaml:"filesystem_policy"`
	Landlock         landlockPolicy           `yaml:"landlock"`
	NetworkPolicies  map[string]networkPolicy `yaml:"network_policies,omitempty"`
}

type filesystemPolicy struct {
	IncludeWorkdir bool     `yaml:"include_workdir"`
	ReadOnly       []string `yaml:"read_only"`
	ReadWrite      []string `yaml:"read_write"`
}

type landlockPolicy struct {
	Compatibility string `yaml:"compatibility"`
}

type networkPolicy struct {
	Name      string     `yaml:"name"`
	Endpoints []endpoint `yaml:"endpoints"`
	Binaries  []binary   `yaml:"binaries"`
}

type binary struct {
	Path string `yaml:"path"`
}

type endpoint struct {
	Host        string   `yaml:"host"`
	Port        int      `yaml:"port"`
	Path        string   `yaml:"path,omitempty"`
	Protocol    string   `yaml:"protocol"`
	Enforcement string   `yaml:"enforcement"`
	Access      string   `yaml:"access,omitempty"`
	MCP         *mcpOpts `yaml:"mcp,omitempty"`
	AllowedIPs  []string `yaml:"allowed_ips,omitempty"`
	Rules       []rule   `yaml:"rules,omitempty"`
}

type mcpOpts struct {
	Versions []string `yaml:"versions,flow"`
}

// rule is one allow rule: an MCP method (and tools), a REST method and
// path, or a GraphQL operation type. OpenShell rejects rules together with
// access on one endpoint.
type rule struct {
	Allow ruleAllow `yaml:"allow"`
}

type ruleAllow struct {
	Method        string   `yaml:"method,omitempty"`
	Tool          *toolAny `yaml:"tool,omitempty"`
	Path          string   `yaml:"path,omitempty"`
	OperationType string   `yaml:"operation_type,omitempty"`
}

type toolAny struct {
	Any []string `yaml:"any,flow"`
}

func buildPolicy(m Manifest, grant ToolGrant, gw GatewayAddr, web bool) policyFile {
	if gw.Host == "" {
		gw.Host = defaultGatewayHost
	}
	if gw.Port == 0 {
		gw.Port = defaultGatewayPort
	}
	if len(gw.AllowedIPs) == 0 {
		gw.AllowedIPs = []string{defaultGatewayIP}
	}
	p := policyFile{
		Version: 1,
		FilesystemPolicy: filesystemPolicy{
			IncludeWorkdir: true,
			ReadOnly:       mergeSorted([]string{"/bin", "/usr", "/lib", "/proc", "/dev/urandom", "/etc", "/var/log"}, m.Files.Read),
			ReadWrite:      mergeSorted([]string{"/sandbox", "/tmp", "/dev/null"}, m.Files.Write),
		},
		Landlock: landlockPolicy{Compatibility: "best_effort"},
	}
	var eps []endpoint
	for _, s := range grant.Servers() {
		rules := make([]rule, 0, len(mcpHandshakeMethods)+1)
		for _, method := range mcpHandshakeMethods {
			rules = append(rules, rule{Allow: ruleAllow{Method: method}})
		}
		rules = append(rules, rule{Allow: ruleAllow{Method: "tools/call", Tool: &toolAny{Any: grant.Tools(s)}}})
		eps = append(eps, endpoint{
			Host: gw.Host, Port: gw.Port, Path: "/mcp/" + s,
			Protocol: "mcp", Enforcement: "enforce",
			MCP:        &mcpOpts{Versions: []string{"2025-06-18", "2025-11-25"}},
			AllowedIPs: gw.AllowedIPs, Rules: rules,
		})
	}
	var bins []binary
	for _, b := range sandboxBinaries {
		bins = append(bins, binary{Path: b})
	}
	if len(eps) > 0 || web {
		p.NetworkPolicies = map[string]networkPolicy{}
	}
	if len(eps) > 0 {
		p.NetworkPolicies["aimux_tool_gateway"] = networkPolicy{Name: "aimux_tool_gateway", Endpoints: eps, Binaries: bins}
	}
	if web {
		p.NetworkPolicies["public_web_readonly"] = networkPolicy{Name: "public_web_readonly", Endpoints: webEndpoints(), Binaries: bins}
	}
	return p
}

// webEndpoints is the read-only public web: one "**.*.<tld>" wildcard per
// reviewed TLD with no allowed_ips (wildcards never reach private addresses
// then, and do not conflict with the provider's api.anthropic.com:443), plus
// one exact endpoint per reviewed apex host, restricted to public addresses
// because exact hosts may otherwise reach the private addresses they resolve to.
func webEndpoints() []endpoint {
	readOnly := func(host string, ips []string) endpoint {
		return endpoint{Host: host, Port: 443, Protocol: "rest", Enforcement: "enforce", Access: "read-only", AllowedIPs: ips}
	}
	var public []string
	for _, p := range publicIPv4() {
		public = append(public, p.String())
	}
	public = append(public, publicIPv6)
	var eps []endpoint
	for _, tld := range WebTLDs() {
		eps = append(eps, readOnly("**.*."+tld, nil))
	}
	for _, host := range WebApexHosts() {
		eps = append(eps, readOnly(host, public))
	}
	return eps
}

func marshalPolicy(p policyFile) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(p); err != nil {
		return nil, fmt.Errorf("marshal policy: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("marshal policy: %w", err)
	}
	return buf.Bytes(), nil
}

// internalIPv4 is every IPv4 range an apex endpoint must not reach: the
// ranges OpenShell treats as internal (crates/openshell-core/src/net.rs
// is_internal_net: loopback, link-local, RFC 1918, CGNAT, special-use,
// documentation, benchmarking) plus "this network", multicast and reserved
// (which holds the limited broadcast address). The entries do not overlap.
var internalIPv4 = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

// publicIPv6 is global unicast; it excludes loopback, link-local, ULA and
// IPv4-mapped addresses. It still includes 6to4 (2002::/16), Teredo
// (2001::/32) and documentation (2001:db8::/32), which OpenShell does not
// block either; reaching them needs a relay.
const publicIPv6 = "2000::/3"

// publicIPv4 is the smallest sorted set of CIDRs covering every IPv4 address
// outside internalIPv4.
func publicIPv4() []netip.Prefix {
	var out []netip.Prefix
	var walk func(p netip.Prefix)
	walk = func(p netip.Prefix) {
		overlaps := false
		for _, in := range internalIPv4 {
			if in.Bits() <= p.Bits() && in.Contains(p.Addr()) {
				return // p lies inside an internal range
			}
			overlaps = overlaps || p.Overlaps(in)
		}
		if !overlaps {
			out = append(out, p)
			return
		}
		lo := netip.PrefixFrom(p.Addr(), p.Bits()+1)
		hi := netip.PrefixFrom(lastAddr(lo).Next(), p.Bits()+1)
		walk(lo)
		walk(hi)
	}
	walk(netip.MustParsePrefix("0.0.0.0/0"))
	return out
}

// lastAddr is the highest address in p (an IPv4 prefix).
func lastAddr(p netip.Prefix) netip.Addr {
	a := p.Addr().As4()
	n := binenc.BigEndian.Uint32(a[:]) | (uint32(1)<<(32-p.Bits()) - 1)
	binenc.BigEndian.PutUint32(a[:], n)
	return netip.AddrFrom4(a)
}

//go:embed builtin/web-tlds.txt
var webTLDsFile string

//go:embed builtin/web-apex.txt
var webApexFile string

var (
	tldRE  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	apexRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	// webTLDs and webApex are package vars so tests can swap the lists.
	webTLDs = mustParseList("web-tlds.txt", webTLDsFile, tldRE)
	webApex = mustParseList("web-apex.txt", webApexFile, apexRE)
)

// WebTLDs is the reviewed TLD list the web policy allows, sorted.
func WebTLDs() []string { return slices.Sorted(slices.Values(webTLDs)) }

// WebApexHosts is the reviewed list of exact apex hosts the web policy allows, sorted.
func WebApexHosts() []string { return slices.Sorted(slices.Values(webApex)) }

// parseList reads one entry per line, skipping blank lines and # comments.
// Every entry must match re and appear once; an empty list is an error.
func parseList(data string, re *regexp.Regexp) ([]string, error) {
	var out []string
	for i, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !re.MatchString(line) {
			return nil, fmt.Errorf("line %d: %q is not a valid entry", i+1, line)
		}
		if slices.Contains(out, line) {
			return nil, fmt.Errorf("line %d: %q is listed twice", i+1, line)
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return nil, errors.New("no entries")
	}
	sort.Strings(out)
	return out, nil
}

func mustParseList(name, data string, re *regexp.Regexp) []string {
	out, err := parseList(data, re)
	if err != nil {
		panic(fmt.Sprintf("profile: embedded builtin/%s invalid: %v", name, err)) // embedded at build time; the tests cover it
	}
	return out
}

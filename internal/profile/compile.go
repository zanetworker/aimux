package profile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/zanetworker/aimux/internal/harnessconfig"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"gopkg.in/yaml.v3"
)

var (
	ErrNotSandboxed = errors.New("profile does not run in a sandbox")
	ErrNotAvailable = errors.New("profile is not available yet")
	ErrUnsupported  = errors.New("not supported by the compiler yet")
	ErrUnknownSet   = errors.New("unknown tool set")
	// ErrUnpinned is a well-formed set whose server has no pinned tools
	// (its backend was unavailable at aimux browser setup). Compile omits
	// such sets; it is still an ErrUnknownSet for callers that resolve one.
	ErrUnpinned = fmt.Errorf("%w: no pinned tools", ErrUnknownSet)
)

const (
	SandboxImage    = "localhost/wingman-sandbox:2.1.319"
	SandboxProvider = "wingman-claude"
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
	Harness         string // Manifest.Harness: the adapter the engines launch
	PolicyYAML      []byte
	Servers         map[string][]string
	Intent          harnessconfig.Intent
}

// PinnedSets resolves "<server>:readonly" to the pinned tools of that server.
func PinnedSets(p toolgateway.Pins) ToolSets {
	return func(set string) (string, []string, error) {
		server, kind, ok := strings.Cut(set, ":")
		if !ok || kind != "readonly" || server == "" {
			return "", nil, fmt.Errorf("%w: %q", ErrUnknownSet, set)
		}
		if len(p[server]) == 0 {
			return "", nil, fmt.Errorf("%w: %q", ErrUnpinned, set)
		}
		tools := make([]string, 0, len(p[server]))
		for t := range p[server] {
			tools = append(tools, t)
		}
		sort.Strings(tools)
		return server, tools, nil
	}
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
	if slices.Contains(m.Network.Allow, "web") {
		return Compiled{}, fmt.Errorf("%w: network.allow web (no reviewed host list to enforce)", ErrUnsupported)
	}
	servers := map[string][]string{}
	var unpinned []string
	for _, set := range m.Tools {
		server, tools, err := sets(set)
		if errors.Is(err, ErrUnpinned) {
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
		return Compiled{}, fmt.Errorf("%w: %s requests %s and none has pinned tools; run aimux browser setup", ErrUnpinned, m.Name, strings.Join(unpinned, ", "))
	}
	if len(servers) > 0 && !slices.Contains(m.Network.Allow, "gateway") {
		return Compiled{}, fmt.Errorf("%w: tools need network.allow gateway", ErrUnsupported)
	}
	policy, err := marshalPolicy(buildPolicy(m, servers, gw))
	if err != nil {
		return Compiled{}, err
	}
	sum := sha256.Sum256(policy)
	return Compiled{
		PolicyYAML:   policy,
		PolicyDigest: "sha256:" + hex.EncodeToString(sum[:]),
		Profile:      m.Name,
		Digest:       m.Digest(),
		Sandbox:      "aimux-" + m.Name,
		Image:        SandboxImage,
		Provider:     SandboxProvider,
		Harness:      m.Harness,
		Servers:      servers,
		Intent: harnessconfig.Intent{
			Mode:        "print",
			Model:       m.Model.Name,
			Tools:       servers,
			Files:       len(m.Files.Read)+len(m.Files.Write) > 0,
			InheritUser: m.Settings.InheritUser,
		},
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
	Host        string    `yaml:"host"`
	Port        int       `yaml:"port"`
	Path        string    `yaml:"path"`
	Protocol    string    `yaml:"protocol"`
	Enforcement string    `yaml:"enforcement"`
	MCP         mcpOpts   `yaml:"mcp"`
	AllowedIPs  []string  `yaml:"allowed_ips"`
	Rules       []mcpRule `yaml:"rules"`
}

type mcpOpts struct {
	Versions []string `yaml:"versions,flow"`
}

type mcpRule struct {
	Allow mcpAllow `yaml:"allow"`
}

type mcpAllow struct {
	Method string   `yaml:"method"`
	Tool   *toolAny `yaml:"tool,omitempty"`
}

type toolAny struct {
	Any []string `yaml:"any,flow"`
}

func buildPolicy(m Manifest, servers map[string][]string, gw GatewayAddr) policyFile {
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
	names := make([]string, 0, len(servers))
	for s := range servers {
		names = append(names, s)
	}
	sort.Strings(names)
	var eps []endpoint
	for _, s := range names {
		rules := make([]mcpRule, 0, len(mcpHandshakeMethods)+1)
		for _, method := range mcpHandshakeMethods {
			rules = append(rules, mcpRule{Allow: mcpAllow{Method: method}})
		}
		rules = append(rules, mcpRule{Allow: mcpAllow{Method: "tools/call", Tool: &toolAny{Any: servers[s]}}})
		eps = append(eps, endpoint{
			Host: gw.Host, Port: gw.Port, Path: "/mcp/" + s,
			Protocol: "mcp", Enforcement: "enforce",
			MCP:        mcpOpts{Versions: []string{"2025-06-18", "2025-11-25"}},
			AllowedIPs: gw.AllowedIPs, Rules: rules,
		})
	}
	if len(eps) > 0 {
		var bins []binary
		for _, b := range sandboxBinaries {
			bins = append(bins, binary{Path: b})
		}
		p.NetworkPolicies = map[string]networkPolicy{
			"aimux_tool_gateway": {Name: "aimux_tool_gateway", Endpoints: eps, Binaries: bins},
		}
	}
	return p
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

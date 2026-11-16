package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/zanetworker/aimux/internal/clipack"
	"github.com/zanetworker/aimux/internal/httpsource"
	"github.com/zanetworker/aimux/internal/toolgateway"
	"github.com/zanetworker/aimux/internal/toolreg"
)

const (
	// RunnerSandbox is the one sandbox cli and http-openapi tools run in.
	RunnerSandbox = "aimux-tools"
	// RunnerImage is SandboxImage plus gh 2.103.3 (sandbox/aimux-tools/Containerfile).
	RunnerImage = "localhost/aimux-tools:2.103.3"
	// curlBinary makes an http-openapi source's requests.
	curlBinary = "/usr/bin/curl"
)

// CompileRunner compiles the aimux-tools sandbox policy from snap: one
// network policy per approved cli or http-openapi source (one with at least
// one active pin, as the gateway serves them), with the pack's endpoints and
// binary for a cli source, and for an http-openapi source a GET rule on the
// base URL's host per operation whose tool has an active pin matching its
// current definition, with curl. Providers are the packs' credential
// providers, sorted, once each, for sources that compiled to at least one
// endpoint. Nothing is writable beyond /tmp. packs loads a pack by name and
// specs lists a source's operations; a pack error fails the compile, while
// an http-openapi source whose spec fails or that names a credential is
// left out (CompileRunnerAvailable says why).
// PolicyDigest covers the policy and the sorted providers, since providers
// attach outside the policy.
func CompileRunner(snap toolreg.Snapshot, packs func(string) (clipack.Pack, error), specs func(toolreg.Source) ([]httpsource.Operation, error)) (Compiled, error) {
	c, _, err := CompileRunnerAvailable(snap, packs, specs, nil)
	return c, err
}

// CompileRunnerAvailable is CompileRunner for the providers the gateway
// has: a source whose provider have reports missing is left out (no policy,
// no provider) and its error, which says how to create the provider, is
// returned under its name, so the other sources still run. An http-openapi
// source whose spec fails, or that names a credential, is left out the
// same way. A nil have leaves no source out for a missing provider.
func CompileRunnerAvailable(snap toolreg.Snapshot, packs func(string) (clipack.Pack, error), specs func(toolreg.Source) ([]httpsource.Operation, error), have func(provider string) bool) (Compiled, map[string]error, error) {
	reg := snap.Registry()
	dropped := map[string]error{}
	served := toolgateway.ServedPins(snap)
	var providers []string
	addProvider := func(p string) {
		if p != "" && !slices.Contains(providers, p) {
			providers = append(providers, p)
		}
	}
	policies := map[string]networkPolicy{}
	for _, name := range slices.Sorted(maps.Keys(reg.Sources)) {
		src := reg.Sources[name]
		if len(served[name]) == 0 {
			continue // not approved: no endpoint, no provider
		}
		var eps []endpoint
		var bin, provider, item string
		switch src.Type {
		case toolreg.TypeCLI:
			pack, err := packs(src.Pack)
			if err != nil {
				return Compiled{}, nil, fmt.Errorf("source %s: %w", name, err)
			}
			for _, e := range pack.Policy {
				eps = append(eps, packEndpoint(e))
			}
			bin = pack.Binary
			if pack.Credential != nil {
				provider, item = pack.Credential.Provider, pack.Credential.Keychain
			}
		case toolreg.TypeHTTPOpenAPI:
			if src.Credential != "" {
				dropped[name] = fmt.Errorf("source %s: %w", name, httpsource.ErrCredential)
				continue
			}
			ops, err := specs(src)
			if err != nil {
				dropped[name] = fmt.Errorf("source %s: %w", name, err)
				continue
			}
			ops, err = pinnedOperations(name, src.BaseURL, ops, served[name])
			if err != nil {
				return Compiled{}, nil, fmt.Errorf("source %s: %w", name, err)
			}
			e, err := httpEndpoint(src.BaseURL, ops)
			if err != nil {
				return Compiled{}, nil, fmt.Errorf("source %s: %w", name, err)
			}
			if e != nil {
				eps = append(eps, *e)
			}
			bin = curlBinary
		default:
			continue // mcp-http and local-notes run in the gateway, not here
		}
		if len(eps) == 0 {
			continue // nothing to reach: no policy, and no credential to attach
		}
		if provider != "" && have != nil && !have(provider) {
			dropped[name] = missingProvider(name, provider, item)
			continue
		}
		addProvider(provider)
		key := "tools_" + name
		policies[key] = networkPolicy{Name: key, Endpoints: eps, Binaries: []binary{{Path: bin}}}
	}
	p := policyFile{
		Version: 1,
		FilesystemPolicy: filesystemPolicy{
			ReadOnly:  []string{"/bin", "/dev/urandom", "/etc", "/lib", "/proc", "/sandbox", "/usr", "/var/log"},
			ReadWrite: []string{"/dev/null", "/tmp"},
		},
		Landlock: landlockPolicy{Compatibility: "best_effort"},
	}
	if len(policies) > 0 {
		p.NetworkPolicies = policies
	}
	policy, err := marshalPolicy(p)
	if err != nil {
		return Compiled{}, nil, err
	}
	slices.Sort(providers)
	sum := sha256.Sum256(append(append(slices.Clone(policy), "\n# providers: "...), strings.Join(providers, ",")...))
	return Compiled{
		PolicyYAML:   policy,
		PolicyDigest: "sha256:" + hex.EncodeToString(sum[:]),
		Sandbox:      RunnerSandbox,
		Image:        RunnerImage,
		Providers:    providers,
		Snapshot:     snap,
	}, dropped, nil
}

// pinnedOperations is the ops whose tool has a pin in served that matches
// its current definition: an operation nobody approved, or whose method,
// path or parameters changed since, gets no rule.
func pinnedOperations(source, baseURL string, ops []httpsource.Operation, served map[string]string) ([]httpsource.Operation, error) {
	var out []httpsource.Operation
	for _, op := range ops {
		pin, ok := served[httpsource.ToolName(source, op)]
		if !ok {
			continue
		}
		raws, err := httpsource.Tools(source, baseURL, []httpsource.Operation{op})
		if err != nil {
			return nil, err
		}
		h, err := toolgateway.PinTool(raws[0])
		if err != nil {
			return nil, err
		}
		if h == pin {
			out = append(out, op)
		}
	}
	return out, nil
}

// missingProvider is why a source whose provider the gateway lacks is left
// out of the runner; item is the secret store item setup creates it from.
func missingProvider(source, provider, item string) error {
	if item != "" {
		return fmt.Errorf("source %s: provider %s is not on the OpenShell gateway: add its token as the Keychain item %s (the Secret Service item on Linux), then run aimux sandbox setup", source, provider, item)
	}
	return fmt.Errorf("source %s: provider %s is not on the OpenShell gateway: create it with openshell provider create, then run aimux sandbox setup", source, provider)
}

// packEndpoint is a pack's endpoint (clipack checked it) in policy form,
// always enforced.
func packEndpoint(e clipack.Endpoint) endpoint {
	out := endpoint{Host: e.Host, Port: e.Port, Path: e.Path, Protocol: e.Protocol, Enforcement: "enforce", Access: e.Access}
	for _, r := range e.Rules {
		out.Rules = append(out.Rules, rule{Allow: ruleAllow{Method: r.Allow.Method, Path: r.Allow.Path, OperationType: r.Allow.OperationType}})
	}
	return out
}

// httpEndpoint is one rest endpoint on baseURL's host allowing GET on each
// operation's path under the base path, {param} segments as * (one
// segment), sorted and once each. No operations is no endpoint.
func httpEndpoint(baseURL string, ops []httpsource.Operation) (*endpoint, error) {
	if len(ops) == 0 {
		return nil, nil
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return nil, fmt.Errorf("base_url is not an https URL with a host") // never echo it: it may hold a secret
	}
	if err := clipack.CheckHost(u.Hostname()); err != nil {
		return nil, fmt.Errorf("base_url: %w", err)
	}
	port := 443
	if s := u.Port(); s != "" {
		if port, err = strconv.Atoi(s); err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("base_url port is out of range")
		}
	}
	base := strings.TrimSuffix(u.Path, "/")
	if strings.ContainsAny(base, "*{}") {
		return nil, fmt.Errorf("base_url path may not hold *, { or }")
	}
	var paths []string
	for _, op := range ops {
		p, err := rulePath(op.Path)
		if err != nil {
			return nil, fmt.Errorf("operation %s: %w", op.ID, err)
		}
		if p = base + p; !slices.Contains(paths, p) {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	e := endpoint{Host: u.Hostname(), Port: port, Protocol: "rest", Enforcement: "enforce"}
	for _, p := range paths {
		e.Rules = append(e.Rules, rule{Allow: ruleAllow{Method: "GET", Path: p}})
	}
	return &e, nil
}

// rulePath turns a path template into a policy path: each whole {param}
// segment becomes *. A template httpsource.CheckPath refuses (a literal
// segment that is not plain RFC 3986 pchar, an empty or dot segment) is
// refused here too.
func rulePath(tmpl string) (string, error) {
	if err := httpsource.CheckPath(tmpl); err != nil {
		return "", err
	}
	segs := strings.Split(tmpl, "/")
	for i, s := range segs {
		if len(s) > 2 && s[0] == '{' && s[len(s)-1] == '}' && !strings.ContainsAny(s[1:len(s)-1], "{}*") {
			segs[i] = "*"
			continue
		}
		if strings.ContainsAny(s, "{}*") {
			return "", fmt.Errorf("path %q: segment %q is not a whole {param}", tmpl, s)
		}
	}
	return strings.Join(segs, "/"), nil
}

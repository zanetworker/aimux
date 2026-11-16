# Runner sandbox spike: OpenShell read-only L7 for REST and GraphQL, providers, exec

Serves plan 2c Task 1 and the spec sections "What OpenShell enforces and what aimux adds", "Curated CLI packs" and "Decisions after the builder check" (tool-runner sandbox) in `2026-11-10-tool-gateway-profiles-design.md`.

Ran against the owner's local gateway with a throwaway sandbox `aimux-spike-tools` and image `localhost/aimux-spike-tools:dev`, both deleted afterwards.

## Verdict

| Check | Result |
|---|---|
| 1. Versions and image | pass: CLI and gateway 0.1.7; base image has `curl`, lacks `gh`; derived image with pinned `gh` 2.103.3 built |
| 2a. REST GET allowed (`gh api repos/cli/cli`, `curl`) | pass |
| 2b. REST writes denied (`POST /markdown`, `PUT`, `PATCH`, `DELETE`) | pass: proxy `policy_denied` 403 |
| 2c. GraphQL query allowed | pass |
| 2d. GraphQL mutation (`addStar`) denied | pass: proxy `policy_denied` 403, engine `l7-graphql` |
| 2e. Binary not in list (`python3`) denied | pass: `EACCES` at connect, logged `transparent_tcp_policy_denied` |
| 3. Provider with a GitHub token | skipped: no token (see below for what it needs) |
| 4. Exec latency and fidelity | pass: p50 477 ms, p95 502 ms; stdout, stderr and exit code intact |
| 5. REST method and path rules with wildcards | pass |

Ruling: **go**, with three changes to Task 4 (listed at the end): the image installs `gh` from the release tarball, not an RPM; the provider needs an imported, trimmed `github` profile; and the denial body reports `protocol: rest` for GraphQL denials, so matching must not key on that field.

## How to read the results

`gh` refuses to send anything without a token, and Step 3 is skipped, so `gh` calls ran with `--env GH_TOKEN=spike-fake-not-a-token` (not a credential). That gives a clean split:

- **Allowed by the proxy:** the request reaches GitHub, and GitHub answers (`200`, `401 Bad credentials`, or `403` rate limit). No `X-OpenShell-Policy` header.
- **Denied by the proxy:** `403 Forbidden`, header `X-OpenShell-Policy: github_api`, JSON body with `"error":"policy_denied"`. The request never leaves the sandbox.

The anonymous rate limit (60/hour) ran out mid-run, so some allowed requests show GitHub's `403 rate limit exceeded`. That response has no `X-OpenShell-Policy` header, so it still proves the request passed the proxy.

All writes were harmless: `POST /markdown` (renders markdown), and `PUT`/`PATCH`/`DELETE`/`POST` against non-existent paths, unauthenticated. The GraphQL mutation used `starrableId:"INVALID"`. Nothing reached GitHub as a write.

Helper used below:

```shell
X(){ openshell -g local sandbox exec aimux-spike-tools --no-login-shell --timeout 30 -- "$@"; }
G(){ openshell -g local sandbox exec aimux-spike-tools --no-login-shell --timeout 30 --env GH_TOKEN=spike-fake-not-a-token -- "$@"; }
```

## Check 1: versions and image

```
$ openshell --version
openshell 0.1.7
$ openshell -g local status
  Gateway: local
  Server: http://127.0.0.1:8090
  Status: Connected
  Version: 0.1.7
```

Base image `localhost/wingman-sandbox:2.1.319`: Ubuntu 24.04.5 LTS, `aarch64`, `apt-get` (no `dnf`), `/usr/bin/curl` present, `/usr/bin/python3` resolves to `/usr/bin/python3.12`, no `gh`. The sandbox runs as `uid=1000(ubuntu)`.

There is no RPM path on Ubuntu, so the derived image installs the official release tarball, pinned by version and SHA-256 (from `gh_2.103.3_checksums.txt`). 2.103.3 (2026-10-26) was picked over 2.104.0 (2026-11-12) as the newest release older than two weeks.

```dockerfile
FROM localhost/wingman-sandbox:2.1.319
ARG GH_VERSION=2.103.3
ARG GH_SHA256=ee32df2323c8847f5fac6cb6b4b69ca75828e9329a8f22f677021f7b3306ce63
RUN curl -fsSL -o /tmp/gh.tgz https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_arm64.tar.gz \
 && echo "${GH_SHA256}  /tmp/gh.tgz" | sha256sum -c - \
 && tar -xzf /tmp/gh.tgz -C /tmp \
 && install -m 0755 /tmp/gh_${GH_VERSION}_linux_arm64/bin/gh /usr/bin/gh \
 && rm -rf /tmp/gh.tgz /tmp/gh_${GH_VERSION}_linux_arm64
```

```
/tmp/gh.tgz: OK
Successfully tagged localhost/aimux-spike-tools:dev
$ podman run --rm --entrypoint gh localhost/aimux-spike-tools:dev --version
gh version 2.103.3 (2026-10-26)
```

Inside the sandbox, `readlink -f` gives `/usr/bin/gh` and `/usr/bin/curl` (real paths, no symlinks). The checksum is arm64-only; an amd64 host needs the `linux_amd64` tarball and its own checksum from the same file.

Sandbox creation:

```shell
openshell -g local sandbox create --name aimux-spike-tools \
  --from localhost/aimux-spike-tools:dev --policy policy-a.yaml \
  --no-auto-providers --detach
```

It reached `Ready` on the first poll.

## Check 2: read-only REST and query-only GraphQL (policy A)

Policy A. The filesystem section is copied from `~/.aimux/sandboxes/aimux-work-readonly.policy.yaml`.

```yaml
version: 1
filesystem_policy:
  include_workdir: true
  read_only: [/bin, /dev/urandom, /etc, /lib, /proc, /usr, /var/log]
  read_write: [/dev/null, /sandbox, /tmp]
landlock:
  compatibility: best_effort
network_policies:
  github_api:
    name: github_api
    endpoints:
      - host: api.github.com
        port: 443
        protocol: rest
        enforcement: enforce
        access: read-only
      - host: api.github.com
        port: 443
        path: /graphql
        protocol: graphql
        enforcement: enforce
        rules:
          - allow:
              operation_type: query
    binaries:
      - path: /usr/bin/gh
      - path: /usr/bin/curl
```

The two endpoints on one host and port coexist. The `/graphql` endpoint wins for that path because it is the more specific match, and every other path goes to the REST endpoint.

### 2a. REST GET allowed

```
$ X gh api repos/cli/cli --jq .full_name          # no GH_TOKEN
To get started with GitHub CLI, please run:  gh auth login
Alternatively, populate the GH_TOKEN environment variable with a GitHub API authentication token.
exit=4                                              # gh refuses locally; no request sent

$ G gh api repos/cli/cli --jq .full_name
gh: Bad credentials (HTTP 401)
{"message": "Bad credentials", "documentation_url": "https://docs.github.com/rest", "status": "401"}
exit=1                                              # reached GitHub: allowed

$ X curl -sS -o /dev/null -w '%{http_code}\n' https://api.github.com/repos/cli/cli
200
$ X curl -sS -I ... https://api.github.com/zen          -> 200 (HEAD allowed)
$ X curl -sS -X OPTIONS ... https://api.github.com/zen  -> 204 (OPTIONS allowed)
```

### 2b. REST writes denied

`gh` output for a denied write (exactly as printed; the body is followed by `gh: HTTP 403` on the same line):

```
$ G gh api -X POST /markdown -f text=hello
{"error":"policy_denied","policy":"github_api","rule":"POST /markdown","detail":"POST /markdown not permitted by policy","layer":"l7","protocol":"rest","method":"POST","path":"/markdown","host":"api.github.com","port":443,"binary":"/usr/bin/gh","rule_missing":{"type":"rest_allow","layer":"l7","method":"POST","path":"/markdown","host":"api.github.com","port":443,"binary":"/usr/bin/gh"},"next_steps":[]}gh: HTTP 403
exit=1
```

The same through `curl`, with headers:

```
$ X curl -sS -i -X POST -H 'Content-Type: application/json' -d '{"text":"hello"}' https://api.github.com/markdown
HTTP/1.1 403 Forbidden
Content-Type: application/json
Content-Length: 408
X-OpenShell-Policy: github_api
Connection: close

{"error":"policy_denied","policy":"github_api","rule":"POST /markdown","detail":"POST /markdown not permitted by policy",...,"binary":"/usr/bin/curl",...}

$ X curl -sS -i -X PUT https://api.github.com/aimux-spike/does-not-exist
HTTP/1.1 403 Forbidden
X-OpenShell-Policy: github_api
{"error":"policy_denied","policy":"github_api","rule":"PUT /aimux-spike/does-not-exist","detail":"PUT /aimux-spike/does-not-exist not permitted by policy",...}

PATCH /aimux-spike/does-not-exist   -> 403 [github_api]
DELETE /aimux-spike/does-not-exist  -> 403 [github_api]
```

Sandbox log line for a denial:

```
[ocsf] HTTP:DELETE [MED] DENIED DELETE https://api.github.com/aimux-spike/does-not-exist [policy:github_api engine:l7] [... reason:L7_REQUEST deny DELETE api.github.com:443/aimux-spike/does-not-exist reason=DELETE /aimux-spike/does-not-exist not permitted by policy]
```

### 2c. GraphQL query allowed

```
$ G gh api graphql -f query='{ viewer { login } }'
gh: Bad credentials (HTTP 401)
{"message": "Bad credentials", "documentation_url": "https://docs.github.com/rest", "status": "401"}
exit=1                                              # reached GitHub: allowed

$ X curl -sS -i -X POST -H 'Content-Type: application/json' -d '{"query":"{ viewer { login } }"}' https://api.github.com/graphql
HTTP/1.1 403 rate limit exceeded
{"message":"API rate limit exceeded for <ip>. ..."}  # GitHub's 403, no X-OpenShell-Policy: allowed
```

### 2d. GraphQL mutation denied

```
$ G gh api graphql -f query='mutation { addStar(input:{starrableId:"INVALID"}) { clientMutationId } }'
{"error":"policy_denied","policy":"github_api","rule":"POST /graphql","detail":"GraphQL operation blocked by endpoint policy","layer":"l7","protocol":"rest","method":"POST","path":"/graphql","host":"api.github.com","port":443,"binary":"/usr/bin/gh","rule_missing":{"type":"rest_allow","layer":"l7","method":"POST","path":"/graphql","host":"api.github.com","port":443,"binary":"/usr/bin/gh"},"next_steps":[]}gh: HTTP 403
exit=1
```

`curl` gets the same body with `HTTP/1.1 403 Forbidden` and `X-OpenShell-Policy: github_api`. The log shows the GraphQL engine parsed the operation:

```
[ocsf] HTTP:POST [MED] DENIED POST https://api.github.com/graphql [policy:github_api engine:l7-graphql] [... graphql_ops=type=mutation name=- fields=addStar persisted=- reason=GraphQL operation blocked by endpoint policy]
```

Quirk: the JSON body says `"protocol":"rest"` and `"rule_missing":{"type":"rest_allow",...}` for a GraphQL denial. Only the `detail` text ("GraphQL operation blocked by endpoint policy") and the log's `engine:l7-graphql` say it was GraphQL. aimux should classify denials by `error == "policy_denied"` plus the `X-OpenShell-Policy` header, never by `protocol`.

### 2e. Binary scoping

```
$ X python3 -c 'import urllib.request; urllib.request.urlopen("https://api.github.com")'
...
PermissionError: [Errno 13] Permission denied
urllib.error.URLError: <urlopen error [Errno 13] Permission denied>
exit=1
```

Log:

```
[openshell_supervisor_network::proxy] Denied staged transparent connection destination=198.18.0.2:443 reason=binary '/usr/bin/python3.12' not allowed in policy 'github_api' (ancestors: [], cmdline: []). SYMLINK HINT: ...
[ocsf] NET:OPEN [MED] DENIED /usr/bin/python3.12(0) -> api.github.com:443 [reason:transparent_tcp_policy_denied]
```

So a binary outside the list gets `EACCES` at `connect()`, not an HTTP 403. The proxy is transparent: no proxy environment variables are set in the sandbox.

Also checked:

- A listed binary to a host not in the policy: `curl https://example.com` fails with `curl: (7) Failed to connect ... Couldn't connect to server` (exit 7). Log: `NET:REFUSE ... DENIED example.com [reason:policy_dns_ineligible]`, then `transparent_tcp_policy_denied`.
- A listed binary started from an unlisted parent: `sh -c 'curl .../zen'` returns 200. Identity is the binary doing the `connect()`, so `sh` in the ancestry does not block it.

## Check 3: provider (skipped)

Skipped: the Keychain item `aimux-github` does not exist, and the owner's `gh auth token` is full-scope, so it must not be used. No token or credential was read.

What it needs:

1. **Token:** a fine-grained personal access token, public repositories only (read-only), no account or organization permissions. Store it as Keychain item `aimux-github`.
2. **A profile on the gateway.** The local gateway's catalog has only `claude-code-oauth` (`openshell -g local provider list-profiles`). In 0.1.7, `provider create --type github` without an imported profile fails with `provider profile 'github' not found; import a matching profile before using this provider type` (`crates/openshell-cli/src/commands/provider.rs`). A profile import changes the gateway, so it was not done in the spike.
3. **A trimmed profile, not the stock one.** An attached provider adds its profile's endpoints to the sandbox's effective policy as `_provider_*` entries. The stock `providers/github.yaml` (v0.1.7) grants `api.github.com` REST and GraphQL read-only, but also `github.com` git transport (`GET/HEAD/OPTIONS **` and `POST /**/git-upload-pack`) and the binaries `/usr/bin/git` and `/usr/local/bin/git`. Attaching it would widen the runner beyond what aimux compiles. aimux should ship its own copy with only the `api.github.com` endpoints and `binaries: [/usr/bin/gh]`.

Commands, from `openshell provider --help`, `provider create --help` and the v0.1.7 profile docs:

```shell
# once per gateway: lint and import the trimmed profile (workspace scope; --global needs Platform Admin)
openshell -g local profile lint -f sandbox/aimux-tools/github-profile.yaml
openshell -g local profile import -f sandbox/aimux-tools/github-profile.yaml

# create the provider, with the token passed through the environment so it never appears in argv
GITHUB_TOKEN="$(security find-generic-password -s aimux-github -w)" \
  openshell -g local provider create --name aimux-github --type github --credential GITHUB_TOKEN

# attach at create time, or to a running sandbox
openshell -g local sandbox create --name aimux-tools --provider aimux-github ...
openshell -g local sandbox provider attach aimux-tools aimux-github   # check exact argv with --help

# verification still to run
openshell -g local sandbox exec aimux-tools --no-login-shell -- gh auth status    # expect: logged in via GITHUB_TOKEN
openshell -g local sandbox exec aimux-tools --no-login-shell -- sh -c 'env | grep -c "^GITHUB_TOKEN="'  # set, and the value is a placeholder
```

Per the v0.1.7 docs, the sandbox sees a placeholder in `GITHUB_TOKEN`/`GH_TOKEN`, and the proxy swaps in the real token only on requests to the profile's endpoints. Confirming that `env` holds a placeholder and not the token is still open until a token exists.

## Check 4: exec latency and fidelity

10 runs of `openshell -g local sandbox exec aimux-spike-tools --no-login-shell -- gh --version`, wall clock on the host:

```
ms: [461, 466, 472, 473, 476, 478, 485, 500, 500, 502]
p50 477  p95 502  max 502
```

All 10 exited 0 with `gh version 2.103.3 (2026-10-26)` on stdout. Without `--no-login-shell`, one run took 473 ms, so the login shell costs nothing measurable here. Task 5 should still pass `--no-login-shell` for predictable startup.

Fidelity:

```
$ openshell ... exec aimux-spike-tools --no-login-shell -- sh -c 'echo out-line; echo err-line >&2; exit 7' 2>err >out
rc=7 stdout=[out-line] stderr=[err-line]
```

Exit code and separate streams come through. 64 KiB of binary output (`head -c 65536 /usr/bin/gh`) hashed the same on the host (`shasum`) and inside (`sha256sum`): `1b87d6d6...0347a4d`. Stdout is byte-clean.

Also: `--timeout <seconds>` on `exec` bounds a hung command. The spike hit this when `curl -X HEAD` waited for a body; Task 5 should always set it.

## Check 5: REST method and path rules (policy B, applied live)

`openshell -g local policy set aimux-spike-tools --policy policy-b.yaml --wait` applied it without recreating the sandbox (`Policy version 2 loaded`). The REST endpoint changed to:

```yaml
      - host: api.github.com
        port: 443
        protocol: rest
        enforcement: enforce
        rules:
          - allow:
              method: GET
              path: /repos/*/*/issues
          - allow:
              method: GET
              path: /repos/*/*/issues/*
          - allow:
              method: GET
              path: /zen
```

| Request | Result |
|---|---|
| `GET /repos/cli/cli/issues` | 200 (GitHub) |
| `GET /repos/cli/cli/issues?state=open&per_page=1` | 200: query strings do not affect path matching |
| `GET /repos/cli/cli/issues/1` | 200 |
| `GET /repos/cli/cli/issues/1/comments` | 403 `policy_denied`: `*` is one segment |
| `GET /repos/cli/cli/pulls` | 403 `policy_denied` |
| `GET /repos/cli/cli` | 403 `policy_denied`: no implicit parent |
| `POST /repos/aimux-spike-nonexistent/x/issues` | 403 `policy_denied` |
| `PATCH /repos/aimux-spike-nonexistent/x/issues/1` | 403 `policy_denied` |
| `DELETE /repos/aimux-spike-nonexistent/x/issues/1` | 403 `policy_denied` |

Policy D changed `/repos/*/*/issues/*` to `/repos/*/*/issues/**`, and then `GET /repos/cli/cli/issues/1/comments` returned 200: `**` matches any depth.

`access` and `rules` on one endpoint are rejected at `policy set`, and the sandbox keeps its old version:

```
Error:   × code: 'Client specified an invalid argument', message: "policy contains
  │ unsafe content: sandbox policy validation failed; network policy
  │ 'github_api': endpoint 0 has invalid L7 configuration: rules and access
  │ are mutually exclusive"
exit=1
```

## GraphQL shapes that work

Both loaded and enforced in 0.1.7:

```yaml
# explicit (policy A)
- host: api.github.com
  port: 443
  path: /graphql
  protocol: graphql
  enforcement: enforce
  rules:
    - allow:
        operation_type: query

# preset (policy D; the shape the stock github provider profile uses)
- host: api.github.com
  port: 443
  path: /graphql
  protocol: graphql
  enforcement: enforce
  access: read-only
```

With `access: read-only` (policy D): an unauthenticated query reached GitHub (403 with no OpenShell header), the `addStar` mutation got `policy_denied`, and `subscription { x }` got `403 [github_api]` (log: `graphql_ops=type=subscription ... blocked`). A document holding `query A` and `mutation B` with `operationName: "A"` passed the proxy. GitHub only runs the named operation, so the mutation does not execute; the proxy judges the selected operation. Use the explicit `operation_type: query` form: it says exactly what is allowed and matches the spec.

## Policy history on the spike sandbox

```
VERSION  HASH           STATUS
3        3e3b7824e91a   Loaded       (policy D)
2        1e5dcd5a0b18   Superseded   (policy B)
1        6c7ea96147f5   Superseded   (policy A, create)
```

The rejected policy C never got a version.

## Cleanup

`openshell -g local sandbox delete aimux-spike-tools` (accepted; gone from `sandbox list`), `podman rmi localhost/aimux-spike-tools:dev` (gone from `podman images`). No providers or profiles were created. Other sandboxes and providers were not touched.

## Rulings for Tasks 2-7

**Go.** 0.1.7 has everything the plan needs: read-only REST, query-only GraphQL on the same host, binary scoping, wildcard path rules, live policy updates, and exec with clean stdout and exit codes. Task 4 must change three things:

1. **Image (Task 4 Step 3).** The base is Ubuntu 24.04 (`apt-get`, no RPM), so the Containerfile installs the `gh` release tarball by version and SHA-256, as in Check 1. Use `RunnerImage = "localhost/aimux-tools:2.103.3"` and `gh` 2.103.3. Keep checksums for both `linux_arm64` (`ee32df23...06ce63`) and `linux_amd64`, and pick one with `TARGETARCH`.
2. **Provider (Task 4, `setup`).** `provider create --type github` needs an imported profile, and the stock profile widens the policy (`github.com` git transport, `git` binaries). `setup` must lint and import an aimux-owned trimmed profile (`api.github.com` only, `binaries: [/usr/bin/gh]`) before `provider create --name aimux-github --type github --credential GITHUB_TOKEN`, passing the token through the environment. `TestSandboxSetup_RunnerImageAndProvider` should check the import step too. Step 3 of this spike (`gh auth status` sees a token; `env` shows a placeholder) still has to run once the owner creates the fine-grained token. Treat it as a gate on Task 7's live acceptance.
3. **Denial detection (Tasks 5, 6).** Treat a request as policy-denied when the status is 403 and the response has `X-OpenShell-Policy` with `"error":"policy_denied"` in the body. Do not use the body's `protocol` field (it says `rest` for GraphQL denials). For `gh`, the body comes first on stdout, followed by `gh: HTTP 403`, exit 1. An unlisted binary or host fails at connect (`EACCES`, or `curl` exit 7), not with an HTTP status.

The shapes for `compile.go` (`endpoint`): REST is either `access: read-only` or `rules: [{allow: {method, path}}]`, never both (the gateway rejects that). In paths, `*` matches one segment and `**` matches any depth. Query strings are ignored by matching. GraphQL is `path: /graphql`, `protocol: graphql`, `rules: [{allow: {operation_type: query}}]`. For Task 6 (OpenAPI GET operations), turn each `{param}` segment into `*`, and allow only `GET` per path template. Exec: always pass `--no-login-shell` and `--timeout`. Budget about 0.5 s per call.

<!-- no-images: spike log of commands and outputs; no architecture or flow to draw -->

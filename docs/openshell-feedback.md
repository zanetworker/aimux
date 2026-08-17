# OpenShell Integration Report: Bugs, Feature Requests, and Security Considerations

From integrating [aimux](https://github.com/zanetworker/aimux) (multi-agent TUI dashboard) with OpenShell v0.0.66 on macOS with the podman compute driver. Tested June 20-21, 2026 over two days of end-to-end work including unit tests, integration tests against a live gateway, interactive sessions, and headless MCP task workers.

aimux orchestrates AI coding agents on remote infrastructure. It uses OpenShell as the sandbox runtime: creating sandboxes, connecting terminals via tmux, executing tasks, and collecting OTEL telemetry. This report captures every issue encountered, with reproduction steps, root cause analysis, and proposed solutions that preserve OpenShell's security model.

## Bugs

### BUG-1: `sandbox exec` rejects newline characters in command arguments

**Severity:** High
**Component:** `crates/openshell-server` (gRPC exec handler)
**Versions affected:** v0.0.66

**What happens:**
Any command passed to `sandbox exec` that contains a literal newline (`\n`) or carriage return (`\r`) in any argument is rejected immediately. The command never reaches the sandbox.

**Error message:**
```text
Error: × code: 'Client specified an invalid argument', message: "command argument 2
  │ contains newline or carriage return characters"
```

**How to reproduce:**

```bash
# 1. Create a sandbox
openshell sandbox create --name exec-newline-test
# (wait until Ready)

# 2. Try to run a command with a newline in the argument
openshell sandbox exec -n exec-newline-test -- bash -c "echo 'line1
line2'"
# Result: Error (command rejected before reaching sandbox)

# 3. This also fails (multi-line Python):
openshell sandbox exec -n exec-newline-test -- python3 -c "
def hello():
    print('hi')
hello()
"
# Result: Same error

# 4. Single-line equivalent works fine:
openshell sandbox exec -n exec-newline-test -- python3 -c "print('hi')"
# Result: hi
```

**Why this is a problem:**
We use `sandbox exec` to run tasks dispatched by a lead AI agent via MCP. The agent sends a prompt like "write a fibonacci function and run it", which our MCP server translates to a `sandbox exec` call. Multi-line scripts (Python, shell) are the natural output. Having to convert everything to single-line with semicolons is fragile and limits what agents can do.

Concrete example: we wanted to write OTEL configuration to `~/.bashrc` in the sandbox before connecting. The natural command:

```bash
openshell sandbox exec -n my-sandbox -- bash -c "echo 'export OTEL_ENDPOINT=http://host.openshell.internal:4318
export OTEL_PROTOCOL=http/protobuf' >> ~/.bashrc"
```

This fails. Our workaround:

```bash
openshell sandbox exec -n my-sandbox -- bash -c \
  "echo 'export OTEL_ENDPOINT=http://host.openshell.internal:4318' >> ~/.bashrc; \
   echo 'export OTEL_PROTOCOL=http/protobuf' >> ~/.bashrc"
```

**Why the restriction exists (our understanding):**
The gRPC exec handler validates command arguments to prevent argument injection. A newline in one argument could theoretically be interpreted as a command separator by the shell. This is a valid security concern for untrusted input.

**Proposed fix that preserves security:**
The restriction makes sense for individual arguments (e.g., `--flag "value\nmalicious"` could be dangerous). But when the argument is the string passed to `bash -c` or `sh -c`, it's already explicitly a shell script. The user chose shell interpretation.

Option A: Allow newlines specifically in the last argument when the command is `bash -c`, `sh -c`, or `python3 -c`. These are "the user wants a script" patterns.

Option B: Support a `--stdin` flag (see FR-3) that pipes data to the command's stdin. stdin is data, not arguments, so newlines are safe. This sidesteps the argument validation entirely.

Option C: Base64-encode the script and decode inside the sandbox: `exec -- bash -c "echo <base64> | base64 -d | bash"`. This works today but is ugly. It could be a built-in feature: `exec --script <base64>`.

### BUG-2: `--env` vars not inherited by provider-started agent processes

**Severity:** High (blocks OTEL telemetry, the main reason we integrated OpenShell)
**Component:** Provider entrypoint / supervisor env injection
**Versions affected:** v0.0.66

**What happens:**
When you create a sandbox with `--env KEY=VALUE`, the variable is visible inside `sandbox exec` and `sandbox connect` shell sessions. But if you also use `--provider claude` (or any provider that auto-starts an agent), the provider-started agent process does NOT have the `--env` variables. The agent inherits its environment from the supervisor (PID 1), which doesn't include user `--env` vars.

**How to reproduce:**

```bash
# 1. Create sandbox with provider AND custom env var
openshell sandbox create --name env-bug --provider claude \
  --env OTEL_EXPORTER_OTLP_ENDPOINT=http://host.openshell.internal:4318

# 2. Wait for Ready, then verify via exec (this WORKS):
openshell sandbox exec -n env-bug -- bash -c 'echo $OTEL_EXPORTER_OTLP_ENDPOINT'
# Output: http://host.openshell.internal:4318  ← correct

# 3. Verify via connect shell (this also WORKS):
openshell sandbox connect env-bug
# Inside the connected terminal:
echo $OTEL_EXPORTER_OTLP_ENDPOINT
# Output: http://host.openshell.internal:4318  ← correct

# 4. But Claude Code started by the provider does NOT have it:
# (inside Claude Code, ask it to run: env | grep OTEL)
# Output: (empty)  ← BUG

# 5. Verify PID 1 doesn't have it:
openshell sandbox exec -n env-bug -- bash -c \
  "cat /proc/1/environ | tr '\0' '\n' | grep OTEL"
# Output: (empty)  ← confirms the supervisor doesn't have --env vars
```

**Our debugging journey (what we tried):**

We spent 8+ hours trying every approach to get OTEL env vars into Claude Code's process:

1. **`--env` at create time** (attempt 1): Created sandbox with `--env OTEL_*=...`. Visible in exec/connect shells, invisible to Claude. Root cause: supervisor (PID 1) doesn't have them, and Claude is a child of the supervisor.

2. **`.bashrc` injection** (attempt 2): Used `sandbox exec` to write `export OTEL_*=...` to `~/.bashrc` before connecting. The connect shell sourced `.bashrc` and had the vars. But when the provider auto-started Claude, Claude was started by the supervisor (which doesn't source `.bashrc`), so no vars.

3. **`tmux send-keys "export ... && claude"`** (attempt 3): After connecting via tmux, sent export commands then started Claude in one command. Problem: the provider sometimes auto-starts Claude on connect (BUG-3), so our `tmux send-keys` typed the export commands into Claude's prompt instead of the shell.

4. **`bash -c "export ... && openshell sandbox connect"`** (attempt 4): Wrapped the connect command in bash to set env vars before connecting. Problem: the provider's entrypoint detection broke because connect was no longer running directly in the tmux session.

5. **All four approaches combined**: Tried `--env` at create time + `.bashrc` injection + `tmux send-keys` fallback. Still inconsistent because of the provider auto-start race (BUG-3).

**Root cause (diagram):**

```text
openshell sandbox create --env OTEL_*=http://...
    │
    ▼
┌────────────────────────────────┐
│ Container created              │
│                                │
│  PID 1 (supervisor)            │ ← env from container spec ONLY
│    │                           │    (does NOT include --env vars)
│    ├─► Provider agent (Claude) │ ← inherits PID 1's env (no OTEL)
│    │                           │
│    └─► SSH server              │
│         │                      │
│         └─► Login shell        │ ← gateway injects --env vars HERE
│              (exec/connect)    │    (OTEL visible, but only in this shell)
│              │                 │
│              └─► User's claude │ ← IF started from this shell, inherits OTEL
│                                │    BUT provider starts Claude from supervisor,
│                                │    not from this shell
└────────────────────────────────┘
```

The fundamental problem: `--env` vars are injected into session-level shells, not into the container spec. The provider's agent process is NOT started from a session shell.

**Why this matters:**
OTEL telemetry collection from sandbox agents is a key use case. Without env var injection into the agent process, integrators cannot configure OTEL endpoints, logging levels, feature flags, or any runtime configuration that the agent reads from its environment.

**Proposed fix:**

Option A (simplest, matches Docker/K8s behavior): Add `--env` vars to the container spec's environment so PID 1 and all children (including provider agents) inherit them. This is what users expect from Docker's `-e` flag and Kubernetes' `env:` in pod specs.

Option B (if Option A has security concerns): Add `--provider-env KEY=VALUE` that passes env vars specifically to the provider's entrypoint, separate from the session env. The provider would set these in the agent's environment before starting it.

Option C (workaround-level): Write `--env` vars to a file in the sandbox (e.g., `/etc/openshell/user-env`) and have the provider's entrypoint source it before starting the agent.

**Security consideration for Option A:** User-supplied `--env` vars would be visible to the supervisor and all container processes, not just sessions. If some env vars should be isolated from the supervisor (e.g., user secrets that the isolation layer shouldn't see), Option B is more granular. However, the supervisor already manages provider credentials (which are higher-sensitivity than user env vars), so the added risk of Option A is minimal.

### BUG-3: Provider auto-start behavior on `sandbox connect` is inconsistent

**Severity:** Medium
**Component:** `claude-code` provider profile / connect entrypoint
**Versions affected:** v0.0.66

**What happens:**
When you create a sandbox with `--provider claude`, the `sandbox connect` command sometimes auto-starts Claude Code (you see the Claude welcome screen immediately), and sometimes it doesn't (you get a bare `sandbox@...:~$` shell prompt and have to type `claude` yourself). The behavior is not predictable from the integrator's perspective.

**How to reproduce:**

The inconsistency is hard to reproduce deterministically. Over our testing, we observed:

```bash
# Scenario A: Claude auto-starts (happened ~40% of the time)
openshell sandbox create --name auto-a --provider claude
openshell sandbox connect auto-a
# Result: Claude Code welcome screen appears
# "Hi! What would you like to work on?"

# Scenario B: Bare shell (happened ~60% of the time)
openshell sandbox create --name auto-b --provider claude
openshell sandbox connect auto-b
# Result: sandbox@sandbox-auto-b:~$
# (Claude NOT started, user must type "claude" manually)
```

**Factors that seem to correlate (but we couldn't prove causally):**

1. **First connect to a fresh sandbox:** Usually drops to shell (no auto-start). The first-run onboarding flow (theme selection, API key confirmation) may need interactive input before Claude can fully start, so the provider might skip auto-start on first connect.

2. **Reconnect to a previously-connected sandbox:** More likely to auto-start (onboarding already complete). The `.claude/` directory has config from the previous session.

3. **Timing:** If you connect very quickly after sandbox reaches Ready, the provider's entrypoint script may not have had time to set up.

4. **`CLAUDE_CODE_ENTRYPOINT=cli`:** When auto-start works, this env var is set. When it doesn't work, the env var is still set but Claude isn't running.

**Why this matters for programmatic integrations:**

We launch remote sandbox sessions from our TUI by:
1. Creating a sandbox with `--provider claude`
2. Opening `openshell sandbox connect <name>` in a tmux session
3. Sending `claude` via `tmux send-keys` to start the agent

Step 3 breaks when the provider already auto-started Claude: the `tmux send-keys "claude"` types the word "claude" into Claude's prompt input instead of the shell. Claude then tries to process "claude" as a user prompt.

If we skip step 3 (relying on auto-start), Claude sometimes doesn't start and the user stares at a blank shell.

We can't detect which case occurred because tmux's pane output takes seconds to update, and by the time we could check, the send-keys has already been sent.

**Proposed fix:**

Add a flag to `sandbox connect` that controls agent auto-start:

```bash
openshell sandbox connect --no-agent my-sandbox   # always shell, never auto-start
openshell sandbox connect --agent my-sandbox       # always start agent, wait for it
openshell sandbox connect my-sandbox               # current behavior (default)
```

For programmatic access (MCP servers, TUI integrations), `--no-agent` would let the caller control when and how the agent starts. For interactive CLI users, the current default is fine.

**Security consideration:** No security impact. The agent and its permissions are the same regardless of how it's started. The flag only controls the UX flow.

### BUG-4: `sandbox create` blocks indefinitely after printing sandbox name

**Severity:** Medium
**Component:** CLI `sandbox create` command
**Versions affected:** v0.0.66

**What happens:**
`openshell sandbox create` prints "Created sandbox: <name>" and then blocks indefinitely, streaming the sandbox lifecycle. It never returns control to the shell. This is correct for interactive use (the user sees the sandbox come up), but makes programmatic creation difficult.

The sandbox IS created and reaches Ready state. The CLI process just doesn't exit.

**How to reproduce:**

```bash
# This never returns:
openshell sandbox create --name blocking-test

# Output:
# Created sandbox: blocking-test
#
#   [0.0s] Requesting compute...
# (cursor blinks here forever, never returns to shell)

# In another terminal, the sandbox IS ready:
openshell sandbox list
# NAME           PHASE
# blocking-test  Ready
```

**Code we had to write to work around this:**

Our `internal/openshell/client.go:CreateSandbox` (80 lines) does this:

```go
func (c *Client) CreateSandbox(ctx context.Context, opts CreateOpts) (string, error) {
    // Build args: sandbox create --name X --provider Y --env K=V...
    args := buildCreateArgs(opts)

    // Start the CLI in the background (it will block forever)
    cmd := exec.CommandContext(ctx, c.cfg.Binary, fullArgs...)
    stdoutPipe, _ := cmd.StdoutPipe()
    cmd.Start()

    // Read the sandbox name from stdout in a goroutine
    // (printed before the CLI blocks)
    outputCh := make(chan string, 1)
    go func() {
        buf := make([]byte, 4096)
        var collected string
        for {
            n, readErr := stdoutPipe.Read(buf)
            collected += string(buf[:n])
            if parseSandboxName(collected) != "" {
                outputCh <- parseSandboxName(collected)
                return
            }
            if readErr != nil { return }
        }
    }()

    // Wait for the name (up to 15s)
    select {
    case name := <-outputCh: // got the name
    case <-time.After(15 * time.Second):
        cmd.Process.Kill()
        return "", errors.New("timeout")
    }

    // Poll sandbox list until Ready (up to 60s)
    for time.Now().Before(deadline) {
        infos, _ := c.ListSandboxes(ctx)
        for _, info := range infos {
            if info.Name == name && info.Status == "Ready" {
                cmd.Process.Kill()  // kill the blocking CLI
                cmd.Wait()
                return name, nil
            }
        }
        time.Sleep(2 * time.Second)
    }
}
```

We also had to write `parseSandboxName` (handles ANSI escape codes in the output) and `StripAnsi` (removes `\x1b[1m`, `\x1b[36m`, etc. from CLI output).

A `--detach` or `--wait-ready` flag would reduce this to:

```go
func (c *Client) CreateSandbox(ctx context.Context, opts CreateOpts) (string, error) {
    args := append(buildCreateArgs(opts), "--wait-ready")
    output, _, err := c.run(ctx, args...)
    return parseName(output), err
}
```

**Proposed fix:**

```bash
# Option 1: detach immediately after creating (prints name, exits)
openshell sandbox create --name test --detach
# Output: test
# (exits immediately, sandbox provisioning continues in background)

# Option 2: wait until Ready, then exit 0
openshell sandbox create --name test --wait-ready
# Output: Created sandbox: test
# (blocks until Ready, then exits)

# Option 3: JSON output includes the name
openshell sandbox create --name test --output json --wait-ready
# Output: {"name":"test","phase":"Ready"}
```

**Security consideration:** None. The sandbox lifecycle is identical regardless of how the CLI process behaves. The sandbox creation, provisioning, and readiness checks all happen on the gateway side.

## Feature Requests

### FR-1: Document `host.openshell.internal` for sandbox-to-host communication

**Priority:** High (already works, just needs docs)

**What it is:**
The hostname `host.openshell.internal` resolves to the gateway host from inside sandboxes. It was added in PR #1279 and works with both the podman and Docker compute drivers. Sandboxes can use it to reach services running on the host (OTEL collectors, databases, dev servers).

**Why it needs documentation:**
We discovered this via a Slack conversation, not through any documentation:

> **Emilien Macchi** (Jun 10): We ran into an issue collecting OTEL metrics from inside sandboxes. The sandbox network isolation routes all traffic through the gateway proxy, so a host-side OTLP collector is unreachable.
>
> **Oliver Walsh** (Jun 10): Have you tried `host.openshell.internal:<port>`? Should work since PR #1279.
>
> **Emilien Macchi** (Jun 10): It works!

Without this knowledge, we spent time trying:
- Direct IP access to the host (`10.200.0.1:4318`): only reaches the gateway proxy
- Disabling network isolation: not an option (breaks the security model)
- Embedding a collector inside the sandbox: unnecessary complexity

**Suggested documentation location:** README.md "Networking" section, and `sandbox create --help` output.

**Security consideration:** This is already the designed behavior. Documenting it doesn't change the security model. The `host.openshell.internal` hostname is injected into `/etc/hosts` inside each sandbox. Sandboxes can already reach the host via the gateway bridge; the hostname just makes it discoverable without guessing IPs.

### FR-2: JSON output for `sandbox list` and other CLI commands

**Priority:** High

**What we need:**
Machine-readable output from CLI commands. Currently all output is human-formatted tables with ANSI color codes.

**Code we had to write to parse the current output:**

```go
// StripAnsi removes ANSI escape codes (30 lines)
func StripAnsi(s string) string {
    var result strings.Builder
    i := 0
    for i < len(s) {
        if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
            j := i + 2
            for j < len(s) && (s[j] < 'A' || s[j] > 'Z') && (s[j] < 'a' || s[j] > 'z') {
                j++
            }
            // ... skip the escape sequence
        }
        result.WriteByte(s[i])
        i++
    }
    return result.String()
}

// parseSandboxList parses the table output (20 lines)
func parseSandboxList(output string) []SandboxInfo {
    clean := StripAnsi(output)
    for _, line := range strings.Split(clean, "\n") {
        // Skip header, empty lines, "No sandboxes" message
        // Split on whitespace, take LAST field as phase (not second!)
        // The table is: NAME  DATE  TIME  PHASE (4 fields, not 2)
    }
}
```

**Gotcha we hit:** The status/phase is the LAST field in the table, not the second. The table columns are `NAME  CREATED_DATE  CREATED_TIME  PHASE`. Our first parser assumed `NAME  STATUS` (2 fields) and broke on real output.

**What we want:**

```bash
openshell sandbox list --output json
[{"name":"happy-fox","phase":"Ready","created":"2026-06-21T10:00:00Z"}]

openshell sandbox create --name test --wait-ready --output json
{"name":"test","phase":"Ready"}

openshell status --output json
{"gateway":"podman-test","server":"http://127.0.0.1:8090","status":"connected","version":"0.0.66"}
```

**Security consideration:** None. JSON output contains the same information as the table. No additional data exposed.

### FR-3: `sandbox exec` should support stdin for multi-line input

**Priority:** Medium

**What we need:**
A way to send multi-line scripts to `sandbox exec` without embedding them in command arguments (which is blocked by BUG-1).

```bash
# Desired:
cat script.py | openshell sandbox exec -n my-sandbox --stdin -- python3

# Or:
openshell sandbox exec -n my-sandbox --stdin -- python3 << 'EOF'
def fib(n):
    a, b = 0, 1
    for _ in range(n):
        a, b = b, a + b
    return a
print(fib(10))
EOF
```

**Use case:** MCP-style task execution where a lead AI agent dispatches a coding task as a multi-line script to run inside a sandbox. The script is the agent's output, not user input, so it naturally contains newlines.

**Security consideration:** stdin piping doesn't bypass the exec security model. The process still runs as the sandbox user with all Landlock/seccomp restrictions. stdin data is data, not command arguments, so it cannot cause argument injection. The `--stdin` flag makes the intent explicit (opt-in, not default).

### FR-4: Pre-onboarded sandbox images for coding agents

**Priority:** Medium

**What happens today:**
Every fresh sandbox requires Claude Code's first-run onboarding flow:
1. Theme selection (interactive prompt with 7 options)
2. API key detection ("Do you want to use this API key? Yes/No")
3. Security notice acknowledgment ("Press Enter to continue")
4. Syntax theme preview ("Enter to confirm")

This is 4+ interactive prompts before Claude accepts user input. For programmatic integrations, this blocks automation entirely. In our TUI, users see these prompts on first connect and have to click through them.

**What would help (any of these):**

1. **`CLAUDE_CODE_SKIP_ONBOARDING=1` env var:** If the provider set this, Claude Code could skip the interactive prompts and use sensible defaults (auto theme, accept API key, acknowledge security notice).

2. **Community sandbox image with onboarding complete:** A pre-configured image where Claude Code's `~/.claude/settings.json` and onboarding markers are already written.

3. **Persistent state volume:** If `.claude/` persisted across sandbox recreations (via a named Podman volume), onboarding would only happen once per user.

**Security consideration:** Pre-onboarded images must NOT include API keys, auth tokens, or session data. Only non-sensitive onboarding state (theme preference, security notice acknowledgment flag). Provider credentials should still be injected at runtime.

### FR-5: Sandbox lifecycle webhooks or events

**Priority:** Low

**What we do today:**
aimux polls `openshell sandbox list` every 2-3 seconds in the discovery tick to detect new/deleted/errored sandboxes. This works but wastes resources and has a 2-3 second delay.

**What would be better:**

```bash
openshell sandbox watch --output json-stream
{"event":"created","name":"happy-fox","phase":"Provisioning","ts":"2026-06-21T10:00:00Z"}
{"event":"ready","name":"happy-fox","phase":"Ready","ts":"2026-06-21T10:00:12Z"}
{"event":"error","name":"sad-cat","phase":"Error","reason":"ContainerExited","ts":"..."}
{"event":"deleted","name":"happy-fox","ts":"2026-06-21T10:05:00Z"}
```

**Security consideration:** Events must be scoped to the authenticated user's sandboxes. In multi-tenant deployments with OIDC/mTLS, one user must not see another user's sandbox events. The existing gateway auth model already handles this for `sandbox list`; the watch endpoint should use the same scoping.

## Gateway Configuration Gotchas

These aren't bugs but each cost us 30-60 minutes of debugging. Better error messages or docs would prevent them.

### 1. JWT keys are mandatory since v0.0.66

Without `[openshell.gateway.gateway_jwt]` in the gateway config, sandboxes crash with an error that appears in the SANDBOX logs (not the gateway logs):

```text
Error: × Policy fetch failed after 5 attempts: no sandbox token source available
```

The sandbox creates successfully, reaches Provisioning phase, then immediately enters Error phase. The gateway logs show nothing useful. You have to `podman logs openshell-sandbox-<name>` to find the error.

**What would help:** A gateway startup warning if JWT is not configured, or a clearer sandbox error message: "Gateway JWT not configured. Add [openshell.gateway.gateway_jwt] to your gateway config. See: <docs-url>"

### 2. `allow_unauthenticated_users` required for plaintext dev gateways

When running with `disable_tls = true` and `gateway_jwt` configured, the CLI gets "missing authorization header" errors. This is because `gateway_jwt` enables sandbox-to-gateway auth, which also enables user auth.

Adding `[openshell.gateway.auth] allow_unauthenticated_users = true` fixes it, but this setting isn't mentioned in the error message or the quickstart guide.

### 3. Podman driver config differs from Docker

The error message when using wrong fields is helpful (lists valid fields), but you only discover this after your gateway crashes on startup. Examples:

- `sandbox_namespace` is a Docker field; podman uses `network_name`
- `image_pull_policy = "IfNotPresent"` is Docker/K8s syntax; podman wants `"missing"`
- `grpc_endpoint` auto-detects on podman; setting it explicitly can break things

**What would help:** Per-driver example configs in the docs, or `openshell gateway validate-config path/to/gateway.toml`.

### 4. Running gateway in a container on macOS requires specific flags

The containerized gateway needs the podman socket. On macOS with podman-machine, this requires `--userns keep-id --security-opt label=disable`. Without these, the gateway gets "Permission denied" on `/run/podman/podman.sock`.

Running the gateway natively via the `openshell-gateway` binary avoids all these issues. The install script generates the right config (`~/.config/openshell/gateway-podman.toml`) and JWT keys (`~/.config/openshell/jwt/`).

**What would help:** Document "native gateway on macOS, containerized on Linux" as the recommended setup in the quickstart.

## What Works Well

1. **Podman driver on macOS:** Reliable once configured. `host_gateway_ip = "192.168.127.254"` for macOS podman-machine is the key setting.

2. **`host.openshell.internal`:** The right abstraction for sandbox-to-host networking. Discovered via Slack, works perfectly.

3. **Provider credential injection:** The `openshell:resolve:env:` reference pattern with proxy-based resolution is clever. API calls work transparently. One `provider create` and all sandboxes get credentials.

4. **Sandbox isolation verified:** We wrote an integration test that creates two sandboxes, writes a file in one, and verifies the other can't read it. Passed.

5. **CLI design:** Clean, discoverable command structure. `doctor check`, `provider list-profiles`, `gateway list` are all useful. Error messages generally include the valid options.

6. **Gateway JWT:** Ed25519 key generation via install script, automatic token minting for sandboxes. Once set up, it just works.

7. **`sandbox exec`:** Fast, reliable, separate from SSH connect. The right tool for programmatic one-off commands (when they don't need newlines).

## Test Environment

- **OpenShell CLI:** v0.0.66
- **Gateway:** v0.0.66 (native binary on macOS, not containerized)
- **Compute driver:** podman (rootless, macOS podman-machine)
- **Podman:** 5.x, netavark, cgroups v2
- **macOS:** Darwin 25.0.0 (Apple Silicon, arm64)
- **Integration:** aimux multi-agent TUI dashboard
- **Code:** Go, ~2000 lines of OpenShell integration (shared client, backend, runtime, spawn, discovery)
- **Tests:** 80+ unit tests, 9 integration tests against live gateway
- **Use cases:** Interactive Claude Code in sandbox via tmux, headless MCP task workers via exec, OTEL telemetry forwarding (blocked by BUG-2), sandbox discovery for TUI agent list

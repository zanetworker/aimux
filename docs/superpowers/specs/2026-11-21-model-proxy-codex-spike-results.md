# Codex model proxy spike results

`aimux model proxy serve` exposes the Anthropic Messages API on IPv4 loopback. A shared Codex app-server starts on the first message, with a new ephemeral thread for each request. The proxy passes through text deltas, interrupts after the first tool call, and returns that call for Claude Code to execute. The next request can replay the call and its result. An HTTP disconnect interrupts an active turn. The request after an app-server exit starts a new process.

```sh
aimux model proxy serve --port 7382
ANTHROPIC_BASE_URL=http://127.0.0.1:7382 ANTHROPIC_API_KEY=local claude
```

Use `--codex-command` to select the executable. The default prefers `codex-lean` on PATH and falls back to `codex`. The proxy accepts any API key. The endpoints are `POST /v1/messages`, `POST /v1/messages/count_tokens`, and `GET /v1/models`. Counting tokens estimates the request body size divided by four. Claude model names select `gpt-6.2-sol` with low effort. An explicit `codex:<id>` leaves effort to Codex.

## Verification

| Check | Result |
| --- | --- |
| `gofmt` on the added and changed Go files | Clean |
| `go vet ./internal/modelproxy/ ./cmd/...` | Passed |
| `go test ./internal/modelproxy/ ./cmd/aimux/... -timeout 300s` | Passed for all three packages with a temporary writable home |
| `go test -race ./internal/modelproxy/ -count=1 -timeout 300s` | Passed |
| Built CLI and loopback HTTP checked by a source-blind validator with the fixture | Passed all six checks and the tool-handoff probe |
| `AIMUX_LIVE_CODEX=1 go test ./internal/modelproxy/ -run '^TestLiveCodex$' -v -count=1 -timeout 300s` | not measured (sandbox) |

The tests build a Go app-server fixture and use real subprocess pipes. They assert the request mapping, SSE frames and flush timing, JSON responses, interrupted turns, and error responses. They also cover two interleaved turns with replies arriving out of order, disconnects before and after the turn-start acknowledgement, and restart after a subprocess exit. The command-line validator saw a crash return HTTP 502 and the next request succeed on the same HTTP listener.

The opt-in run used the installed Codex 0.170.0 through `codex-lean`. The default app-server failed to open its SQLite memories database and exited with status 1 before replying to `initialize`. Redirecting the state database to a writable directory with `CODEX_SQLITE_HOME` still hit the proxy's 15-second handshake deadline. Both attempts produced HTTP 502. A real app-server with a fresh temporary Codex home initialized and sent requests to a loopback capture server. That setup inspected the outbound request without access to the authenticated model.

The command packages include an export test that writes under `~/.aimux/exports`. The focused suite passed after pointing the test process at a writable temporary home. The worktree also needed the ignored dashboard build assets for Go embedding. The full `go test ./... -timeout 300s` run still had failures in `acprun`, `discovery`, `mcpproxy`, and `provider` around local process discovery and exit monitoring. The sandbox denied `/bin/ps`, which the provider tests invoke.

## Open questions

### Token overhead

**not measured (sandbox).** The spec's earlier probe reported about 16.6k input tokens for a tiny request. The authenticated app-server exited before a turn here, so there are no new `inputTokens` values for the baseline or any of these variants.

| Variant | Input tokens |
| --- | --- |
| Baseline | not measured (sandbox) |
| `include_environment_context=false` | not measured (sandbox) |
| `include_permissions_instructions=false` | not measured (sandbox) |
| `include_apps_instructions=false` | not measured (sandbox) |
| `include_collaboration_mode_instructions=false` | not measured (sandbox) |
| Host skill discovery off with `skip_host_skill_discovery` | not measured (sandbox) |

The spike retains the supplied instruction settings and disables the `skill_search` tool as specified. The context switches need a logged-in run before choosing defaults.

### Tools offered to the model

The authenticated tool inventory is **not measured (sandbox)**. With `experimentalRawEvents: true`, a real Codex 0.170.0 process using a fresh home sent the following tool lists to a loopback Responses capture server. Each request offered one dynamic tool, `Lookup`, to app-server.

| Model and config | Tools in outbound request |
| --- | --- |
| `gpt-5.4`, `update_plan` enabled | 2, `update_plan` and `Lookup` |
| `gpt-5.4`, `request_user_input` enabled | 2, `request_user_input` and `Lookup` |
| `gpt-5.4`, both disabled | 1, `Lookup` |
| Bundled `gpt-6.2-sol`, both disabled | 0 |
| Bundled `gpt-6.2-astra`, both disabled | 0 |

The direct-tool control confirms that `tools.update_plan.enabled=false` suppresses `update_plan`. The spike also sets `tools.experimental_request_user_input.enabled=false` based on the capture. Both bundled GPT-6.2 profiles reported `tool_mode: code_mode_only` from `codex debug models`. Whether the authenticated catalog exposes the dynamic tools on the advertised models remains the main compatibility question. [Codex 0.170.0 tool-mode selection](https://github.com/openai/codex/blob/rust-v0.170.0/codex-rs/core/src/tools/mod.rs#L66)

### Continuing after a tool result

Real Codex 0.170.0 acknowledged `turn/start` with `input: []` as `inProgress` after injecting the call and its result. The loopback capture contained both the `function_call` and the `function_call_output` with `PAPAYA`. The implementation sends an empty array on tool-result-only turns. Continuation by the authenticated model is **not measured (sandbox)**.

### SDK coverage

**Use the minimal JSON-RPC client.** The SDK v0.167.2 types cover `thread/inject_items`, the `experimentalApi` initialization capability, and the `item/tool/call` server request. Its manually maintained `ThreadStartParams` omits `dynamicTools`, and the high-level initializer supplies only client info. The proxy implements the required wire subset and keeps the existing Go dependencies. The installed 0.170.0 CLI generated the protocol schema used for the wire shapes. [SDK thread parameters](https://github.com/pmenglund/codex-sdk-go/blob/v0.167.2/protocol/manual_types.go#L843) · [SDK initializer](https://github.com/pmenglund/codex-sdk-go/blob/v0.167.2/codex.go#L27)

## Before taking the spike further

Verify the authenticated tool inventory and complete a tool round trip on each advertised GPT-6.2 model. Measure input tokens for the baseline and each context switch using an identical prompt. A production implementation would also need Anthropic compatibility beyond the mapped request fields, multiple tool calls per step, and load testing for long-running streams and large histories.

## Live verification outside the sandbox

Run on the owner's Mac with the logged-in Codex 0.170.0, after the sandboxed build.

| Check | Result |
| --- | --- |
| `go test ./internal/modelproxy/ ./cmd/aimux/... -count=1` | Passed |
| `AIMUX_LIVE_CODEX=1 ... -run '^TestLiveCodex$'` | Passed: tool turn, then continuation from `input: []` after the tool result |
| Claude Code end to end (`ANTHROPIC_BASE_URL=http://127.0.0.1:7392 claude -p ... --model codex:gpt-6.2-sol`), Read then Edit then Bash in a scratch folder | Passed in 24 s: the file changed to `hello world`, the answer was correct, three tool calls, and the owner's `PreToolUse`/`PostToolUse` hooks ran for Read, Edit and Bash per the session's debug log |

The first two Claude Code runs exposed three gaps the fixture could not, each now fixed test-first with a mutation check:

1. Claude Code sends `role: "system"` messages inside `messages`, including after the final user message. They map to `developer` messages in place; the final user message is the last non-system one. When reminders follow it, that user text is injected as history ahead of them and the turn starts with `input: []`, so Codex sees them in order (checked live: an injected question plus a trailing "answer in French" reminder returned `quatre`).
2. Codex 0.170 rejects dynamic tools named `mcp` or starting with `mcp__` (`thread_processor.rs`), which is every Claude Code MCP tool. The proxy sends `mcp_<rest>` and maps calls back through `TurnRequest.ToolNames`; a rename collision is a 400. The fixture now rejects reserved names the way Codex does.
3. Codex's environment and permissions context named the proxy's empty read-only cwd, so the model read the wrong path and refused the edit. The app-server now starts with `include_environment_context=false`, `include_permissions_instructions=false` and `include_apps_instructions=false`; Claude Code's system prompt carries the real environment.

## Tools the logged-in model sees

With `experimentalRawEvents`, the `additional_tools` item for `gpt-6.2-sol` offered: the dynamic tools, `functions.exec` (JavaScript in a fresh V8 isolate, the only route to the dynamic tools in this code-mode-only profile), `functions.wait`, `functions.send_message_to_user_async`, and a `collaboration` namespace (`spawn_agent`, `send_message`, `followup_task`, `wait_agent`, `list_agents`, `interrupt_agent`). The collaboration tools stayed after disabling `multi_agent` and `collaboration_modes`.

Asked to use `exec` without the dynamic tools, the model reported `require`, `process`, `Deno` and `fetch` as undefined and `import('node:fs')` as "unsupported import in exec": the isolate reaches no files or network, so file access still goes through Claude Code. That is the model's report of its `exec` output; the raw cell output was not inspected. Still open: interrupt and return an error when Codex starts a sub-agent, since sub-agents would run outside Claude Code.

## Measured numbers

- Input tokens per step: 16,132 before the context switches, 15,904 after. The bulk is elsewhere (likely the global AGENTS.md, the skills list and the code-mode tool descriptions); not yet attributed.
- Usage counts against the ChatGPT plan's Codex limit (`account/rateLimits/updated`, `limitId: "codex"`).
- Default port 7382 is already taken by a running aimux process on the owner's machine; the end-to-end run used 7392. Pick a free default before shipping.

## Lean catalog (follow-up)

At startup the proxy runs `<codex command> debug models` (Codex refreshes the catalog from OpenAI first), rewrites every model to `tool_mode: direct`, removes `multi_agent_version`, sets `apply_patch_tool_type` to null and `experimental_supported_tools` to `[]`, writes the result to a 0600 temp file and starts app-server with `-c model_catalog_json=<file>` and `--enable omit_skill_descriptions`. If the catalog cannot be built, the backend refuses to start Codex rather than run with Codex's own tools. Base instructions open with a short preamble telling the model it is Claude Code's model and to ignore instructions addressed to Codex (the shared AGENTS.md tells Codex to read `~/.claude/rules`).

| Check | Result |
| --- | --- |
| Unit and fixture tests (`catalog_test.go`, fake app-server enforcing the lean file, its mode and the flags) | Pass; mutation checks 4/4 (code mode kept, skill flag dropped, preamble dropped, start without catalog) |
| Live test, continuation step | 5,633 input tokens (15,934 before) |
| Live test, tool step | Reported 0 at first (usage can arrive after the tool call, and an interrupted response reports none); fixed below, now 5,603 |
| Claude Code end to end, Read then Edit then Bash | Pass in 25 s, three tool calls, file changed, correct answer |

Measured with probes before the change: the lean catalog leaves the dynamic tools as the only tools offered (no `exec`, `wait`, sub-agent, `apply_patch`, `clock` or async message tools).

## Hardening (follow-up)

| Change | Behavior | Evidence |
| --- | --- | --- |
| Usage on tool steps | The tool call is handed to Claude Code at once; the interrupt waits for the step's usage report, at most 3 s | Live: tool step 5,603 input tokens (was 0); usage arrived 0.00 to 0.07 s after the call in three runs; mutation check |
| Proxy key | Every request needs the key from the Keychain entry `aimux-model-proxy` (account `aimux`), as `x-api-key` or `Authorization: Bearer`; constant-time compare; an empty key rejects everything. `serve` creates a random 32-byte key on first start and never replaces one it cannot read | Unit tests on a real key file; live: a request without the key got 401; mutation checks 2/2 |
| Crash retry | When Codex exits before anything reached the client, the turn is retried once on a fresh process | Fixture scenario that crashes once; mutation checks 2/2 (no retry; retry after output) |
| Version check | `<codex> --version` is read once; a major.minor other than 0.170 gets a warning on stderr | Fixture versions 0.170.0, 0.170.4, 0.171.0; mutation check |
| Default port | 7385 (7382 is used elsewhere in aimux and was taken on the owner's machine) | Command test |

`claude-codex [model]` in `~/.zshrc` reads the key from Keychain, checks the proxy answers, and points Claude Code at `http://127.0.0.1:7385` with `codex:<model>`. End to end from a fresh shell: Read, Edit, Bash, correct file and answer.

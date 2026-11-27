# Tool calls in the ledger: evidence

What Claude Code hands a hook for each tool call, measured before the stage 5 plan was written. One throwaway `claude -p` session (Claude Code 2.1.332) ran with hooks that saved their input: two shell commands (one failing), one file read, and one `general-purpose` subagent that ran a shell command. Only the names of the fields are recorded here, never their content.

## What a hook is given

| Event | Fields that identify | Fields that describe |
|---|---|---|
| `UserPromptSubmit` | `session_id`, `prompt_id` | (the prompt, not read) |
| `PreToolUse` | `session_id`, `prompt_id`, `tool_use_id` | `tool_name`; `tool_input` (not read) |
| `PostToolUse` | `session_id`, `prompt_id`, `tool_use_id` | `tool_name`, `duration_ms`; `tool_input`, `tool_response` (not read) |
| `PostToolUseFailure` | `session_id`, `prompt_id`, `tool_use_id` | `tool_name`, `duration_ms`, `is_interrupt`; `error` (not read) |
| any of the three tool events, inside a subagent | the same, plus `agent_id` and `agent_type` | |
| `SubagentStop` | `session_id`, `prompt_id`, `agent_id`, `agent_type` | |

Every event also carries `transcript_path`, `cwd`, `permission_mode` and `hook_event_name`.

## What follows

- **A tool call can be joined to its session, its turn and its subagent.** `tool_use_id` matched between the event before a call and the one after it, for all five calls. All events of the session carried one `session_id`, the subagent's included.
- **A failure is told apart without reading output.** A shell command that exits non-zero arrives as `PostToolUseFailure`, with `is_interrupt` saying whether it was stopped by the owner.
- **The duration is given.** `duration_ms` is Claude Code's own measure; the hook does not have to time anything.
- **The kind of subagent is known here and nowhere else.** `agent_type` (`general-purpose`, `code-reviewer`) is on every tool event a subagent makes. The model proxy sees the same `agent_id` on that subagent's model calls (the `X-Claude-Code-Agent-Id` header) and no kind. A ledger holding both can say what kind of subagent made a model call.
- **The kind is known before the subagent's first model call.** The call that starts a subagent is itself a tool call (`tool_name` `Agent`), and its input names the kind. This plan does not read tool input; stage 6 decides whether that one field may be read.
- **`prompt_id` identifies the turn directly.** The turn rule in `2026-11-25-turn-identity.md` has the hook count the user prompts in the transcript. Whether `prompt_id` or the count agrees better with the proxy's turn number is measured in task 1.
- **No new process per tool call.** On the owner's machine `aimux remote hooks notify` already runs on `PostToolUse` and `PostToolUseFailure` (installed for permission alerts). Starting aimux once takes 22 ms at the median (15 runs of `aimux version`, at most 24 ms). The ledger rides the process that is already there.

## Second run: telemetry beside hooks

The owner asked why not OpenTelemetry. Claude Code already exports telemetry events to the collector in his sessions, so one two-turn session through the model proxy was observed three ways at once: its hooks, its telemetry (sent to a capture on a spare port as JSON), and its transcript. It made eleven tool calls: shell commands (one failing), a file read, a tool search, an MCP call, two subagents started together, and a subagent started by a subagent.

### Completeness

| Source | Tool calls seen |
|---|---|
| Transcript | 11 |
| Hooks (`PostToolUse` and `PostToolUseFailure`) | 11 |
| Telemetry (`tool_result`) | 11 |

The same eleven ids in all three. The telemetry's 14 `api_request` events equal the 14 model calls the proxy wrote to the ledger for that session.

### What each carries

| Fact | Hook | Telemetry |
|---|---|---|
| Session, tool call id, tool name, duration, failed or not | yes | yes (`session.id`, `tool_use_id`, `tool_name`, `duration_ms`, `success`) |
| The turn | `prompt_id` | `prompt.id`, the same value |
| A class of failure, without its text | no (`error` is text) | yes (`error_type`, for example `ShellError`) |
| The subagent | `agent_id` | `agent_id` |
| The subagent that started this subagent | no | yes (`parent_agent_id`) |
| The kind of subagent | `agent_type`, on tool events | `agent.name` on every model call (`api_request`), and `agent_type` on `subagent_completed` |
| Which model call asked for a tool call | no | yes: `request_id` on `tool_result` matched an `api_request` for all 11 |
| Model calls, with tokens, for a session that does not go through the proxy | no | yes (`api_request`) |
| The name of an MCP tool | yes (`mcp__aimux__list_sessions`) | no: `tool_name` is `mcp_tool` |
| Sizes of a tool's input and result, in bytes | no | yes |

### What telemetry does badly

- **It loses what it could not deliver.** With the capture down for the first 30 seconds of a session, the two tool calls made in that time never arrived; the three made after it came up did. Nothing is kept for later.
- **MCP tool names need a setting that also sends content.** With `OTEL_LOG_TOOL_DETAILS=1` the events gain `tool_input` and `tool_parameters`: the command, the arguments. `tool_name` still reads `mcp_tool`. That setting stays off.
- **It carries things a ledger line may not hold.** Every event has the account's email and ids; `user_prompt` has the prompt when prompt logging is on, which aimux turns on for traces. The collector must copy named fields only.
- **Only sessions with telemetry switched on.** The owner's shells have it.

### What hooks do badly

- **The identifying fields come after the content.** A `PostToolUse` input is written `session_id`, …, `tool_name`, `tool_input`, `tool_response`, `tool_use_id`, `duration_ms`. Today's hook decodes the whole object under a 1 MiB cap, so a large result loses the id and the duration.
- **A process per tool call**, on the hook the owner's permission alerts depend on.

### Turns

The session's four values of `prompt.id` map onto the proxy's turns 1 to 4 in order. A turn began not only when the owner typed: two began when a subagent that had been running in the background reported back. One subagent call was numbered turn 1 by the proxy and carried the second `prompt.id` in telemetry: the proxy gives a side call the turn that is current when the request arrives. `prompt.id` is the harness's own word for the turn, and where the two differ it is the one to believe.

### Which carrier

Telemetry, as the first and main source: it is complete on the normal path, it is already flowing, it costs nothing per tool call, it cannot disturb the permission alerts, and it carries three things nothing else does (the parent subagent, the model call behind a tool call, model calls of sessions outside the proxy). Its two gaps are known and can be shown: what was lost while the collector was down (every event has a sequence number per session, so a hole is visible), and MCP tool names (the MCP proxy's own line names the tool). A hook that reports is added only if a week of real use shows those gaps matter.

## Not measured

- Whether an MCP request reaching the MCP proxy carries the `tool_use_id`, which decides how its line is joined to the telemetry's.
- How long telemetry keeps trying when the collector is down briefly (a restart of a few seconds), as against the 30 seconds tested.
- Whether a session that ends at once after its last tool call still delivers its last batch.
- The Codex harness.

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
- **MCP tool names come only with a setting that also sends content.** With `OTEL_LOG_TOOL_DETAILS=1` the events gain `tool_input` and `tool_parameters`: the command, the arguments. `tool_name` still reads `mcp_tool`; the server and the tool are inside `tool_parameters`. The sessions aimux starts have that setting on, and prompt logging too (`OTELEnv`, for the trace view), so the collector already receives that content and must copy named, checked fields only.
- **It carries things a ledger line may not hold.** Every event has the account's email and ids; `user_prompt` has the prompt when prompt logging is on, which aimux turns on for traces. The collector must copy named fields only.
- **Only sessions with telemetry switched on.** The owner's shells have it.

### What hooks do badly

- **The identifying fields come after the content.** A `PostToolUse` input is written `session_id`, …, `tool_name`, `tool_input`, `tool_response`, `tool_use_id`, `duration_ms`. Today's hook decodes the whole object under a 1 MiB cap, so a large result loses the id and the duration.
- **A process per tool call**, on the hook the owner's permission alerts depend on.

### Turns

The session's four values of `prompt.id` map onto the proxy's turns 1 to 4 in order. A turn began not only when the owner typed: two began when a subagent that had been running in the background reported back. One subagent call was numbered turn 1 by the proxy and carried the second `prompt.id` in telemetry: the proxy gives a side call the turn that is current when the request arrives. `prompt.id` is the harness's own word for the turn, and where the two differ it is the one to believe.

### Which carrier

Telemetry, as the first and main source: it is complete on the normal path, it is already flowing, it costs nothing per tool call, it cannot disturb the permission alerts, and it carries three things nothing else does (the parent subagent, the model call behind a tool call, model calls of sessions outside the proxy). Its two gaps are known and can be shown: what was lost while the collector was down (shown by comparing the ids in the ledger with those in the transcript; the events' own sequence number does not show it, since it counts every kind of event for a process and is not reset or kept across sessions), and MCP tool names (present when tool details are on). A hook that reports is added only if a week of real use shows those gaps matter.

## Third run: outages, a killed session, MCP names

Task 1 of the plan. Each row is one throwaway session whose telemetry went to a capture that closed and reopened its own port; the count is against the session's transcript.

| What was done | Tool calls in the transcript | Arrived by telemetry | What was lost |
|---|---|---|---|
| Capture down for 3 s in the middle | 10 | 7 | the calls made while it was down, and the one made just before |
| Down for 10 s | 10 | 7 | the same |
| Down for 30 s | 11 | 7 | the same |
| Session killed (`kill -9`) by its own last shell command | 3 | 2 | only the call that did the killing, which never finished |

- **Any outage loses calls, however short.** Nothing is sent again: a batch that could not be delivered is dropped, and with it the events that were waiting in it. A restart of the collector, as `aimux upgrade collector` does, therefore loses the tool calls every open session makes in those seconds and just before.
- **A session that dies delivers what it had finished.** Both calls that completed before the kill arrived.
- **MCP names are there when tool details are on.** `tool_parameters` then holds `mcp_server_name` and `mcp_tool_name` for an MCP call, on both `tool_decision` and `tool_result`. For a shell command the same attribute holds the command, so only those two keys may be read from it.
- **A failure has a class.** A file that could not be read arrived as `success` false with `error_type` `TelemetrySafeError`; a failed shell command as `ShellError`.

### What this changes

Telemetry alone would leave holes after every upgrade. The transcript closes them without a hook: it is on disk, it holds every tool call's id and name and whether its result was an error, and reading those three things is not reading content. So the collector's lines are the live record, and a tool call the transcript has and the ledger lacks is added from the transcript, marked as such, with no duration (the transcript does not give one). The plan gains that as a task.

## Fourth run: what the model proxy can read from the traffic

The owner asked what the alternatives were and whether they had been studied. One had not: every request a harness sends to the model holds the conversation so far, with the tool calls the model asked for and the results that came back. A recorder was put between Claude Code and the model proxy for one two-turn session (the same kind as the second run, twelve tool calls). It kept only ids, tool names and the failed flag, and the names of the headers.

| Source | Tool calls seen | The same twelve as the transcript |
|---|---|---|
| Transcript | 12 | |
| Hooks | 12 | yes |
| Telemetry | 12 | yes |
| The traffic: asked for in an answer | 12 | yes |
| The traffic: result seen in a later request | 12 | yes |

What the traffic gives, for each of the twelve:

- **The id and the name**, the moment the model asks for the tool, and again in every later request of that conversation.
- **The full name of an MCP tool** (`mcp__aimux__list_sessions`), with no telemetry setting involved.
- **Failed or not**, from the result's `is_error`: the same answer as the hook's for all twelve.
- **The subagent and its parent**, from two request headers: `x-claude-code-agent-id` matched the hook's `agent_id` on every call, and there is an `x-claude-code-parent-agent-id`.
- **What a run has done so far, at the time of each request.** A subagent's second request already lists the tools its first answer used. This is the only source that the proxy has in hand when it would choose a model.

What it does not give:

- **How long a tool took.** The gap between the answer that asked for a tool and the request that brought its result was about one second more than the true duration for a single call, and far off when calls ran together (a six second command read as 35 seconds, because its result came back in the same request as a slow call beside it).
- **The kind of subagent.** No header carries it.
- **Anything about a session that does not go through the proxy.**
- **A tool call whose result is never sent back**, such as the last one of a session that is killed. All twelve came back here.

Two of the nineteen requests belonged to another session on a small model: the harness's own side work, as the earlier evidence found.

### Which carrier, revisited

The proxy, for the record. It saw exactly what the other three saw, it is already the writer of the session's model-call lines, a tool call belongs to the model call that asked for it without any joining, and when the proxy is down the session cannot run, so there is no outage in which calls are lost. Telemetry then adds only what the proxy cannot know (a true duration, the kind of subagent), and the transcript is needed only for sessions outside the proxy.

## Not measured

- A tool call refused at a permission prompt: whether it leaves a `tool_result`. The attempt did not produce a refusal (the owner's settings allow shell commands), so it is still open.
- An interactive session, and one continued after `/clear`. The resumed session of the second run was complete.
- Whether an MCP request reaching the MCP proxy carries the `tool_use_id`.
- Which session id the installed collector files a session's events under.
- The Codex harness.

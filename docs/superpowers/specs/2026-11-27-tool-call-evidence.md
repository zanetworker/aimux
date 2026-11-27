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

## Not measured

- An MCP tool call: whether its events have the same shape, and what its `tool_name` looks like.
- Whether an MCP request reaching the MCP proxy carries the `tool_use_id` (the tracker's unverified item 2).
- The hook's cost with the report added, with a large tool result, and with the collector down (the tracker's unverified item 4).
- A session with more than one subagent running at once, and a subagent started by a subagent.
- The Codex harness.

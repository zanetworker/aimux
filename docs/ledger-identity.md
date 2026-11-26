# Ledger identity contract

**What exists today:** in `internal/ledger`, the line type (schema `aimux.ledger/1`), the counting rule, a writer (`OpenWriter`: one file per writer, numbered lines, failed writes counted) and a reader (`ReadDir`: every `*.jsonl` file in the folder, lines of another schema skipped and counted), with tests. The model proxy writes a `model_call` line per request that reached its engine, with the session, a turn number and a call id it assigns as the request arrives (`internal/modelproxy/ledgerline.go`, `turns.go`). **Planned, not built:** the other writers (the collector for hooks, the MCP proxy, the tool gateway), and the run id. This document is the contract those will follow.

The ledger is a folder of JSON lines files, one file per writer. This page says which ids each line carries, where each writer gets them, and the rule a reader uses so every action is counted once. The code for the rule is `ledger.Count` in `internal/ledger`.

## The ids

Every line carries up to four ids, and the join is decided when the line is written. Nothing is joined afterwards by timing or text.

| Id | Is | Comes from |
|---|---|---|
| run | one Wingman question, spanning its stages | new: the run engine mints it per question and passes it to each stage with the lease. `profilerun.RunState.Children` already maps each stage to its session. Never inferred from a sandbox name |
| session | one conversation in a harness | the harness's own session id, in full. The 8-character tag derived from it stays for display only |
| turn | one user request and everything done for it | a number per session, derived by the model proxy and the hook each on its own; the rule and its evidence are in `superpowers/specs/2026-11-25-turn-identity.md` |
| call | one tool invocation, or one model request | for a tool, the harness's own id for it (`tool_use_id` in Claude Code, the call id in Codex): hooks receive it, and the model proxy sees it in the conversation. For a model request, the model proxy mints one |

Each line also carries its writer's name and a sequence number that only goes up for that writer. Hook events carry an event id: the call id plus the hook event's name.

## Which lines carry which ids

| Kind | Written by | Ids it carries |
|---|---|---|
| `model_call` | model proxy | session, turn, and a call id the proxy mints. A subagent's call carries its parent's session and its agent id and is marked as a side call. A background call has no session in its request body; its session comes from the `X-Claude-Code-Session-Id` header |
| `tool_call` (primary) | collector, from a hook | session, turn, the harness's call id, and the event id |
| `tool_call` (primary) | tool gateway, in a sandbox | run, session and call id. A call from an admin-token caller has no session |
| `tool_call` (secondary) | MCP proxy | the call id when the MCP request carries it; the proxy does not know the session today |
| `verdict` | whoever reached it: model proxy, MCP proxy, tool gateway, or collector for a hook's refusal | the ids of the call it ruled on. A tool gateway verdict for an admin-token caller has no session |
| `switch` | model proxy | session and turn. When the switch started a new session, the old one is `session` and the new one is `to_session` |

## The counting rule

A harness's call id is only unique inside its session, so the reader identifies a call by session and call id together. The one exception is a secondary line, which carries no session and can only be matched to a primary line by call id alone.

A reader applies these rules when it reads, so the order lines arrive in does not matter.

1. Two lines from the same writer and session with the same event id are one line, and the earliest is kept. This is how a hook event delivered twice is counted once.
2. A secondary tool call is dropped when a primary tool call has the same call id. The primary line is the record; the secondary one is only a cross-check.
3. A secondary tool call with no call id is listed as unmatched. It is never counted and never silently dropped.
4. A secondary tool call whose call id no primary line has is counted, once. It is then the only record of that call. It counts even without a session, which the MCP proxy never has today: the call happened, and who made it is unknown.
5. Any line with no session is listed as unmatched, except a line written by the tool gateway, because an admin-token caller has no session and its calls and verdicts are still real.
6. The order of the input lines does not change the result.
7. Two primary tool calls with the same session and call id are one invocation, and the earliest is kept. This covers a sandbox that has hooks, where the collector and the tool gateway would both report the same call.

When rules overlap they apply in this order: duplicates are removed first (rule 1), then a secondary tool call that a primary line covers is dropped (rule 2), even when it has no session, and only then are lines with no call id or no session set apart (rules 3 and 5).

The reader also reports gaps: for each writer, the runs of sequence numbers missing between the lowest and highest it saw, as ranges. A missing number is a lost line. Ranges keep the report small when one sequence number is corrupt.

## Known limits of the rule

These follow from what the writers can know today, and each is a reason the MCP proxy's lines are only a cross-check until it learns the session.

- **A secondary line is matched by call id alone.** The MCP proxy knows no session, so if two sessions used the same call id, a hook's line in one would stand in for the proxy's line in the other, and two calls seen only by the proxy would count as one. Claude Code's tool-use ids are unique across sessions in practice; Codex's have not been checked. The fix is the one the design already names: bind each MCP connection to a session, so the proxy's lines carry it.
- **Two primary lines for one call keep only the earliest.** If the collector and the tool gateway both report a sandbox call, the ids the later line alone carried (the run on the gateway's line, the turn on the collector's) are not merged into the kept one. Writers that can both see a call must each attach every id they know.
- **A gateway line with a session but no call id is counted as it stands.** If the collector reported the same call with a call id, the two cannot be told to be one call. Whether a gateway request carries the harness's call id is the first item below.

## Not yet confirmed

Two assumptions this contract rests on have not been tested yet:

- Whether an MCP request carries the harness's call id to the MCP proxy and the tool gateway. If it does not, the MCP proxy's secondary lines have no call id and all land in unmatched, which limits the cross-check but cannot cause double counting.
- That Codex hooks receive a call id the model proxy also sees. If they do not, a Codex tool call reported by its hook cannot be joined to the model call that asked for it.

## The file format

One JSON object per line, in `~/.aimux/ledger/<writer>.jsonl` (folder 0700, files 0600). Every line names its `schema`. Field names follow OpenTelemetry's generative-AI conventions where one exists (`gen_ai.request.model`, `gen_ai.response.model`, `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`), version 1.27.0 (the first with `gen_ai.usage.input_tokens`), recorded on each line as `semconv`; aimux's own fields start with `aimux.`. A line says how an action went and never what it said: no prompt, answer, tool argument or path.

Two rules keep a mix of versions safe. A reader that meets a line of a schema it does not know skips it and reports how many it skipped, and any total over them is incomplete. A writer that finds such a line at the end of its own file refuses to write there.

A writer continues its numbering from the last line on disk. A write that fails is counted, and its number is not used again, so a reader sees the loss as a gap. One limit: lines lost at the very end of a file, just before a restart, leave no gap, because the numbering picks up from what is on disk. They show only in the writer's health answer (its latest number and its count of failed writes) while it runs.

`gen_ai.usage.input_tokens` is the whole prompt, as OpenTelemetry defines it; the part read from a cache is included in it and also given on its own as `aimux.usage.cache_read_input_tokens`. Only one process may have a ledger file open for writing; a second is refused.

A model call carries `aimux.role`, the kind of work it was: `main`, `subagent` or `background`. It is decided from the facts of the call (its session, a subagent's id, whether the request itself named the session), never from the model named, and a request does not say which kind of subagent sent it. `aimux.side` stays, true for the two that are not `main`. A line written before roles has no `aimux.role`; a reader gets its role by the same rule from `aimux.agent` and `aimux.side`, which it does have.

A model call that failed carries `http.response.status_code`, the status the session was given: 429 for a rate limit, 401 for a refused login, 502 when the proxy itself had to answer for an engine that gave none. It is absent on a call that succeeded and on one that ended before any answer; `aimux.outcome` still holds the writer's own word for what went wrong. The field is an addition to schema 1: a reader that does not know it ignores it.

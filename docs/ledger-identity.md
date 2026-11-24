# Ledger identity contract

**What exists today:** the line type and the in-memory counting rule in `internal/ledger`, with tests. **Planned, not built:** the ledger files, the writers in each service, and the assignment of ids at run time. This document is the contract those will follow.

The ledger is a folder of JSON lines files, one file per writer. This page says which ids each line carries, where each writer gets them, and the rule a reader uses so every action is counted once. The code for the rule is `ledger.Count` in `internal/ledger`.

## The ids

Every line carries up to four ids, and the join is decided when the line is written. Nothing is joined afterwards by timing or text.

| Id | Is | Comes from |
|---|---|---|
| run | one Wingman question, spanning its stages | new: the run engine mints it per question and passes it to each stage with the lease. `profilerun.RunState.Children` already maps each stage to its session. Never inferred from a sandbox name |
| session | one conversation in a harness | the harness's own session id, in full. The 8-character tag derived from it stays for display only |
| turn | one user request and everything done for it | new; the model proxy and the hook both derive it |
| call | one tool invocation, or one model request | for a tool, the harness's own id for it (`tool_use_id` in Claude Code, the call id in Codex): hooks receive it, and the model proxy sees it in the conversation. For a model request, the model proxy mints one |

Each line also carries its writer's name and a sequence number that only goes up for that writer. Hook events carry an event id: the call id plus the hook event's name.

## Which lines carry which ids

| Kind | Written by | Ids it carries |
|---|---|---|
| `model_call` | model proxy | session, turn, and a call id the proxy mints. Background model calls arrive with no session |
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

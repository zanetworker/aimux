# Roles and limits: what real sessions showed

<!-- no-images: a record of four runs and their tables; the plan it corrects carries the figure -->

This is task 0 of `../plans/2026-11-26-stage-4-right-model-per-task.md`. Nothing was built. Four real Claude Code sessions (version 2.1.332, non-interactive) ran through a model proxy with both engines on a spare port. In front of it sat a recorder that wrote the shape of each request and answer: header names, the model named, metadata key names, counts. It wrote no prompt, no answer and no credential. The ledger of that proxy gave the rest.

## Answers

| Question | Answer | Seen in |
|---|---|---|
| Does a subagent whose definition names `model: codex:gpt-6.2-sol` reach the proxy naming it? | Yes. The request named `codex:gpt-6.2-sol`, carried the agent id, went to Codex and was answered | runs 1 and 2 |
| Does `CLAUDE_CODE_SUBAGENT_MODEL` accept a `codex:` name? | Yes, for a subagent of the owner's own that names no model: it then named `codex:gpt-6.2-astra` | run 2 |
| Does a model named in a subagent's definition win over that variable? | Yes: the pinned subagent stayed on `codex:gpt-6.2-sol` with the variable set to another model | run 2 |
| Does the variable move the built-in Explore subagent? | No. Explore named the session's model with and without the variable | runs 1 and 2 |
| Does a request say which kind of subagent sent it? | No. It carries `x-claude-code-agent-id`, an id, and nothing naming the kind. Claude Code knows the kind (its own diagnostic line prints `agent:custom:pinned-impl`) and does not send it | runs 1 and 2 |
| Is a helper call on a small model told apart from a main conversation moved to that model? | The question was wrong. Inside one session no call named a smaller model than the session's. The 60 small-model calls counted in the plan were whole sessions of their own, each with its own session id | the owner's ledger, and run 1 |
| What does a plan limit look like? | Not captured: none occurred. But every answer from Anthropic on the owner's login carries the state of his plan's allowance in its headers, so a limit does not have to be guessed from a status code | run 3 |

## The runs

A scratch project held two subagent definitions of the test's own: `pinned-impl` (`model: codex:gpt-6.2-sol`) and `unpinned-impl` (no model). The session ran on `claude-sonnet-5-5` and was asked to use each of them and the built-in Explore subagent.

**Run 1, no subagent default.** One session, nine model calls:

| Caller | Model named | Engine | Agent id |
|---|---|---|---|
| main conversation, 5 calls | `claude-sonnet-5-5` | anthropic | none |
| `pinned-impl` | `codex:gpt-6.2-sol` | codex | yes |
| `unpinned-impl` | `claude-sonnet-5-5` | anthropic | yes |
| Explore, 2 calls | `claude-sonnet-5-5` | anthropic | yes |

**Run 2, `CLAUDE_CODE_SUBAGENT_MODEL=codex:gpt-6.2-astra`.** One session, ten model calls:

| Caller | Model named | Agent id |
|---|---|---|
| main conversation, 4 calls | `claude-sonnet-5-5` | none |
| `pinned-impl` | `codex:gpt-6.2-sol` | yes |
| `unpinned-impl` | `codex:gpt-6.2-astra` | yes |
| Explore, 4 calls | `claude-sonnet-5-5` | yes |

Each subagent answered what it was asked to, on whichever engine it ran.

**Run 3, one call on `claude-haiku-5-5`,** to read the answer's headers. A main conversation on a small model looks like any other: a session id in the body, no agent id.

**After run 1** one more request arrived under a different session id, on `claude-sonnet-5-5`, with no agent id. It was a session of its own, started by something on this machine that runs Claude Code and inherits the shell's variables. That is what the plan had read as helper calls.

## What every request carries

- Header `x-claude-code-session-id` on every model call, and the same id as `session_id` inside `metadata.user_id` (with `account_uuid` and `device_id`).
- Header `x-claude-code-agent-id` on a subagent's calls only.
- Nothing else that tells one kind of call from another. A subagent's request has fewer tools (1 for the test's own, 11 for Explore, against 16 for the main conversation), which is a property of the definition and not an identity.

No call arrived without a session id in its body, so the "background" case of the role rule was not seen in these runs.

## The allowance is in every answer

On the owner's subscription login, each answer from Anthropic carried:

| Header | Value seen | Reading |
|---|---|---|
| `anthropic-ratelimit-unified-status` | `allowed` | whether the plan allows the request |
| `anthropic-ratelimit-unified-representative-claim` | `seven_day` | which window is the binding one |
| `anthropic-ratelimit-unified-7d-utilization` | `0.36` | the share of the weekly allowance used |
| `anthropic-ratelimit-unified-7d-status` | `allowed` | that window's own state |
| `anthropic-ratelimit-unified-7d-reset`, `anthropic-ratelimit-unified-reset` | a time in seconds | when the window starts again |
| `anthropic-ratelimit-unified-fallback-percentage` | `0.5` | not known what it governs |
| `anthropic-ratelimit-unified-overage-status`, `-overage-disabled-reason` | `rejected`, `org_level_disabled` | paying beyond the plan is off for this account |

These pass through the proxy today, unread. Two consequences:

1. The owner's open question, whether there is a source for Claude's remaining allowance, has an answer: yes, on every call, at no cost.
2. A plan limit can be told from a short rate limit by the plan's own word (`unified-status`) and not by the status code alone. What that header says when the plan refuses was not seen; `rejected` is the value its neighbour uses, and is a guess until one is captured.

## What Claude Code says about a `codex:` name

For each call on a `codex:` model it prints one diagnostic line to its error output, `[claude-code:unrecognized_model]`, naming the model and the caller. The call still goes out and is answered. Its documentation says an entry for the model stops the line; that was not tried.

## What stayed unknown

1. What `anthropic-ratelimit-unified-status` holds when the plan refuses, and whether a short rate limit carries these headers at all.
2. Whether an interactive session makes calls a non-interactive one does not (a title, a summary), and what those carry. The owner's own interactive session in the ledger named only its own model and the Codex model he switched to.
3. Whether the built-in general-purpose subagent behaves as Explore did. Only Explore was run.
4. Whether `CLAUDE_CODE_SUBAGENT_MODEL` moves a subagent defined by a plugin. Only project definitions were run.
5. Each result is from one run of each case.

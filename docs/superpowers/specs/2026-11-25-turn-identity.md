# What identifies a turn

A turn is one user request and everything done for it. The ledger needs a turn id on every line, and the model
proxy and, later, a hook have to arrive at the same one without talking to each other. This page records what
each side can see in a real Claude Code session, and the rule that follows.

<!-- no-images: two tables and one rule; a figure would repeat them -->

## What was observed

Claude Code's non-interactive mode was run through a model proxy on a spare port, on Codex, with a temporary
line that recorded the shape of each request: which fields and headers were present, each message's role, and
the kinds of its content blocks. No text, argument or header value was recorded. The shapes are kept as fixtures
in `internal/modelproxy/testdata/turns/`.

| Request | Session id in the metadata | `X-Claude-Code-Session-Id` header | `X-Claude-Code-Agent-Id` header | Last user message holds |
|---|---|---|---|---|
| A new prompt | yes | yes, the same id | no | text only |
| A tool result going back | yes | yes | no | a `tool_result`, sometimes with a text block beside it |
| A subagent's request | yes, **its parent's id** | yes | **yes** | text only (its task) |
| A request under a Claude model name, seen in one run only | **not recorded** | yes | no | varies |
| The first prompt after `/compact` | yes | yes | no | text only |

Four things were not known before:

1. **A subagent is marked.** Its requests carry the header `X-Claude-Code-Agent-Id` and the parent's session id.
   Hooks receive the same id as `agent_id`. Nothing has to be inferred from the shape of the conversation.
2. **A tool result can come with text.** The harness sometimes adds a text block next to a `tool_result`. So
   "the last user message is not only tool results" is the wrong test; "it holds no `tool_result`" is the right one.
3. **The proxy only reads the session of requests that name a `codex:` model.** A request under a Claude model
   name is answered by Codex all the same, and the request history counts it as sessionless whatever its
   metadata holds. In one run, 16 of 27 requests were of that kind; three later runs had none, and what makes the
   harness send them was not found. The ledger reads the session from the metadata whatever model is named.
4. **After a compaction the message list starts again** from a summary. It held two prompts in a session that had
   had four, so a turn number cannot be counted from the list alone.

Messages with the role `system` also appear inside the list, after the user message they follow.

## The rule

- **Session.** The session id from the request's metadata; when there is none, the `X-Claude-Code-Session-Id`
  header; when there is neither, the line has no session.
- **Side.** A request is off the main conversation when it carries `X-Claude-Code-Agent-Id` (a subagent) or has
  no session id in its metadata. Its line is marked `aimux.side` and, for a subagent,
  carries the agent id as `aimux.agent`.
- **Turn.** Each session has a turn number. It goes up by one when a request that is not a side request arrives
  whose last user message holds no `tool_result`. Every other request takes the session's current number.
- **Never backwards.** The number is never below the count of such prompts in the request's own message list,
  and at start the proxy takes each session's last number from its ledger file. So a session the proxy forgot, a
  restarted proxy, and a compaction do not reuse a number.

The turn is written as a decimal number, counted per session: turn `3` of one session and turn `3` of another are
different turns. A reader identifies a turn by session and number together.

## What a hook has to do to agree

A hook is given `session_id`, `transcript_path` and, inside a subagent, `agent_id`. To reach the same number it
counts the user prompts in the session's transcript up to the event. The transcript keeps the prompts from
before a compaction, which the request's message list does not, so the hook's count is the fuller one; the
proxy's seed from its own ledger is what keeps the two in step across a compaction.

## Confirmed, and not

| Statement | State |
|---|---|
| A subagent's request carries its parent's session id and an agent header | Seen, in two runs |
| The session header and the metadata's session id are the same value | Seen, by comparing one-way hashes |
| A request under a Claude model name carries the session header | Seen (16 of 16, in one run) |
| Such a request carries a session id in its metadata, and what it is for | Not known. The first capture recorded only whether the proxy had attached a session, which it never does for those model names; later runs did not produce such requests |
| A hook's count of prompts in the transcript equals the proxy's turn number | Not checked. Slice 3, when the hook exists |
| An interactive session behaves like the non-interactive one | Not checked. Only `claude -p` was run |
| Whether an MCP `tools/call` request carries the harness's tool-use id | Not checked here. Slice 4 |
| Codex as a harness | Not looked at |

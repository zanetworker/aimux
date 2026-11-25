# How the proxy key and the login travel together

A Claude Code session on the subscription login sends that login in `Authorization`. The model proxy asks for
its own key in the same header today. For the proxy to forward a session to Anthropic, the key has to travel
somewhere else. This page records what a real session sends, and the decision that follows. It is task 1 of
[the stage 3 plan](../plans/2026-11-25-stage-3-second-engine.md).

<!-- no-images: three tables and one decision; a figure would repeat them -->

## Decision

**The proxy key travels as a path prefix in the base URL: `ANTHROPIC_BASE_URL=http://127.0.0.1:7385/k/<key>`.**

Both ways tried reach the proxy on `POST /v1/messages`. Only the path prefix is present on every request: the
header set with `ANTHROPIC_CUSTOM_HEADERS` is missing from `HEAD /api/hello`, so a proxy relying on it would
have to answer one path with no key at all.

What the choice asks of the proxy (task 3):

- Read the key from the first two path segments, compare it in constant time, and remove the prefix before
  anything else sees the path.
- Never write the unstripped path anywhere: not in the request log, the ledger, an error message or a
  response. A request log line shows the path after the prefix is removed.
- Keep the query string. Every `POST /v1/messages` carried one.
- Leave `Authorization` alone on its way to Anthropic, and never send it to Codex.

And of the Anthropic engine (task 2): read usage from a gzip-compressed stream, and ask Anthropic for `gzip` only
when the client offered it, so the answer is never in an encoding the reader cannot follow. The bytes of the
answer still reach the client unchanged.

## What was done

A throwaway forwarder listened on `127.0.0.1:7397` and passed every request to `https://api.anthropic.com`,
streaming flushed straight through, removing a leading `/k/test` when present. Per request it recorded the
method, path, status, duration, the names of the headers, the model named in the body, whether the test header
arrived, and the first eight hex digits of a SHA-256 of `Authorization`, of `X-Api-Key`, of the session header
and of the session id in the metadata. It recorded no header value and no body, and it removed the test header
before forwarding. Claude Code 2.1.332 ran in non-interactive mode on the subscription login, with no API key,
token or other provider setting in the environment.

## What was seen

| Question | Answer |
|---|---|
| Does a base URL with a path prefix arrive with the prefix kept, on every path? | **Yes.** `HEAD /k/test/api/hello` and `POST /k/test/v1/messages`, in every run with the prefix set. Anthropic answered 200 once the prefix was removed, except for the `codex:` model below |
| Does a header from `ANTHROPIC_CUSTOM_HEADERS` arrive on every request? | **No.** `X-Aimux-Key` arrived on `POST /v1/messages` and not on `HEAD /api/hello`, which carries four headers only: `Accept`, `Accept-Encoding`, `Connection`, `User-Agent` |
| Which paths does a session call? | Two: `HEAD /api/hello` once at the start, with no login on it, and `POST /v1/messages` with a query string. Nothing else in these runs |
| Which header carries the login? | `Authorization`, scheme `Bearer`. `X-Api-Key` was never sent |
| Does a subagent's request carry the same login as the main one? | **Yes.** Same `Authorization` hash, same session id in the header and in the metadata, plus `X-Claude-Code-Agent-Id` |
| Is the login the same across sessions? | Yes, one hash across all eleven `POST /v1/messages` requests of eight sessions |
| Can a session name a `codex:` model with `--model`? | **Yes, the client sends it.** `--model codex:gpt-6.2-sol` produced two `POST /v1/messages` naming that model, carrying the subscription login and the path prefix, and no `X-Api-Key`. Anthropic answered 404 both times, as it should, and the client then reported a problem with the selected model. The client does not check the name before sending |
| Is the answer compressed? | **Yes.** The client offers `gzip, deflate, br, zstd`, and the streamed answer to `POST /v1/messages` came back with `Content-Encoding: gzip` in both sessions checked. The plan's usage reader treated a compressed body as unreadable, which would leave every call without token figures |
| Does the session id match between the header and the metadata? | Yes, on every `POST /v1/messages`, whatever model was named |

The runs: a one-word reply with the path prefix; the same with the custom header and no prefix; a run with one
Bash tool call and one subagent (three requests in the main session, the middle one the subagent's); and the
`codex:` model run.

### A second session after each run

After each run ended, a second session appeared within a second: its own `HEAD /api/hello`, then one
`POST /v1/messages` on `claude-sonnet-5-5`, with a session id of its own and the same login. It used the same
base URL as the run before it, prefix included, and it carried the test header when the run before it did. So
it is a child process that inherits the environment. Which program starts it was not established; the owner's
settings run several programs when a session stops or ends, and one of them is the likely source.

This matters twice. A session started through the proxy brings its child sessions with it, so they are counted
without any setup of their own. And it is a likely explanation for the requests under a Claude model name
recorded in [the turn identity note](2026-11-25-turn-identity.md): a child session started by a `codex:`
session inherits the proxy address and asks for its own default Claude model. That is a reading of the
evidence, not a confirmed cause.

## Confirmed, and not

| Statement | Status |
|---|---|
| The path prefix is kept on every path a non-interactive session calls | Confirmed, six sessions |
| The custom header is absent from `HEAD /api/hello` | Confirmed, two sessions |
| The login travels in `Authorization` only, and a subagent uses the same one | Confirmed |
| A `codex:` model name reaches the proxy with the login beside it | Confirmed. That Codex then answers it is not shown here: the forwarder only spoke to Anthropic |
| Child sessions inherit the base URL and the header setting | Confirmed. What starts them is not |
| An interactive session calls the same two paths | **Not shown.** Only non-interactive runs were made |
| `/model codex:gpt-6.2-sol` typed in a session is accepted | **Not shown.** The interactive picker may check a name where `--model` does not, and may read a list of models from the base URL (`GET /v1/models`), which no run here requested |
| Changing model with `/model` mid-session keeps the prefix and the session id | **Not shown.** The earlier passthrough test changed model between two non-interactive turns of one conversation; typing `/model` was not tried |
| The login is renewed without touching the base URL | **Not shown.** No renewal happened in these runs. If renewal goes through the base URL, a new path will appear and the proxy must forward it |
| Other paths under long or unusual use (token counting, file uploads, images) | **Not shown.** The proxy forwards an unknown path to Anthropic unchanged when the key is right, and records its name |

The owner checks the three interactive lines in task 5, with the proxy's own record of paths open beside the
session.

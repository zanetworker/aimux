# Passthrough test: results

The question, from step 0a of the design: does Claude Code's subscription login work when Claude Code is
pointed at a local address that forwards to Anthropic?

<!-- no-images: a results note for one experiment; the design documents carry the figures -->

## Answer

**Yes, from the host.** The sandbox half has not been run yet.

## What was done

A throwaway forwarder listened on `127.0.0.1:7399` and passed every request to `https://api.anthropic.com`
unchanged, with streaming flushed straight through. It recorded, per request: method, path, status,
duration, the names of the request headers, the model name from the request body, and a short hash of the
session header. It recorded no header value and no body. Claude Code ran on the subscription login, with no
API key, token, or other provider setting in the environment, and `ANTHROPIC_BASE_URL` set to the forwarder.

## What was seen

| Check | Result |
|---|---|
| A command with a tool call (`echo passthrough-ok` through the Bash tool) | Worked. Two `POST /v1/messages`, both 200, both streamed: the tool call, then its result |
| Which header carries the login | `Authorization`. No `X-Api-Key` was sent |
| Other paths requested | `HEAD /api/hello`, twice, 200 |
| A session id on the request | Yes: a header `X-Claude-Code-Session-Id` on every `/v1/messages` request |
| Changing model inside one conversation | Worked. A first turn on `claude-haiku-5-5`, then the same conversation continued on `claude-sonnet-5-5`, which recalled a word given in the first turn. The model name in the request changed; the session header's hash stayed the same |
| An unexplained request | On the continued turn, one extra `/v1/messages` request carried a different session hash before the main one. Likely a background call; not investigated |

Header names seen on `/v1/messages`: `Accept`, `Accept-Encoding`, `Anthropic-Beta`,
`Anthropic-Dangerous-Direct-Browser-Access`, `Anthropic-Version`, `Authorization`, `Connection`,
`Content-Length`, `Content-Type`, `User-Agent`, `X-App`, `X-Claude-Code-Session-Id`, and seven `X-Stainless-*`
headers.

## What it means for the design

- **The passthrough engine can be built (section 6).** Forwarding the request and its `Authorization` header
  unchanged is enough. The model proxy today claims the credential header for its own key, so a passthrough
  listener needs its own way to tell a caller it trusts: a second listener, or a key carried somewhere other
  than `Authorization`.
- **Routing by hand inside a session works as designed (section 6, level 1).** The model name is in every
  request and changes when the owner changes model, and the conversation's content travels with the request.
- **Identity is easier than the design assumed (section 2).** The session id arrives as a header on every
  request, not only inside `metadata.user_id` for Codex-mode requests. Whether background calls carry the
  same id is the open point the unexplained request raises.
- **The meter can see every Claude Code model call (section 5)** once passthrough exists, for sessions
  started through the door.

## Not done, and not known

- **The sandbox half.** The same test from inside an OpenShell sandbox with Wingman's provider, reaching the
  forwarder as `host.containers.internal`. To be run with the Wingman session.
- **An interactive session with `/model`.** The model change was tested with two non-interactive turns of one
  continued conversation, which exercises the same request path but is not the same as typing `/model`.
- **Whether Anthropic's prompt cache still matched across the model change.** The forwarder did not read
  usage figures.
- **Long sessions, images, and other request kinds.** Only short text turns and one tool call were tried.
- **Whether this keeps working.** It was observed once, on one Claude Code version; the header name
  `Anthropic-Dangerous-Direct-Browser-Access` suggests the login path is not a stable public contract.

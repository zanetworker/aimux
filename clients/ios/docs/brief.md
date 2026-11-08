# aimux iOS app: starting brief

Goal: a native iPhone app (SwiftUI) for aimux's remote API, so the user can find, follow, continue and start Claude Code sessions on their Mac from the phone, by touch or voice, and answer permission requests. Separate repo; talks only to the API.

## The server

- `aimux remote serve` on the Mac (127.0.0.1:7382), exposed on the tailnet with `tailscale serve --bg --https=8443 http://127.0.0.1:7382`.
- Base URL: `https://my-mac.tail1234.ts.net:8443` (make it a setting).
- Contract: `api/remote-v1.openapi.yaml` in github.com/zanetworker/aimux (main), also served at `GET /v1/openapi.yaml`. Generate or hand-write Codable types from it. `internal/remoteapi/contract.go` has the server's typed structs.
- Compatibility rules (in the spec): v1 only adds things. Ignore unknown fields and enum values (event kinds, states, error codes, capabilities); breaking changes go to /v2. Check `GET /v1/health` `capabilities` before using optional features.

## Auth

None in the app: the phone must be on the tailnet (Tailscale iOS app, same account), and `tailscale serve` adds the caller's identity. Requests must not send an `Origin` header (native apps don't). The allowed login is set on the Mac (`remote_access.allowed_logins`).

## Endpoints (see the spec for shapes)

- `GET /v1/health` → login, version, acpProtocolVersion, capabilities
- `GET /v1/sessions?limit=` → list (state: idle | busy | open_in_terminal; title, project, lastActive, lastPrompt)
- `GET /v1/search?q=&limit=` → results with `snippet` (matches in [brackets])
- `GET /v1/sessions/{id}?turns=` → latest turns (user, reply, tools); state `unavailable` for a created session that never started
- `GET /v1/projects` → recent folders where a new session may start
- `POST /v1/sessions` {cwd, text, key} → new session + first message
- `POST /v1/sessions/{id}/messages` {text, key} → start a turn (key = idempotency key: reuse it when retrying the same text)
- `GET /v1/sessions/{id}/events?after=&log=` → server-sent events (`id: <log>.<seq>`); `?wait=0` for one JSON page {events, log}
- `POST /v1/sessions/{id}/cancel`, `POST /v1/approvals/{id}` {option}

Event kinds: turn_started, update (an ACP SessionUpdate: agent_message_chunk, agent_thought_chunk, tool_call, tool_call_update, plan, ...), approval {approvalId, title, options, toolCall.rawInput}, approval_resolved, turn_done {stopReason}, turn_error.

## Lessons from the web client (now `clients/web` in aimux, PR #71)

- Load order that avoids duplicates: events snapshot (wait=0), then history, then stream from the snapshot's last seq with `log=`. A running turn is the last event's turn; drop the history's latest turn if it is the running one (same prompt, recorded after the turn started).
- One send at a time; keep the idempotency key for a failed send and reuse it on retry; drafts per session.
- Sessions open in a terminal on the Mac are read-only (409 open_in_terminal).
- Approvals are answered by a tap only, never by voice; read them aloud in voice mode.
- Voice in the browser was unreliable on iOS: use SFSpeechRecognizer (on-device where available) and AVSpeechSynthesizer. Stop recognition before speaking; don't auto-send after a recognition error or when the user leaves the screen.
- Server speed: list ~0.3-0.7 s and session view ~0.45 s on a 131 MB session since PR #72.

## Not in the API yet

- Push notifications while the app is closed (approvals on the lock screen) need server work: aimux sending APNs, or a relay.
- List paging; Codex sessions; live streaming of sessions open in a terminal.

Test session that is safe to message (a claude -p session, hidden from the list): af916f5e-3622-4f54-910f-14aeb16b4208 (a throwaway in a scratch folder).

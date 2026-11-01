# Send now from the queue, and a peek into working sessions

Both phone clients, `aimux remote serve`, terminal input, shared fixtures.

## Intent

The owner keeps the chat queue to organise thoughts while Claude works, but a correction must reach Claude during the turn, not after it. And with many sessions working, the owner wants to understand what one is doing without opening its chat: what it is running, what came out, and in plain words what is going on.

## Goals

1. **Send now.** A queued message, or the composer's text, can skip the queue and go to Claude at once, mid-turn. Everything else stays queued in order.
2. **Peek.** From any list, a working session can be looked into: elapsed time, the current step, the last few steps with a few lines of their output, and Claude's latest text. On demand, a cheap model explains in two or three plain sentences what the session is doing and where it stands.

## Non-goals

- Interrupting Claude (Stop stays the existing action).
- Send now for sessions started from the phone (headless ACP runs take one turn at a time); the action is hidden for them.
- Streaming raw terminal output; the peek shows what the transcript and tool results hold.
- Automatic explanations; the model runs only when the owner taps Explain.

## Design

### Send now (terminal sessions)

- Clients: a queued message's row gets **Send now** (iPhone: swipe or context menu; web: a row button); a long-press on the composer's send arrow (iPhone) or a secondary action (web) sends the box's text now. Copy: "Send now" / "Sent now; Claude sees it during this turn".
- API: `POST /v1/sessions/{id}/queue/{item}/send-now` (takes the item out of the queue and sends it) and a `now: true` field on `POST /v1/sessions/{id}/terminal/messages`. Idempotent by key like other sends.
- Server (terminput): a send while Claude works is allowed only with `now`. The checks stay: no permission prompt on screen, the owner is not using that pane on the Mac, the pane's foreground process is the session's agent, and the input box is empty or holds only aimux's own unsent text; the text is typed, shown in the box (BoxShows) before Enter, then Enter. Claude Code queues it and hands it to Claude mid-turn. Refusals keep their codes (`not_at_input`, `mac_in_use`, `box_has_unsent` ...); a refused Send now keeps the item in the queue at its place.
- The queue's own dispatch is unchanged.
- Verification: unit tests with the fake driver; one live check in a throwaway iTerm2 tab the controller opens and closes itself, only after the owner approves it, confirming a mid-turn message is delivered and never mixes into a prompt.

### Peek

- API: `GET /v1/sessions/{id}/peek` returns `{working, elapsedSeconds?, step?, steps: [{title, status, output}], text}`: the latest turn's last 5 steps, each with up to 6 trailing lines of its tool result (trimmed, 600 characters cap), and the last 400 characters of Claude's latest text. Built from the existing parsed turns (provider ParseTail and trace tool spans), no new transcript reader. Cheap: no model call.
- `POST /v1/sessions/{id}/explain` asks the naming model (same backend, settings and daily accounting as Update summary) for 2 to 3 plain sentences: what the session is working on, the current step, and anything stuck or failing. Input: the session title and the latest turn's prompt, steps and recent output, capped at 2,000 characters. Answer cached per session and turn for 2 minutes. Same privacy statement as Update summary (sent to Anthropic only on tap).
- Clients: iPhone: touch and hold a session row in Now or All shows a preview (native context menu preview) with the peek, refreshed every 3 s while open, and an **Explain** button; tap opens the chat. Web: an expand control on the row shows the same block. Near-monochrome; output in monospace.

## Constraints

- Additive API; device login on the new routes; audit actions for send-now and explain without text.
- No new dependencies. Never type into the owner's real sessions in tests.
- Parity: both clients, shared fixtures for the peek shape and the Send now copy.

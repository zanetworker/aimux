# Chat Queue Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** iTerm2 sessions take phone messages while the owner works at the Mac unless that exact session is the focused one, and every session gets a queue held by the Mac that the owner can add to, reorder, edit and delete, sent one item at a time when the session is free.

**Architecture:** `internal/terminput` gains a focused-pane guard (iTerm2 driver reports the frontmost app and focused session tty) and a "working" screen check that refuses busy direct sends. A new package `internal/remoteapi/chatqueue` holds the queue store (persisted JSON, pure state machine with an injected clock) and the dispatcher (ACP via the broker's turn-done, iTerm2 via polling terminal status and history). `internal/remoteapi` exposes the queue routes, `SessionDetail.queue` and a `queue` event. Both clients add "Add to queue" and a queue list, pinned by shared fixtures.

**Tech Stack:** Go; plain ES modules; SwiftUI + AimuxKit; shared JSON fixtures.

**Spec:** `docs/superpowers/specs/2026-10-29-chat-queue-design.md` (read it whole; "Builder check: answers" overrides the sections above it).

## Global Constraints

- Branch `feat/clients`, worktree `aimux-worktrees/clients`. Commit with `&&` after tests pass; never push; never `git stash`. Trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. `git add -f .claude/CLAUDE.md` when touched.
- Every implementer and reviewer invokes `development-tools:crafted-code` first: test first (see it fail), errors with context, no message text in logs or audit, simple and minimal.
- Never type into, create, focus or close the owner's real iTerm2 windows or sessions in tests. Live checks use a scratch session the controller arranges with the owner.
- Guard: blocked only when iTerm2 is frontmost AND the target is the focused session of iTerm2's current window (matched by tty) AND HID idle < 120 s; checked before typing and before Enter; `allow_while_in_use` skips it. Reason line: "You're using this session on your Mac."
- Busy: a direct `POST …/terminal/messages` (and the existing ACP `…/messages`) to a working session answers 409 `busy`. "Working" for iTerm2 = Claude Code's working line on screen (pinned by a screen fixture) and the registry status when present.
- Queue: max 20 items per session; text limits and terminal text rules as for messages (control characters refused, leading `!` refused, leading `/` refused unless `allow_slash_commands`) checked at add time (`bad_text`); idempotency key stored with each item; item states `waiting`, `sending`, `answering`; queue state `running` or `paused {reason}` with fixed reasons; at most one item `sending` or `answering` per session.
- Persistence: `~/.aimux/remote-queue.json`, 0600, server is the only writer, atomic temp-file rename. On load, any `sending` or `answering` item makes its queue `paused` with "This message may have been sent. Check the session, then resume or delete it."
- Done signal: ACP, the turn-done of the turn the item started; iTerm2, a history turn whose user text matches the item (Go port of the clients' `samePrompt`, wrappers removed, pinned by `api/testdata/clients/terminal/saved-prompt-match.json`) followed by not working and an empty input box. No such turn within 2 minutes of Enter → paused "No answer seen yet".
- A new history turn the queue did not send → paused "You used this session on your Mac".
- Errors: wait silently on `busy`, `not_at_input`, a permission prompt, `mac_in_use` (shown "Waiting: you're using this session on your Mac"); pause with a fixed reason on `not_claude`, `automation_denied`, `no_terminal_driver`, `disabled`, `not_submitted` ("Your message is in the input box on your Mac"), any other error; never auto-retry a terminal send. Resume after `not_submitted` presses Enter only if the box holds exactly that text, else stays paused "Clear the input box on your Mac, then resume".
- An iTerm2 session whose `claude` exited pauses "This session closed on your Mac"; ACP queues never pause for idling between turns; a queue never moves to ACP by itself.
- API (device login applies): `GET/POST /v1/sessions/{id}/queue`, `PATCH/DELETE /v1/sessions/{id}/queue/{item}`, `POST /v1/sessions/{id}/queue/resume`; 409 `queue_full`, `item_sending`, `bad_text`; only Resume resumes. `SessionDetail.queue {state, reason?, items}`; event `queue` on the session stream.
- Audit actions: `queue_add`, `queue_edit`, `queue_move`, `queue_delete`, `queue_send`, `queue_paused`, `queue_resume`, never text.
- Clients: while busy or blocked, Send becomes "Add to queue" (also reachable while free); queue under the composer with edit, reorder (iOS drag; web Move up/Move down buttons), delete and Resume with the reason. Near-monochrome, light and dark, accessible.
- Fixtures invented. Docs in the last task.

## Review Focus

- **Double send:** a send whose answer is still running, a restart mid-send, a client retry: never two items in one turn and never one item twice. Task 3 tests with a fake clock and fake terminal.
- **Typing into the owner's session:** guard flips between the type step and Enter; the owner's own Mac turn between two queued items. Tasks 1 and 3.
- **A queue that never moves or never stops:** text left in the box, Claude exited, automation revoked: always a visible paused reason, never silent waiting forever. Task 3.
- **Concurrent edits:** PATCH/DELETE racing the dispatcher's move to `sending`. Task 2 and 4 tests.
- **Old clients:** a client without queue support against the new server still works (sees 409 busy as before for ACP; terminal sends while working are newly refused with a fixed message). Task 4 contract test.

## Task 1: Focused-pane guard and busy refusal (terminput)

**Files:** `internal/terminput/terminput.go`, `types.go`, `probe.go`, `screen.go`, `iterm2/iterm2.go`, `iterm2/helper.js`, tests, `testdata/screens/` (a working screen fixture), `internal/remoteapi/api.go` (map `busy` for terminal messages).

**Interfaces:** Produces an optional driver capability `FocusReporter { Focused(ctx) (frontmost bool, tty string, err error) }` implemented by the iTerm2 driver; `screen.go` `Working(screen string) bool`; the service's in-use check uses idle && frontmost && tty == target tty; `Send` refuses with `ErrBusy` (code `busy`) when Working; `Status` exposes `working bool` for Task 3.

- [ ] Tests first: guard table (idle short/long × frontmost yes/no × focused tty same/different × allow_while_in_use) with fakes; a focus change between type and Enter stops with `not_submitted`; `Working` on the new fixture and on idle and prompt fixtures; `Send` on a working screen returns `busy` and types nothing; JXA helper method returns frontmost and focused tty (unit-tested through the helper's JSON contract, live test behind the `iterm2` build tag only).
- [ ] Implement; `go test ./internal/terminput/... ./internal/remoteapi/...`; commit `feat(terminput): guard only the focused session; refuse sends while Claude works`.

## Task 2: Queue store and state machine

**Files:** Create `internal/remoteapi/chatqueue/queue.go`, `store.go`, tests; Go `SamePrompt` (port of the clients' rule) in `internal/remoteapi/chatqueue/prompt.go` with a test over `api/testdata/clients/terminal/saved-prompt-match.json`.

**Interfaces:** Produces `type Item struct{ID, Text, Key string; Created time.Time; State string}`, `type Queue struct{State, Reason string; Items []Item}`, `Store` with `Open(path string, now func() time.Time)`, `Get(session) Queue`, `Add(session, text, key) (Item, error)`, `Edit(session, item, text)`, `Move(session, item, position)`, `Delete(session, item)`, `Resume(session)`, `Pause(session, reason)`, `Next(session) (Item, bool)` (first `waiting` when running and nothing in flight), `MarkSending/MarkAnswering/Done(session, item)`; errors `ErrFull`, `ErrItemSending`, `ErrBadText`, `ErrNotFound`; a `Changes() <-chan string` (session ids) for the API's `queue` event.

- [ ] Tests first: limits (20, text rules incl. `!`, `/` with and without allow, control characters), idempotent Add by key, Move bounds, Edit/Delete refused while `sending`/`answering` (`ErrItemSending`), only Resume resumes, Next respects one in flight and paused, persistence round-trip at 0600, a file with `sending` or `answering` loads paused with the fixed reason, concurrent Add/Move/MarkSending under `-race`.
- [ ] Implement; `go test -race ./internal/remoteapi/chatqueue/`; commit `feat(remote): chat queue store`.

## Task 3: Dispatcher

**Files:** Create `internal/remoteapi/chatqueue/dispatch.go`, tests; wire into `internal/remoteapi/broker.go` (turn-done hook) and the terminal status path (`terminal_cache.go`, `local.go` history).

**Interfaces:** Consumes Tasks 1 and 2. Produces `Dispatcher` with injected `Senders` (ACP send, terminal send, terminal status, history turns since), clock and audit; `Run(ctx)`; `Kick(session)` (after queue changes and turn-done).

- [ ] Tests first with fakes and an injected clock: ACP sends the first item at turn-done and the second only after the first's turn-done; iTerm2 sends when canType, box empty, no prompt, not working; `answering` until a matching history turn and idle; 2-minute timeout pauses "No answer seen yet"; a foreign history turn pauses "You used this session on your Mac"; each error class waits or pauses as in Global Constraints; `not_submitted` pauses with its reason and Resume presses Enter only when the box holds exactly the text; Claude exited pauses "This session closed on your Mac"; restart mid-send never re-sends; audit lines carry no text.
- [ ] Implement; `go test -race ./internal/remoteapi/...`; commit `feat(remote): chat queue dispatcher`.

## Task 4: API, contract and events

**Files:** `internal/remoteapi/api.go`, `contract.go`, `local.go` (`SessionDetail.queue`), events (`queue`), `cmd/aimux/cmd/remote.go` (store path, dispatcher start), `api/remote-v1.openapi.yaml`, contract and handler tests, smoke entries if the repo's smoke test covers remoteapi.

- [ ] Tests first: each route's happy path and errors (`queue_full`, `item_sending`, `bad_text`, unknown item 404), device login required, audit actions, `SessionDetail.queue` present, a `queue` event after each change, old-client behaviour (terminal send while working → 409 busy with a fixed message).
- [ ] Implement; full Go suite and `-race` on remoteapi; commit `feat(remote): chat queue API`.

## Task 5: Shared fixtures

**Files:** `api/testdata/clients/queue/*.json`, a "Queue" section in `api/testdata/clients/README.md`.

- [ ] Fixtures for how clients show a queue: composer label by session state (free → Send, busy/blocked → Add to queue), item rows by state, paused reasons and the Resume action, the waiting line for `mac_in_use`; invented text. Commit `test(clients): chat queue fixtures`.

## Task 6: Web client

**Files:** Create `clients/web/webapp/queue.mjs` (+ `queue.test.mjs`); modify `chatView.mjs`, `app.js`, `app.css`.

- [ ] Tests first over the Task 5 fixtures and a fake fetch (add, edit, move, delete, resume, handling 409s); Playwright against a real server with a scratch HOME (pattern from the device-login task's check script in the session scratchpad) using an ACP session stub or a fake, screenshots light and dark at 390 px; commit `feat(web): queue messages for a session`.

## Task 7: iPhone app

**Files:** Create `clients/ios/AimuxKit/Sources/ChatQueue.swift` (+ tests), `clients/ios/App/Components/QueueList.swift`; modify `ChatModel.swift`, `ChatView.swift`, `Composer.swift`, `project.yml` (build +1).

- [ ] Tests first (fixtures, fake transport); UI test for add, reorder, delete, resume in the demo chat; screenshots light and dark; `swiftlint --strict`; commit `feat(ios): queue messages for a session`.

## Task 8: Docs and live check

**Files:** `docs-site/src/content/docs/guides/remote-access.mdx`, `RELEASE_NOTES.md`, `clients/ios/README.md`, `clients/ios/docs/project-context.md`, `.claude/CLAUDE.md`.

- [ ] Docs: the new guard and its scope, the busy refusal, the queue (limits, states, reasons, restart behaviour, audit), API.
- [ ] Live check: only after the controller confirms with the owner that a scratch iTerm2 window may be opened (it can take focus): scratch HOME, a scratch Claude Code session in a new iTerm2 window, queue two items from a script, see them go one per turn, focus the scratch session and confirm the guard holds the next item, close it and confirm the pause; record output; close nothing of the owner's.
- [ ] `cd docs-site && npm run build`; commit `docs: chat queue`.

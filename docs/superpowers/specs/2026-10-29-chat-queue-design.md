# Chat with sessions: a smarter guard and a queue on the Mac

Both phone clients and `aimux remote serve`.

## Intent

Chatting with sessions from the phone is the owner's most important feature, and two things stop it today. A session open in iTerm2 refuses typing whenever the Mac was used in the last 2 minutes, even when the owner was typing somewhere else. And a busy session refuses a second message, so the owner cannot line up what comes next. The owner wants to keep the protection against typing into a session they are using on the Mac, keep the server's refusal of a direct second message, and get a queue they control (add, reorder, edit, delete) that keeps working while the phone is locked or offline.

## Goals

- An iTerm2 session is blocked only when it is the session in front on the Mac and the Mac was used in the last 2 minutes. Every other iTerm2 session takes phone input while the owner works at the Mac.
- Each session has a queue held by the Mac. Clients add, reorder, edit and delete items; the Mac sends the first item when the session is free, then waits for that answer before the next.
- The queue survives a server restart, the phone being locked, and the phone being offline.
- Same queue for sessions started from the phone (ACP) and sessions in iTerm2.

## Non-goals

- A tmux driver (planned next; the driver interface already allows it).
- Other terminals, VS Code, the Claude app.
- Scheduling (send at a time), conditions, or templates.
- Sending a queued item while a permission prompt is on screen.

## Anti-goals

- Typing into the session the owner is looking at on the Mac.
- A queue that sends two items into one turn, or loops on a failing item.
- Message text in any log or audit line.

## Constraints

- All existing terminal-input checks stay (enabled, registry pid, foreground is claude, empty input box, control characters, `!` and `/` rules, idempotency keys, the log without text); only the idle check changes as below.
- Device login applies to every new route; audit lines for queue changes and sends carry no text.
- No new dependency in any component.

## Approaches

1. **Queue on the Mac (chosen).** The server keeps the queue per session, persists it, and a dispatcher sends items when the session is free. Clients only show and edit it. Works with the phone locked or offline; one place decides when to send.
2. **Queue on the phone.** Simple server, but iOS suspends apps in the background, so items only go while the app is open. Rejected by the owner after seeing this limit.
3. **Phone queue woken by silent push or background tasks.** iOS decides when these run (often minutes or hours later) and push needs Apple's service. Rejected.

## Decided

### The guard

- Blocked when all hold: iTerm2 is the frontmost app; the target session is the current session of iTerm2's current window (matched by tty); the Mac's HID idle time is under 120 s. Otherwise the in-use check passes.
- Checked before typing and again just before Enter (the existing re-read of the screen); a change in between stops the send with the existing `not_submitted` path.
- `allow_while_in_use` keeps its meaning (skip the check).
- The reason line for a blocked session becomes "You're using this session on your Mac." Messages for it are queued, not refused.
- Verified read-only on the owner's Mac: JXA returns the frontmost app name and the current session id of iTerm2's current window.

### The queue

- Per session: up to 20 items, each `{id, text, created, state, reason?}` with state `waiting`, `sending` or `paused`. Text limits are the existing message limits.
- Stored in `~/.aimux/remote-queue.json` (0600, written atomically by the server, the only writer).
- API (behind device login):
  - `GET /v1/sessions/{id}/queue` → `{items}`.
  - `POST /v1/sessions/{id}/queue {text, key}` appends (idempotent by key); 409 `queue_full` at 20.
  - `PATCH /v1/sessions/{id}/queue/{item} {text?, position?}` edits or moves a `waiting` or `paused` item; a `sending` item cannot change.
  - `DELETE /v1/sessions/{id}/queue/{item}` removes a `waiting` or `paused` item.
  - `POST /v1/sessions/{id}/queue/resume` resumes a paused queue.
- `SessionDetail` gains `queue` (the same items) so clients that poll see it; the session event stream gains a `queue` event on every change.
- A direct `POST …/messages` or `…/terminal/messages` to a busy session still answers 409 `busy`; clients offer "Add to queue" instead.

### The dispatcher

- One loop in the server. For each session with a `waiting` first item, it sends when the session is free:
  - ACP (started from the phone): when the current turn ends (the broker's turn-done), through the existing send path.
  - iTerm2: when terminal input says `canType`, the input box is empty, no permission prompt is on screen, and the session is not working (Claude Code's busy state), checked at least every 2 s.
- One item at a time: the next waits until the answer to the previous one has finished (turn done, or the terminal session back to idle with the input box empty).
- A send that fails (any error other than "not free yet") marks the first item `paused` with a fixed reason; the queue stops for that session until the owner resumes it, edits or deletes the item. No automatic retry of a terminal send (a timeout can leave partial input, as today).
- A session that ends or closes pauses its queue with reason "Session not running".
- Audit actions: `queue_add`, `queue_edit`, `queue_move`, `queue_delete`, `queue_send`, `queue_paused`, `queue_resume`.

### Clients

- While the session is busy or blocked, the composer's send becomes "Add to queue" (also available while free via a long press or menu, so the owner can line up several).
- The queue shows under the composer: each item's text (one line, tap to edit), its state, drag to reorder (iOS `List` `onMove`; web: Move up and Move down buttons, plus drag where cheap), swipe or button to delete, and "Resume" when paused with its reason.
- Shared fixtures in `api/testdata/clients/queue/` pin how both clients show states and the composer's label.

## Left to the builder

- How the dispatcher reads "Claude is working" for an iTerm2 session (existing session state, the screen, or both), file layout, exact copy, and animation.

## Builder check: answers (my calls; they override the sections above where they differ)

1. **Busy terminal sessions refuse a direct message (new rule).** `POST …/terminal/messages` answers 409 `busy` while Claude is working, as the owner asked; clients offer "Add to queue". "Working" is decided by the screen (Claude Code's working line, e.g. "esc to interrupt") together with the registry status when it has one; the builder confirms both on a scratch iTerm2 session (never the owner's) and pins the screen form in `internal/terminput/testdata/screens/`.
2. **Restart with an item in `sending` or `answering`:** it becomes `paused` with "This message may have been sent. Check the session, then resume or delete it." Same for ACP. The idempotency key of each item is stored with it.
3. **States and the done signal.** Item states: `waiting`, `sending`, `answering` (sent; its answer is not finished), and the queue itself is `running` or `paused {reason}`. An item leaves the queue when its answer is finished: ACP, the turn-done event of the turn it started; iTerm2, a history turn whose user text matches the item (the existing `samePrompt` rule, wrappers removed) and, after it, the session no longer working with an empty input box. If that turn is not seen within 2 minutes of Enter, the queue pauses with "No answer seen yet". The next item is never sent while an item is `sending` or `answering`.
4. **A turn the owner starts on the Mac pauses the queue.** When a new history turn appears that the queue did not send, the queue pauses with "You used this session on your Mac" and waits for Resume, so an old queued message never follows the owner's own turn unseen.
5. **Text left in the box (`not_submitted`).** The queue pauses with "Your message is in the input box on your Mac". Resume then presses Enter only if the box holds exactly that item's text (the existing re-read rule), else stays paused with "Clear the input box on your Mac, then resume".
6. **"In front" means the focused pane only:** keystrokes reach only the focused session, so typing into a visible split pane, a second window or a hotkey window cannot mix with the owner's typing. Documented as the guard's scope.
7. **"Not running".** ACP sessions never pause for being idle between turns. An iTerm2 session whose `claude` exited (registry entry gone) pauses its queue with "This session closed on your Mac"; the queue never moves to ACP by itself (it could clash with `claude --resume` at the Mac).
8. **Errors.** Wait silently (not free yet): `busy`, `not_at_input`, a permission prompt on screen, `mac_in_use` (shown as "Waiting: you're using this session on your Mac"). Pause with a fixed reason: `not_claude`, `automation_denied`, `no_terminal_driver`, `disabled`, any other error. `POST …/queue` checks the terminal text rules (control characters, a leading `!`, a leading `/` unless allowed) when the item is added and refuses with `bad_text`, so a bad item never enters the queue.
9. **Pause is a queue state, not an item state.** Moving or editing items never resumes; only Resume does. PATCH or DELETE of an item in `sending` or `answering` answers 409 `item_sending`.
10. **The server never queues by itself:** a refused direct send stays refused; the client offers "Add to queue" (no duplicate risk).

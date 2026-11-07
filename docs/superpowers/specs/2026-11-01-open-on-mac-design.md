# Open on Mac

Both phone clients, `aimux remote serve`, terminal input.

## Intent

Sessions started or continued from the phone run as a background Claude process on the Mac (no window). The owner wants to keep that, because it is reliable while away, and also be able to hand a session to a real iTerm tab with one tap, so it is waiting at the Mac and the phone then drives it like any terminal session.

## Goals

1. **Open on Mac** on any session that is not already open in a terminal: phone-started ones, and idle ones from All.
2. It opens as a new tab in one dedicated iTerm2 window named "aimux", created the first time and reused after; the owner's other windows are never touched.
3. The tab runs `claude --resume <id>` in the session's folder. When Claude holds the session, the phone's chat switches to terminal mode (queue, Send now, permission alerts).
4. No session is ever held by two processes: the background run is closed first, and the session lock decides.

## Non-goals

- Moving a terminal session back to the background.
- Other terminals than iTerm2 (the action is hidden when no terminal driver is available).
- Opening several sessions at once.

## Approaches

- **A. Server closes the background run, then opens the tab through the iTerm2 driver (chosen).** One endpoint does the whole handoff; the driver already locates panes and types, so it gains "open tab in the aimux window". Reliable ordering, one place for refusals.
- B. Phone first stops the run, then asks for the tab: two calls, a window in between where the queue could start a new run.
- C. Always iTerm (no background): rejected by the owner.

## Design

- **API:** `POST /v1/sessions/{id}/open-on-mac` (device login, audit action `open_on_mac`, no text). Answers `{state}` once Claude holds the session in the new tab, or an error code:
  - `busy`: a phone turn is running; the phone offers to wait or Stop first.
  - `open_in_terminal`: already open somewhere; the phone offers Jump instead.
  - `no_terminal`: no iTerm2 driver.
  - `in_use_elsewhere`: the session lock is held by another process.
  - `timeout`: the tab opened but Claude did not take the session within 20 s (the tab stays, so the owner sees why).
- **Server:** refuse while a turn runs; close the idle background run for that session (releases the lock); hold the queue's dispatch for that session until the handoff ends; open the tab through the driver; wait for the session's registry entry to name the new Claude process; then the session lists as `open_in_terminal` and the existing terminal path takes over. A created session without a transcript yet is refused (`not_started`): there is nothing to resume.
- **Driver (`terminput/iterm2`):** `OpenTab(ctx, title, dir, command)` finds the window named "aimux" (by a stored window id, falling back to its name) or creates it, adds a tab, sets the tab title to the session's shown title, and runs the command. The command is built from the session id and folder only, quoted, never from session text.
- **Clients:** iPhone: an "Open on Mac" action in the chat's menu and the session row's context menu; web: the same in the chat header menu. Progress: "Opening on your Mac…", then the chat shows the terminal state. Copy and error texts in the shared fixtures.

## Constraints

- Additive API; contract and fixtures updated; both clients at parity.
- Tests never create, focus, type into or close the owner's windows: unit tests use the fake driver; one live check opens and closes its own "aimux" window only after the owner approves it.
- No new dependencies.

## Left to the builder

Exact driver script, the stored window id's location, the hold mechanism in the queue dispatcher, menu placement details.

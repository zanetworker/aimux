# Talking to terminal sessions from the phone: design

## Intent

Sessions open in a Claude Code terminal on the Mac are read-only from the phone: aimux cannot drive a session another process owns, so `POST /v1/sessions/{id}/messages` answers `409 open_in_terminal`. The owner is usually away from the Mac when using the phone, and most of their sessions live in iTerm2 tabs, so read-only cuts off most of their work. They want to send a message, see Claude's answer, and answer its permission prompts from the phone, quickly, without the session getting garbled, and without opening a hole in a public, open-source project.

The approach: aimux types the phone's message into the session's own terminal pane, so the running Claude Code receives it as the owner's typed input. Measurements and the license constraint are in `2026-10-27-terminal-input-evaluation.md`.

## Goals

- From either client, send a message into a terminal session and see the answer.
- See whether a terminal session is working or waiting, and answer its permission prompts from the phone.
- Fast: typing into the pane adds about 50 ms (150 ms when the pane must be found again).
- Pluggable: a terminal is supported by one driver; iTerm2 now, others later without API or client changes.

## Non-goals

- Live steps, thoughts and tool calls for terminal sessions. aimux still cannot see inside them; the phone shows the saved turns, refreshed while the session works.
- Terminals other than iTerm2 in this round (tmux is the likely next driver).
- Showing terminal prompts on the Now screen; they appear in the chat only this round.
- Claude Code's internal peer-messaging socket (labelled as another session, cannot approve, internal and changeable).
- The iTerm2 Python API or anything built from its `api.proto` (license, see Constraints).

## Anti-goals

- **Typing into the wrong place.** Text must only ever reach a pane whose foreground process is the session's own `claude`. Text that lands in a shell is a remote shell.
- **Typing while someone is at the Mac.** If the Mac was used in the last 2 minutes, a phone message is refused unless the owner turned on the developer override; someone at the Mac while the owner is away is a warning sign, not a race to win.
- **Stealing focus or changing the owner's windows.** aimux never creates, focuses, resizes or closes windows, tabs or panes.
- **Leaking anything into the public repo**: tailnet names, ttys, window or session ids, iTerm2 cookies, real session text.

## Constraints

- aimux is MIT and public. iTerm2 and its `api.proto` are GPL-2.0. aimux does not vendor `api.proto`, code generated from it, or a library built from it. iTerm2 is driven through Apple Events (JavaScript for Automation run by `osascript`), which carries no license obligation.
- aimux builds with `CGO_ENABLED=0`; no cgo.
- macOS asks once to let the program that runs `osascript` control iTerm2 (Automation). Run from an iTerm2 tab, aimux inherits iTerm2's own grant; as a background service it gets its own prompt.
- Claude Code's input box takes text and Enter separately: type the text, wait 300 ms, read the screen again, then Enter only if the box still holds just that text and no prompt shows. Longer text goes in as a bracketed paste (`ESC[200~ … ESC[201~`).
- The registry `~/.claude/sessions/<pid>.json` gives each open session's pid and `status` (`busy`, `idle` observed). The tty comes from the pid (`ps -o tty= -p <pid>`).

## Approaches

| Approach | Speed | Trade-off |
|---|---|---|
| **Persistent JXA helper per aimux process (chosen)** | ~50 ms | No license exposure, nothing to install; one Automation grant |
| One `osascript` per send | 0.6 to 1 s | Simplest; slow with many sessions open |
| iTerm2 API (Go client or Python helper) | ~2 ms | GPL question; Python helper needs Python and a package |
| Claude Code peer socket | n/a | Not the owner's input; cannot approve; internal; a safety check blocked documenting its auth |

Chosen by the owner: the pluggable driver with the persistent JXA helper. A faster iTerm2 API driver may be added later outside the aimux repository once the license question is settled.

## How it works

### Driver interface (`internal/terminput`, new, no UI imports)

```go
type Target struct{ PID int; TTY string }       // the session's claude process and its tty, e.g. "/dev/ttys001"
type Pane struct{ Driver string; ID string }    // a driver's own handle, opaque to callers

type Driver interface {
    Name() string                                              // "iterm2"
    Locate(ctx context.Context, t Target) (Pane, bool, error)  // false: not this driver's pane
    Type(ctx context.Context, p Pane, text string) error       // types text without Enter; paste for long, multi-line or tabbed text
    Key(ctx context.Context, p Pane, key Key) error            // one key: digits 1 to 9, Escape, Enter
    Screen(ctx context.Context, p Pane) (string, error)        // visible text
}
```

`Screen` is required for any driver that sends: the service reads the screen before every `Send` and `Key` (below). A future driver that cannot read the screen can only be used read-only. (Claude's call: typing blind into an unknown screen state is the main way text reaches the wrong place.)

`terminput.Service` holds the drivers in order and the shared rules: `Status(ctx, sessionID)`, `Send(ctx, sessionID, text)`, `Key(ctx, sessionID, key)`. Drivers only move bytes; every check lives in the service, so a new driver cannot skip one.

### Checks before every Send and Key (in the service)

1. Terminal input is enabled in config (`remote_access.terminal_input.enabled` in `~/.aimux/config.yaml`, next to the existing `remote_access` settings the `/v1` server reads; default `false`, so a fresh install of the open-source project never types into terminals until its owner opts in).
2. The session is open in a terminal: a live registry entry (`discovery.IsLiveHolder`) not owned by aimux.
3. The pid's tty is known, the tty's foreground process group is that pid's group (`ps -o tpgid=` of the tty equals `ps -o pgid=` of the pid) and that group leader's command is `claude` or `node` (`ps -o comm=`). Otherwise `409 not_claude`. This check runs again right before typing, before Enter and before a key press.
4. The Mac has been idle at least 120 s (`HIDIdleTime` from `ioreg -c IOHIDSystem`), unless `remote_access.terminal_input.allow_while_in_use: true`. Otherwise `409 mac_in_use`.
5. A driver locates the pane. Otherwise `409 no_terminal_driver`; if macOS refused the Automation request (`osascript` error -1743), `409 automation_denied`.
6. For `Send`: the screen shows Claude Code's input box and it is empty (the line between the two horizontal rules holds only `❯` and, at most, Claude Code's grey placeholder). The box is also accepted while Claude works: text typed then is queued by Claude Code, as tested. Anything else (a permission prompt, a plan approval, a question menu, a half-typed draft) gives `409 not_at_input`, and the phone re-reads the detail, so a prompt that appeared shows as an approval card instead. (Claude's call: otherwise the text and its Enter could pick whichever option is highlighted, usually "1. Yes".) After typing (without Enter) the service waits 300 ms and reads the screen again: Enter is pressed only if no prompt shows and the bottom input box holds the typed text (wrapped rows allowed, compared without whitespace; for a paste, also Claude Code's "[Pasted text #n]" marker). Otherwise `409 not_submitted`: the text waits in the box, nothing was submitted, and the phone re-reads the detail.
7. For `Key`: the screen shows a recognised permission prompt whose `promptId` equals the one in the request, and the key is one of its options. Otherwise `409 no_prompt` or `409 prompt_changed`. (Claude's call: a late or retried key must not answer a different prompt that appeared since.)

Text starting with `!` after optional white space is always refused (`400 bad_text`): it switches Claude Code into bash mode and Enter runs a shell command with no prompt. Text starting with `/` (a slash command) is refused the same way unless `remote_access.terminal_input.allow_slash_commands` is true (default false).

Every attempt is appended to `~/.aimux/logs/terminal-input.log` (mode 0600): time, session id, kind (text or key), length or key, outcome. Never the text itself.

### iTerm2 driver (`internal/terminput/iterm2`)

- A JXA script embedded with `go:embed`, run as one long-lived `osascript -l JavaScript` child per aimux process, started on first use. Requests and replies are one JSON object per line on stdin and stdout (`{"op":"locate","tty":...}`, `{"op":"send","window":...,"session":...,"text":...,"paste":bool}`, `{"op":"key",...}`, `{"op":"screen",...}`). Text travels as JSON, never inside script source, so nothing can be injected into the script.
- `locate` fetches all sessions' tty, window id and session id in one Apple Event and caches tty to (window, session); a cache entry is dropped when a send reports the session missing, then looked up once more.
- The child is restarted if it exits or a request takes longer than 3 s; requests are serialised.
- The script types text only; the 300 ms pause, the second screen read and Enter (a `key` request) belong to the service, so a prompt that appears while typing is never answered by that Enter.

### API (`api/remote-v1.openapi.yaml`, additive)

- `Health.capabilities` gains `terminal_input` when it is enabled.
- `SessionDetail` gains optional `terminal` for sessions open in a terminal:
  `{ status: "busy"|"idle"|unknown, canType: bool, reason?: "disabled"|"not_claude"|"mac_in_use"|"no_terminal_driver"|"automation_denied"|"not_at_input", prompt?: { promptId: string, question: string, options: [{ key: "1", label: "Yes" }] } }`. `prompt` comes from parsing the screen, with optional `detail` (the lines between the rule above the prompt and its question: the command and its description, or the file and a preview; at most 20 lines and 2,000 characters), shown in monospace on the card; `promptId` is a hash of the whole body, the question and the option labels (the question and labels alone collided for every "Do you want to proceed?" Bash prompt), so the same prompt keeps its id across reads and a new prompt gets a new one.
- `POST /v1/sessions/{id}/terminal/messages` `{ text, key? }` → `202 { status: "typed" }`; errors `403 disabled`, `404 unknown_session`, `409 not_in_terminal|not_claude|mac_in_use|no_terminal_driver|automation_denied|not_at_input`, `400` empty text. The idempotency `key` prevents typing the same message twice on a retry (remembered for 10 minutes).
- `POST /v1/sessions/{id}/terminal/keys` `{ promptId, option, key? }` → `202 { status: "typed" }`; `409 no_prompt|prompt_changed` when no prompt is showing, it is a different prompt, or `option` is not one of its keys. `key` is the idempotency key, as for messages. (The pressed key is in `option`, so `key` means the same thing on both endpoints.)
- `POST /v1/sessions/{id}/messages` is unchanged and still answers `409 open_in_terminal`, so older clients behave exactly as today.

### Prompt parsing (`internal/terminput/prompt.go`)

Recognises Claude Code's permission prompt on the screen: a line starting `Do you want to` followed by numbered options (`❯ 1. Yes`, `2. Yes, and …`, `3. No`), ending at `Esc to cancel`. Returns the question and the options with their digit keys. Pinned by fixtures of captured screens in `internal/terminput/testdata/prompts/` (from the spike: create-file prompt, a Bash prompt, a wrapped option label, no prompt, a prompt scrolled half off). Any screen that does not match returns no prompt.

### Clients (iPhone and web, same behaviour)

- A terminal session's chat shows the composer when `terminal.canType`; otherwise the read-only line explains why ("Your Mac is in use", "Not running in iTerm2", "Terminal input is off on your Mac").
- The header shows "On your Mac · Working" or "On your Mac · Your turn" from `terminal.status`.
- Sending posts to `terminal/messages`; the message shows as pending "Sent to your Mac's terminal" and clears when a history turn with the same prompt appears (the existing history-match rule). The rule is extended for two shapes Claude Code saves: a message typed while busy is saved with the prefix "The user sent a new message while you were working:", and a long paste is saved wrapped in pasted-content markers; both count as the same prompt after the wrapper is removed. Neither shape is confirmed live: the live check sent only short messages while idle, which were saved as typed. The shapes come from earlier transcripts and stay unconfirmed until a live check sends a long paste and a message while busy. After 2 minutes without a match the pending line changes to "Sent; no answer yet" rather than staying forever.

### Setup at the Mac

`aimux remote terminal-input check`, run at the Mac, sends one harmless Apple Event to iTerm2 (listing session ttys) so macOS shows its Automation prompt while the owner is there, then prints which sessions a driver can reach and whether input is enabled. The remote access guide tells the owner to run it once after enabling. Today `aimux remote serve` runs from an iTerm2 shell, so it inherits iTerm2's own grant; a background service would need this step.
- While `terminal.status` is busy, or for 2 minutes after a send, the chat re-reads the detail every 2 s; otherwise every 15 s.
- `terminal.prompt` renders as the existing approval card; each option posts its key to `terminal/keys`; a 409 `no_prompt` collapses the card without an error.

## Security

- Opt-in per Mac (`enabled: false` by default); the phone only sees `terminal_input` in capabilities when it is on.
- Same identity check as the rest of `/v1` (Tailscale identity, no `Origin`); no new listener.
- The foreground-process and idle checks run on every request, server side.
- No secrets exist in this design: Apple Events need no cookie or key. The log never holds message text.
- Nothing in the repository names the owner's machine, tailnet, ttys or sessions; fixtures use invented text. The pre-commit secrets scan covers the new files.
- aimux never creates, focuses or closes windows.

## Testing

- `terminput.Service` with a fake driver: every check and error code, the idempotency key, the log line (and that it holds no text).
- Prompt parser on the fixture screens.
- iTerm2 driver: unit tests for the JSON-lines protocol against a fake helper; an opt-in integration test (build tag `iterm2`, run by hand) against a scratch iTerm2 pane running `cat`, which never creates windows itself (the tester opens the pane first).
- API: handler, contract and smoke entries for both endpoints and the `terminal` field.
- Clients: unit tests for the composer rules, pending clearing and polling cadence; UI tests with demo terminal sessions.
- Live check before calling it done: a scratch Claude session in a pane the tester opens; send, approve with `1`, deny with `3`, idle guard on and off.

## Left to the builder

- Exact wording of the read-only reasons on the phone.
- The JXA script's internals, as long as the protocol and timings above hold.
- How the service caches `Status` between requests (it may reuse the registry read and the screen read for a second).

# Sending phone messages into terminal sessions: evaluation

Sessions open in a terminal on the Mac are read-only from the phone today (`409 open_in_terminal`). This note records how aimux could type a phone message into the terminal pane instead, what was measured, and the license constraint that shapes the choice.

## What was tested

Against scratch Claude Code sessions (2.1.311) in new iTerm2 windows, never the owner's sessions:

| Case | Result |
|---|---|
| Message while idle | answered (`PONG`) |
| Message while busy | queued by Claude Code ("Press up to edit queued messages"), answered after the running command |
| Permission prompt | key `1` approved ("Wrote 1 line to made.txt"), key `3` denied ("The user doesn't want to proceed…") |
| Multi-line (bracketed paste) | answered; recorded as one user turn with the newline |
| 4,091 characters | answered; recorded as pasted content |
| Arabic text | answered |
| Transcript | every typed message is a real user turn (`type: user`), not a message from another session |

### Live check of the built feature

The opt-in test `internal/terminput/iterm2/service_live_test.go` (build tag `iterm2`, `AIMUX_LIVE_SESSION_ID`) runs the real `terminput.Service`, `SystemProbe`, iTerm2 driver and Claude's session registry against one scratch Claude Code session (2.1.311, manual permission mode, its own iTerm2 window, 27 iTerm2 sessions open). It does not go through HTTP: `/v1` needs a Tailscale identity a local test cannot present, so the HTTP layer is covered by handler and contract tests. All steps passed in one run of 42 s:

| Step | Result | Time |
|---|---|---|
| Status while idle | `idle`, `canType` true | 49 ms |
| Send "Reply with the single word LIVEONE" | typed; answered | send 374 ms, answer 14.6 s |
| Send a request for the Write tool | typed; Status showed the prompt "Do you want to create live.txt?" with options 1 Yes, 2 "Yes, and switch to accept edits ...", 3 No | send 370 ms, prompt seen after 8.1 s |
| Send while the prompt shows | refused `not_at_input`, nothing typed | 50 ms |
| Press `1` with the prompt's id | `live.txt` written with "ok" | 67 ms, file after 0.5 s |
| Second prompt, press `3` (labelled No) | denied; `no.txt` not created | 66 ms |
| Press again with the old prompt id | `no_prompt` | 33 ms |
| Idle guard on (`allow_while_in_use` false), Mac idle 2.9 s | `mac_in_use`, nothing typed | 48 ms |
| 300 characters (sent as a paste) | answered; saved as typed | 371 ms |
| Two lines (sent as a paste) | answered; saved as typed, with the newline | 373 ms |
| Text with `ESC[201~` | `bad_text`, nothing typed | under 1 ms |

The real prompt ends with the footer "Esc to cancel · Tab to amend" and nothing below it, so the parser's rule (only blank lines after the footer) holds. Both pastes were saved in the transcript exactly as typed, with no "while you were working" prefix or `<pasted_content>` wrapper (both were sent while idle and are short); the shared fixture `api/testdata/clients/terminal/saved-prompt-match.json` gained that two-line case.

Rules learned:

- Send the text, wait about 300 ms, then send Enter on its own. Text and Enter in one write was sometimes taken as a paste and left unsent.
- Creating a window takes keyboard focus. aimux must never create or focus windows; writing to an existing session does not move focus.
- Before typing, the pane's foreground process must be `claude`; otherwise the text would land in a shell.

## Speed

Measured on the owner's Mac with 27 iTerm2 sessions open:

| Method | Per send |
|---|---|
| `osascript`, find the pane by walking every session's tty | 1,054 ms |
| Same, compiled script | 1,219 ms |
| `osascript`, address the window and session by id | 578 ms |
| `osascript` start-up alone | 332 ms |
| Walking 27 sessions one property at a time (JXA) | 849 ms |
| Bulk fetch of all sessions' tty and id in one Apple Event (JXA) | 142 ms (one-off process) |
| Persistent JXA process, address by id | 50 ms (first send 144 ms) |
| iTerm2 Python API, persistent connection | 0.5 to 1.9 ms (connect 4 ms) |

The persistent JXA process removes `osascript` start-up and keeps a cache of tty to window and session id; with bulk lookup on a cache miss it stays around 50 to 150 ms.

### Driver as built

The iTerm2 driver (`internal/terminput/iterm2`, persistent JXA helper), measured with `live_test.go` against a scratch pane, 27 iTerm2 sessions open, two runs:

| Call | Run 1 | Run 2 |
|---|---|---|
| Locate, cold (helper start and bulk listing) | 485 ms | 219 ms |
| Locate, cached | 0 ms | 0 ms |
| Probe (bulk listing, warm helper) | 67 ms | 50 ms |
| Send (includes the 300 ms pause) | 346 ms | 342 ms |
| Send as paste, multi-line | 330 ms | 335 ms |
| Key `1` | 24 ms | 23 ms |
| Key escape | 37 ms | 33 ms |
| Screen | 28 ms | 40 ms |
| Close (helper exits on end of input) | 3 ms | 2 ms |

A send costs about 40 ms on top of the required 300 ms pause, in line with the estimate above. Through the service (locate, screen read, then send) a send took 370 to 374 ms in the live check.

## License constraint

aimux is MIT licensed and public. iTerm2, including its API protocol definition `proto/api.proto`, is GPL-2.0 (the repository's license; the file has no header of its own). The existing Go client `marwan-at-work/iterm2` is MIT but contains code generated from that file. Code in aimux derived from the GPL protocol file could oblige aimux to be distributed under the GPL, or force its removal, and published copies cannot be withdrawn. Whether a client written from scratch against the documented wire protocol counts as derived is a legal question this note does not answer.

So, until the owner decides otherwise:

- aimux does not vendor `api.proto`, generated code from it, or a library built from it.
- AppleScript and JXA talk to iTerm2 through Apple Events, which carry no license obligation.
- A faster iTerm2 API driver can be added later, behind the same driver interface, once the license question is settled (for example a separately installed helper the user adds, outside the aimux repository).

## Options

| Option | Speed | Constraint |
|---|---|---|
| Persistent JXA helper (`osascript -l JavaScript`, started by aimux) | ~50 ms | none; Apple Events; macOS asks once to let aimux control iTerm2 when it runs outside an iTerm2 tab |
| One `osascript` per send | 0.6 to 1 s | none; simplest |
| iTerm2 API, Go client | ~2 ms | GPL question above |
| iTerm2 API, separate Python helper using the `iterm2` package | ~2 ms after connect | needs Python and the package on the Mac; the package is GPL but stays a separate program |
| In-process Apple Events from Go (cgo) | tens of ms (not measured) | aimux builds with `CGO_ENABLED=0` (`.goreleaser.yml`); would change the release build |
| tmux `send-keys` | not measured | only for sessions in tmux; a later driver |
| Injecting into the tty (`TIOCSTI`) | not measured | not pursued: restricted on macOS and a known attack vector |

## Recommendation

A pluggable terminal driver in aimux (find the pane, send text, press a key, optionally read the screen), with a first driver for iTerm2 built on a persistent JXA helper: about 50 ms per send, no license exposure, no new dependencies. The shared code, not each driver, enforces the safety rules: the pane runs `claude`, the Mac-idle guard with a developer override, a local log of what was typed, and no secrets in the repository.

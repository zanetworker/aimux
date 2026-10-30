# Terminal input from the phone. Implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Both clients can send a message into a Claude Code session open in an iTerm2 pane and answer its permission prompts, through a pluggable terminal driver with server-side safety checks.

**Architecture:** A new core package `internal/terminput` holds the driver interface, the service with every check (enabled, live terminal session, foreground `claude`, Mac idle, empty input box, prompt id), the screen parsers and the local log. The iTerm2 driver talks to one persistent `osascript -l JavaScript` helper over JSON lines. `internal/remoteapi` exposes two endpoints and an optional `terminal` field on the session detail; the clients render it.

**Tech Stack:** Go 1.25 (no cgo), JavaScript for Automation, Swift 6/SwiftUI iOS 27, plain ES modules.

**Spec:** `docs/superpowers/specs/2026-10-27-terminal-input-design.md` (read it whole) and `docs/superpowers/specs/2026-10-27-terminal-input-evaluation.md`.

## Global Constraints

- Branch `feat/clients`, worktree `aimux-worktrees/clients`. Commit with `&&` after tests pass; never push; never `git stash`. Trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Core packages (everything under `internal/` except `tui/`) never import UI packages (`.claude/CLAUDE.md`). `internal/terminput` imports nothing from `remoteapi`.
- Never vendor iTerm2's `api.proto`, code generated from it, or a library built from it. No cgo.
- aimux never creates, focuses, resizes or closes iTerm2 windows, tabs or sessions. Tests that need a pane use one the tester opened by hand.
- Never type into, read the screen of, or otherwise touch the owner's existing sessions; live tests use scratch sessions only.
- Off by default: `remote_access.terminal_input.enabled: false`.
- The log (`~/.aimux/logs/terminal-input.log`, mode 0600) never contains message text.
- Fixtures and test data are invented: no real paths, session titles, tailnet names, ttys from this machine's sessions, statusline content, costs or account names.
- Every new endpoint: handler test, contract coverage, smoke entry (`.claude/CLAUDE.md` Three-Tier Testing).

## Review Focus

- **A permission prompt appears between the phone's last read and the send:** the send is refused with `409 not_at_input`; nothing is typed. Pinned in Task 1 (service test) and Task 2 (parser fixture `prompt-showing.txt` rejected by the input-box check).
- **A half-typed draft at the Mac** (owner override on): refused `409 not_at_input`, the draft is untouched. Pinned by fixture `draft-in-box.txt`.
- **Claude exited and the pane is a shell:** refused `409 not_claude`. Pinned in Task 1 with a fake process table.
- **A retried or late key for a prompt that changed:** `409 prompt_changed`; a retry with the same idempotency key after success is not typed twice. Pinned in Task 1.
- **The helper hangs or iTerm2 quits:** the request fails within 3 s with a clear error, the helper restarts on the next request. Pinned in Task 3 with a fake helper that stops answering.

## Task 1: `internal/terminput` service with a fake driver

**Files:**
- Create: `internal/terminput/terminput.go` (types, `Driver`, `Service`, errors), `internal/terminput/probe.go` (tty and foreground lookup via `ps`, `HIDIdleTime` via `ioreg`, behind an injectable `System` interface), `internal/terminput/log.go`
- Modify: `internal/config/config.go` (`TerminalInputConfig{Enabled bool; AllowWhileInUse bool}` under `RemoteAccessConfig` as `TerminalInput` yaml `terminal_input`)
- Test: `internal/terminput/terminput_test.go`, `internal/config/config_test.go`

**Interfaces:**
- Produces:
  - `type Target struct{ PID int; TTY string }`, `type Pane struct{ Driver, ID string }`, `type Key string` (`"1"`..`"9"`, `"escape"`, `"enter"`)
  - `type Driver interface { Name() string; Locate(ctx, Target) (Pane, bool, error); Send(ctx, Pane, text string) error; Key(ctx, Pane, Key) error; Screen(ctx, Pane) (string, error) }`
  - `type System interface { TTY(pid int) (string, error); ForegroundIsPID(tty string, pid int) (bool, error); IdleSeconds() (float64, error) }` with a real implementation using `ps -o tty= -p`, `ps -o tpgid= -t`/`ps -o pgid= -p`, and `ioreg -c IOHIDSystem`
  - `type Sessions interface { TerminalPID(sessionID string) (int, bool) }` (the registry lookup; the server passes one built on `discovery.ReadClaudeRegistry` + `IsLiveHolder`, excluding aimux-owned pids)
  - `type Status struct { State string /*busy|idle|unknown*/; CanType bool; Reason string; Prompt *Prompt }`, `type Prompt struct { ID, Question string; Options []Option }`, `type Option struct { Key Key; Label string }`
  - `func NewService(cfg config.TerminalInputConfig, sessions Sessions, sys System, log io.Writer, drivers ...Driver) *Service`
  - `func (s *Service) Status(ctx, sessionID, registryStatus string) Status`
  - `func (s *Service) Send(ctx, sessionID, text, idemKey string) error`
  - `func (s *Service) Press(ctx, sessionID, promptID string, option Key, idemKey string) error`
  - Errors with stable codes: `ErrDisabled` (`disabled`), `ErrNotInTerminal` (`not_in_terminal`), `ErrNotClaude`, `ErrMacInUse`, `ErrNoDriver` (`no_terminal_driver`), `ErrAutomationDenied`, `ErrNotAtInput`, `ErrNoPrompt`, `ErrPromptChanged`; `func Code(err error) string`
- Consumes from Task 2 (write Task 2's parser first inside this task if convenient, or stub then wire): `ParsePrompt(screen string) *Prompt`, `InputBoxEmpty(screen string) bool`.

Checks in order, as in the design: enabled; terminal pid; tty; foreground is that pid; idle ≥ 120 s unless `AllowWhileInUse`; a driver locates the pane (`ErrAutomationDenied` passes through from the driver); screen read; then `InputBoxEmpty` for `Send`, or `ParsePrompt` with matching `ID` and an option `Key` for `Press`. Idempotency: a successful `(sessionID, idemKey)` is remembered 10 minutes and returns nil without typing again; an empty idemKey is never remembered. Log line per attempt: RFC 3339 time, session id, `text` with rune count or `key` with the option, and the outcome code (`ok` or the error code).

- [ ] **Step 1: Failing tests** with a fake driver, fake system and fake sessions: one test per error code in check order (`TestSendDisabled`, `TestSendNotInTerminal`, `TestSendNotClaude`, `TestSendMacInUse`, `TestSendMacInUseOverride` (allowed), `TestSendNoDriver`, `TestSendAutomationDenied`, `TestSendNotAtInputWhenPromptShowing`, `TestSendTypesWhenBoxEmpty` (driver saw exactly the text), `TestSendIdempotent`, `TestPressMatchingPrompt`, `TestPressPromptChanged`, `TestPressUnknownOption`, `TestPressNoPrompt`, `TestLogHasNoText` (log line contains the rune count and not the text), `TestStatusReasons` (each failing check maps to `CanType=false` and its reason; `Prompt` set when showing); config test that `terminal_input` parses and defaults to disabled.
- [ ] **Step 2: Run** `go test ./internal/terminput/ ./internal/config/`; expect compile failures.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go build ./... && go vet ./internal/terminput/ && go test ./internal/terminput/ ./internal/config/`.
- [ ] **Step 5: Commit** `feat(terminput): service with safety checks and a driver interface`.

## Task 2: Screen parsers with fixtures

**Files:**
- Create: `internal/terminput/screen.go` (`ParsePrompt`, `InputBoxEmpty`), `internal/terminput/testdata/screens/*.txt`, `internal/terminput/screen_test.go`

Fixtures are invented screens in Claude Code's layout. The create-file prompt captured in the spike looked like this (paths and the status line must be replaced with invented ones):

```
⏺ Write(made.txt)
────────────────────────────────────────────────────────────────────────────────
 Create file
 made.txt
╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
  1 hi
╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
 Do you want to create made.txt?
 ❯ 1. Yes
   2. Yes, and switch to accept edits (auto-approve file edits and common file
      commands) for this session (shift+tab)
   3. No
 Esc to cancel · Tab to amend
```

An idle input box looked like this (the line between the two rules; the placeholder is grey text after `❯ `):

```
────────────────────────────────────────────────────────────────────────────────
❯ Try "how does <filepath> work?"
────────────────────────────────────────────────────────────────────────────────
  [Model ◔ medium] │ project
```

and with nothing in it, `❯ ` followed by spaces. A half-typed draft shows the draft after `❯ `. While Claude works, the box sits under a spinner line (`✶ Ionizing… (10s · ↓ 76 tokens)`).

Because the screen text does not carry colour, the placeholder cannot be told from typed text by style. Rule: the box counts as empty when the text after `❯ ` is empty, or starts with `Try "` and ends with `"`, or is `Press up to edit queued messages` (both are Claude Code placeholders seen in the spike). Anything else is a draft.

- [ ] **Step 1: Fixtures** (one file each): `idle-empty.txt`, `idle-placeholder.txt`, `busy-empty.txt` (spinner above the box), `busy-queued-hint.txt` (`Press up to edit queued messages`), `draft-in-box.txt`, `prompt-create-file.txt` (as above), `prompt-bash.txt` (a `Bash command` prompt with `Do you want to proceed?` and 3 options), `prompt-wrapped-label.txt` (an option label wrapping to a second line, as option 2 above), `prompt-half-scrolled.txt` (the question line is above the visible screen: no prompt), `shell-prompt.txt` (a zsh prompt, Claude not running: no box), `plan-approval.txt` (a non-permission dialog with numbered choices but no `Do you want to`: not a permission prompt, and the box is not empty).
- [ ] **Step 2: Failing tests:** table test over all fixtures asserting `InputBoxEmpty` and `ParsePrompt` (question, option keys and labels with wrapped lines joined by one space, and that `ID` is stable for the same question and labels and differs when a label differs).
- [ ] **Step 3: Implement** (`ID`: first 16 hex chars of SHA-256 over the question and labels joined by `\n`).
- [ ] **Step 4: Run** `go test ./internal/terminput/`.
- [ ] **Step 5: Commit** `feat(terminput): recognise Claude Code's input box and permission prompts`.

## Task 3: iTerm2 driver and the check command

**Files:**
- Create: `internal/terminput/iterm2/iterm2.go` (driver), `internal/terminput/iterm2/helper.js` (embedded), `internal/terminput/iterm2/iterm2_test.go`, `internal/terminput/iterm2/live_test.go` (`//go:build iterm2`)
- Modify: `cmd/aimux/cmd/remote.go` (subcommand `aimux remote terminal-input check`)
- Test: `cmd/aimux/cmd/remote_test.go` (check command output with a fake driver)

**Interfaces:**
- Produces: `func New(opts Options) *Driver` implementing `terminput.Driver`; `Options{ Command func(ctx) *exec.Cmd /* default: osascript -l JavaScript <embedded script via -e or a temp file> */; Timeout time.Duration /* 3 s */ }`.
- Helper protocol, one JSON object per line each way: requests `{"id":n,"op":"locate"}` (returns `{"id":n,"sessions":[{"window":123,"session":"UUID","tty":"/dev/ttysNNN"}]}` from one bulk Apple Event: `app.windows.tabs.sessions.tty()` and `.id()` plus `app.windows.id()`), `{"op":"send","window":w,"session":s,"text":t,"paste":bool}` (writes text without newline, waits 300 ms, writes Enter; `paste` wraps the text in `ESC[200~`/`ESC[201~`; use paste when the text has a newline or is over 200 characters), `{"op":"key","window":w,"session":s,"key":"1"|"escape"|"enter"}`, `{"op":"screen","window":w,"session":s}` (returns `{"text":...}` from the session's `contents`/`text`). Errors `{"id":n,"error":"missing"|"automation_denied"|"<message>"}`; map Apple Event error -1743 to `automation_denied`.
- The Go side starts the helper lazily, serialises requests, caches tty → (window, session) from `locate`, re-locates once on `missing`, kills and restarts the helper on a 3 s timeout or exit.

- [ ] **Step 1: Failing tests** with `Options.Command` pointing at a fake helper (a small Go test binary or `sh` script that speaks the protocol): locate and cache, send uses cached ids, `missing` triggers one re-locate, timeout restarts the helper and returns an error within 3.5 s, `automation_denied` maps to `terminput.ErrAutomationDenied`, text with quotes/newlines/Arabic round-trips as JSON unchanged.
- [ ] **Step 2: Run, expect failures; implement; run** `go test ./internal/terminput/...`.
- [ ] **Step 3: Live test** (`//go:build iterm2`, run by hand): reads `AIMUX_ITERM2_TEST_TTY` (a pane the tester opened, running `cat`); locate finds it, send delivers, screen reads it; skips when the variable is unset. Run it against a pane you open yourself running `stty -echo; cat > /dev/null` and record the timings in the report. Never open windows from code.
- [ ] **Step 4: Check command:** `aimux remote terminal-input check` prints whether input is enabled, the Automation result, and for each open Claude session (from the registry) whether a driver reaches its pane (session id short form and tty only). Test with fakes.
- [ ] **Step 5: Commit** `feat(terminput): iTerm2 driver over a persistent JXA helper`.

## Task 4: API

**Files:**
- Modify: `api/remote-v1.openapi.yaml` (schemas `TerminalStatus`, `TerminalPrompt`, `TerminalOption`, `TerminalMessageRequest`, `TerminalKeyRequest`, `TerminalAccepted`; optional `terminal` on `SessionDetail`; capability `terminal_input`; the two endpoints with their error codes), `internal/remoteapi/api.go`, `internal/remoteapi/local.go`, `cmd/aimux/cmd/remote.go` (build the service from config and the registry; pass the iTerm2 driver), `docs-site/src/content/docs/guides/remote-access.mdx` (setup, the setting, the check command, what the phone shows, the safety rules)
- Test: `internal/remoteapi/api_test.go`, `contract_test.go`, the smoke test where `/v1` endpoints are listed

**Interfaces:** JSON exactly as in the design's API section; `terminal` only for sessions in state `open_in_terminal`; `Health.capabilities` includes `terminal_input` only when enabled. Error codes map 1:1 from `terminput.Code`. `POST /v1/sessions/{id}/messages` unchanged.

- [ ] **Step 1: Failing tests:** detail carries `terminal` for a terminal session with a fake service (status, canType, reason, prompt) and omits it otherwise; each endpoint's success and every error code; contract validates both endpoints and a detail with `terminal`; capability present only when enabled.
- [ ] **Step 2: Implement; run** `go build ./... && go vet ./... && go test ./... -timeout 60s`; `cd docs-site && npm run build`.
- [ ] **Step 3: Commit** `feat(remote): terminal input endpoints and status`.

## Task 5: iPhone client

**Files:** AimuxKit (`SessionDetail.swift` decode `terminal`; `APIClient` `terminalSend(sessionID:text:key:)`, `terminalPress(sessionID:promptID:option:key:)`; `ChatModel` terminal mode: polling cadence, pending "Sent to your Mac's terminal" with the extended history match, `not_at_input` re-read, `prompt_changed`/`no_prompt` collapse; `ChatPresentation` header and read-only reason text), App (`ChatView` composer for terminal sessions, approval card for `terminal.prompt`), `DebugHooks.swift` demo terminal sessions, `UITests/ChatUITests.swift`, `project.yml` build number +1.

Rules from the design's Clients section, verbatim. History match extended: strip the prefix `The user sent a new message while you were working:` and pasted-content wrappers before comparing. Polling: every 2 s while `terminal.state == busy` or within 2 minutes of a send, else 15 s; stops on disappear. After 2 minutes unmatched, the pending line reads "Sent; no answer yet".

- [ ] **Step 1: Failing tests** (AimuxKit): decode with and without `terminal`; send posts the right body with a key, retry reuses it; `not_at_input` triggers one detail re-read; prompt answer posts `promptId`/`option`; `prompt_changed` collapses; pending clears on a wrapped history turn; cadence 2 s/15 s with an injected clock. UI test with `-demo chat-terminal`: composer visible, sending shows the pending line, a terminal prompt card answers.
- [ ] **Step 2: Implement; full xcodebuild test; swiftlint --strict; screenshots** of the terminal chat (light and dark, 14 Pro iOS 27 `BDA646FE-5717-4244-A935-C3917E94689F`) into the session scratchpad.
- [ ] **Step 3: Commit** `feat(ios): talk to terminal sessions`.

## Task 6: Web client

**Files:** `clients/web/webapp/app.js` (terminal mode in the chat), `chatView.mjs`, `timeline.mjs` (export the extended prompt match if it lives there, shared semantics with iOS), new tests in the matching `.test.mjs` files; the Playwright behaviour check in the scratchpad (mocked `/v1`, per R13 of the previous plan).

Same rules as Task 5.

- [ ] **Step 1: Failing tests** (node) for the history match on wrapped turns and the cadence helper; Playwright: composer for a terminal session, send posts `/terminal/messages` with a key, pending line, `not_at_input` re-read, prompt card posts `/terminal/keys`.
- [ ] **Step 2: Implement; run** `node --test "clients/web/webapp/*.test.mjs"`, `go test ./clients/web/`, the Playwright check; screenshots.
- [ ] **Step 3: Commit** `feat(web): talk to terminal sessions`.

## Task 7: Docs and live check

**Files:** `clients/ios/README.md`, `clients/ios/docs/project-context.md`, `.claude/CLAUDE.md` (code map: `internal/terminput`), `RELEASE_NOTES.md` (Unreleased), `docs/superpowers/specs/2026-10-27-terminal-input-evaluation.md` (add the measured helper timings from Task 3).

- [ ] **Step 1: Update** docs to what exists.
- [ ] **Step 2: Live check** with a scratch Claude session in a pane the tester opens by hand (the controller opens it and tells the owner): enable in a scratch config, run the server against it on a scratch port, then through the real endpoints: send while idle, send while busy, `not_at_input` while a prompt shows, approve with `1`, deny with `3`, idle guard on (expect `mac_in_use` while the tester is active) and off. Record results in the evaluation doc.
- [ ] **Step 3: Commit** `docs: terminal input`.

# Open on Mac Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One tap on the phone hands a session that is not open in a terminal to a new tab in a dedicated "aimux" iTerm2 window running the harness's resume command, after which the phone drives it as a terminal session.

**Architecture:** `acprun.Manager` can drop one idle run; the `Broker` can hold a session so no turn (queue or phone) starts during the handoff; a new one-shot `iterm2.Opener` (separate from the never-create-windows helper) opens the tab; `remoteapi.Handoff` orders hold, drop, open and wait, behind `POST /v1/sessions/{id}/open-on-mac`; `remoteapp` wires it with the provider's own `ResumeCommand`. Both clients add the action.

**Tech Stack:** Go (net/http, osascript JXA), ES modules (web client, node --test), SwiftUI + AimuxKit (XcodeGen, XCTest).

**Spec:** `docs/superpowers/specs/2026-11-01-open-on-mac-design.md`

## Global Constraints

- Additive API: `api/remote-v1.openapi.yaml` gains the route, the `openOnMac` health flag and the error codes; existing shapes unchanged; device login required; audit action `open_on_mac`, never with text.
- Tests never create, focus, type into or close the owner's iTerm2 windows. Unit tests use fakes; the one live test is opt-in (build tag `iterm2`, env `AIMUX_LIVE_OPENTAB=1`) and is run by the controller only after the owner approves.
- The existing `internal/terminput/iterm2/helper.js` keeps its promise (never creates windows); opening lives in a separate script.
- The command typed into the new tab is built only from the provider's `ResumeCommand` (binary path, args, dir), shell-quoted in Go; session text never reaches the shell. The tab title may carry the shown title, passed as data.
- No harness names in remoteapi or remoteapp logic beyond what the provider registry gives.
- No new dependencies. Copy is shared through `api/testdata/clients/terminal/open-on-mac.json`; both clients read the same strings and codes.
- Never print session text or titles in command output. Stage files by explicit path. Do not push.
- Writing in copy and docs: no em dashes.

Exact copy (fixture values, verbatim):

| key | text |
|---|---|
| action | Open on Mac |
| progress | Opening on your Mac… |
| done | Open in the aimux window on your Mac |
| busy | Claude is answering. Wait for it to finish, or Stop it first. |
| open_in_terminal | Already open in a terminal on your Mac. |
| no_terminal | Open on Mac needs iTerm2 on your Mac. |
| not_authorized | aimux may not control iTerm2. Allow it in System Settings, Privacy & Security, Automation. |
| in_use | Another aimux process is using this session. |
| not_started | This session has no messages yet, so there is nothing to open. |
| timeout | The tab opened, but Claude did not pick up the session. Check the aimux window on your Mac. |

## Review Focus

1. A folder or title with spaces, quotes, `$`, newlines or non-ASCII: the command still runs exactly `cd <dir> && <bin> --resume <id>`, nothing else (Task 3 test).
2. The owner closed the aimux window since last time: the next open creates a new window and remembers it (Task 3 test).
3. Two taps at once on the same session: the second answers `busy`, only one tab opens (Task 4 test).
4. A queued message during the handoff: it is not started as a background turn; afterwards the terminal path sends it (Task 2 and Task 4 tests).
5. Claude never takes the session (binary missing, tab closed at once): `timeout` after 20 s, the hold is released, nothing restarts the background run on its own (Task 4 test).

## Deviations from the spec

- The lock error keeps the API's existing code `in_use`, not `in_use_elsewhere`.
- The aimux window is found by its stored id only; when that window is gone a new one is created (iTerm2 window names follow the running command, so a name lookup is unreliable).
- A new `not_authorized` refusal covers macOS denying aimux control of iTerm2.

### Task 1: Drop one idle run

**Files:**
- Modify: `internal/acprun/manager.go`
- Test: `internal/acprun/manager_test.go`

**Interfaces:**
- Produces: `func (m *Manager) Drop(sessionID string) error`: closes that session's run if it is idle and releases its reservation; `nil` when there is no run; `ErrBusy` (existing busy error of the manager; add `var ErrBusy` only if none exists) when a turn is running.

- [ ] **Step 1: Failing tests** `TestManager_DropClosesIdleRunAndReleasesLock` (after `send`, Drop returns nil, `Running("a")` false, `SessionLock(dir,"a")` succeeds), `TestManager_DropWithoutRunIsNil`, `TestManager_DropBusyRunRefuses` (a fake run blocked mid-turn: `errors.Is(err, ErrBusy)`, still Running).
- [ ] **Step 2:** `go test ./internal/acprun/ -run Drop` fails (undefined Drop).
- [ ] **Step 3:** implement under the manager's lock, reusing the retire path Reap uses.
- [ ] **Step 4:** `go test ./internal/acprun/ -count=1 -race` passes.
- [ ] **Step 5:** commit `feat(acprun): Drop closes one idle run`.

### Task 2: Hold a session in the Broker

**Files:**
- Modify: `internal/remoteapi/broker.go`
- Test: `internal/remoteapi/broker_test.go`

**Interfaces:**
- Produces: `func (b *Broker) Hold(session string) (release func(), err error)`: refuses with the busy error `Start` uses when a turn runs or another hold exists; while held, `Busy(session)` is true and `Start(session, …)` returns that busy error; `release` is idempotent.

- [ ] **Step 1: Failing tests** `TestBroker_HoldBlocksStartAndReadsBusy`, `TestBroker_HoldRefusesWhileTurnRuns`, `TestBroker_SecondHoldRefused`, `TestBroker_ReleaseTwiceIsSafe` (after release, Start works again).
- [ ] **Step 2:** run, fails.
- [ ] **Step 3:** a `held map[string]bool` under the Broker's existing lock.
- [ ] **Step 4:** `go test ./internal/remoteapi/ -count=1 -race` passes (queue dispatch tests included: `QueueACP.Busy` now sees holds).
- [ ] **Step 5:** commit `feat(remoteapi): Broker.Hold keeps turns out during a handoff`.

### Task 3: Open a tab in the aimux window

**Files:**
- Create: `internal/terminput/iterm2/opener.go`, `internal/terminput/iterm2/opentab.js` (embedded)
- Create: `internal/terminput/iterm2/opener_test.go`, `internal/terminput/iterm2/opentab_live_test.go` (build tag `iterm2`)

**Interfaces:**
- Produces:
  - `type Opener struct { StatePath string; Run func(ctx context.Context, args ...string) ([]byte, error) }` (Run nil: `osascript -l JavaScript`; StatePath: `~/.aimux/iterm-aimux-window.json`, owner-only).
  - `func (o *Opener) OpenTab(ctx context.Context, title string, argv []string, dir string) error`.
  - `func ShellLine(argv []string, dir string) string` = `cd '<dir>' && '<argv0>' '<arg>'…` with POSIX single-quote escaping.
  - `var ErrNotAuthorized = errors.New("iterm2: automation not allowed")`.
- Script contract (`opentab.js`, run with `osascript -l JavaScript <script> <windowID> <title> <line>`; all three are argv data, never evaluated): if a window with `windowID` exists, add a tab there with the default profile, else create a window with the default profile and use its first tab; set that tab's session name to `title`; write `line` plus newline into that session; print `{"window": <id>}`. It never activates, focuses or closes anything. Apple event denial (-1743) prints `{"error":"not_authorized"}`.
- Opener: reads the stored window id (0 when missing or unreadable), runs the script, maps `not_authorized` to `ErrNotAuthorized`, stores the returned id atomically with 0600.

- [ ] **Step 1: Failing tests** with a fake `Run` recording args:
  - `TestShellLine_QuotesEverything`: dir `/tmp/a b/it's $HOME`, argv `[/usr/local/bin/claude --resume 3f2a-…]` gives exactly `cd '/tmp/a b/it'"'"'s $HOME' && '/usr/local/bin/claude' '--resume' '3f2a-…'`; a newline in dir stays inside quotes.
  - `TestOpener_PassesDataAsArgs`: the script path and three args reach Run unchanged; the title with quotes is one arg.
  - `TestOpener_RemembersWindow`: first call passes `0`, the reply `{"window":42}` is stored; second call passes `42`.
  - `TestOpener_NewWindowWhenStoredOneIsGone`: the script replies a new id 77; it is stored.
  - `TestOpener_NotAuthorized`: reply `{"error":"not_authorized"}` gives `ErrNotAuthorized`, state unchanged.
  - `TestOpener_StateFileIsOwnerOnly`: mode 0600.
- [ ] **Step 2:** run, fails.
- [ ] **Step 3:** implement; embed the script with `//go:embed`, write it to an `os.CreateTemp` file for each call (removed afterwards) and pass the three values as argv after the script path.
- [ ] **Step 4:** `go test ./internal/terminput/iterm2/ -count=1` passes; `go vet` clean.
- [ ] **Step 5:** live test `TestOpenTabLive` (skips unless `AIMUX_LIVE_OPENTAB=1`): opens a tab running `echo aimux-live` in a temp StatePath's window, reads its id, then closes only that window through a separate JXA call by that id. Do not run it; the controller runs it after the owner approves.
- [ ] **Step 6:** commit `feat(iterm2): Opener opens a tab in the aimux window`.

### Task 4: The handoff and its route

**Files:**
- Create: `internal/remoteapi/handoff.go`, `internal/remoteapi/handoff_test.go`
- Modify: `internal/remoteapi/api.go` (field, route, health flag, error codes), `internal/devicelogin` (action constant `ActionOpenOnMac = "open_on_mac"`), `api/remote-v1.openapi.yaml`
- Create: `api/testdata/clients/terminal/open-on-mac.json` (the copy table above plus `codes`: the error codes with their HTTP status)

**Interfaces:**
- Consumes: Task 1 `Drop`, Task 2 `Hold`, Task 3 `OpenTab`.
- Produces:
  ```go
  type Handoff struct {
      Hold    func(session string) (release func(), err error)
      Drop    func(session string) error
      State   func(ctx context.Context, session string) (state string, err error) // catalog state: "idle", "busy", "open_in_terminal"
      Target  func(ctx context.Context, session string) (title string, argv []string, dir string, err error) // ErrNotStarted when no transcript
      Open    func(ctx context.Context, title string, argv []string, dir string) error
      Wait    time.Duration // default 20s
      Poll    time.Duration // default 500ms
  }
  var ErrNotStarted, ErrHandoffTimeout error
  func (h *Handoff) Run(ctx context.Context, session string) error
  ```
  Order: State (open_in_terminal: `ErrOpenInTerminal`-mapped error) → Target → Hold → Drop → Open → poll State until `open_in_terminal` or Wait → release. `API.Handoff *Handoff`; nil: route answers 503 `no_terminal` and health `openOnMac:false`.
- Route `POST /v1/sessions/{id}/open-on-mac`, audited `ActionOpenOnMac`: 200 `{"state":"open_in_terminal"}`; 409 `busy`, 409 `open_in_terminal`, 409 `in_use`, 409 `not_started`, 503 `no_terminal`, 403 `not_authorized`, 504 `timeout`, 404 `unknown_session`.

- [ ] **Step 1: Failing tests** (fakes for every func):
  - `TestHandoff_OrderAndSuccess`: calls happen in order Hold, Drop, Open, and Run returns nil once State turns `open_in_terminal`; release called once.
  - `TestHandoff_AlreadyInTerminal`: no Hold, no Open, open_in_terminal error.
  - `TestHandoff_BusyRefused`: Hold returns busy, no Drop or Open.
  - `TestHandoff_NotStarted`: Target `ErrNotStarted`, nothing else called.
  - `TestHandoff_TimeoutReleasesHold` (Wait 50ms): `ErrHandoffTimeout`, release called, Drop not undone.
  - `TestHandoff_NotAuthorizedReleasesHold`.
  - `TestHandoff_ConcurrentSecondIsBusy`: with the real `Broker.Hold`, two Runs at once: one succeeds, one busy, Open called once.
  - `TestHandoff_QueuedItemWaits`: with a real Broker and `QueueACP`, `Busy` is true during Open.
  - API tests: each code and status above; audit line `open_on_mac` with no text; health `openOnMac` true and false; contract test that the openapi file declares the route.
- [ ] **Step 2:** run, fails.
- [ ] **Step 3:** implement; map errors in `writeFailure` alongside the existing codes.
- [ ] **Step 4:** `go test ./internal/remoteapi/... ./internal/devicelogin/ -count=1 -race` passes.
- [ ] **Step 5:** commit `feat(remoteapi): open-on-mac hands a session to an iTerm tab`.

### Task 5: Wire it in remote serve

**Files:**
- Modify: `internal/remoteapp/services.go` and/or `server.go`
- Test: `internal/remoteapp/server_test.go`

**Interfaces:**
- Consumes: Task 4 `Handoff`, Task 3 `Opener`, `Manager.Drop`, `Broker.Hold`, the catalog's `State`/lookup, the default provider's `ResumeCommand(agent.Agent) *exec.Cmd` (argv from `cmd.Args` with `cmd.Path` as argv0, dir from `cmd.Dir` or the session's cwd).
- `Handoff` is set only when an iTerm2 terminal driver is configured and terminal input is enabled; `Target`'s title is `history.ShownTitle` cut to 60 runes; `ErrNotStarted` when the catalog has no transcript file for the session.

- [ ] **Step 1: Failing tests**: with the fake driver list empty, `/v1/health` reports `openOnMac:false` and the route answers `no_terminal`; with a fake iTerm2 driver and a fake Opener, the route on a seeded idle session calls Open with argv ending `--resume <id>` and dir = the session's cwd; a created session without a file answers `not_started`.
- [ ] **Step 2:** run, fails.
- [ ] **Step 3:** implement; startup output unchanged.
- [ ] **Step 4:** `go test ./internal/remoteapp/ ./cmd/aimux/cmd/ -count=1 -race`, `bash scripts/check-layer-discipline.sh` pass.
- [ ] **Step 5:** commit `feat(remoteapp): wire Open on Mac`.

### Task 6: Web client

**Files:**
- Create: `clients/web/webapp/openOnMac.mjs`, `clients/web/webapp/openOnMac.test.mjs`
- Modify: `clients/web/webapp/chatView.mjs` (header menu), `clients/web/webapp/rows.mjs` or the row menu, the API module

**Interfaces:**
- Produces: `canOpenOnMac(session, health) -> boolean` (health.openOnMac and session.state !== "open_in_terminal"); `openOnMacMessage(code) -> string` from the fixture; `openOnMac(api, id) -> Promise<{ok:true}|{ok:false, code, message}>`.

- [ ] **Step 1: Failing tests** reading `api/testdata/clients/terminal/open-on-mac.json`: every code maps to its fixture text, an unknown code falls back to the timeout text; `canOpenOnMac` false for open_in_terminal and for `openOnMac:false`; the action shows "Opening on your Mac…" while pending and the chat reloads its state on success.
- [ ] **Step 2:** `node --test clients/web/webapp/openOnMac.test.mjs` fails.
- [ ] **Step 3:** implement.
- [ ] **Step 4:** `node --test "clients/web/webapp/*.test.mjs"` passes.
- [ ] **Step 5:** commit `feat(web): Open on Mac`.

### Task 7: iPhone client

**Files:**
- Create: `clients/ios/AimuxKit/Sources/OpenOnMac.swift`, `clients/ios/AimuxKit/Tests/AimuxKitTests/OpenOnMacTests.swift`
- Modify: `clients/ios/AimuxKit/Sources/APIClient.swift`, `Models.swift` (health flag), `clients/ios/App/ChatView.swift` (menu), `clients/ios/App/NowView.swift` and `AllView.swift` (row context menu); bump `CURRENT_PROJECT_VERSION` in `clients/ios/project.yml`.

**Interfaces:**
- Produces: `OpenOnMacCopy` loaded from the shared fixture (same keys), `func canOpenOnMac(_ s: SessionSummary, health: Health) -> Bool`, `APIClient.openOnMac(id:) async throws`, mapping server codes to `OpenOnMacCopy` texts.

- [ ] **Step 1: Failing tests**: fixture parity (every key and code present, texts equal the fixture), `canOpenOnMac` cases as in Task 6, the API call posts to `/v1/sessions/<id>/open-on-mac` and maps a 409 `busy` body to the busy text.
- [ ] **Step 2:** `swift test` in `clients/ios/AimuxKit` fails.
- [ ] **Step 3:** implement; menu item "Open on Mac" with a progress state; on success the chat refreshes its detail (it becomes a terminal chat).
- [ ] **Step 4:** `swift test` passes; `xcodegen` and an `xcodebuild` simulator build on `F923E38C-7EF0-42F3-9021-E8ECFD754F50` succeed; one screenshot of the chat menu with the action, saved under `.superpowers/reports/`.
- [ ] **Step 5:** commit `feat(ios): Open on Mac`.

### Task 8: Docs

**Files:**
- Modify: the remote access page in `docs-site` (grep `terminal_input`), `clients/ios/docs/project-context.md` (feature and any P2s), `RELEASE_NOTES.md` Unreleased, `.claude/CLAUDE.md` package map (Opener, Handoff, Drop, Hold).

- [ ] **Step 1:** document the action, the aimux window, the refusals with their texts, the Automation permission, and that background stays the default.
- [ ] **Step 2:** docs-site build passes.
- [ ] **Step 3:** commit `docs: Open on Mac`.

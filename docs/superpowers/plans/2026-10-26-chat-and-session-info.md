# aimux clients: chat, session info, title and spacing fixes. Implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Tapping a session in either client opens a chat you can talk to (messages, thoughts, folded tool steps, plan, approvals, stop, Next bar) and a session info sheet with cost, tokens, size and time; titles and spacing on the lists are fixed.

**Architecture:** The server adds an optional `stats` object to `GET /v1/sessions/{id}`, filled from aimux's session index. The chat timeline (history turns plus live events merged into display items) is decided once, in shared JSON fixtures under `api/testdata/clients/chat/`, which a pure Swift function and a pure JS module both pass; the views only render the items. The web client's working chat keeps its transport (EventSource, send, cancel) but renders from the shared timeline module and takes the design's visual system.

**Tech Stack:** Go 1.25; Swift 6, SwiftUI (iOS 27), XcodeGen, Swift Testing, XCUITest; plain ES modules tested with `node --test`; Playwright 1.58 for the web behaviour check.

**Spec:** `docs/superpowers/specs/2026-10-26-aimux-ios-design.md` (Screen: Chat, Visual system, Build order step 2). Owner decisions this round: metrics shown are cost and tokens plus size and time; labels (star, annotation, ROI) stay read-only and are not shown yet; a read-only preview of the last turns is shown for terminal sessions; titles that are only an id are shortened; rows get a little more air than 0.2.

## Global Constraints

- Branch `feat/clients` in the worktree `aimux-worktrees/clients`. Commit only with `&&` after tests pass; never push; never `git stash`. Commit trailer: `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Contract changes are additive and optional: an older client must keep working against the new server, and the new clients against a server without `stats` (show "Not available").
- Visual system as in the spec: near-monochrome; status dots only (green `#34c759`/`#30d158`, amber `#c56f00`/`#ffb340`, red `#ff3b30`/`#ff453a`, grey); user bubbles near-black (`#1c1c1e` on light, `#2c2c2e` on dark) with white text; replies plain text, no bubble; approval card neutral with an amber dot, first allow option filled black/white, reject options red text; send and stop black/white.
- Titles may be Arabic: bidi-isolate titles and message text, leading-aligned.
- Unknown event kinds, update kinds, tool kinds and option kinds are kept or skipped, never crash.
- Fixtures use invented titles, prompts, paths and commands only; never real session content, customer or partner names, or tailnet hostnames.
- iOS: deployment target 27.0, `MARKETING_VERSION` 0.3, raise `CURRENT_PROJECT_VERSION` per install; logic in `AimuxKit`, no SwiftUI there. Web: no framework, no build step, no npm dependencies, CSP `script-src 'self'`.
- Never start or seed real sessions on the owner's running aimux; behaviour checks use the Go test server or Playwright route mocks.
- Docs: `.claude/CLAUDE.md` requires API changes in `docs-site/src/content/docs/` and a handler test plus contract coverage for the changed endpoint.

## Review Focus

- **History and live events describe the same turn** (the session file already holds the running turn's first part): the turn appears once. Pinned by `chat/running-turn-in-history.json`.
- **The server restarted** (the stream's `log` changes, or a stored cursor belongs to an old log): the chat reloads its state and shows no duplicate or missing turns, no error banner once reconnected. Pinned by the reconnect tests in Tasks 4 and 6.
- **Approval answered elsewhere** (Mac or the other client) while the chat is open: the card collapses to its one-line outcome on `approval_resolved`; tapping a stale button returns 404 `no_such_approval` and the card collapses, no error. Pinned by `chat/approval-resolved.json` and the ChatModel test.
- **Session open in a terminal**: no composer, the read-only line, the last turns shown, no stream errors; a 409 on send is impossible because send is hidden. Pinned by the Task 5 UI test and the Task 6 Playwright check.
- **Very long output** (a reply of 8 KiB, a step target of 200 chars, 30 history turns): the timeline stays responsive, long targets truncate on one line, replies wrap. Pinned by `chat/long-content.json` and the screenshots.

## Task 1: Display titles and list spacing (both clients)

**Files:**
- Modify: `clients/ios/AimuxKit/Sources/SessionRowFormat.swift`, `clients/ios/App/NowView.swift`, `clients/ios/App/AllView.swift`, `clients/ios/App/SearchView.swift`, `clients/ios/App/Components/SessionRows.swift`
- Modify: `clients/web/webapp/rows.mjs`, `clients/web/webapp/app.css`
- Test: `clients/ios/AimuxKitTests/SessionPresentationTests.swift`, `clients/web/webapp/rows.test.mjs` (new)

**Interfaces:**
- Produces: Swift `SessionRowFormat.displayTitle(_ title: String, id: String) -> String`; JS `displayTitle(title, id)` exported from `rows.mjs`.

Rules: a title that is empty, equal to the id, or matches `^Session [0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$` (case-insensitive), or is a bare UUID, becomes `Session ` + the first 8 characters of the id; anything else is returned trimmed. Row titles on Now, All and Search show at most 2 lines. Spacing: list rows get 10 pt top and bottom padding (web 10px), minimum row height 44 (was 36), space between sections 20 (was 12); the Now header keeps its layout.

- [ ] **Step 1: Failing tests** in both suites with the same cases: `"Session d12ce59e-17e0-4534-8027-eac22f02c34f"` with that id gives `"Session d12ce59e"`; `""` gives `"Session d12ce59e"`; the bare UUID gives `"Session d12ce59e"`; `"  Fix login page  "` gives `"Fix login page"`; `"Session notes for Monday"` is unchanged. Run `node --test "clients/web/webapp/*.test.mjs"` and the iOS `-only-testing:AimuxKitTests` run; expect failures.
- [ ] **Step 2: Implement** both functions and use them wherever a session title is rendered (all rows, search results, and later the chat header).
- [ ] **Step 3: Apply** the line limit and spacing values in both clients.
- [ ] **Step 4: Run** both suites; take one screenshot of Now on the iPhone 14 Pro simulator (`-demo list -tab now`, light) and one 390x844 web screenshot; read both.
- [ ] **Step 5: Commit** `fix(clients): short titles for id-only sessions; more space between rows`.

## Task 2: Session stats on GET /v1/sessions/{id} (server)

**Files:**
- Modify: `api/remote-v1.openapi.yaml` (new schema `SessionStats`; optional `stats` on `SessionDetail`), `internal/remoteapi/api.go` (`SessionStats` type, `Stats *SessionStats json:"stats,omitempty"` on `SessionDetail`), `internal/remoteapi/local.go` (fill it), `docs-site/src/content/docs/guides/remote-access.mdx` (API section)
- Test: `internal/remoteapi/local_test.go`, `internal/remoteapi/api_test.go`, `internal/remoteapi/contract_test.go` (the contract run must cover a detail with stats)

**Interfaces:**
- Produces (JSON): `stats: { costUsd: number, tokensIn: integer, tokensOut: integer, cacheReadTokens: integer, cacheWriteTokens: integer, turns: integer, startTime: date-time, durationMin: number, model?: string, gitBranch?: string }`, all from `history.Session` (`CostUSD`, `TokensIn`, `TokensOut`, `CacheReadTokens`, `CacheWriteTokens`, `TurnCount`, `StartTime`, `DurationMin`, `Model`, `GitBranch`). Omitted when the session is not in the index yet (created here, file not written).

- [ ] **Step 1: Failing tests:** `TestLocalIndexSessionCarriesStats` (a fake `Discover` returns a session with cost 1.25, tokens 1000/200/5000/300, 12 turns, model `claude-opus-x`, branch `main`; `Session()` returns those values); `TestLocalIndexSessionWithoutIndexHasNoStats` (created-not-written session: `Stats == nil`); the contract test validates a response with stats against the schema.
- [ ] **Step 2: Implement:** have the summary lookup return the matching `history.Session` too, so `Session()` builds `Stats` from the same scan (one `Discover` call, not two).
- [ ] **Step 3: Run** `go build ./... && go vet ./internal/remoteapi/ && go test ./... -timeout 60s`.
- [ ] **Step 4: Docs:** document `stats` in the remote access guide; `cd docs-site && npm run build`.
- [ ] **Step 5: Commit** `feat(remote): session stats on GET /v1/sessions/{id}`.

## Task 3: Shared chat timeline fixtures and the JS timeline module

**Files:**
- Create: `api/testdata/clients/chat/*.json`, a "Chat" part in `api/testdata/clients/README.md`
- Create: `clients/web/webapp/timeline.mjs`, `clients/web/webapp/timeline.test.mjs`

**Interfaces:**
- Produces: `buildTimeline({ turns, events, now }) -> { items, working, pendingApproval }` where `turns` are `SessionDetail.turns`, `events` an `EventPage.events` list, `working` is true while a turn has started and not ended, `pendingApproval` the id of an unresolved approval or null. Items, in display order:
  - `{kind: "user", text}`
  - `{kind: "thought", text, seconds}`: consecutive `agent_thought_chunk`s in a turn joined; `seconds` from its first chunk to the next event of another kind (null for history)
  - `{kind: "steps", count, seconds, filesChanged, steps: [{kind, target, status}]}`: consecutive tool calls between replies; step `kind` from ACP `kind` (`read`, `edit`, `search`, `execute`→`run`, everything else `other`) or, for history `TurnTool.name`, `Read`→read, `Edit`/`Write`/`MultiEdit`/`NotebookEdit`→edit, `Grep`/`Glob`/`WebSearch`→search, `Bash`→run, else other; `target` the tool call `title` (later titled updates replace it; untitled updates keep it) or the history `snippet`; `status` `running`|`ok`|`failed` from ACP `status` (`pending`/`in_progress` running, `completed` ok, `failed` failed) or history `ok`; `filesChanged` the distinct targets of edit steps; `seconds` from the group's first to last event (null for history)
  - `{kind: "plan", done, total, entries: [{text, status}]}`: one per turn at the position of its first `plan` update, replaced by later ones
  - `{kind: "reply", text}`: consecutive `agent_message_chunk` texts joined
  - `{kind: "approval", approvalId, title, command, options, outcome}`: `outcome` null until `approval_resolved` gives `allowed`/`denied`/`expired`
  - `{kind: "error", text}` from `turn_error`; `{kind: "stopped"}` from `turn_done` with `stopReason` `cancelled`
- Merge rules (from the web client's current `load()` in `app.js`, which is the reference): history turns first, oldest first, each as user, steps (if any), reply; the running turn (latest turn without `turn_done`/`turn_error`) is rendered from its events, and the latest history turn is dropped when its `user` equals the running turn's prompt and its time is no earlier than 2 s before the turn started; a `turn_error` is placed after the history turn with its prompt recorded within its window, else at the end with its prompt; events of finished turns already covered by history are not repeated.

Fixture format: `{ "name", "now", "turns": [...], "events": [...], "expect": { "items": [...], "working": bool, "pendingApproval": string|null } }`.

- [ ] **Step 1: Fixtures**, one file each: `history-only.json`, `running-with-steps.json` (thought, 3 tool calls with a titled update and an untitled one, reply), `running-turn-in-history.json`, `approval-pending.json`, `approval-resolved.json`, `turn-error-after-history.json`, `turn-error-never-written.json`, `cancelled.json`, `plan-updated.json` (two plan updates, one item), `start-aged-out.json` (running turn whose `turn_started` is gone), `unknown-update-kind.json`, `long-content.json` (8 KiB reply, 200-char target), `arabic.json`.
- [ ] **Step 2: Failing test:** `timeline.test.mjs` reads every file in `api/testdata/clients/chat/` and `deepEqual`s `buildTimeline` output with `expect`; plus a guard that at least 13 fixtures ran. Run, expect `Cannot find module`.
- [ ] **Step 3: Implement** `timeline.mjs` (use `apiDate` from `apiDate.mjs` for every time).
- [ ] **Step 4: Run** `node --test "clients/web/webapp/*.test.mjs"`; all pass.
- [ ] **Step 5: Commit** `feat(clients): shared chat timeline fixtures and the JS timeline`.

## Task 4: AimuxKit chat: timeline, event stream, ChatModel

**Files:**
- Create: `clients/ios/AimuxKit/Sources/Timeline.swift`, `EventStream.swift`, `ChatModel.swift`, `SessionDetail.swift` (decoding `SessionDetail`, `Turn`, `TurnTool`, `SessionStats`)
- Modify: `clients/ios/AimuxKit/Sources/APIClient.swift` (`session(id:turns:)`, `cancel(sessionID:)`, `eventPage(sessionID:after:log:)`)
- Test: `clients/ios/AimuxKitTests/TimelineTests.swift`, `EventStreamTests.swift`, `ChatModelTests.swift`

**Interfaces:**
- Consumes: `SessionEvent`, `EventPage`, `Approval` (Events.swift); `APIClient.answer`, `sendMessage`; `rankNow`.
- Produces:
  - `func buildTimeline(turns: [Turn], events: [SessionEvent], now: Date) -> TimelineResult` with `TimelineResult { items: [TimelineItem]; working: Bool; pendingApproval: String? }` and `enum TimelineItem: Equatable { user(String), thought(text: String, seconds: Double?), steps(StepGroup), plan(PlanItem), reply(String), approval(ApprovalItem), error(String), stopped }`, mirroring Task 3 exactly
  - `struct EventStream`: `func events(sessionID: String, after: Int, log: String) -> AsyncThrowingStream<StreamEvent, Error>` over `URLSession.bytes`, parsing SSE frames (`id: <log>.<seq>`, `event:`, `data:`, comment keep-alives), where `StreamEvent` carries the `SessionEvent` and its `log`
  - `@MainActor @Observable final class ChatModel { result: TimelineResult; session: SessionDetail?; state: ChatState (.loading, .live, .reconnecting, .readOnly, .failed(String)); func load() async; func send(_ text: String) async; func stop() async; func answer(_ approval: Approval, option: PermissionOption) async; func close() }`

Behaviour: `load()` follows the web client's sequence (events snapshot `wait=0`, detail with `turns=30`, a second snapshot after the last seq; if the log changed or a turn started, read both again, at most 3 attempts), builds the timeline, then follows the stream from the last seq with the page's `log`; on stream end or error it waits 2 s (doubling to 30 s) and calls `load()` again, which also covers a server restart. Terminal sessions (`openInTerminal`) load the detail only, no stream, state `.readOnly`. `send` shows the user item at once (pending) with a fresh idempotency key and keeps it on retry; `stop` posts cancel; `answer` mirrors NowModel (in-flight guard, 404 `noSuchApproval` collapses the card).

- [ ] **Step 1: Failing tests:** `TimelineTests` runs every `chat/` fixture (bundle folder `clients/chat`, same loader and folder guard as `NowRankingTests`); `EventStreamTests` feeds canned SSE bytes through a URLProtocol stub (two events, a comment line, an id with log prefix, a frame split across chunks) and asserts the decoded events and logs; `ChatModelTests` with stubs: `loadRetriesWhenATurnStartsMeanwhile`, `reconnectsAfterStreamEnds` (second load uses the new log), `terminalSessionIsReadOnly` (no stream opened), `sendShowsPendingThenKeepsKeyOnRetry`, `staleApprovalCollapses`.
- [ ] **Step 2: Run** the AimuxKit tests; expect compile failures.
- [ ] **Step 3: Implement** the four files and the APIClient methods.
- [ ] **Step 4: Run** all AimuxKit tests and `swiftlint lint --strict`; all pass.
- [ ] **Step 5: Commit** `feat(ios): chat timeline, event stream and chat model`.

## Task 5: iPhone chat screen and session info

**Files:**
- Create: `clients/ios/App/ChatView.swift`, `clients/ios/App/Components/{TimelineRows,Composer,NextBar,SessionInfoSheet}.swift`
- Modify: `clients/ios/App/NowView.swift`, `AllView.swift`, `SearchView.swift` (rows push `ChatView`), `clients/ios/App/Components/SessionRows.swift` (remove the placeholder view), `clients/ios/App/DebugHooks.swift` (`-demo chat`, `-demo chat-approval`, `-demo chat-readonly` from the chat fixtures' shapes), `clients/ios/UITests/NowAllUITests.swift`, `clients/ios/project.yml` (0.3)

**Interfaces:**
- Consumes: `ChatModel`, `TimelineItem`, `SessionStats`, `displayTitle`, `rankNow` (Next bar target).

Screen rules (spec Screen: Chat, `images/chat.png`): header with back, the display title (2 lines max), a status dot with "Working", "Your turn", "Failed" or "Open on your Mac · 14h ago" and the project; tapping the title or the info button opens `SessionInfoSheet`. Thought: one grey line "Thought for 8s ›", tap to read. Steps: one line "6 steps · 41s · 3 files changed", tap to open one line per step (SF Mono 13.5 for targets, spinner on running, red mark on failed). Plan: checklist "2 of 4". Reply: plain text, code in monospace. Approval card as on Now; resolved collapses to one line ("Allowed", "Denied", "Expired" plus the command). Error: red dot line with the text. Next bar above the composer: the first other session in `rankNow`'s Needs you, else Working ("Next · <title> · your turn ›"); hidden while this chat has a pending approval. Composer: field and send; send becomes stop while `working`. Read-only sessions: last turns plus the line "Read-only here while it's open in a terminal on your Mac", no composer. Info sheet: Cost (`$0.42`, `<$0.01` below a cent), Tokens in, out, cache read, cache write (`1.2M`, `34k`), Model, Turns, Duration (`1h 12m`), Started, Last active, Branch, Folder; "Not available" when `stats` is absent. Attachments and voice are not in this plan.

- [ ] **Step 1: UI test first:** `-demo chat`: tapping a Now row opens the chat, the steps line opens to show steps, the info button shows "Cost"; `-demo chat-approval`: tapping Allow collapses the card to "Allowed"; `-demo chat-readonly`: the read-only line is shown and no send button exists. Run, expect failure.
- [ ] **Step 2: Build the views** until the UI test passes.
- [ ] **Step 3: Screenshots**, light and dark, SE 3 and iPhone 14 Pro (iOS 27 simulator `BDA646FE-5717-4244-A935-C3917E94689F`), for the three demos and the info sheet, into the session scratchpad; compare with `images/chat.png`; check AX5 once.
- [ ] **Step 4: Full run** (logic and UI tests), `swiftlint lint --strict`, the App Store check: 0 FAIL.
- [ ] **Step 5: Commit** `feat(ios): chat screen and session info`. Do not install on the phone; the controller asks the owner.

## Task 6: Web chat in the design, rendered from the shared timeline

**Files:**
- Modify: `clients/web/webapp/app.js` (session view: keep transport, replace the DOM merge in `load()`/`handle()` with `buildTimeline` plus a renderer), `index.html`, `app.css`
- Create: `clients/web/webapp/chatView.mjs` (renders `buildTimeline` items, header, Next bar, read-only line), `clients/web/webapp/sessionInfo.mjs` (the info sheet, with a `.test.mjs` for its formatting)

**Interfaces:**
- Consumes: `timeline.mjs`, `rankNow` (Next bar), `displayTitle`, the stats object from Task 2.

Same screen rules as Task 5, in HTML and CSS with the light/dark tokens already in `app.css`. The existing reconnect, draft keeping and idempotent send stay as they are; the old red-accent dark-only session styling is removed.

- [ ] **Step 1: Behaviour check first** (scratchpad Playwright script, mocked `/v1` per the Global Constraints): open a busy session with a running turn (steps line, open it), answer an approval (collapses to "Allowed"), a stale approval 404 `no_such_approval` collapses without an error line, open the info sheet ("Cost"), a terminal session shows the read-only line and no composer, simulate a stream drop with a changed `log` and assert no duplicated turn. Run against the current UI; expect failure.
- [ ] **Step 2: Implement** until the check passes.
- [ ] **Step 3: Screenshots** 390x844, light and dark, for working, approval, read-only and info; compare with the iPhone screenshots from Task 5.
- [ ] **Step 4: Run** `node --test "clients/web/webapp/*.test.mjs"` and `go test ./... -timeout 60s`.
- [ ] **Step 5: Commit** `feat(web): chat in the shared design, with session info`.

## Task 7: Docs

**Files:**
- Modify: `clients/ios/README.md` (code map, backlog), `clients/ios/docs/project-context.md` (state, open items), `.claude/CLAUDE.md` (web client files), `docs-site/src/content/docs/guides/remote-access.mdx` (chat and session info in both clients), `RELEASE_NOTES.md` (Unreleased)

- [ ] **Step 1: Update** each file to describe what exists after Tasks 1-6.
- [ ] **Step 2: Verify** every file named in a code map exists (`ls`), and `cd docs-site && npm run build` succeeds.
- [ ] **Step 3: Commit** `docs: chat and session info in both clients`.

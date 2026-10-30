# aimux clients: move into aimux, then Now and All in both clients. Implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the iPhone app and the phone web client into `aimux/clients/`, then replace both clients' session list with the designed Now and All screens, driven by today's `/v1` API and one set of shared ranking fixtures.

**Architecture:** Both clients rank sessions on the device with the same rules. The rules are pinned by JSON fixtures in `aimux/api/testdata/clients/` (sessions and event snapshots in, sections out) that the Swift tests and the web client's `node --test` tests both run. The server is unchanged in this plan; activity comes from `GET /v1/sessions` plus one `GET /v1/sessions/{id}/events?wait=0` snapshot per candidate session.

**Tech Stack:** Swift 6, SwiftUI (iOS 27), XcodeGen, Swift Testing, XCUITest; Go 1.25; plain ES modules for the web client, tested with Node 22's built-in `node --test`; GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-10-26-aimux-ios-design.md` (moves to `aimux/docs/superpowers/specs/` in Task 1). Build steps 0 and 1 only; chat, new session, attachments, voice, push and server work get their own plans.

## Global Constraints

- All work happens in a git worktree of aimux from `origin/main` (`git worktree add ../aimux-worktrees/clients -b feat/clients origin/main`). The main aimux checkout is on `feat/remote-session-access` with the owner's uncommitted `README.md` and `docs-site/astro.config.mjs` edits: never touch it.
- Commit only with `&&` after the tests pass, never `;`. Push only when the owner says so, with `git ship -m "<msg>" <paths>`. Expect the pre-push Codex review to run; fix P0-P2 findings.
- iOS: deployment target iOS 27.0, team `KDDRCCVFU4`, bundle IDs `me.adelzaalouk.*` as today, `MARKETING_VERSION` 0.2, `CURRENT_PROJECT_VERSION` raised per install. Logic in `AimuxKit` (no SwiftUI); views verified by screenshots.
- Web client: no framework, no build step, no npm dependencies; ES modules served under the existing Content-Security-Policy (`script-src 'self'`).
- Ranking constants (from the spec): active window 15 minutes (900 s); Working lists sessions directly when 5 or fewer, groups by project above 5; at most 5 project groups, the rest folded into "N more projects"; On your Mac shows at most 8 rows, and none (header and link only) when needs-you + finished + working is more than 5.
- Visual system (from the spec): near-monochrome; status dots only: green `#34c759`/`#30d158` working, amber `#c56f00`/`#ffb340` needs you, red `#ff3b30`/`#ff453a` failed, grey idle; "N need you" and "Needs you" are the only amber text; light/dark follow the system.
- Titles may be Arabic: bidi-isolate titles, leading-aligned; isolate project names and times.
- Unknown enum values (states, event kinds, option kinds) are kept and never crash; unknown event kinds are skipped.

## Review Focus

- **Approval already answered in the same snapshot** (an `approval` followed by its `approval_resolved`): the session must not appear in Needs you. Pinned in Task 4 fixture `approval-resolved.json`.
- **Events endpoint refuses a session** (404 `unknown_session`, 409, or no event log because the server restarted): treat as no activity, keep the session in its state-based section, never show an error banner. Pinned in Task 6 (Swift) and Task 9 (web) tests.
- **`lastActive` in the future** (Mac clock ahead of the phone): treat as now, never negative ages, still inside the active window. Pinned in Task 4 fixture `clock-skew.json`.
- **Two projects with the same folder name** (`/a/app`, `/b/app`): grouped separately by full path, labelled with the folder name. Pinned in Task 4 fixture `same-folder-name.json`.
- **Twenty or more working sessions**: Now stays within one screen (5 groups + "more"), and On your Mac collapses to its header. Pinned in Task 4 fixture `twenty-working.json` and the Task 7 screenshot.


## Step 0: move

### Task 1: Move the iPhone app to `aimux/clients/ios`

**Files:**
- Create (copy from aimux-ios): `clients/ios/{project.yml,.swiftlint.yml,App/,AimuxKit/,AimuxKitTests/,UITests/,README.md,AGENTS.md,docs/brief.md}`
- Create: `docs/superpowers/specs/2026-10-26-aimux-ios-design.md` and `docs/superpowers/specs/images/` (from aimux-ios), `docs/superpowers/plans/2026-10-26-clients-move-and-now.md` (this file)
- Create: `.github/workflows/clients-ios.yml`
- Modify: `.gitignore`, `.pre-commit-config.yaml`, `CLAUDE.md` (code map)

**Interfaces:**
- Produces: `clients/ios` builds and tests from `cd clients/ios && xcodegen generate`; later tasks add files there.

- [ ] **Step 1: Copy the app.** Copy the listed paths from `/Users/azaalouk/go/src/github.com/zanetworker/aimux-ios`. Do not copy `.github/`, `LICENSE`, `.pre-commit-config.yaml`, `.repo-kit.yaml`, `CLAUDE.md`, `.claude/`, `build/`, `.superpowers/` (aimux already has its own). In `clients/ios/AGENTS.md`, replace the line about the contract with: the contract is `../../api/remote-v1.openapi.yaml` in this repo.
- [ ] **Step 2: Ignore build output.** Add to `.gitignore`: `clients/ios/build/`, `clients/ios/*.xcodeproj/`, `clients/ios/build-appstore*`.
- [ ] **Step 3: Lint hook.** Add a local `swiftlint` hook to `.pre-commit-config.yaml` with `files: ^clients/ios/` and `entry: swiftlint lint --quiet --config clients/ios/.swiftlint.yml`.
- [ ] **Step 4: Verify it builds and passes where it is.**
  Run: `cd clients/ios && xcodegen generate && SIM=$(xcrun simctl list devices available | awk -F'[()]' '/Aimux iPhone SE 3/{print $2; exit}') && timeout 1200 xcodebuild test -project Aimux.xcodeproj -scheme Aimux -destination "id=$SIM" -derivedDataPath build/sim -collect-test-diagnostics never > build/test.log 2>&1; grep -E "Test run with|Executed [0-9]+ test" build/test.log`
  Expected: `Test run with 57 tests ... passed` and `Executed 2 tests, with 0 failures`.
- [ ] **Step 5: CI.** `clients-ios.yml`: `on: push/pull_request` to `main` with `paths: ['clients/ios/**', 'api/**', '.github/workflows/clients-ios.yml']`; one job on `macos-26` that installs XcodeGen and SwiftLint with Homebrew, runs `swiftlint lint --strict` in `clients/ios`, `xcodegen generate`, then `xcodebuild test -scheme Aimux -only-testing:AimuxKitTests -destination "platform=iOS Simulator,name=iPhone 17" CODE_SIGNING_ALLOWED=NO`. (UI tests stay local: simulators on hosted runners are too slow for them.)
- [ ] **Step 6: Code map.** Add `clients/ios/` (iPhone app, see its README) to the code map in `CLAUDE.md`.
- [ ] **Step 7: Commit.** `git add clients/ios .github/workflows/clients-ios.yml .gitignore .pre-commit-config.yaml CLAUDE.md docs/superpowers && git commit -m "feat(clients): move the iPhone app into clients/ios"`

### Task 2: Move the phone web client to `aimux/clients/web`

**Files:**
- Move: `internal/remoteclient/` to `clients/web/` (`git mv`), package renamed `remoteclient` to `webclient`
- Modify: `cmd/aimux/cmd/remote.go:28` (import), `CLAUDE.md` code map, any doc naming `internal/remoteclient` (`git grep -n internal/remoteclient`)

**Interfaces:**
- Produces: `webclient.Handler() http.Handler` (unchanged behaviour), assets under `clients/web/webapp/`.

- [ ] **Step 1: Move and rename.** `git mv internal/remoteclient clients/web`; change `package remoteclient` to `package webclient` in `clients/web/*.go` and the import in `cmd/aimux/cmd/remote.go` to `github.com/zanetworker/aimux/clients/web` with the call `webclient.Handler()`.
- [ ] **Step 2: Run the tests.**
  Run: `go build ./... && go vet ./... && go test ./... -timeout 60s`
  Expected: all pass, including `TestHandler_ServesTheAppUnderAStrictPolicy` and `TestHandler_OnlyItsOwnFiles`.
- [ ] **Step 3: Fix references.** `git grep -n "internal/remoteclient"` returns nothing outside `docs/superpowers/` and past release notes.
- [ ] **Step 4: Commit.** `git add -A clients/web internal cmd CLAUDE.md docs-site && git commit -m "refactor(clients): move the phone web client to clients/web"`


## Step 1: Now and All

### Task 3: Events, approvals and activity models in AimuxKit

**Files:**
- Create: `clients/ios/AimuxKit/Sources/Events.swift`
- Modify: `clients/ios/AimuxKit/Sources/APIClient.swift`
- Test: `clients/ios/AimuxKitTests/EventsTests.swift`, fixture `clients/ios/AimuxKitTests/Fixtures/events-approval.json` (shape copied from `api/remote-v1.openapi.yaml`)

**Interfaces:**
- Produces:
  - `struct SessionEvent: Decodable, Equatable, Sendable { seq: Int; turn: String; kind: EventKind; time: Date; data: EventData }`
  - `enum EventKind { turnStarted, update, approval, approvalResolved, turnDone, turnError, unknown(String) }`
  - `enum EventData { turnStarted(text: String), update(sessionUpdate: String, title: String?), approval(Approval), approvalResolved(approvalId: String, outcome: String), turnDone(stopReason: String), turnError(error: String), other }`
  - `struct Approval: Equatable, Sendable { approvalId: String; title: String; options: [PermissionOption]; command: String? }` where `command` is `toolCall.rawInput.command` when it is a string
  - `struct PermissionOption: Equatable, Sendable { optionId: String; name: String; kind: String }`
  - `struct EventPage: Decodable { events: [SessionEvent]; log: String }` (events decoded with `LossyList`)
  - `APIClient.eventSnapshot(sessionID: String) async throws -> EventPage` (`GET /v1/sessions/{id}/events?wait=0`)
  - `APIClient.answer(approvalID: String, optionID: String) async throws` (`POST /v1/approvals/{id}` with body `{"option": "<optionId>"}`, per `AnswerRequest` in the contract)

- [ ] **Step 1: Failing tests.** `decodesEveryEventKind` (one event per kind decodes to the matching `EventData`), `approvalCarriesOptionsAndCommand` (`options.map(\.kind) == ["allow_once", "allow_always", "reject_once"]`, `command == "go test ./internal/remoteapi/... -run Push"`), `unknownKindIsSkippedNotFatal` (a page with kind `"telemetry"` decodes, that event is `.unknown("telemetry")`), `snapshotRequestsWaitZero` (URLProtocol stub sees path `/v1/sessions/s1/events` and query `wait=0`), `answerPostsOption` (method POST, path `/v1/approvals/a1`, JSON body equals `{"option": "o1"}`).
- [ ] **Step 2: Run, expect compile failures** (`cannot find 'SessionEvent'`).
- [ ] **Step 3: Implement** in `Events.swift` and `APIClient.swift`; `EventData` decodes from `kind` plus `data`.
- [ ] **Step 4: Run, expect all AimuxKit tests pass.**
- [ ] **Step 5: Commit** `feat(ios): decode session events and answer approvals`.

### Task 4: Shared ranking fixtures and the Swift ranking

**Files:**
- Create: `api/testdata/clients/now/*.json`, `api/testdata/clients/README.md` (format, one paragraph)
- Create: `clients/ios/AimuxKit/Sources/Activity.swift`, `clients/ios/AimuxKit/Sources/NowRanking.swift`
- Modify: `clients/ios/project.yml` (add `../../api/testdata/clients` to `AimuxKitTests` as a folder resource)
- Test: `clients/ios/AimuxKitTests/NowRankingTests.swift`

**Interfaces:**
- Consumes: `Session`, `SessionEvent`, `Approval` (Task 3).
- Produces:
  - `enum Activity: Equatable, Sendable { none, working(step: String?), needsApproval(Approval), failed(error: String, at: Date), finished(at: Date) }`
  - `func deriveActivity(_ events: [SessionEvent]) -> Activity`: a pending approval (an `approval` with no later `approval_resolved` for its id) wins; otherwise the last turn event decides (`turn_error` failed, `turn_done` finished, `turn_started` with no end working, with `step` the title of the latest `tool_call`/`tool_call_update` in that turn, or `"Thinking"` after an `agent_thought_chunk`); no turn events: `.none`.
  - `struct NowView: Equatable { needsYou: [NowItem]; finished: [NowItem]; working: WorkingSection; onMac: [Session]; onMacTotal: Int; summary: NowSummary }`
  - `struct NowItem: Equatable { session: Session; activity: Activity }`
  - `enum WorkingSection: Equatable { list([NowItem]), groups([ProjectGroup], more: MoreProjects?) }`
  - `struct ProjectGroup: Equatable { project: String; name: String; count: Int; newest: NowItem }`, `struct MoreProjects: Equatable { projects: Int; sessions: Int }`
  - `struct NowSummary: Equatable { needYou: Int; working: Int; onMac: Int }`
  - `func rankNow(sessions: [Session], activity: [String: Activity], seen: [String: Date], now: Date) -> NowView`

Rules for `rankNow`, in order (each session lands in at most one section):
1. Needs you: `.needsApproval` (any age), then `.failed` with `at` within 900 s; newest first by event time.
2. Finished: `.finished(at)` within 900 s and `at > seen[id]` (or never seen); newest first.
3. Working: `state == .busy` or `.working`, or `state == .openInTerminal` with `lastActive` within 900 s (no step: shown as "Active Nm"); newest `lastActive` first. More than 5: group by `project` path, groups sorted by count then newest, first 5 kept, the rest summed into `MoreProjects`.
4. On your Mac: remaining `openInTerminal` sessions, newest first; `onMacTotal` is their count; `onMac` holds the first 8, or none when `needsYou + finished + working` counts exceed 5.
5. Everything else (idle, unavailable) is not on Now.
A `lastActive` after `now` counts as `now`.

Fixture format (`api/testdata/clients/now/<case>.json`):

```json
{
  "name": "one approval, two working, idle terminals",
  "now": "2026-10-26T09:00:00Z",
  "seen": {},
  "sessions": [ { "id": "a", "provider": "claude", "title": "Push notifications", "project": "/src/aimux", "lastActive": "2026-10-26T08:59:00Z", "state": "busy" } ],
  "events": { "a": [ { "seq": 1, "turn": "t1", "kind": "turn_started", "time": "2026-10-26T08:58:00Z", "data": { "text": "wire APNs" } } ] },
  "expect": {
    "needsYou": ["a"], "finished": [],
    "working": { "list": [] },
    "onMac": ["m1", "m2"], "onMacTotal": 2,
    "summary": { "needYou": 1, "working": 0, "onMac": 2 }
  }
}
```
(`working` is either `{"list": [ids]}` or `{"groups": [{"project": "/src/research", "count": 9, "newest": "r1"}], "more": {"projects": 2, "sessions": 2}}`.)

- [ ] **Step 1: Write the fixtures.** One file per case: `approval-pending.json`, `approval-resolved.json` (resolved in the same page: not in Needs you), `failed-recent.json` and `failed-old.json` (16 minutes ago: not on Now), `finished-unseen.json` and `finished-seen.json`, `terminal-active-and-idle.json` (a terminal session active 4 min ago is Working with no step; one idle 11 h is On your Mac), `five-working.json` (list), `six-working.json` (groups), `twenty-working.json` (9/4/3/2/1/1 across 6 projects: 5 groups + more `{projects: 1, sessions: 1}`; On your Mac empty, `onMacTotal` 14), `on-mac-cap.json` (12 idle terminals, 0 active: 8 shown, total 12), `same-folder-name.json`, `clock-skew.json`, `unknown-state.json` (`state: "hibernating"` is on no section and does not crash).
- [ ] **Step 2: Failing test.** `@Test(arguments: fixtureNames) func matchesSharedFixture(name:)` loads the fixture from the bundle's `clients/now` folder, decodes sessions with `APIDecoder`, derives activity per session with `deriveActivity`, calls `rankNow`, and compares ids per section with `expect`. Plus `deriveActivity` unit tests: `pendingApprovalWins`, `lastTurnErrorIsFailed`, `toolCallTitleIsStep`, `thoughtIsThinking`.
- [ ] **Step 3: Run, expect failures** (types missing).
- [ ] **Step 4: Implement** `Activity.swift` and `NowRanking.swift`.
- [ ] **Step 5: Run, expect every fixture case and unit test to pass.**
- [ ] **Step 6: Commit** `feat(clients): shared Now ranking fixtures and the Swift ranking`.

### Task 5: All sections (shared fixtures, Swift)

**Files:**
- Create: `api/testdata/clients/all/*.json`, `clients/ios/AimuxKit/Sources/AllSections.swift`
- Delete: `clients/ios/AimuxKit/Sources/SessionBrowse.swift`'s `SessionGrouping` (replaced); keep `SessionFilter`, `ProjectSummary`, `Snippet`
- Test: `clients/ios/AimuxKitTests/AllSectionsTests.swift`

**Interfaces:**
- Produces:
  - `enum AllSort: String, CaseIterable { newest, mostActive, project }`
  - `struct DaySection: Equatable { day: String; sessions: [Session] }` where `day` is `"today"`, `"yesterday"` or `YYYY-MM-DD` in the given time zone
  - `func allSections(_ sessions: [Session], project: String?, sort: AllSort, now: Date, timeZone: TimeZone) -> [DaySection]`
  - `mostActive` orders busy, then open in terminal, then the rest, each newest first, inside each day. `project` sort returns one section per project (`day` holds the project path), projects by newest session.

- [ ] **Step 1: Fixtures** `newest-by-day.json`, `project-filter.json`, `sort-project.json`, `sort-most-active.json`, `midnight-in-berlin.json` (fixture carries `"timeZone": "Europe/Berlin"`; a session at 23:30 UTC on the 25th is "today" on the 26th in Berlin).
- [ ] **Step 2: Failing fixture test** like Task 4's, comparing `[day: [ids]]`.
- [ ] **Step 3-4: Implement, run, pass.**
- [ ] **Step 5: Commit** `feat(clients): All sections with shared fixtures`.

### Task 6: NowModel (polling and snapshots)

**Files:**
- Create: `clients/ios/AimuxKit/Sources/NowModel.swift`
- Modify: `clients/ios/AimuxKit/Sources/SessionListModel.swift` (keeps loading and caching the full list for All and search; `NowModel` reads its `sessions`)
- Test: `clients/ios/AimuxKitTests/NowModelTests.swift`

**Interfaces:**
- Consumes: `SessionListModel`, `APIClient.eventSnapshot`, `APIClient.answer`, `deriveActivity`, `rankNow`.
- Produces: `@MainActor @Observable final class NowModel { view: NowView; func refresh() async; func answer(_ approval: Approval, option: PermissionOption, session: String) async; func markSeen(_ id: String) }` and a protocol `ActivitySource: Sendable { func eventSnapshot(sessionID: String) async throws -> EventPage; func answer(approvalID: String, optionID: String) async throws }` that `APIClient` conforms to.

- [ ] **Step 1: Failing tests** with a stub `ActivitySource`:
  - `snapshotsOnlyCandidates`: snapshots are requested for `busy` sessions and for non-terminal sessions active within 900 s, at most 10 per refresh, never for `openInTerminal`.
  - `refusedSnapshotIsNoActivity`: a snapshot throwing `.server(status: 404, code: .unknownSession, ...)` or `.server(status: 409, ...)` leaves that session with `.none`, `view` still built, no error surfaced (Review Focus).
  - `answeringRemovesTheCard`: after `answer` succeeds, the next `view.needsYou` no longer contains that session even before the next snapshot.
  - `seenClearsFinished`: `markSeen` stores the time in `UserDefaults` (injected), and the finished item disappears.
- [ ] **Step 2-4: Run (fail), implement, run (pass).** Polling every 15 s while visible lives in the view (`.task` loop with `Task.sleep(for: .seconds(15))`), not in the model.
- [ ] **Step 5: Commit** `feat(ios): Now model with event snapshots and approvals`.

### Task 7: iPhone Now and All screens with the tab bar

**Files:**
- Create: `clients/ios/App/RootTabs.swift`, `clients/ios/App/NowView.swift`, `clients/ios/App/AllView.swift`, `clients/ios/App/SearchView.swift`, `clients/ios/App/Components/{StatusDot,ApprovalCard,SessionRows}.swift`
- Delete: `clients/ios/App/SessionListView.swift` (its `ErrorBanner` and `SessionPlaceholderView` move into `Components/`)
- Modify: `clients/ios/App/AimuxApp.swift`, `clients/ios/App/DebugHooks.swift` (demo data for every Now case, `-tab now|all`, `-demo twenty`), `clients/ios/UITests/SessionListUITests.swift`, `clients/ios/project.yml` (version 0.2)

**Interfaces:**
- Consumes: `NowModel`, `SessionListModel`, `SearchModel`, `allSections`, `SessionRowFormat`.

Screen rules (from the spec's Now and All sections and `images/now.png`, `images/all-and-attachments.png`):
- `TabView` with `Tab("Now")`, `Tab("All")` and `Tab(role: .search)`; compose button top right (opens nothing yet; new session is a later plan, so hide it until then).
- Now: large title "Now"; summary line "N need you · N working · N on your Mac" with only the first part amber; Needs you approval cards (title, `approval.title` large, `command` in monospace when present, one button per option: first `allow_*` filled black/white, `reject_*` in red text, "Open ›"); failed rows with red dot and "Retry" (sends `lastPrompt` as a new message with a fresh idempotency key); Finished rows (blue dot is not in the palette: use the grey dot and the label "Finished"); Working list or project groups (tap a group to expand in place); On your Mac with grey dots, "All N" link to the All tab filtered to terminal sessions; empty state text "Nothing needs you".
- All: large title "All Sessions"; project menu and sort menu; day sections with counts; lock glyph on terminal sessions; pull to refresh.
- Search tab: field on top; cached-list title matches at once, server hits with bold snippets after 400 ms (existing `SearchModel`).
- Every row: one status dot, no coloured text except the amber summary and "Needs you" label.

- [ ] **Step 1: UI test first.** Rewrite `SessionListUITests` as `NowAllUITests`: launch `-demo list -tab now`; the approval card shows its options; tapping "Allow once" removes the card; the All tab shows "Today"; the search tab finds "Weekly report". Run it, expect failure.
- [ ] **Step 2: Build the views** until the UI test passes.
- [ ] **Step 3: Screenshots** (light and dark) on the SE 3 simulator and an iPhone 14 Pro simulator for `-demo list`, `-demo twenty`, `-demo empty`, `-tab all`, search; read every image; compare with `images/now.png` and `images/now-20-working.png`; no truncated titles where the mockup wraps, Arabic row leading-aligned.
- [ ] **Step 4: Full test run** (`Test run with N tests ... passed`, `Executed 2 tests, with 0 failures`), `swiftlint lint --strict`, `appstore-check.sh clients/ios Aimux` 0 FAIL.
- [ ] **Step 5: Install on the owner's iPhone**, confirm with `devicectl device info apps` that it shows 0.2 and the new build number; say "installed, not opened" if the phone is locked.
- [ ] **Step 6: Commit** `feat(ios): Now and All screens with the tab bar`.

### Task 8: Web client ranking modules (same fixtures)

**Files:**
- Create: `clients/web/webapp/now.mjs`, `clients/web/webapp/all.mjs`, `clients/web/webapp/now.test.mjs`, `clients/web/webapp/all.test.mjs`
- Create: `.github/workflows/clients-web.yml`
- Modify: `clients/web/remoteclient_test.go` (renamed `webclient_test.go`): the handler must not serve `*.test.mjs`

**Interfaces:**
- Produces (ES module exports, mirroring Task 4 and 5):
  - `deriveActivity(events) -> {kind: 'none'|'working'|'needsApproval'|'failed'|'finished', step?, approval?, error?, at?}`
  - `rankNow({sessions, activity, seen, now}) -> {needsYou, finished, working: {list} | {groups, more}, onMac, onMacTotal, summary}` (items carry `{session, activity}`)
  - `allSections({sessions, project, sort, now, timeZone}) -> [{day, sessions}]`

- [ ] **Step 1: Failing tests.** `now.test.mjs` reads every file in `../../../api/testdata/clients/now/` with `node:fs`, runs `deriveActivity` and `rankNow`, and asserts ids per section with `node:assert/strict` `deepEqual`; `all.test.mjs` does the same for `all/` (use `Intl.DateTimeFormat` with the fixture's `timeZone`).
  Run: `node --test clients/web/webapp/`
  Expected: FAIL (`Cannot find module './now.mjs'`).
- [ ] **Step 2: Implement** `now.mjs` and `all.mjs` with the rules from Task 4 and 5.
- [ ] **Step 3: Run** `node --test clients/web/webapp/`; expect all fixture cases pass. Run `go test ./clients/web/` with a new case `TestHandler_DoesNotServeTests` (GET `/app/now.test.mjs` returns 404).
- [ ] **Step 4: CI.** `clients-web.yml`: paths `['clients/web/**', 'api/**', '.github/workflows/clients-web.yml']`, ubuntu, Node 22, `node --test clients/web/webapp/`.
- [ ] **Step 5: Commit** `feat(web): Now and All ranking modules on the shared fixtures`.

### Task 9: Web client Now and All screens

**Files:**
- Modify: `clients/web/webapp/index.html`, `clients/web/webapp/app.css`, `clients/web/webapp/app.js` (list and search views only; the session view is untouched in this plan)

**Interfaces:**
- Consumes: `now.mjs`, `all.mjs`; existing `api()` helper, `runSearch`, `showSession`.

- [ ] **Step 1: Behaviour first.** Write a Playwright check script (in the scratchpad, not the repo) for the `qa-tools:behavior-validator` run: open `/app/` against a local `aimux remote serve` with a seeded busy session, expect the Now tab, the summary line, the On your Mac section, the All tab with "Today", and search returning hits. Run it, expect failure on the old UI.
- [ ] **Step 2: Rebuild the list views** in `app.js` to render `rankNow`/`allSections` output with the visual system (CSS custom properties for light and dark under `prefers-color-scheme`, status dot classes, bottom tab bar with Now / All and a round search button, approval cards posting to `/v1/approvals/{id}`, 15 s polling while the page is visible via `document.visibilityState`). A refused snapshot (404/409) is treated as no activity (Review Focus).
- [ ] **Step 3: Run the check script** until it passes; take phone-size screenshots (390x844) light and dark and read them against `images/now.png`.
- [ ] **Step 4: Run** `go test ./... -timeout 60s` and `node --test clients/web/webapp/`.
- [ ] **Step 5: Commit** `feat(web): Now and All screens in the web client`.

### Task 10: Docs

**Files:**
- Modify: `README.md` (clients section), `clients/ios/README.md` (new paths, code map with `Events.swift`, `Activity.swift`, `NowRanking.swift`, `AllSections.swift`, `NowModel.swift`, the new views, backlog without the done items), `docs-site/src/content/docs/guides/remote-access.mdx` (web client screens, where the iPhone app lives), `RELEASE_NOTES.md` (Unreleased: clients moved; Now and All), `docs/project-context.md` if present in aimux, else `clients/ios/docs/project-context.md` copied from aimux-ios and updated

- [ ] **Step 1: Update** each file to describe what exists after Tasks 1-9; no references to `internal/remoteclient` or the aimux-ios repo as current.
- [ ] **Step 2: Verify** `git grep -n "internal/remoteclient\|aimux-ios"` lists only historical docs (`docs/superpowers/`, past release notes).
- [ ] **Step 3: Commit** `docs: clients layout, Now and All`.

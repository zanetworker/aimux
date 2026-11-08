# Finding Sessions: Mac Layout and One Folder Identity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Now mirrors iTerm2's windows, tabs and panes; every session has one folder identity; All has Recent and Folders with search on top; the Search tab goes away, in both phone clients.

**Architecture:** The server adds three additive fields to each listed session: `folder` (resolved in the background from git, cached in `~/.aimux/folders.json`), `mac` (iTerm2 place from a read-only JXA `layout` op joined to Claude Code's registry by tty) and `working` (registry busy). The clients build Now, All Folders and search from shared fixtures in `api/testdata/clients/`, which pin the rules once for both.

**Tech Stack:** Go (server, `internal/...`), JXA (`internal/terminput/iterm2/helper.js`), SwiftUI + AimuxKit (`clients/ios`, XcodeGen), plain ES modules with `node --test` (`clients/web/webapp`).

**Spec:** `docs/superpowers/specs/2026-10-31-finding-sessions-mac-layout-design.md`

## Global Constraints

- Contract changes are additive to `api/remote-v1.openapi.yaml`; old clients keep working; `lastOnMac` stays in the list.
- The layout read is read-only: never start iTerm2 (`app.running()` first), never focus, rename, rearrange or type. It does not depend on `remote_access.terminal_input.enabled`.
- Window and tab names, titles and paths never appear in logs, fixtures or commits. Fixtures use made-up names and `/src/...`-style paths.
- Layout cache 3 s; on a failed read serve the last good layout for up to 30 s. Clients keep the shown layout until `mac` has been absent for 30 s.
- Folder resolution never runs on the request path; a path not resolved yet uses the non-git rule.
- `lastInFront` follows `lastOnMac`'s 12-hour rule (43200 s, inclusive).
- No new dependency. Near-monochrome UI, one state colour, titles dominant; folder and place are quiet secondary text. Pin mark is an SF Symbol / icon, not an emoji.
- Writing in UI copy and docs: no em dashes.
- iOS: `xcodegen generate` before every build; lint with swiftlint (file length 400, type body 250, line 120). Simulator: iPhone 17e `F923E38C-7EF0-42F3-9021-E8ECFD754F50`.
- Never type into, focus or close the owner's real iTerm2 windows in tests; the live JXA read is exercised only by an opt-in live test, read-only.

## Review Focus

1. A registry session whose tty matches no pane (or whose pane runs a shell in front): listed under Elsewhere, never dropped. Test in Task 3 and the Now fixtures (Task 4).
2. A window whose name equals its active pane's name: "Window N"; a renamed window keeps its name. Test in Task 2.
3. Two git repos with the same folder name, and two non-git folders with the same name: distinct `repoPath`, labels disambiguated by the parent. Test in Task 1.
4. A pinned session that is open in iTerm2: shown once, in its pane, not under Pinned. Fixture in Task 4.
5. The layout read failing for one poll: the server serves the last good layout (Task 3); the clients keep the shown layout (Tasks 6 and 8).

## File Map

- Create `internal/folders/folders.go`, `folders_test.go`: `Folder`, `Resolver` (git or non-git rule, cache file, background queue).
- Modify `internal/terminput/iterm2/helper.js`, `iterm2.go`, `iterm2_test.go`: `layout` op and `Driver.Layout`.
- Create `internal/terminput/layout.go`, `layout_test.go`: `MacPlace`, `LayoutReader` (join, window names, caching).
- Modify `internal/remoteapi/api.go` (`Session` fields, `API.Places`, `API.Folders`), `local.go` (`working`), `contract.go`, `cmd/aimux/cmd/remote.go` (wiring), `api/remote-v1.openapi.yaml`.
- Fixtures and rules: `api/testdata/clients/now/` (rewritten), `api/testdata/clients/all/` (Folders added, `mostActive` and `project` removed), new `api/testdata/clients/search/`, `api/testdata/clients/README.md`.
- iOS kit: `Models.swift` (fields), `NowRanking.swift` (rewritten), new `FolderTree.swift`, `SessionSearch.swift` (replaces `localMatches` in `SearchModel.swift`), `AllSections.swift`.
- iOS app: `NowView.swift`, `AllView.swift`, new `Components/SearchField.swift`, `RootTabs.swift` (Search tab removed, `SearchView.swift` deleted), `NowModel.swift` (layout hold), `DebugHooks.swift`, `DemoFinding.swift`, UI tests.
- Web: `now.mjs` (rewritten), new `folders.mjs`, `search.mjs`, `nowView.mjs`, `tabs.mjs` (Search tab removed), `all.mjs`, `nowModel.mjs`, `app.css`, tests.
- Docs: `docs-site/src/content/docs/guides/remote-access.mdx`, `RELEASE_NOTES.md`, `clients/ios/README.md`, `clients/ios/docs/project-context.md`, `docs/remote-agents.md` (parity note), `.claude/CLAUDE.md` (new files).

### Task 1: Folder resolver

**Files:** Create `internal/folders/folders.go`, `internal/folders/folders_test.go`.

**Interfaces:**
- Produces:
  ```go
  type Folder struct {
      Repo     string `json:"repo"`               // shown name of the top folder
      RepoPath string `json:"repoPath"`           // its full path, the grouping key
      Worktree string `json:"worktree,omitempty"` // linked worktree folder name; absent for the main checkout
      Sub      string `json:"sub,omitempty"`      // path below the worktree top, "/"-separated
      Label    string `json:"label"`              // "aimux / clients · ios"
  }
  type Resolver struct{ /* Home, CachePath, Git func(dir string) (top, commonDir string, err error), Exists func(string) bool */ }
  func NewResolver(home, cachePath string) *Resolver
  func (r *Resolver) Folder(path string) Folder   // never blocks on git; queues unknown paths
  func (r *Resolver) Run(ctx context.Context)     // background: resolves queued paths, at most 4 per tick, saves the cache
  func (r *Resolver) ResolveNow(path string)      // test helper and Run's step: resolve one path synchronously
  ```

- [ ] **Step 1: Write the failing tests** (`Git` and `Exists` faked; `t.TempDir()` for the cache)
  - `TestFolder_MainCheckoutAndSub`: path `/src/aimux/clients/ios` with top `/src/aimux`, common dir `/src/aimux/.git` → `{Repo:"aimux", RepoPath:"/src/aimux", Sub:"clients/ios", Label:"aimux · clients/ios"}`.
  - `TestFolder_LinkedWorktree`: path `/wt/clients` with top `/wt/clients`, common dir `/src/aimux/.git` → `{Repo:"aimux", RepoPath:"/src/aimux", Worktree:"clients", Label:"aimux / clients"}`; a path `/wt/clients/ios` → `Sub:"ios"`, `Label:"aimux / clients · ios"`.
  - `TestFolder_NonGit`: `/home/me/_experiments/x` (git fails) → `{Repo:"_experiments", RepoPath:"/home/me/_experiments", Worktree:"x", Label:"_experiments / x"}`; the home folder itself → `{Repo:"Home", RepoPath:"/home/me", Label:"Home"}`.
  - `TestFolder_SameNamesDisambiguated`: repos `/a/aimux` and `/b/aimux` → `Repo` values `"a / aimux"` and `"b / aimux"`; non-git `/home/me/work/src/x` and `/home/me/play/src/y` → `"work / src"` and `"play / src"`. A lone name is never prefixed.
  - `TestFolder_UnresolvedUsesNonGitRuleUntilRun`: `Folder` on a new git path returns the non-git rule and records no git call; after `ResolveNow`, the git rule.
  - `TestFolder_CacheSurvivesDeletedWorktree`: resolve, save, new Resolver on the same cache with `Exists` false and `Git` failing → same Folder.
  - `TestFolder_CacheFileIsOwnerOnly`: cache mode `0600`.
- [ ] **Step 2: Run** `go test ./internal/folders/` → FAIL (package missing).
- [ ] **Step 3: Implement.** Git via `git -C <dir> rev-parse --show-toplevel --git-common-dir` (5 s timeout) as the default `Git`; the main checkout's top is the parent of the common dir. Disambiguation is computed over all known RepoPaths each time `Folder` is called (the set is small). Cache: JSON map path → Folder fields minus Label/Repo display, written atomically.
- [ ] **Step 4: Run** `go test ./internal/folders/ -race` → PASS.
- [ ] **Step 5: Commit** `feat(folders): one folder identity per session path`.

### Task 2: iTerm2 `layout` op

**Files:** Modify `internal/terminput/iterm2/helper.js`, `internal/terminput/iterm2/iterm2.go`, `internal/terminput/iterm2/iterm2_test.go`, `internal/terminput/terminput.go` (the `PaneRef` type, next to `Pane`).

**Interfaces:**
- Produces:
  ```go
  // in package terminput, shared like terminput.Pane
  type PaneRef struct {
      WindowID   int    // iTerm2 window id
      WindowName string // as iTerm2 shows it
      ActiveName string // name of the window's current session
      Tab        int    // 1-based position in the window
      TabName    string // name of the tab's current session
      Pane       int    // 1-based position in the tab
      Panes      int    // panes in the tab
      TTY        string
  }
  func (d *iterm2.Driver) Layout(ctx context.Context) ([]terminput.PaneRef, error) // empty, nil error when iTerm2 is not running
  ```
- [ ] **Step 1: Write the failing test** `TestLayoutParsesHelperReply` using the existing fake-helper pattern in `iterm2_test.go`: a reply with two windows (ids 9 and 4), tabs with 2 and 1 panes → `[]PaneRef` in the reply's order with every field set; a helper error string → error; `{"windows":[]}` → empty slice, nil error.
- [ ] **Step 2: Run** `go test ./internal/terminput/iterm2/ -run Layout` → FAIL.
- [ ] **Step 3: Implement.** helper.js `case 'layout'`: if `!app.running()` return `{windows: []}`; else read in bulk (`app.windows.id()`, `.name()`, `app.windows.currentSession.name()`, `app.windows.tabs.currentSession.name()`, `app.windows.tabs.sessions.tty()`) and return nested arrays; never call `select`, `activate` or `write`. Go flattens to `[]PaneRef`. Add `Layout` to the `response` struct as a `Windows` field.
- [ ] **Step 4: Run** `go test ./internal/terminput/iterm2/` → PASS.
- [ ] **Step 5: Commit** `feat(iterm2): read-only layout op`.

### Task 3: Mac places joined to sessions

**Files:** Create `internal/terminput/layout.go`, `internal/terminput/layout_test.go`.

**Interfaces:**
- Consumes: `PaneRef` and `Driver.Layout` (Task 2) through an interface; `System.TTY`, `System.ForegroundIsPID` (existing); `TerminalPIDs func() map[string]int` (existing `LocalIndex.TerminalPIDs`); `LastOnMac func() (id string, at time.Time, ok bool)` (existing).
- Produces:
  ```go
  type LayoutSource interface{ Layout(ctx context.Context) ([]PaneRef, error) } // iterm2.Driver satisfies it
  type MacWindow struct{ ID string `json:"id"`; Index int `json:"index"`; Name string `json:"name"` }
  type MacTab struct{ Index int `json:"index"`; Name string `json:"name"` }
  type MacPlace struct {
      Window      MacWindow `json:"window"`
      Tab         MacTab    `json:"tab"`
      Pane        int       `json:"pane"`
      Panes       int       `json:"panes"`
      LastInFront bool      `json:"lastInFront"`
  }
  type LayoutReader struct{ Source LayoutSource; Sys System; Sessions func() map[string]int; LastOnMac func() (string, time.Time, bool); Now func() time.Time; Log io.Writer }
  func (r *LayoutReader) Places(ctx context.Context) map[string]MacPlace // session id -> place; nil when unknown
  ```
- [ ] **Step 1: Write the failing tests** (fake source, fake System, fixed clock)
  - `TestPlaces_JoinsByTTY`: sessions `s1`→pid 11 (tty `/dev/ttys001`, foreground) and `s2`→pid 12 (`/dev/ttys002`) with panes on those ttys → both placed with window, tab, pane, panes.
  - `TestPlaces_UnmatchedOrShellIsAbsent`: a session whose tty is in no pane, and one whose pid is not the pane's foreground → absent from the map.
  - `TestPlaces_WindowNames`: window ids 9 and 4 → indexes by ascending id (4 is 1, 9 is 2); a window whose name equals its active pane's name → `"Window 2"`; a renamed window keeps its name; a window name that contains the active pane's name as a prefix plus a suffix iTerm2 adds (such as " (zsh)") counts as echoing too.
  - `TestPlaces_LastInFrontWithin12h`: `LastOnMac` returns `s1` at now-43200s → `LastInFront` true; at now-43201s → false.
  - `TestPlaces_CachesAndHoldsLastGood`: two calls within 3 s read the source once; a failing read 10 s after a good one returns the good places; 31 s after → nil. The log line on failure contains no names.
- [ ] **Step 2: Run** `go test ./internal/terminput/ -run Places` → FAIL.
- [ ] **Step 3: Implement** `Places`, with a mutex around the cache.
- [ ] **Step 4: Run** `go test ./internal/terminput/... -race` → PASS.
- [ ] **Step 5: Commit** `feat(terminput): Mac places of open sessions`.

### Task 4: Server fields, contract and wiring

**Files:** Modify `internal/remoteapi/api.go`, `internal/remoteapi/local.go`, `internal/remoteapi/contract.go` (if types live there), `internal/remoteapi/contract_test.go`, `internal/remoteapi/local_test.go`, `cmd/aimux/cmd/remote.go`, `api/remote-v1.openapi.yaml`.

**Interfaces:**
- Consumes: `folders.Resolver.Folder` (Task 1), `terminput.LayoutReader.Places` and `terminput.MacPlace` (Task 3).
- Produces on `remoteapi.Session`: `Folder *folders.Folder \`json:"folder,omitempty"\``, `Mac *terminput.MacPlace \`json:"mac,omitempty"\``, `Working bool \`json:"working,omitempty"\``; on `API`: `Places func(ctx) map[string]terminput.MacPlace`, `Folder func(path string) folders.Folder`.
- [ ] **Step 1: Write the failing tests**
  - `TestListSessions_FolderMacWorking` (api test with fakes): `Folder` set for every session from `Project`; `Mac` only for ids in `Places`; `Working` true only for an `open_in_terminal` session whose registry status is `busy`; a nil `Places` or `Folder` leaves the fields absent.
  - Contract test: the list response validates against the updated OpenAPI schema (existing contract test pattern) with all three fields present and absent.
- [ ] **Step 2: Run** `go test ./internal/remoteapi/ -run 'ListSessions_FolderMacWorking|Contract'` → FAIL.
- [ ] **Step 3: Implement.** `Working` in `LocalIndex.Sessions` from `open[id].Status == "busy"`; `Folder` and `Mac` filled in `listSessions` after `a.Sessions`. Wire in `remote.go`: one `folders.NewResolver(home, ~/.aimux/folders.json)` started with `go resolver.Run(ctx)`; one `LayoutReader` over the iTerm2 driver (also when terminal input is off), `SystemProbe{}`, `index.TerminalPIDs`, and the existing `lastFocus.Last` when present. OpenAPI: `Folder`, `MacPlace` schemas and the three optional properties on `Session`, each described as in the spec.
- [ ] **Step 4: Run** `go test ./... -timeout 300s` → PASS; `make build`.
- [ ] **Step 5: Commit** `feat(remoteapi): folder, Mac place and working on listed sessions`.

### Task 5: Shared fixtures and rules

**Files:** `api/testdata/clients/README.md`; rewrite `api/testdata/clients/now/`; extend `api/testdata/clients/all/`; create `api/testdata/clients/search/`.

**Interfaces:**
- Produces the rules every client implements. Write the README sections first, then the fixtures.
- **Now** (`now/*.json`): input `name`, `now`, `sessions` (wire shape with `folder`, `mac`, `working`, `pinned`), `events`, optional `query`, optional `workingOnly`. `expect`:
  ```json
  { "needsYou": ["ids"], "pinned": ["ids"],
    "windows": [{ "id": "w4", "name": "Window 1", "tabs": [{ "index": 3, "name": "aimux", "panes": 4, "working": 1, "open": true, "sessions": ["ids"] }] }],
    "hiddenTabs": 0, "elsewhere": [{ "label": "aimux / clients", "sessions": ["ids"] }],
    "other": ["ids"], "working": 4, "open": 25 }
  ```
  Rules: Needs you as today (pending approvals, failures of the last 900 s); a session appears once, Needs you first. Open is `state` busy or `open_in_terminal`, or working activity. Working is `state` busy, working activity, or `working: true`. Pinned: pinned, not in Needs you, no `mac`; newest first, ties by id. Windows by `mac.window.index`, tabs by `mac.tab.index`, sessions by `mac.pane`; a tab's `working` counts its working sessions; `open` is true for the tab holding a `lastInFront` session, else false. Elsewhere: open sessions with no `mac`, not pinned, not in Needs you, grouped by `folder.label`, groups by their newest session, sessions newest first, ties by id. `query` (words, lower-case, every word a substring of title, description, `folder.label`, `mac.tab.name`, `mac.window.name` or `lastPrompt`): only matches stay in every section; tabs with no match are left out and counted in `hiddenTabs`; tabs with a match have `open: true`; `other` lists matching sessions that are not open and not in Needs you or Pinned, newest first. `workingOnly`: only working sessions stay; tabs without one are left out and counted. `working` and `open` count over all sessions, before query or filter.
  Fixtures (replace the old set; delete files that pin Last on your Mac, Finished, Working groups, On your Mac caps and `onMacFolders`): `mac-order.json`, `window-renamed.json`, `last-in-front-opens-tab.json`, `no-last-in-front.json`, `unmatched-goes-elsewhere.json`, `pinned-open-stays-in-pane.json`, `pinned-closed.json`, `elsewhere-by-folder.json`, `needs-you-wins.json` (kept, updated), `working-only.json`, `query-hides-tabs.json`, `query-other-sessions.json`, `no-mac-at-all.json` (every open session under Elsewhere by folder).
- **All** (`all/*.json`): `sort` is `recent` (today's `newest`, renamed) or `folders`. Remove `mostActive` and `project` and their fixtures. `folders` expect:
  ```json
  { "repos": [{ "repoPath": "/src/aimux", "name": "aimux", "count": 5, "working": 1, "open": true,
      "sessions": ["main checkout ids"],
      "worktrees": [{ "name": "clients", "count": 3, "working": 1, "open": true, "sessions": ["ids"] }] }] }
  ```
  Rules: repos by `folder.repo` A to Z (case-insensitive), ties by `repoPath`; sessions newest first, ties by id; worktrees A to Z; `open` is true only on the repo and worktree holding the newest session overall; a session with no `folder` goes under a repo named by the last part of `project` with `repoPath` = `project`. Fixtures: `folders-tree.json`, `folders-same-name.json`, `folders-no-folder-field.json`, plus the existing day fixtures renamed to `recent`.
- **Search** (`search/*.json`): `name`, `query`, `sessions`, `expect: {"open": [ids], "other": [ids]}`. Same word rule as Now. `open`: matching open sessions with `mac` in Mac order, then matching open sessions without `mac` newest first; `other`: the rest newest first. Empty or blank query → both empty. Fixtures: `title-and-label.json`, `tab-name.json`, `every-word.json`, `blank.json`, `open-first.json`.
- [ ] **Step 1:** Write the README sections and fixtures. All ids, names and paths made up.
- [ ] **Step 2:** `python3 -c` JSON-parse every new fixture; `node --test clients/web/webapp/now.test.mjs` → FAIL (old rules) as expected.
- [ ] **Step 3: Commit** `test(clients): fixtures for Mac layout, folders and search`.

### Task 6: iOS kit (models and rules)

**Files:** `clients/ios/AimuxKit/Sources/Models.swift`, `NowRanking.swift` (rewrite), create `FolderTree.swift`, `SessionSearch.swift`, modify `AllSections.swift`, `SearchModel.swift` (drop `localMatches`), `NowModel.swift`; tests `AimuxKitTests/NowRankingTests.swift`, `FolderTreeTests.swift`, `SessionSearchTests.swift`, `AllSectionsTests.swift`, `NowModelTests.swift`.

**Interfaces:**
- Consumes: the fixtures of Task 5.
- Produces:
  ```swift
  public struct Folder: Codable, Equatable, Sendable { repo, repoPath: String; worktree, sub: String?; label: String }
  public struct MacPlace: Codable, Equatable, Sendable { window: MacWindow; tab: MacTab; pane: Int; panes: Int; lastInFront: Bool }
  // Session gains: folder: Folder?, mac: MacPlace?, working: Bool (default false)
  public struct NowTab: Equatable, Sendable { index: Int; name: String; panes: Int; working: Int; open: Bool; sessions: [Session] }
  public struct NowWindow: Equatable, Sendable { id: String; name: String; tabs: [NowTab] }
  public struct FolderGroup: Equatable, Sendable { label: String; sessions: [Session] }
  public struct NowView: Equatable, Sendable { needsYou: [NowItem]; pinned: [NowItem]; windows: [NowWindow]; hiddenTabs: Int; elsewhere: [FolderGroup]; other: [Session]; working: Int; open: Int }
  public func rankNow(sessions: [Session], activity: [String: Activity], now: Date, query: String = "", workingOnly: Bool = false) -> NowView
  public struct RepoNode, WorktreeNode  // fields as in the all/ folders fixture
  public func folderTree(_ sessions: [Session], now: Date) -> [RepoNode]
  public func searchSessions(_ sessions: [Session], query: String) -> (open: [Session], other: [Session])
  public func matches(_ session: Session, words: [String]) -> Bool
  ```
  `NowModel` keeps the last shown `mac` per session for 30 s when a list arrives without any `mac` (pure function `holdPlaces(previous:current:now:) -> [Session]`, tested).
- [ ] **Step 1: Write the failing tests:** fixture-driven tests that load every file in `now/`, `all/` and `search/` (existing loader pattern) and compare to `expect`; `holdPlaces` tests: absent for 10 s keeps places, 31 s drops them, a new list with places replaces them.
- [ ] **Step 2: Run** `xcodebuild test ... -only-testing:AimuxKitTests` → FAIL.
- [ ] **Step 3: Implement.** Keep `deriveActivity` and Needs you logic; delete the Last on your Mac, Finished, Working-group and On-your-Mac-cap code.
- [ ] **Step 4: Run** kit tests → PASS; swiftlint clean.
- [ ] **Step 5: Commit** `feat(ios kit): Mac layout, folder tree and one search`.

### Task 7: iOS screens

**Files:** `clients/ios/App/NowView.swift`, `AllView.swift`, create `App/Components/SearchField.swift` (shared box + results list), `RootTabs.swift`, delete `SearchView.swift`, `DebugHooks.swift`, `DemoFinding.swift`, `UITests/*` that reference the Search tab or old sections, `project.yml` (build 32).

**Interfaces:**
- Consumes: Task 6.
- Produces: Now (search box, `Working · N` chip, Needs you, Pinned, On your Mac windows and tabs, Elsewhere, and in query mode "N tabs with no match are hidden" plus Other sessions and In conversations); All (search box, Recent | Folders segmented control persisted in `@AppStorage("allMode")`, default `recent`); tab open state in `@AppStorage("openTabs")` keyed `"<window name>|<tab name>"` storing explicit true/false, falling back to the fixture's `open`; folder open state the same way keyed by `repoPath` and `repoPath|worktree`. Rows: title, state line ("Working · 1m 20s · step", "Your turn · 4 min"), secondary line `folder.label` and "pane N"; pinned-and-open rows show `pin.fill`; `lastInFront` shows a small `diamond.fill`.
- [ ] **Step 1: Write the failing UI tests** (`UITests/FindingUITests.swift`, demo data): with `-demo finding`, Now shows "Window 1" and the open tab's sessions, a folded tab line "openshell · 2 panes · 1 working", tapping it opens it; the tab bar has exactly Now and All; All shows Recent selected first, switching to Folders shows `aimux`, the choice survives relaunch; typing "queue" on Now hides other tabs and shows "Search" results under Other sessions.
- [ ] **Step 2: Run** the UI tests → FAIL.
- [ ] **Step 3: Implement.** Demo data in `DemoFinding.swift` gets `folder`, `mac` and `working` for its sessions (made-up names, two windows, one renamed).
- [ ] **Step 4: Run** all iOS tests → PASS; screenshots of Now, Now with query, All Recent, All Folders in light and dark on the 17e simulator, looked at before moving on.
- [ ] **Step 5: Commit** `feat(ios): Now mirrors the Mac; All with Recent and Folders; search on both`.

### Task 8: Web client

**Files:** `clients/web/webapp/now.mjs` (rewrite), create `folders.mjs`, `search.mjs`, modify `nowView.mjs`, `tabs.mjs`, `all.mjs`, `nowModel.mjs`, `rows.mjs`, `app.css`; tests `now.test.mjs`, `folders.test.mjs`, `search.test.mjs`, `all.test.mjs`, `nowModel.test.mjs`, `tabs.test.mjs`.

**Interfaces:**
- Consumes: Task 5 fixtures; same names as Task 6 in camelCase: `rankNow({sessions, activity, now, query, workingOnly})`, `folderTree(sessions, now)`, `searchSessions(sessions, query)`, `matches(session, words)`, `holdPlaces(previous, current, now)`.
- Produces: the same screens as Task 7; storage keys `allMode`, `openTabs`, `openFolders` in `localStorage`, every access in try/catch.
- [ ] **Step 1: Write the failing tests:** fixture loops for `now/`, `all/`, `search/`; `holdPlaces`; `tabs.test.mjs`: the tab bar renders Now and All only; All's switch persists.
- [ ] **Step 2: Run** `node --test "clients/web/webapp/*.test.mjs"` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the web tests → PASS; open the web client in a browser against the running server (or a static fixture page) and look at Now and All at phone width, light and dark.
- [ ] **Step 5: Commit** `feat(web): Now mirrors the Mac; All with Recent and Folders; search on both`.

### Task 9: Docs and live check

**Files:** `docs-site/src/content/docs/guides/remote-access.mdx`, `RELEASE_NOTES.md` (Unreleased), `clients/ios/README.md`, `clients/ios/docs/project-context.md`, `docs/remote-agents.md` (Frontend Parity: Mac layout and folder identity are phone-only for now), `.claude/CLAUDE.md` (new files: `internal/folders`, `internal/terminput/layout.go`).

- [ ] **Step 1:** Rewrite the guide's "Finding sessions" section for the new Now, All and search, and the API table rows for `folder`, `mac`, `working`; note the layout read is read-only and needs macOS Automation access to iTerm2.
- [ ] **Step 2:** Live check, read-only, printing shapes only: `curl` the list through the tailnet and count sessions with `mac` and `folder`; compare the count of `mac` sessions with the iTerm2 pane count of Claude sessions. No titles or names printed.
- [ ] **Step 3: Commit** `docs: finding sessions by Mac layout and folders`.

## Self-review notes

- Spec coverage: server fields (Tasks 1 to 4), Now (5, 6, 7, 8), All (5 to 8), search (5 to 8), Search tab removal (7, 8), edge rules (1, 3, 5, 6, 8), parity and docs (9), success measures are the owner's ten lookups after Task 9 (not automated).
- Ruling: the existing Needs you section (pending approvals and recent failures of phone sessions) stays above Pinned; the spec does not mention it, and it is cheap and already pinned by fixtures. The spec's left-out "Needs you" is the terminal-screen polling, which stays out.

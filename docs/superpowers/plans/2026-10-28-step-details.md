# Step Details and Colour Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Tap a step in the phone chat to read its full command, its error and an edit's removed and added lines, with colour only for failure and diffs, in the iPhone app and the web client.

**Architecture:** The server adds an optional `detail` to each history tool (`TurnTool.detail`), filled from the parsed transcript and capped. Both clients' timelines give every step item a `detail`, from history `detail` or from live ACP `tool_call` data, pinned by the shared chat fixtures. The step row stays one line; tapping it opens the detail in place.

**Tech Stack:** Go (server), plain ES modules (web, no deps), SwiftUI + AimuxKit (iOS), shared JSON fixtures.

**Spec:** `docs/superpowers/specs/2026-10-28-step-details-design.md`

## Global Constraints

- Branch `feat/clients`, worktree `aimux-worktrees/clients`. Commit with `&&` after tests pass; never push; never `git stash`. Trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. `.claude/` is git-ignored: stage it with `git add -f .claude/CLAUDE.md`.
- Caps: input 4096 bytes, error 2048 bytes, each of `old`/`new` 4096 bytes; longer text is cut on a UTF-8 boundary and ends with `…`. `input` and `error` are trimmed; `old` and `new` keep their whitespace (indentation matters).
- API changes additive and optional: `detail` and each of its fields may be absent; old clients ignore it, new clients show no detail without it.
- Closed step unchanged: one line, its `target` from `snippet` (history) or `title` (live).
- Colours: failed step icon and line in the status red already used by status dots; diff lines muted red (removed) and green (added) backgrounds with `-`/`+` marks, light and dark; the `$` before a command in the secondary text colour. No other new colour.
- Edit view: removed lines then added lines, at most 40 lines in total, then a line "N more lines".
- Web: no framework, no build step, no npm deps, CSP unchanged; server text never reaches `innerHTML`. iOS: no new packages; logic in AimuxKit, views in `App/`. Code and commands are left to right.
- Fixtures invented; no real session text, paths or repos.
- Docs updated in the last task.

## Review Focus

- **A step with no detail at all** (old server, Codex history, a live call without `rawInput`): the row is not tappable and shows no disclosure affordance. Pinned in Task 2 fixtures (`detail` all `""`) and asserted in Tasks 3 and 4 view tests.
- **Huge inputs** (a 50 KB heredoc command, a 20 KB edit): the server caps them (Task 1 test), and the edit view stops at 40 lines with "N more lines" (Tasks 3 and 4 tests).
- **Hostile text in a command or error** (`<img onerror>`, `javascript:`): rendered as text, never as markup or a link. Task 3 test.
- **Edit whitespace**: leading spaces and tabs in `old`/`new` survive server and clients. Task 1 and Task 2 fixture.
- **A failed live step that later succeeds** (a `tool_call_update` with `completed` after `failed`): the last status and its error win; an error is cleared when the final status is not failed. Task 2 fixture.

## Task 1: Server: step detail on history tools

**Files:**
- Modify: `internal/trace/trace.go` (`ToolSpan`), `internal/provider/claude.go` (fill `Input`), `internal/remoteapi/contract.go` (`TurnTool`), `internal/remoteapi/local.go` (`turnOf`), `api/remote-v1.openapi.yaml` (`TurnTool`)
- Test: `internal/provider/claude_test.go`, `internal/remoteapi/local_test.go`, `internal/remoteapi/contract_test.go`

**Interfaces:**
- Produces: `trace.ToolSpan.Input string` (full input text); `remoteapi.TurnTool.Detail *ToolDetail` as JSON `detail`, omitted when every field is empty; `type ToolDetail struct { Input, Error, Old, New string }` with JSON `input`, `error`, `old`, `new`, each `omitempty`.

- [ ] **Step 1: Failing parser test** `TestClaudeParseTrace_ToolInputIsFull` in `claude_test.go`: a transcript with a Bash call whose command is 300 characters gets `Input` equal to the whole trimmed command and a `Snippet` still ending in `...` at 60 characters; Grep → its `pattern`, Glob → `pattern`, WebFetch → `url`, WebSearch → `query`, Task → `description`, Read/Edit/Write → `file_path`, an unknown tool → `""`.
- [ ] **Step 2: Run** `go test ./internal/provider/ -run TestClaudeParseTrace_ToolInputIsFull` → FAIL (no field).
- [ ] **Step 3: Implement** `claudeToolInput(toolName string, input map[string]interface{}) string` in `claude.go` beside `claudeToolInputSnippet`, set `Input` where the span is built (where `claudeToolInputSnippet` is called).
- [ ] **Step 4: Failing mapping test** `TestTurnOf_ToolDetail` in `local_test.go`: a span with `Input` 5000 bytes, `ErrorMsg` 3000 bytes, `OldString` `"\tif x {\n"` and `NewString` 5000 bytes maps to `detail.input` of length ≤ 4096+len("…") ending in `…`, `error` ≤ 2048+len("…") ending in `…`, `old` exactly `"\tif x {\n"`, `new` cut and ending in `…`; a span with none of them has `Detail == nil` and its JSON has no `detail` key; `snippet` unchanged. Write's `Content` maps to `new`.
- [ ] **Step 5: Run** `go test ./internal/remoteapi/ -run TestTurnOf_ToolDetail` → FAIL.
- [ ] **Step 6: Implement** `capText(s string, n int) string` (no trim, UTF-8 safe, `…` suffix) in `local.go` and the mapping in `turnOf` (`input`, `error` trimmed then `shorten`-style capped; `old`, `new` via `capText`).
- [ ] **Step 7: Contract** add `ToolDetail` to the OpenAPI (`additionalProperties: false`, all four optional strings, descriptions naming the caps) and `detail` to `TurnTool`; extend `contract_test.go` so a `TurnTool` with detail validates.
- [ ] **Step 8: Run** `go test ./internal/... ./api/... -timeout 60s` → PASS, then commit `feat(remoteapi): step detail on history tools`.

## Task 2: Shared fixtures: step detail in the chat timeline

**Files:**
- Modify: every `api/testdata/clients/chat/*.json` with step items, `api/testdata/clients/README.md` (Chat section)
- Create: `api/testdata/clients/chat/step-details-history.json`, `step-details-live.json`

**Interfaces:**
- Produces: every `steps[]` item is `{"kind", "target", "status", "detail": {"input", "error", "old", "new"}}`, all four strings always present (`""` when absent).
  - History: copied from the tool's `detail` fields.
  - Live, from the latest `tool_call` / `tool_call_update` for the id (fields it carries win, as for `title`): `input` is `rawInput.command`, else `rawInput.pattern`, `rawInput.url`, `rawInput.query`, `rawInput.description`, `rawInput.file_path`, the first that is a string, else `""`; `old`/`new` from the first `content` item of `type` `diff` (`oldText`, `newText`), else `rawInput.old_string`/`new_string`, else for a Write `rawInput.content` as `new`; `error` is the text of the `content` items of `type` `content` whose `content.type` is `text`, joined by `\n`, only when the final status is `failed`, else `""`. Clients do not cap live text (the event stream already bounds it).

- [ ] **Step 1: README rule** in the Chat section: the `detail` field and the two derivations above, and that a step whose four detail strings are empty has nothing to open.
- [ ] **Step 2: Update existing fixtures**: add `"detail": {"input": "", "error": "", "old": "", "new": ""}` (or the derived values when the fixture's events carry `rawInput`/`content`) to every step item; keep all other values.
- [ ] **Step 3: New fixtures** (invented data): `step-details-history.json` with a Bash tool with a long `input`, a failed tool with `error`, an Edit with `old` `"\tlet a = 1\n"` and `new` `"\tlet a = 2\n"`, and a tool with no `detail`; `step-details-live.json` with a Bash `tool_call` carrying `rawInput.command`, an edit with a `diff` content item, a call that fails with a text content item, and one that goes `failed` then `completed` (error `""`).
- [ ] **Step 4: Run** both clients' existing timeline tests to see them FAIL on the new field (expected), and `go test ./api/...` if it validates fixtures → PASS. Commit `test(clients): step detail in the chat fixtures`.

## Task 3: Web client: step detail and colour

**Files:**
- Modify: `clients/web/webapp/timeline.mjs`, `chatView.mjs`, `render.mjs` (if the step line lives there), `app.css`
- Test: `clients/web/webapp/timeline.test.mjs`, `references.test.mjs` (fake-document view tests)

**Interfaces:**
- Consumes: Task 2 item shape. Produces: `stepDetail(step)` in `render.mjs` returning a DOM node or `null` when all four strings are empty.

- [ ] **Step 1: Failing tests**: the timeline suite passes all `chat/*.json` including the two new files; a view test with the fake document: a step with detail renders a `button` row with `aria-expanded="false"` that opens a block holding the command in a `pre` (`dir="ltr"`) prefixed by a `$` span with class `dim`, the error under "Failed" with class `failed`, edit lines with classes `del`/`add` and `-`/`+` marks; 60 diff lines show 40 and "20 more lines"; a step with empty detail renders no button; a command `<img src=x onerror=alert(1)>` appears as text (no `img` element).
- [ ] **Step 2: Run** `node --test "clients/web/webapp/*.test.mjs"` → FAIL.
- [ ] **Step 3: Implement** the timeline fields and the view: click toggles open state per step (kept across re-renders by step index within its group), `--failed`, `--diff-del-bg`, `--diff-add-bg` tokens in `app.css` for light and dark; failed step icon and line use `--failed`.
- [ ] **Step 4: Run** the suite → PASS; Playwright screenshot of an open Bash step, a failed step and an edit, light and dark, at 390 px, saved to the session scratchpad `step-details/`; read them.
- [ ] **Step 5: Commit** `feat(web): open a step to read its command, error and edit`.

## Task 4: iPhone app: step detail and colour

**Files:**
- Modify: `clients/ios/AimuxKit/Sources/Timeline.swift` (`Step` gains `detail: StepDetail`), `clients/ios/App/Components/TimelineRows.swift` (`StepsCard`), `clients/ios/App/DemoChat.swift` (a demo with details)
- Create: `clients/ios/App/Components/StepDetailView.swift`
- Test: the AimuxKit timeline fixture tests, a UI test in `ChatUITests`

**Interfaces:**
- Consumes: Task 2 item shape. Produces: `public struct StepDetail: Equatable, Hashable, Sendable { input, error, old, new: String; var isEmpty: Bool }`.

- [ ] **Step 1: Failing tests**: the kit's chat fixture test passes all `chat/*.json` including the new ones (compare `detail`); a unit test that a 60-line edit gives 40 shown lines and 20 hidden (pure helper `diffLines(old:new:limit:) -> (lines: [DiffLine], hidden: Int)` in AimuxKit); a UI test opens a step with a command in the demo chat and finds the full command text and a "Failed" label on the failed step; a step without detail has no button trait.
- [ ] **Step 2: Run** the kit and UI tests → FAIL.
- [ ] **Step 3: Implement** `StepDetail` decoding from history `detail` and live events, `diffLines`, `StepDetailView` (monospace, wrapped, `$` in `.secondary`, error in the status red, diff rows with muted `.red`/`.green` backgrounds at low opacity, VoiceOver expand action), tap toggles per step in `StepsCard`; failed step icon and text in the status red.
- [ ] **Step 4: Run** kit tests, UI tests on the iPhone SE 3 simulator, `swiftlint --strict` → PASS; screenshots of the demo chat with an open command, a failed step and an edit, light and dark, to the session scratchpad `step-details/`; read them. Bump `CURRENT_PROJECT_VERSION` in `project.yml`.
- [ ] **Step 5: Commit** `feat(ios): open a step to read its command, error and edit`.

## Task 5: Docs

**Files:** `api/testdata/clients/README.md` (already in Task 2; check), `clients/ios/docs/project-context.md`, `clients/ios/README.md` (demo flag if added), `.claude/CLAUDE.md`, `docs-site/src/content/docs/guides/remote-access.mdx` (steps open to show command, error, edit; `TurnTool.detail` in the API table), `RELEASE_NOTES.md`.

- [ ] **Step 1: Update; verify file names; `cd docs-site && npm run build`.**
- [ ] **Step 2: Commit** `docs: step details`.

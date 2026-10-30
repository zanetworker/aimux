# Finding Sessions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Sessions are easy to find on the phone: the one last in front on the Mac is first on Now, pinned sessions follow, every session shows a useful title and (when Claude Code wrote one) its away summary as a description, folders on Now read as folders, and names cost almost no tokens.

**Architecture:** `internal/history` reads Claude Code's `away_summary` and keeps owner and model values in `.meta.json` through one locked writer used by every front end. A small namer (`internal/history/namer.go`) makes titles only for sessions whose shown title is useless, batched and within a daily budget, via the API key or an isolated `claude -p`. `remote serve` exposes descriptions, pins, owner edits, "Update summary", naming status and `lastOnMac` (from the terminal-input focus probe). Both clients show Last on your Mac, Pinned, folder rows and description lines, pinned by shared fixtures.

**Tech Stack:** Go; plain ES modules; SwiftUI + AimuxKit; shared JSON fixtures.

**Spec:** `docs/superpowers/specs/2026-10-30-finding-sessions-design.md` (read it whole; "Builder check: answers" overrides the body, and "Economy revision" overrides both for naming).

## Global Constraints

- Branch `feat/clients`, worktree `aimux-worktrees/clients`. Commit with `&&` after tests pass; never push; never `git stash`. Trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. `git add -f .claude/CLAUDE.md` when touched.
- Every implementer and reviewer invokes `development-tools:crafted-code` first: test first (see it fail), errors with context, no session text in logs or audit, simple and minimal.
- Privacy and secrets: never read, print or store an API key aimux did not already have; never print session text in logs, audit, test output or reports; fixtures invented. Tests never call a real model or `claude -p` (inject the backend).
- Economy: model calls only for sessions whose shown title is useless (empty, only an id with or without "Session", "continue" plus an id, or only a slash command) and for explicit "Update summary"; event-driven, never polling; batches of up to 5; input per session capped at 500 characters, output at 60 tokens; automatic naming only for sessions a phone client shows (Lean revision); "Update summary" input capped at 1,200 characters; default `sessions.naming_daily_limit` 20 automatic sessions per day (Lean revision); every call's input and output tokens recorded under the label "naming".
- Models: `haiku` → `claude-haiku-5-5` (default), `sonnet` → `claude-sonnet-5-5`, `opus` → `claude-opus-5-5`; unknown names fall back to the default with one log line; on "model not found" from the API, retry once through `claude -p --model haiku`.
- `claude -p` fallback: run from an empty temporary folder, no tools, no MCP servers, no hooks, the flags confirmed with `claude --help` and recorded; timeout 60 s; its own sessions are automated and never named.
- Metadata: `.meta.json` writes from every writer go through one helper: exclusive file lock, read again, change only the caller's fields, keep unknown keys, atomic replace. New fields: `description`, `title_by`, `description_by` ("model" or "owner"), `described_at`, `described_turns`. Missing `title_by` means model. Owner values are never overwritten by the model. Pin is the existing `starred`.
- Automatic naming runs only in `remote serve`, only with `sessions.auto_title` on, only for sessions active in the last 14 days, skipping automated and subagent sessions.
- API (device login applies; audit `describe`, `meta_edit`, `pin`, `unpin`, no text): `Session` and `SessionDetail` gain `description` and `pinned`; `PATCH /v1/sessions/{id}/meta {title?, description?, pinned?}`; `POST /v1/sessions/{id}/describe` → 200 with `{title, description, changed: [...], kept: [...]}`, 409 `owner_set` only when both are owner-set; health gains capability `describe` and `naming {today, limit, failing?}`; `GET /v1/sessions` gains `lastOnMac {id, at}` (in memory, from the focus probe every 15 s while the Mac's idle time is under 120 s, only with terminal input on).
- Clients: Now order: Last on your Mac (one row, hidden when unknown or older than 12 hours), Pinned, then the existing sections. Folder groups: folder icon instead of the status dot, semibold name, count and chevron, more space above, sessions indented under a thin guide line; single-session folders keep the icon. Rows show the description as one truncated secondary line. Info sheet: title and description with Edit, "Update summary", Pin. Long press (iOS) / row menu (web): Pin or Unpin. Search matches titles and descriptions locally first. Near-monochrome, light and dark, accessible.
- Docs in the last task.

## Review Focus

- **Cost runaway:** a loop that names the same session again and again (namer runs on its own `claude -p` sessions, a model title that is itself "useless", a failing call retried every event). Task 2 tests.
- **Lost writes:** the TUI starring while the namer writes; an older field kept. Task 1 tests with two writers.
- **Privacy:** session text in logs, audit, errors or test output; a key printed. Tasks 1-3.
- **Stale Last on your Mac:** a closed session, or the Mac idle for hours. Task 3 and fixtures.
- **Owner edits:** never overwritten by automatic naming or "Update summary". Tasks 2-3.

## Task 1: History: away summaries and one safe metadata writer

**Files:** `internal/history/history.go` (Meta fields, `Session.AwaySummary`, scan of `system` entries with subtype `away_summary`, latest wins), `internal/history/scancache.go` (version bump), new `internal/history/metawrite.go` (`UpdateMeta(sessionFile string, fn func(*Meta)) error`: lock, re-read, keep unknown keys, atomic replace); route every existing writer through it: `internal/controller/session_meta.go`, `internal/frontend/tui/app.go`, `internal/frontend/web/handlers.go`, `internal/history/titler.go`. Tests.

- [ ] Tests first: away summary latest wins, absent when none, automated sessions unaffected; `UpdateMeta` keeps unknown keys, two concurrent writers (goroutines and two processes via a helper binary or a re-read test) keep both changes, file mode unchanged, atomic replace; every writer uses it (a test or a grep-based check in the package test). Commit `feat(history): away summaries and a locked metadata writer`.

## Task 2: The namer

**Files:** new `internal/history/namer.go` (useless-title rule, title from summary or first message, Update summary, batching, budget, backends: API key or isolated `claude -p`, model resolution, token recording), update `titler.go` model map and reuse, `internal/config/config.go` (`naming_daily_limit`, `auto_describe` not needed now: leave out), `internal/cost` (record "naming" usage the way the package records other usage; find the existing pattern). Tests with an injected backend.

- [ ] Also (spec section "Our own descriptions"): automatic descriptions for sessions idle 5 minutes with no away summary and none of ours, once per session, same call as a useless title, 800-character input, shown-description order owner > newer away summary > ours. Tests for each rule.
- [ ] Tests first: useless-title table; owner values never overwritten; a model title that comes back useless is not stored and does not trigger another call; batching (5 per request, one request for 3); budget stops automatic calls at the limit and resets the next day, on-request calls still allowed and counted; failure backoff per session (1 h) and `failing` state after 3 consecutive failures with a fixed reason; model resolution and fallback; the `claude -p` command line is built with the isolation flags and an empty working folder (assert the args, never run it); automated and older-than-14-days sessions skipped; no session text in any log line or error. Commit `feat(history): economical session namer`.

## Task 3: remote serve: API, events, Last on your Mac

**Files:** `internal/remoteapi/api.go`, `local.go`, `contract.go`, `api/remote-v1.openapi.yaml`, `cmd/aimux/cmd/remote.go` (start the namer event-driven from history changes, focus probe ticker), `internal/terminput` (expose the focus probe result as session id via the driver's tty map, read-only), tests, contract tests.

- [ ] Tests first: `description`/`pinned` in list and detail; PATCH meta (owner values, pin = starred, validation, device login, audit); POST describe (200 changed/kept, 409 owner_set, one in flight per session, error reason on failure); health capability and naming status; `lastOnMac` set only while idle < 120 s and terminal input on, cleared when the session is gone, ignored after 12 hours by clients (server keeps `at`); namer triggered on a finished first exchange and on a new away summary, never on a timer. Commit `feat(remote): descriptions, pins, update summary, last on your Mac`.

## Task 4: Shared fixtures

**Files:** `api/testdata/clients/now/*.json` (new cases), `api/testdata/clients/README.md` (Now rules).

- [ ] Cases: Last on your Mac first; hidden when older than 12 hours or unknown; Pinned section order; a pinned session also working (decide: appears once, in Pinned, with its status dot); folder group rows (`kind: "folder"`, single-session folder); the description line (present, absent). Commit `test(clients): finding sessions fixtures`.

## Task 5: Web client

**Files:** `clients/web/webapp/now.mjs`, `nowView.mjs`, `nowModel.mjs`, `rows.mjs`, `sessionInfo.mjs`, `tabs.mjs` (search), `app.css`, tests.

- [ ] Tests first over the fixtures; Playwright against a scratch server (pattern from earlier checks); screenshots light and dark at 390 px to the session scratchpad `finding/`; look at them. Commit `feat(web): last on your Mac, pins, folders, descriptions`.

## Task 6: iPhone app

**Files:** `clients/ios/AimuxKit/Sources/NowRanking.swift`, `NowModel.swift`, `Models.swift`, `SessionDetail.swift`, `SearchModel.swift`, `clients/ios/App/NowView.swift`, the info sheet, `project.yml` (build +1), tests, UI tests.

- [ ] Tests first over the fixtures; UI tests for pin from long press, edit title, Update summary (fake), folder rows; screenshots light and dark. Commit `feat(ios): last on your Mac, pins, folders, descriptions`.

## Task 7: Docs

**Files:** `docs-site/src/content/docs/guides/remote-access.mdx`, the configuration page (naming settings), `RELEASE_NOTES.md`, `clients/ios/README.md`, `clients/ios/docs/project-context.md`, `.claude/CLAUDE.md`.

- [ ] Docs for names, descriptions, away summaries, the budget, the cost label, privacy (what text goes to Anthropic and when), pins, Last on your Mac. `cd docs-site && npm run build`. Commit `docs: finding sessions`.

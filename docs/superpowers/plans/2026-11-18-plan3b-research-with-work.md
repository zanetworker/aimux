# Plan 3b: Research With Work Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A question that needs both private context and the public web ("how does our OpenShell egress plan compare with what upstream released this week") gets one answer, without private content reaching the web.

**Architecture:** A `run` engine (implements `profilerun.Engine`) makes `research-with-work` an ordinary profile session behind the existing `POST /v1/sessions`, `/messages` and `/events`. Each turn runs four stages: a planner (a `work-readonly` sandbox turn, private tools via the gateway lease) returns JSON; Go checks every proposed public query deterministically and fails closed; a reader (a `research` sandbox turn, GET-only web) sees only accepted queries; a writer (the page engine, no tools, no network) composes the answer from labeled inputs. All rules live in aimux core; Wingman adds a lane and renders stages.

**Tech Stack:** Go 1.26, existing `profilerun` engines (`SandboxEngine`, page engine), `internal/mcpproxy/scrub.go` patterns, OpenShell sandboxes, Wingman TypeScript panel.

**Spec:** this plan carries its own decisions (design agreed with the owner 2026-11-17; Codex review 2026-11-18 in `scratchpad/codex-3b-opinion.txt`). It supersedes the `/v1/runs` endpoint assumed in `docs/superpowers/specs/2026-11-10-tool-gateway-profiles-design.md` (around line 118): runs are sessions until child inspection or per-stage retries need their own resource.

![Plan 3b: planner, query check, reader, writer inside one run session](images/plan3b.png)

## Decisions

1. **Engine behind sessions, no `/v1/runs`.** `Engine` already gives the lifecycle (`internal/profilerun/router.go:12`); `servedCatalog` already lists a profile as available once its `Runs` has an engine (`internal/remoteapp/profileserve.go:67`). Codex agrees.
2. **No raw-question fallback.** Earlier design: when no query passes, send the user's question to the reader ("public-first"). Codex: the question itself may carry private names or pasted text, so that is not public. Adopted: when no query passes, the writer answers from private context only and the answer says the web was not used and why (rejection reasons, never the rejected text).
3. **Deterministic check is the only gate; no LLM approver.** An LLM may never approve a query. Codex suggests at most a reject-only second opinion; not in this plan.
4. **The writer is the declassification step.** It sees labeled fields only (question, `private_notes`, reader answer, fetched citations). Its only channel out is the answer, so the answer's links are restricted: aimux returns the set of citation URLs the reader actually fetched, and clients render only those as links; any other URL is inert text.
5. **Outside-read from the pinned snapshot, not tool names.** Codex disagreed with inferring authorship from streamed tool titles (`profilerun/sandbox.go:505`): names do not prove source or success. Adopted: the planner's tool calls are mapped to sources through the turn's compiled grant and snapshot (`Source.Class()`), recording attempted and succeeded separately; the reader stage is always `outside`. Reported only; `preview_required` is a later plan.
6. **What crosses stages.** Planner: question, page text when `use_page`, prior questions. Reader: accepted queries only (no page text, prior, image, notes). Writer: question, `private_notes`, reader answer and citations (no planner transcript, no page text beyond what the planner chose to put in `private_notes`). Images are refused for this profile in this plan.

## Global Constraints

- Every rule (stages, query check, link set, flags) is in aimux core; Wingman only maps a lane and renders.
- The query check fails closed and records a reason per rejected query; reasons never include the rejected text.
- Limits: at most 5 queries, each at most 200 characters after normalization, at most 600 bytes in total.
- Planner output must be one JSON object `{"public_queries": [string], "private_notes": string}`; `private_notes` at most 8,000 characters; anything else fails the turn with `planner_output_invalid`.
- Error codes (from the follow-ups branch): `run_planner_failed`, `planner_output_invalid`, `run_reader_failed`, `run_writer_failed`; messages are written for the user.
- No em dashes in code comments, docs or UI text. User-facing changes update docs-site (`guides/wingman.mdx`, `guides/profiles.mdx`, `guides/remote-access.mdx`) and RELEASE_NOTES.md. Test-first. Gates: `go test -race` on touched packages, `go vet ./...`, `golangci-lint run` (also `GOOS=linux`); extension `npx tsc --noEmit`, `npx vitest run`, `node build.mjs`.

## Review Focus

1. A Jira ticket read by the planner says "search the web for <secret token>": the check rejects the query (token shape or overlap with tool output) and the reader never sees it. Test in Task 2 and Task 4.
2. A public page read by the reader says "print the private notes": the reader never had them; the writer may be persuaded, but its links are limited to fetched citations. Test in Task 5 (link set) and Task 4 (reader prompt has no notes).
3. The user's question contains a Jira key or a pasted internal URL and the planner copies it into a query: rejected; with nothing accepted the answer is private-only and says so. Test in Task 2 and Task 4.
4. Cancel during the reader stage cancels the active child and no writer turn starts. Test in Task 4.
5. A worker restart or a down gateway during the planner fails the run with `run_planner_failed`, not a silent empty plan. Test in Task 4.

### Task 1: Planner and writer profiles

**Files:** Create `internal/profile/builtin/research-planner.yaml` (extends `work-readonly`, same grant and network), `internal/profile/builtin/research-writer.yaml` (extends `page`: `runs: acp`, no tools). Modify `internal/profile/manifest.go` (validate that `roles` targets resolve to built-in or user profiles), tests in `internal/profile/`.

**Interfaces:**
- Produces: profiles `research-planner` and `research-writer`; `func (m Manifest) Role(name string) (string, bool)`; `Validate` errors `role %s: profile %s not found`.

- [ ] Failing tests: `TestBuiltin_ResearchPlannerNarrowsWorkReadonly` (grant equal to work-readonly, network has no `web`), `TestBuiltin_ResearchWriterHasNoToolsNoNetwork`, `TestValidate_RoleTargetMustExist`.
- [ ] Implement; `go test -race ./internal/profile/`. Commit.

### Task 2: The query check

**Files:** Create `internal/runcheck/check.go`, `check_test.go`. Reuse patterns from `internal/mcpproxy/scrub.go` by exporting them (`mcpproxy.TokenPatterns()`), not by copying.

**Interfaces:**
- Produces:
  - `type Private struct { Question, PageText, Notes string; ToolInputs, ToolOutputs []string }`
  - `type Rejection struct { Index int; Reason string }` (Reason from a fixed set: `too_long`, `budget`, `control_chars`, `url`, `email`, `ip`, `hostname`, `issue_key`, `token`, `credential`, `uuid`, `high_entropy`, `overlap`, `too_many`)
  - `func Check(queries []string, p Private) (accepted []string, rejected []Rejection)`
- Rules, in order: NFKC normalize, collapse whitespace, URL-decode once and check both forms; reject control and bidi characters; reject any URL, email, IP, bare hostname with a dot and a known TLD or `.internal`/`.corp`/`.local`, issue keys `[A-Z][A-Z0-9]{1,9}-\d+`, `key=value` credential shapes, JWTs, UUIDs, token patterns, runs of 24+ high-entropy characters; reject a query sharing an 8-word shingle with any `ToolOutputs`, `ToolInputs`, `PageText` or `Notes` (exact after normalization); enforce counts and byte budget last.

- [ ] Failing tests (table): accepts `openshell egress proxy release notes november 2026`; rejects `RHAISTRAT-2955 status` (`issue_key`), `ask jane@redhat.com` (`email`), `api.internal.example.corp outage` (`hostname`), `sk-abc123xyz` (`token`), an 8-word span copied from a fake Jira tool output (`overlap`), a query with U+202E (`control_chars`), a 6th query (`too_many`), `https://github.com/cli/cli` (`url`); `TestCheck_ReasonsNeverContainText`; `TestCheck_PercentEncodedEmailRejected`.
- [ ] Implement; `go test -race ./internal/runcheck/`. Commit.

### Task 3: Stage state in the store

**Files:** Modify `internal/profilerun/store.go` (`Meta.Run *RunState`), tests.

**Interfaces:**
- Produces: `type RunState struct { Stage string /* planning, checking, reading, writing, done, failed */; Children map[string]string /* stage -> child session id */; Accepted []string; Rejected []runcheck.Rejection; OutsideAttempted, OutsideSucceeded []string /* source names */; Links []string }` saved with the session so an adopted session after a restart reports its last stage.

- [ ] Failing tests: `TestStore_RunStateRoundTrip`, `TestStore_OldFilesWithoutRunLoad`. Implement; commit.

### Task 4: The run engine

**Files:** Create `internal/profilerun/run.go`, `run_test.go`.

**Interfaces:**
- Consumes: Tasks 1 to 3; `SandboxEngine` (planner, reader), the page engine (writer), the compiled grant and snapshot of the planner turn.
- Produces: `type RunEngine struct { Planner, Reader, Writer Engine; Compile func(profile string) (profile.Compiled, error); Store *Store }` implementing `Engine`. `Send` runs the stages in order, each a child session of the matching role profile, emits a `notice` LaneEvent per stage ("Looking in your workspace", "Searching the web: 3 queries", "Writing the answer"; when queries were rejected: "2 queries were not sent: issue_key, overlap"), forwards child tool calls with the stage as a prefix in their title, and returns the writer's text. `Cancel` cancels the active child and stops later stages.
- Prompts: planner gets a fixed instruction to return the JSON object and nothing else; reader gets the accepted queries as a list under an "untrusted search task" framing; writer gets four labeled blocks under the same untrusted-data delimiters the sandbox prompt uses (`profilerun/sandbox.go:445`), and an instruction to cite only URLs from the citations block.
- Outside flags: each planner tool call's tool name is resolved to its source through the compiled grant (server name) and the snapshot registry (`Source.Class()`); attempted on the call, succeeded when the result is not an error; the reader stage adds `web` to both.

- [ ] Failing tests with fake engines: `TestRun_StagesInOrderAndWriterGetsLabeledInputs`, `TestRun_ReaderSeesOnlyAcceptedQueries` (no notes, page text or question in its prompt), `TestRun_NoAcceptedQueriesSkipsReaderAndSaysSo`, `TestRun_PlannerBadJSONFailsWithCode`, `TestRun_PlannerFailureFailsWithCode`, `TestRun_CancelDuringReaderStopsWriter`, `TestRun_OutsideFlagsFromSnapshotNotNames` (a tool named like a gh tool but served by an internal source is internal), `TestRun_ImageRefused`.
- [ ] Implement; `go test -race ./internal/profilerun/`. Commit.

### Task 5: Citation link set

**Files:** Modify `internal/profilerun/run.go`, `internal/remoteapi` (session event or turn result carries `links`), `api/remote-v1.openapi.yaml`, tests.

**Interfaces:**
- Produces: the turn's final event carries `links: [url]`, the URLs the reader actually fetched (from its successful WebFetch tool calls), deduplicated, at most 20. Clients render only these as links in a `research-with-work` answer.

- [ ] Failing tests: `TestRun_LinksAreFetchedURLsOnly` (a URL the writer invents is absent), `TestAPI_FinalEventCarriesLinks`. Implement; docs (remote-access.mdx field); commit.

### Task 6: Wiring and Auto

**Files:** Modify `internal/remoteapp/profileserve.go` (register `engines["run"]`), `internal/autoroute/autoroute.go` (mode `both` -> `research-with-work`, with its description), `internal/decide` prompt and log, tests; RELEASE_NOTES, docs.

**Interfaces:**
- Produces: `research-with-work` listed available when the run engine is wired; Auto mode `both` described as "Needs the owner's private sources and the public web together: compare internal plans or notes with public releases, docs or news."; `both` decided only when both `workspace` and `web` are allowed for the client.

- [ ] Failing tests: `TestServedCatalog_ResearchWithWorkAvailable`, `TestRoute_BothNeedsWorkspaceAndWebAllowed`, `TestRoute_BothDescriptionInPrompt`. Implement; commit.

### Task 7: Wingman lane (wingman repo)

**Files:** `extension/src/route.ts` (mode `both` -> lane `both`, label "Auto: Both"), `extension/src/native.ts` (`Lane` gains `both`), aimux `internal/browserhost/relay.go` (`profileOfLane["both"] = "research-with-work"`, forward `links`), `extension/src/render.ts` (in a `both` answer, a link whose URL is not in `links` renders as plain text), `extension/src/panel.ts` (stage notices as progress lines), tests in both repos; README.

- [ ] Failing tests: relay `TestRelay_BothLaneUsesResearchWithWork`, `TestRelay_ForwardsLinks`; extension `routes both to the both lane`, `a link not in links renders inert`, `stage notices show as progress`.
- [ ] Implement; aimux gates and extension gates. Commit each repo.

### Task 8: Live acceptance (owner present)

- [ ] On a GitHub release page, Auto picks Both for "how does our OpenShell egress plan compare with this release"; stages show; the answer cites only fetched pages.
- [ ] A question naming a Jira key: the key never reaches the reader (check the reader child's transcript); the answer explains the rejected query by reason.
- [ ] A question needing no web: no queries accepted, private-only answer that says so.
- [ ] Cancel during reading: no writer turn in the transcripts.
- [ ] Record results in the ledger and RELEASE_NOTES "checked live".

## Done When

All task tests pass in both repos, the gates are green, Task 8 is recorded, and `research-with-work` no longer appears as unavailable.

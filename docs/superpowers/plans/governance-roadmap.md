# Governance programme: roadmap and status

The one place that says what is done, what is next, and what is waiting. The design is in
`../specs/2026-11-23-aimux-governance-metering-and-evals-design.md`; the five-minute version is
`../specs/aimux-design-one-page.html`. This page is updated in the same change as the work it describes.

<!-- no-images: a status table; the design documents carry the figures -->

## Where we are

The first slice is merged and live: the model proxy, the MCP proxy and the collector all run build 194, and
`aimux status` reads all ok. The tool gateway reports no build, so its build is not known from the table.

Next come three small pieces of work (below), then the ledger (slice 1), which is the next piece of real work.

## Slices

Each slice gets its own plan under this folder, written when the one before it is done.

| Slice | What it delivers | Depends on | State |
|---|---|---|---|
| 0a | Passthrough test: does the Claude login work through a local address | nothing | Host half passed. Sandbox half with the Wingman session |
| 0b | `aimux status`, one view of the four services | nothing | Done, live ([#130](https://github.com/zanetworker/aimux/pull/130)) |
| 0c | Ledger line and the rule that counts each action once | nothing | Done ([#129](https://github.com/zanetworker/aimux/pull/129)) |
| 1 | The ledger: the model proxy writes lines with ids and both models; a reader; a first meter | 0c | Next. No plan yet |
| 2 | Entry wrapper replacing the shell function | 1 | Not started |
| 3 | Reporting hook through the collector and its holding folder; then Codex | 1 | Not started |
| 4 | MCP proxy and tool gateway write lines; pins in warn mode; sandbox log reader | 1 | Not started |
| 5 | Full meter: Codex prices, measured or estimated or unpriced, allowances with age | 1, better with 3 and 4 | Not started |
| 6 | Traces produced from the ledger | 1 | Not started |
| 7 | Rules: the shared check, the per-project tool rule, the allowance cap | 1 | Not started |
| 8a | Anthropic passthrough engine and a sandbox entrance; switching model by hand in a session | 0a | Not started. Could be pulled forward to right after slice 1 |
| 8b | Engine policies: roles mapped to engines; choice at the start on allowance and capability | 8a | Not started |
| 8c | Fallback when a limit error comes back | 8a | Not started |
| 8d | Model catalog, `aimux models`, and "auto" by rules | 8b | Not started |
| 8e | Keeping both engines warm, so switching back costs only the missed turns | 8a | Not started. Rests on an untested Codex ability |
| 8f | The route function as auto's second tier, with keep-or-change feedback | 8d | Not started |

Slices 2, 3 and 4 touch different packages and can run at the same time once slice 1 has fixed the shared
shapes. So can 5, 6 and 7.

## Small work, in the order agreed

| Item | Why | State |
|---|---|---|
| Promote survives a cold start | The first request after a restart can time out, and a good build is then rejected (it happened once on 2026-11-24 and passed on retry). The proxy should start Codex when it starts, and the promote's last check should get one retry | Next |
| Services run from their own copy of the program | Three launchd jobs start `~/go/bin/aimux`, which any install overwrites: a crash would start an untested build, and the collector would not restart until its job was registered again | After the above |
| Flaky test `TestProxyStatus_NeverPrintsEnvValues` | It failed CI once on an unrelated change and passed on rerun | After the above |
| Requested and served model in the request history | A Claude model name is answered by the default Codex model; only the first is recorded | Folded into slice 1 |
| Codex web search as a per-listener setting | Wingman's web reader needs it | With slice 8a |
| The tool gateway reports its build | Its build column in `aimux status` is empty | Any time |
| `aimux status` advisory notes | A collector on a non-default port is not found; the model proxy's suggested fix exists only in a source checkout; a row's state is decided in the command package | Any time |
| One upgrade for all services | Mixed builds stay until someone restarts each service by hand | After services have their own copy |

## Paused or parked

| Item | Where it lives | Resumes when |
|---|---|---|
| Evals | Paused by the owner on 2026-11-24. The eval session's design is in the research wiki, `wiki/agentic-evals/my-eval-loop/aimux-eval-engine-design.md` | The turn id exists (after slice 1); the two documents are then reconciled |
| Policy from sessions | A session's profiler and a draft sandbox policy; a safety copy is in the research repo under `raw/session-to-policy-scratch/` | The owner says what it should become |
| Switching a session to another harness (`handoff`) | Designed in the spec, section 6 | Same-harness switching (8a to 8c) shows the need |

## Not yet verified, and where it gets tested

1. The Claude login through a local address from inside a sandbox (0a, Wingman session).
2. Whether an MCP request carries the harness's call id to the MCP proxy and the tool gateway (slice 1).
3. That Codex hooks receive a call id the model proxy also sees (slice 3).
4. The cost of the reporting hook per tool call (slice 3).
5. A transcript written under Codex, continued on Anthropic (8a).
6. Whether a held Codex conversation can be given turns another engine produced (8e).
7. That Anthropic's prompt cache still matches after turns spent on another engine (8e).
8. Quality of each model inside this harness, per role (8b onward).
9. That the fix in [#131](https://github.com/zanetworker/aimux/pull/131) removes the failures on new conversations: check `aimux model proxy stats` after a day of use.

## Waiting on the owner

1. Whether the hook may ever refuse a tool call. Slice 3 is report-only whatever the answer; decide after the visibility release.
2. Whether there is a source for Claude's remaining allowance. Without one the limit error is the only trigger.
3. What "session-to-policy" should become, and whether its work is saved to the research wiki.
4. Whether the Wingman writer's citation sentence should move so its prompt caches as well as the planner's.
5. Whether to pull slice 8a forward, to get switching by hand before the meter exists.

## Who builds what

- The design stays in one file. Other sessions send corrections to the session that owns it.
- This programme: the ledger, the shared base, `aimux status`, engines and routing.
- Wingman's model-backend work is folded in here. Files both would change are listed in the spec under "Who builds what".
- Evals, when resumed: the eval session.

## How a slice is run

Learned on the first slice, and worth repeating:

- A written plan first, with tests named.
- Well-specified tasks to Codex (Sol) or an Opus subagent. Sol through `codex exec` cannot open ports or write outside its folder, so tests that start a server are run outside it.
- The pre-push review stays in the loop. It caught every serious problem on the first slice, including a key sent to an unknown listener.
- The real command is run against the real services before a pull request, and a source-blind behaviour check for anything a user sees.
- Three numbers per task for the model catalog: correct on the first pass, time taken, and what the review changed.

## Done

| When | What |
|---|---|
| 2026-11-24 | [#129](https://github.com/zanetworker/aimux/pull/129) ledger line and counting rule |
| 2026-11-24 | [#130](https://github.com/zanetworker/aimux/pull/130) `aimux status`, keyless `/healthz` on the model proxy, `/health` on the collector |
| 2026-11-24 | [#131](https://github.com/zanetworker/aimux/pull/131) each lifecycle step gets its own clock; a timeout is recorded as `timeout` |
| 2026-11-24 | Design, first-slice plan and passthrough results committed; promoted to build 194 |

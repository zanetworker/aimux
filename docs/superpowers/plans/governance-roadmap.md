# aimux programme: goal, state and order of work

The one place that says where aimux is going, what is being built now, what is next, and what is waiting. Read
this first. It is updated in the same change as the work it describes.

- The shape and the experience: `../specs/2026-11-25-aimux-product-shape-draft.md`
- The proxies, the ledger, the meter, engines and rules: `../specs/2026-11-23-aimux-governance-metering-and-evals-design.md`
- The five-minute version of the second: `../specs/aimux-design-one-page.html`

<!-- no-images: a status page; the design documents carry the figures -->

## The goal

This section changes only by the owner's decision, and each change is a line in the decision log below.

aimux is one tool that is very easy to use:

```text
install      one file, then `aimux`: your sessions, no setup
turn on      aimux setup: inference proxy, MCP proxy, phone access, sandboxes, evals
work         plain `claude`, on any model, recorded
see          cost, trace and refusals in one view, whoever started the session
improve      the right model per task; your corrections become tests
upgrade      one command, the previous build put back when the new one does not answer
```

Three things hold on the way there:

1. **The daily setup keeps working.** The owner depends on the model proxy and the MCP proxy every day. Existing
   commands keep working, and a service changes build only through an upgrade that can put the previous one back.
2. **One record.** Every model call, tool call and refusal is one ledger line, and anything aimux could not see is
   labelled as not covered.
3. **One tool, one version.** The parts share a release. What is versioned separately is what they agree on:
   the ledger line, the health answer, the sessions API, shared files.

## Now

| | |
|---|---|
| In progress | Stage 1: the plan is written (`2026-11-25-stage-1-ledger.md`) and waits for the owner's review |
| Next | Stage 2: the entry wrapper, the parts menu, generated job files |
| Waiting on the owner | To promote the model proxy once the ledger change is merged; see "Waiting on the owner" |

## Stages

A stage is done when its "done when" line is true on the owner's machine, not when its code is merged.

| Stage | Delivers | Done when | Roadmap items | State |
|---|---|---|---|---|
| 0. Land what is waiting | Promote survives a cold start; `aimux upgrade`; the MCP proxy on its own copy; the two flaky tests | A promote has passed with the retry in place; `aimux upgrade` has moved the live model proxy, collector and MCP proxy, so `aimux status` shows no service running `~/go/bin/aimux` and a `go install` changes none; a broken build has been rolled back once on purpose; both flaky tests pass 20 runs in a row | Small work below | In progress: everything is built and the three services are moved. Two parts of its "done when" are not yet true: no promote has run since the retry was added, and a rollback has only been seen on a test job |
| 1. One record | Ledger files written by the model proxy; a reader; a first meter | `aimux cost` shows requests and tokens per session and model for a day of real use, and its totals match `aimux model proxy stats` | Slice 1 | In progress. Plan: `2026-11-25-stage-1-ledger.md`. Built: the line schema, the writer and the reader ([#138](https://github.com/zanetworker/aimux/pull/138)); what identifies a turn ([#139](https://github.com/zanetworker/aimux/pull/139)); the model proxy writing a line per model call, which waits for the owner to promote it. Left: `aimux cost`, and a day of real use |
| 2. One way in, and identity | The entry wrapper behind `claude-codex`; the registry of parts; the parts menu in `aimux setup`; `enable`, `disable`; job files generated from the registry | A background call carries its session in the ledger; the model proxy's job file is one aimux wrote | Slice 2, base items B1 and B3 | Not started |
| 3. A second engine, by choice | Anthropic passthrough and a sandbox entrance; switching model by hand in a session | A Claude session routed through the proxy runs a streamed tool call on Anthropic, then continues on Codex | Slice 8a | Not started. Waits on the sandbox half of 0a |
| 4. The right model per task | Roles mapped to engines; fallback on a limit error | A helper runs on another engine than its main conversation without being told to, and the ledger shows both | Slices 8b, 8c | Not started |
| 4, in parallel. Evals resume | In the eval session: contracts, detection in shadow, migration, the review queue | A correction made in a Codex session appears in the review queue | Eval phases 0 to 3; base item B5, built in this stage | Paused by the owner. Resumes after stage 1 only on his word |
| 5. See everything | Hooks report tool calls; the MCP proxy and tool gateway write lines; changed tools flagged on the host | One session's timeline shows its model calls, built-in tools and MCP tools, each with its source | Slices 3, 4 | Not started |
| 6. Numbers, and "auto" | Full meter; spans; rules; the model catalog fed by eval cases; "auto" | Cost with measured, estimated or unpriced on every surface; "auto" names the evidence for a choice | Slices 5, 6, 7, 8d to 8f; eval phases 4 to 6 | Not started |
| 7. Every session covered | The inference proxy in front of all Claude Code sessions | Plain `claude` is recorded, after the checklist in the product-shape draft has passed on the dev channel | New | Not started |
| 8. Later | Schedules and triggers; `aimux run`; backends beyond local | Decided when stage 6 is done | Base item B4; the `managed-sessions` design | Not started |

## How this page keeps the work on course

1. **Every change names its stage.** A pull request's description starts with the stage it serves. Work that
   serves no stage is either small work (below) or parked.
2. **A new idea goes to "Parked" first.** It moves into a stage only when it serves the current or the next one,
   or the owner moves it.
3. **When the order changes, three things change together:** the Stages table, one line in the decision log
   saying what moved and why, and a check that "The goal" still reads true. If the goal no longer reads true,
   the owner decides, not the session.
4. **"Now" always names one thing in progress and one thing next.**
5. **A stage closes with evidence.** Its "done when" line is checked on the real services, and what was checked
   is written under "Done".

## Small work

| Item | Why | Stage | State |
|---|---|---|---|
| Promote survives a cold start | The first request after a restart can time out, and a good build is then rejected | 0 | Done ([#133](https://github.com/zanetworker/aimux/pull/133)). Not yet seen in a real promote |
| `aimux upgrade`: services run from their own copy | Three launchd jobs start `~/go/bin/aimux`, which any install overwrites | 0 | Done for the model proxy and the collector, in the same change as this line. The live jobs move when the owner runs `aimux upgrade` |
| `aimux upgrade` probes the port its job passes | A job started with `--port` is probed on the configured port, so a healthy upgrade would be rolled back. Neither live job passes a port | 0 | Not started |
| A stale promote lock after a killed model proxy upgrade | The lock is a folder and outlives a killed process; the error names it so it can be removed. A lock that releases on exit needs the promote script to share it | 0 | Not started |
| A kill in the instant after `aimux upgrade` puts a leftover build back | The record of the interrupted swap is used up before the restart; a kill exactly there leaves the unchecked process running with nothing on disk to say so | Any | Not started |
| `aimux upgrade` and a previous build without a health answer | A rollback to a build older than `/healthz` restarts it but cannot confirm it, and says the rollback failed | Any | Not started |
| A request waiting for a slow Codex start cannot give up | The wait is on a mutex, so a caller's own deadline is not honoured while another caller starts Codex | Any | Not started |
| The MCP proxy runs from its own copy | `aimux mcp proxy install` pointed its job at the binary that ran it and would have moved it back. `aimux upgrade` now handles it, and an install keeps a moved job where it is | 0 | Done ([#136](https://github.com/zanetworker/aimux/pull/136)). The live job was moved on 2026-11-25 and runs build 213 from its own copy. It has no previous build beside it: the CLI it ran had already been replaced by the same build |
| Flaky test `TestProxyStatus_NeverPrintsEnvValues` | A real fault, not a test fault: an MCP server that exits so fast that the start itself fails lost its stderr, so its error did not say why. Reproduced under Linux (3 of 3 runs) and fixed in `internal/mcpproxy/stdio.go` | 0 | Done, in the same change as this line |
| Flaky test `TestClaude_SharesOneLiveCache` | Failed once in the full suite; it depended on a one-second cache surviving a loaded machine. An attempt that outlasts the window is now repeated | 0 | Done, in the same change as this line |
| Requested and served model in the request history | Only the first is recorded | 1 | Folded into slice 1 |
| Hooks that run another aimux than the one upgraded | If Claude Code's MCP hooks run an older aimux from another path, its installer points the MCP proxy's job back at itself. On this machine the hooks run the CLI on the PATH, so it does not arise; the guide says so | Any | Not started |
| Every merge makes every service look out of date | One version for the tool means `aimux status` flags a service after a merge that did not change it, and a plain `aimux upgrade` restarts it for nothing | 2 | Not started. Belongs with upgrading a service only when idle |
| The tool gateway reports its build | Its build column in `aimux status` is empty | Any | Not started |
| The tool gateway runs from its own copy | It runs a hand-kept copy under `~/.aimux/wingman/bin/` | 2 | With the Wingman session |
| Services follow an installed release by themselves | After `brew upgrade` nothing moves the services | 2 | Not started. For release builds only; the owner's services move when he says so |
| `aimux status` advisory notes | A collector on a non-default port is not found; a row's state is decided in the command package | Any | Not started |
| Codex web search as a per-listener setting | Wingman's web reader needs it | 3 | Not started |
| Show the inference proxy under that name | The command and package are `model proxy` | 2 | Decision 6 in the product-shape draft |

## The slices, for reference

The numbered slices are the governance spec's build order. Each gets its own plan under this folder, written
when the one before it is done. The Stages table above says when each is done.

| Slice | What it delivers | Depends on | State |
|---|---|---|---|
| 0a | Passthrough test: does the Claude login work through a local address | nothing | Host half passed. Sandbox half with the Wingman session |
| 0b | `aimux status` | nothing | Done, live ([#130](https://github.com/zanetworker/aimux/pull/130)) |
| 0c | Ledger line and the rule that counts each action once | nothing | Done ([#129](https://github.com/zanetworker/aimux/pull/129)) |
| 1 | The ledger: lines with ids and both models; a reader; a first meter | 0c | Next |
| 2 | Entry wrapper | 1 | Not started |
| 3 | Reporting hook through the collector; then Codex | 1 | Not started |
| 4 | MCP proxy and tool gateway write lines; pins in warn mode; sandbox log reader | 1 | Not started |
| 5 | Full meter | 1 | Not started |
| 6 | Traces produced from the ledger | 1 | Not started |
| 7 | Rules | 1 | Not started |
| 8a | Anthropic passthrough engine and a sandbox entrance; switching by hand | 0a | Not started |
| 8b | Engine policies: roles mapped to engines | 8a | Not started |
| 8c | Fallback when a limit error comes back | 8a | Not started |
| 8d | Model catalog, `aimux models`, "auto" by rules | 8b | Not started |
| 8e | Keeping both engines warm | 8a | Not started. Rests on an untested Codex ability |
| 8f | The route function as auto's second tier | 8d | Not started |

Base items from the product-shape draft: B1 the registry of parts, B2 the service contract (done by doing slices
3 and 4), B3 generated job files, B4 sessions with triggers, B5 one shared session reader.

## Decision log

| When | Decision | Why |
|---|---|---|
| 2026-11-24 | Evals paused | Until the turn id exists |
| 2026-11-25 | `aimux upgrade` replaces extending the promote script to every service | Promoting each piece by hand is a release gate, not how a tool is used |
| 2026-11-25 | Parts keep their real names: inference proxy, MCP proxy | What is on the screen matches the documentation and the logs |
| 2026-11-25 | Generated job files first; a daemon only when schedules or a second platform need one | No new code between the owner and his two daily proxies |
| 2026-11-25 | Routing per task and evals move ahead of the reporting hook; schedules and `aimux run` go last | The owner ranks them higher, and neither needs the hook |
| 2026-11-25 | The inference proxy in front of every session is a separate, late stage with a checklist | It would make the proxy a dependency of every session |
| 2026-11-25 | At most two pre-push review rounds per change, then it ships with the open findings listed, unless a P0 is open | `aimux upgrade` took six rounds; each found narrower cases and cost about eight minutes |
| 2026-11-25 | Pull requests stay small | The same change was 2,200 lines, which is what made each round slow |

## Paused or parked

| Item | Where it lives | Resumes when |
|---|---|---|
| Evals | The eval session's design, research wiki, `wiki/agentic-evals/my-eval-loop/aimux-eval-engine-design.md` | Stage 1 is done and the owner says so; the two documents are then reconciled |
| Policy from sessions | A profiler and a draft sandbox policy; a safety copy in the research repo under `raw/session-to-policy-scratch/` | The owner says what it should become |
| Switching a session to another harness (`handoff`) | Governance spec, section 6 | Same-harness switching (stages 3 and 4) shows the need |
| A supervising daemon | Product-shape draft, "The daemon, if it is built" | Schedules or a second platform make the need concrete |
| `aimux upgrade` fetching a release for a binary install | Product-shape draft, step 8 | Someone installs without Homebrew |

## Not yet verified, and where it gets tested

1. The Claude login through a local address from inside a sandbox (0a, Wingman session).
2. Whether an MCP request carries the harness's call id to the MCP proxy and the tool gateway (slice 4; not looked at in stage 1).
13. What the requests under a Claude model name are that Claude Code sent in one captured run (16 of 27) and in none of three later ones, and whether their metadata names their session. The ledger records the model asked for and the session on every line, so a day of real use will show them (stage 1, task 5).
14. That a hook counting prompts in the transcript reaches the model proxy's turn number (slice 3). The rule is in `../specs/2026-11-25-turn-identity.md`.
3. That Codex hooks receive a call id the model proxy also sees (slice 3).
4. The cost of the reporting hook per tool call (slice 3).
5. A transcript written under Codex, continued on Anthropic (8a).
6. Whether a held Codex conversation can be given turns another engine produced (8e).
7. That Anthropic's prompt cache still matches after turns spent on another engine (8e).
8. Quality of each model inside this harness, per role (stage 4 onward; eval cases are the source).
9. That the fix in [#131](https://github.com/zanetworker/aimux/pull/131) removes the failures on new conversations: check `aimux model proxy stats` after a day of use.
10. Why the first answer after a restart of the stable proxy took 28 to 50 seconds on 2026-11-24. Measured alone, Codex starts in 2 to 4 seconds and a first answer begins 3 seconds later. The step timings come with the ledger's model-call lines (stage 1).
11. A rollback by `aimux upgrade` on a live job. The upgrade itself worked on all three on 2026-11-25 (model proxy and collector 194 to 211 with build 194 kept as `aimux.prev`; MCP proxy 194 to 213), nothing left aside. A rollback has only been seen on a job of the test's own (`TestUpgradeLive_AgainstLaunchd`, build tag `launchdlive`).
12. Whether the collector answers within 20 seconds of a restart under launchd.

## Waiting on the owner

1. To promote the model proxy (`scripts/model-proxy-promote promote`, or `aimux upgrade model-proxy`) once the change that makes it write the ledger is merged. Until then nothing is recorded. It is also the first promote since the retry was added, which closes one open part of stage 0.
2. Whether evals resume right after stage 1 for the parts that need only the turn id.
3. Whether the hook may ever refuse a tool call. Slice 3 is report-only whatever the answer.
4. Whether there is a source for Claude's remaining allowance. Without one the limit error is the only trigger.
5. What "session-to-policy" should become.
6. Whether the Wingman writer's citation sentence should move so its prompt caches as well as the planner's.
7. The open decisions at the end of the product-shape draft.

## Who builds what

- The designs stay in their files. Other sessions send corrections to the session that owns them.
- This programme: the ledger, the shared base, `aimux status`, `aimux upgrade`, the parts, engines and routing.
- Wingman's model-backend work is folded in here. Files both would change are listed in the governance spec under "Who builds what".
- Evals, when resumed: the eval session.

## How a slice is run

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
| 2026-11-25 | [#132](https://github.com/zanetworker/aimux/pull/132) this page as the programme's tracker, and the product-shape draft |
| 2026-11-25 | [#133](https://github.com/zanetworker/aimux/pull/133) the proxy starts Codex when it starts; the promote's last check retries once |
| 2026-11-25 | [#134](https://github.com/zanetworker/aimux/pull/134), [#136](https://github.com/zanetworker/aimux/pull/136) `aimux upgrade`; the model proxy, the MCP proxy and the collector run from their own copies on the owner's machine |
| 2026-11-25 | [#135](https://github.com/zanetworker/aimux/pull/135) both flaky tests; one was a real fault in how a fast-exiting MCP server's stderr was read |

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
| In progress | Stage 3: a second engine, by choice (plan: `2026-11-25-stage-3-second-engine.md`). All six tasks are built and merged; everything is off until the proxy is started with `--anthropic` or `--messages-upstream`, and the stable proxy has neither. On spare ports, with real sessions: a tool call on Anthropic continued on Codex and back; a session on each engine from a shell set by `aimux model proxy env`; a tool call answered by a hosted model on the internal gateway. The proxy's own added time is 0.09 ms a request (`../specs/2026-11-25-second-engine-evidence.md`). The stage closes with the owner's checks below. Seen on 2026-11-26 in the owner's own interactive session on a test proxy: 39 calls on his Claude login recorded per session, then `/model` to Codex, a `switch anthropic -> codex` line and a Codex call that answered, with `aimux cost` showing both engines for the session. The switch back followed (`codex -> anthropic`, 18:40 UTC), and the same evening the owner upgraded the services to build 301, turned the Anthropic engine on for the stable proxy (`aimux engine list`: on in the file and running) and ran sessions through it on both engines, with a switch recorded there too. Stage 3 is done on the owner's machine |
| Also open | Stage 1 is built and recording; it closes after a day of real use (task 5 of `2026-11-25-stage-1-ledger.md`) |
| Done | Stage 2, on 2026-11-26: see its row below. Built after its close, from the owner's first real runs: `aimux tools review` (setup only reports tools that need review), `aimux model proxy route` (the signal a status line reads) and the owner's HUD reading it, `aimux model proxy doctor`, the status a failed call was given in the ledger line, Codex in the `/model` picker of a proxied shell, the engines a starting proxy answers with, and calls passed on to other engines in `aimux model proxy status` |
| Next | Stage 4, the right model per task: its plan is to be written for the owner's review. After it, agreed with the owner on 2026-11-26: profiles in `aimux spawn`, overlays on a profile (his session-to-policy analysis of 3,042 sessions: 75% need only a base policy, 11% need two or more add-ons), and the proxy inside sandboxed sessions. No separate launcher is built: sessions go through the proxy by one line in the shell's start file, which every way he starts Claude Code inherits |

## Stages

A stage is done when its "done when" line is true on the owner's machine, not when its code is merged.

| Stage | Delivers | Done when | Roadmap items | State |
|---|---|---|---|---|
| 0. Land what is waiting | Promote survives a cold start; `aimux upgrade`; the MCP proxy on its own copy; the two flaky tests | A promote has passed with the retry in place; `aimux upgrade` has moved the live model proxy, collector and MCP proxy, so `aimux status` shows no service running `~/go/bin/aimux` and a `go install` changes none; a broken build has been rolled back once on purpose; both flaky tests pass 20 runs in a row | Small work below | In progress: everything is built and the three services are moved. Two parts of its "done when" are not yet true: no promote has run since the retry was added, and a rollback has only been seen on a test job |
| 1. One record | Ledger files written by the model proxy; a reader; a first meter | `aimux cost` shows requests and tokens per session and model for a day of real use, and its totals match `aimux model proxy stats` | Slice 1 | In progress. Everything is built: the schema, writer and reader ([#138](https://github.com/zanetworker/aimux/pull/138)), what identifies a turn ([#139](https://github.com/zanetworker/aimux/pull/139)), the model proxy writing a line per call ([#140](https://github.com/zanetworker/aimux/pull/140)), and `aimux cost`. The model proxy was moved to build 228 on 2026-11-25 and writes the ledger. Left: a day of real use, then the count is compared with the request history's |
| 2. One way in, and identity | The entry wrapper behind `claude-codex`; the registry of parts; the parts menu in `aimux setup`; `enable`, `disable`; job files generated from the registry | A background call carries its session in the ledger; the model proxy's job file is one aimux wrote | Slice 2, base items B1 and B3 | Done on the owner's machine, 2026-11-26. Both conditions hold: all 56 background and subagent calls the stable proxy recorded that day carry their session (and all 190 main ones), and the model proxy and the MCP proxy are under management, adopted through the menu in `aimux setup` with no restart, `aimux setup --check` saying "Nothing to change". One thing the owner found on that run is still to build: starting a session through the proxy takes an `eval` line nobody would guess; a launcher (`aimux claude`) is proposed and waits on his word for the name. Plan: `2026-11-25-stage-2-one-way-in.md`; settings shape: `../specs/2026-11-25-parts-settings.md` |
| 3. A second engine, by choice | Anthropic passthrough and a sandbox entrance; switching model by hand in a session | A Claude session routed through the proxy runs a streamed tool call on Anthropic, then continues on Codex | Slice 8a | Done on the owner's machine, 2026-11-26: a Claude session through the proxy ran tool calls on Anthropic, continued on Codex and came back, in his own interactive session, and the stable proxy answers with both engines. Not built: the sandbox entrance, which moves to the profiles work after stage 4. Plan: `2026-11-25-stage-3-second-engine.md` |
| 4. The right model per task | Roles mapped to engines; fallback on a limit error | A helper runs on another engine than its main conversation without being told to, and the ledger shows both | Slices 8b, 8c | In progress, built but for one piece. The owner approved the plan on 2026-11-26 (`2026-11-26-stage-4-right-model-per-task.md`). Done and merged: task 0, evidence; task 1, every model call has a role (`aimux cost --by role`; on the owner's ledger, 331 main and 85 subagent calls in a day); task 2, `models`, `roles` and `on_limit` in the settings, with `aimux engine model` and `aimux engine role`, and what each of his subagent definitions runs on in `aimux engine list`; task 3, the subagent role reaches a session through its shell; the first half of task 4, the plan's allowance read from each answer and shown (`aimux model proxy status`: 38% of the week used on 2026-11-26), and a limit named `short` or `plan` on the ledger line. Left for the driver: the `on_limit: report` line. Left for the owner: `aimux upgrade`, since his services run build 301 and none of this is in the running proxy until then; then set a role and see a subagent on Codex |
| 4, in parallel. Evals resume | In the eval session: contracts, detection in shadow, migration, the review queue | A correction made in a Codex session appears in the review queue | Eval phases 0 to 3; base item B5, built in this stage | Paused by the owner. Resumes after stage 1 only on his word |
| 4b. A limit continues on another engine | The fallback the stage 4 report prepares: decided before the first byte, only on a fresh turn, lasting the whole turn | A session that meets Claude's plan limit carries on on Codex and says so | Slice 8c | Not planned yet: waits on a captured plan limit and the owner's decision on who may be sent to a second provider |
| 4c. A first page outside the terminal | One page in the web dashboard: the services and their health, the proxy's diagnosis, engines and roles, cost per session, engine and role; the TUI and dashboard cost views read the ledger's meter | The owner answers "is anything broken, what ran where" from a browser, and the three frontends agree on cost | New, approved by the owner on 2026-11-26 | In progress. Done: the dashboard's System tab ([#183](https://github.com/zanetworker/aimux/pull/183), [#184](https://github.com/zanetworker/aimux/pull/184)) and the TUI's System view on `M` ([#186](https://github.com/zanetworker/aimux/pull/186)): services, the proxy's diagnosis, engines and roles, model calls by role, model, session and day, read from the same functions as the commands. Left: the dashboard header and the TUI cost view still count from transcripts; they move to the meter with prices |
| 4d. Profiles | `aimux spawn --profile`; overlays on a profile; the proxy inside sandboxed sessions | A host session and a sandboxed one start from the same command and both are recorded | The owner's session-to-policy analysis; the sandbox entrance left over from stage 3 | Not started. Touches the Wingman session's side and is coordinated with it |
| 5. See everything | Hooks report tool calls; the MCP proxy and tool gateway write lines; changed tools flagged on the host | One session's timeline shows its model calls, built-in tools and MCP tools, each with its source | Slices 3, 4 | Not started |
| 5b. See it in one place | An admin view in the dashboard over the ledger: cost and calls per engine, model and session; the distribution of tools; a session's timeline; the health of each service | The owner answers "what ran where, what did it cost, and is anything broken" from one page, with no command | New, asked for by the owner on 2026-11-26 | Not started. Comes after stage 5, since only then does the ledger hold tool calls as well as model calls; the owner is content for it to come late |
| 5c. Change it from where you see it | The System view (TUI) and the System tab (dashboard) can change what they show, interactively: switch an engine or a part, give a role a model, upgrade or restart a service, run the fix a finding names. Each goes through the functions the commands use (`parts.EnableEngine`, `SetRole`, `PlanParts`/`ApplyParts`, `UpgradeServices`), shows the plan first and asks before a restart. The views also show what lives in Claude Code's own settings and bears on a session: the hooks aimux installed there (`internal/claudehooks`), and how compaction is set (on or off, the auto-compact window, what runs before it), read only unless aimux owns the entry | The owner configures aimux from aimux, without leaving for a terminal and remembering a command | Asked for by the owner on 2026-11-27, for later | Not started. Today both views read only and name the command to run. `parts.EditCandidates` already lists what each change would accept, which is what a picker needs |
| 6. Numbers, and "auto" | Full meter; spans; rules; the model catalog fed by eval cases; "auto" | Cost with measured, estimated or unpriced on every surface; "auto" names the evidence for a choice | Slices 5, 6, 7, 8d to 8f; eval phases 4 to 6 | Not started |
| 7. Every session covered | The inference proxy in front of all Claude Code sessions | Plain `claude` is recorded, after the checklist in the product-shape draft has passed on the dev channel | New | Begun on the owner's machine, 2026-11-26: one line at the end of his `~/.zshrc` runs `aimux model proxy env` in every new shell, so every way he starts Claude Code goes through the proxy, and a shell opened while the proxy is down goes direct. Not covered: scheduled jobs, sandboxed sessions, the Codex CLI. The checklist in the product-shape draft has not been walked |
| 8. Later | Schedules and triggers; `aimux run`; backends beyond local; the Codex CLI as a harness in front of the proxy (the Responses API served beside the Messages API), last, on the same architecture | Decided when stage 6 is done | Base item B4; the `managed-sessions` design | Not started |

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
| A request in flight at shutdown loses its ledger line | The proxy closes its ledger before its handlers have finished, as it does its request history | 1 | Not started |
| A resent request after a proxy restart counts as a new turn | The fingerprint that recognises it is kept in memory only | Any | Not started |
| The TUI cost view and the dashboard read the meter | They count from transcripts with their own arithmetic; `aimux cost` reads the ledger | 5 | Not started |
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
| 2026-11-25 | Stage 3 (a second engine) is built before stage 2 (entry wrapper, parts menu) | The owner approved it: the two do not depend on each other, and the second engine changes what he can do each day |
| 2026-11-25 | A session going to Anthropic presents the proxy key as a path prefix in its base URL, not as a custom header | The prefix arrived on every request of six real sessions; the header was missing from `HEAD /api/hello` |
| 2026-11-25 | The merged model list (`GET /v1/models` answered with both engines' models) waits for the interactive check in stage 3's task 5 | No real session asked for the list in any captured run. With a login the request goes to Anthropic unchanged; if `/model` turns out to read the list from the proxy, it is built then |
| 2026-11-25 | The `parts:` section: engines are map keys named for where a request goes; `type` is the backend's protocol and `auth` whose credential is sent; the front (`serves`) is separate from the back (`engines`); `enable` and `disable` switch, `add` and `remove` are for a named upstream | The owner's review of the first shape, which copied the command line's flags. Named things are already map keys in aimux's other files |
| 2026-11-25 | aimux changes only what it owns in a job file (the program and the arguments from the settings), and refuses a file it cannot split | Rewriting the file would drop what was set by hand, the service's PATH among it |
| 2026-11-25 | `aimux model proxy env` sets nothing about telemetry, though the stage 2 plan had it print the collector's variables | Those variables also turn on logging of prompts to the collector; that should be a choice in the owner's shell, where his is already made, not a side effect of pointing a shell at the proxy |
| 2026-11-25 | A command asked to apply exits 1 when the file changed and the machine did not, and says which | A script must not read "done" for a switch that was only written down |
| 2026-11-26 | aimux emits the signal of where a shell's sessions go and whether its proxy answers (`aimux model proxy route`); a status line only reads it | The owner's HUD is his own tooling and other people have theirs. A rule kept in one HUD would be wrong in every other |
| 2026-11-26 | An admin view over the ledger is its own stage, 5b | The owner asked for it: what aimux records should be visible in aimux, not only through commands |
| 2026-11-26 | No separate launcher: sessions go through the proxy by a line in the shell's start file | The owner starts Claude Code five ways (`claude`, `clauded`, `claudeb`, `cf`, `claude-ws`). A variable every one of them inherits covers all five; a launcher would have been a sixth way, and a second command beside `aimux spawn` |
| 2026-11-26 | The Codex CLI in front of the proxy comes last, and reuses what is built | The owner's decision. Until then, anything that decides an engine or a role, or writes the ledger, takes neutral inputs and not a Messages request, so that front supplies its own reading of a request and nothing else |
| 2026-11-26 | After stage 4: profiles in `aimux spawn`, overlays on a profile, the proxy inside sandboxed sessions | The owner's order. His analysis of 3,042 sessions: 75% need only a base policy, 11% need two or more add-ons, which one `extends` chain cannot say |
| 2026-11-26 | A Codex turn refused for lack of room is told to the session as the Messages API's "prompt is too long" | The owner's session was stuck: the proxy answered a server error, which Claude Code sent again ten times. With the API's own answer it made one request and recognised it, in a test with the real client |
| 2026-11-26 | The commands for a role follow the vocabulary rule: `update` and `delete`, not `set` and `clear` | The plan named them before the rule was checked. No new exemption was made |
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
13. What the requests under a Claude model name are that Claude Code sent in one captured run (16 of 27) and in none of three later ones, and whether their metadata names their session. The ledger records the model asked for and the session on every line, so a day of real use will show them (stage 1, task 5). A likely source, seen on 2026-11-25 and not confirmed as the cause: a child session started when a session ends inherits the proxy address and asks for its own default Claude model (`../specs/2026-11-25-second-engine-evidence.md`).
16. Why a prompt that writes to the cache took about 0.3 s longer through the proxy than direct in six real turns each way, when no step in the proxy accounts for it and fully cached prompts showed no gap. Twenty turns each way would settle whether it is noise (stage 3, task 5).
17. What starts a `claude-sonnet-5-5` session of about 66,000 input tokens each time a session ends on the owner's machine, seen in every captured run. The ledger shows it once sessions go through the proxy; it is the owner's to look at.
15. What an interactive session does that a non-interactive one did not show: whether `/model codex:<name>` is accepted, whether a model change keeps the path prefix and the session id, and whether renewing the login or anything else calls a path besides `HEAD /api/hello` and `POST /v1/messages` (stage 3, task 5, the owner).
14. That a hook counting prompts in the transcript reaches the model proxy's turn number (slice 3). The rule is in `../specs/2026-11-25-turn-identity.md`.
3. That Codex hooks receive a call id the model proxy also sees (slice 3).
4. The cost of the reporting hook per tool call (slice 3).
5. A transcript written under Codex, continued on Anthropic (8a). Seen once on 2026-11-25, on a spare port: a session's third turn ran on Anthropic after a Codex turn and recalled a word from the first. Not yet seen: a long session, or one with tool calls made under Codex.
6. Whether a held Codex conversation can be given turns another engine produced (8e).
7. That Anthropic's prompt cache still matches after turns spent on another engine (8e).
8. Quality of each model inside this harness, per role (stage 4 onward; eval cases are the source).
9. That the fix in [#131](https://github.com/zanetworker/aimux/pull/131) removes the failures on new conversations: check `aimux model proxy stats` after a day of use.
10. Why the first answer after a restart of the stable proxy took 28 to 50 seconds on 2026-11-24. Measured alone, Codex starts in 2 to 4 seconds and a first answer begins 3 seconds later. The step timings come with the ledger's model-call lines (stage 1).
11. A rollback by `aimux upgrade` on a live job. The upgrade itself worked on all three on 2026-11-25 (model proxy and collector 194 to 211 with build 194 kept as `aimux.prev`; MCP proxy 194 to 213), nothing left aside. A rollback has only been seen on a job of the test's own (`TestUpgradeLive_AgainstLaunchd`, build tag `launchdlive`).
12. Whether the collector answers within 20 seconds of a restart under launchd.

## Waiting on the owner

1. Stage 3's close (task 5 of its plan), in this order:
   - Start a dev proxy with both engines (`aimux model proxy serve --port 7386 --anthropic`), and in another shell run `lines="$(aimux model proxy env --port 7386)" && eval "$lines"`, then `claude`.
   - In that session: a prompt with a tool call on a Claude model; `/model` to a `codex:` model and a prompt that refers to the first; `/model` back and a prompt that refers to both. Expected: three answers that recall what came before, `aimux cost --session <id>` showing both engines, two `switch` lines in the ledger.
   - Stop the dev proxy and send a prompt, to see what a dead proxy looks like; `eval "$(aimux model proxy env --off)"` returns the shell to going direct.
   - Either accept the latency figures in the evidence note or ask for the plan's twenty-turn measure.
   - Then add `--anthropic` to the stable proxy's job and run `aimux upgrade model-proxy`.
2. Whether evals resume right after stage 1 for the parts that need only the turn id.
3. Whether the hook may ever refuse a tool call. Slice 3 is report-only whatever the answer.
4. Answered on 2026-11-26, no longer waiting: there is a source for Claude's remaining allowance. Every answer from Anthropic on the owner's login carries it in its headers (`../specs/2026-11-26-roles-and-limits-evidence.md`), so a choice at the start of a session on the allowance left needs no extra call. Stage 4 reads and shows it.
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

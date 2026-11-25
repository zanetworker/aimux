# aimux: one tool, a clean base

Status: draft for discussion. Nothing here is built unless it says so. It does not replace
`2026-11-23-aimux-governance-metering-and-evals-design.md` (the doors, the ledger, the meter, engines) or the
backend architecture spec on the `refactor/managed-sessions` branch (sessions, agents and sandboxes over named
backends). It says how those two fit into one product, what the user sees, and what the base under both should be.

<!-- no-images: text diagrams only in this draft; rendered figures come when the shape is agreed -->

## The problem

aimux grew one useful piece at a time: a session dashboard, a phone API, a model proxy, an MCP proxy, a tool
gateway, a collector, sandboxed profiles. Each piece works. Together they are hard to use, for four reasons.

1. **The commands show the plumbing.** A user meets `aimux model proxy serve`, `aimux mcp proxy gateway install`
   and `aimux collect`, which are background processes, when what they wanted was "Claude Code on another model".
2. **Every background process is its own launchd job**, installed its own way. Two have installer commands, two
   have job files written by hand, one runs a separate copy of the program. Nothing owns them as a set.
3. **launchd is one operating system's supervisor.** The same pieces should run on Linux, in a container and on a
   cluster, and none of those has launchd.
4. **Agent jobs that run without a person** (a scheduled `claude -p`, a Wingman stage, a phone message continuing
   a session) are each built separately, and scheduled ones live outside aimux entirely, as hand-written launchd
   jobs with their own failure modes.

## The shape in one picture

```text
SURFACES     terminal UI · dashboard · phone · CLI · browser panel · aimux's own MCP server
                                        |
CONTROL      one registry: turns parts on and off, keeps the services alive, starts sessions
             on a schedule or a trigger, upgrades everything, answers "what is running"
                                        |
             +--------------------------+---------------------------+
             |                                                      |
SESSIONS     the only unit of work.                     DOORS       what a session's traffic passes.
             A conversation in a harness,                           inference proxy: engines behind
             started by a person or a trigger,                      MCP proxy: MCP servers behind
             running on a backend:                                  hooks: built-in tool calls
             local · sandbox · gateway · cluster                    |
             |                                                      |
             +--------------------------+---------------------------+
                                        |
RECORD       the ledger: one line per model call, tool call, verdict and switch.
             Read by the meter, traces, rules and evals.
```

Five words carry the whole design:

| Word | Is | Already designed in |
|---|---|---|
| Part | Something a user turns on, under its real name: the inference proxy, the MCP proxy, phone access, sandboxes, schedules, evals. A part may need services, schedules or wiring, or none | New here |
| Service | A background process a part needs | Governance spec, section 4 |
| Session | One conversation in a harness, wherever it runs | Backend spec (`Sessions`), governance spec (identity) |
| Door | A proxy that session traffic passes and that writes the record | Governance spec, section 3 |
| Ledger | The one record | Governance spec, sections 1 and 2 |

## The experience, end to end

Output below is illustrative. The target assumes the Anthropic passthrough engine exists (slice 8a), so the
inference proxy can sit in front of every session, the way the MCP proxy already does.

### 1. Install

```text
$ brew install zanetworker/aimux/aimux        # or
$ curl -L <release url> -o ~/bin/aimux && chmod +x ~/bin/aimux
$ aimux
```

Either way is the whole install. aimux is one file and needs nothing from the package manager afterwards. The
dashboard opens and lists the Claude Code and Codex sessions already running. No setup, no background process.

### 2. Turn on what you want

The parts keep their real names, so what is on the screen is what the documentation and the logs call it.

```text
$ aimux setup

  Sessions dashboard   on    always
  Inference proxy      off   in front of every session: records each model call, and lets a harness
                             run on another engine (Codex, another model server)
  MCP proxy            off   one shared copy of each MCP server, started on demand, tools reviewed
  Phone access         off   your sessions from your phone, through Tailscale
  Sandboxes            off   profiles that run in an OpenShell sandbox
  Schedules            off   agents that run on a schedule
  Evals                off   finds your corrections after each session, proposes fixes, re-tests weekly

  Turn on: 2 3
  Inference proxy ... on   every claude session now goes through it. Pick a model with /model
  MCP proxy ......... on   5 servers moved; 1 kept direct (needs a browser login)
```

Each part is wired once. `aimux enable inference-proxy` and `aimux disable mcp-proxy` do the same without the
menu. "Inference", not "models": what sits behind it is an engine, which can be a model endpoint or another
harness.

### 3. Work as you do today

```text
$ claude
$ claude --model codex:gpt-6.2-sol
```

Plain `claude`. Its model calls pass the inference proxy and its MCP calls pass the MCP proxy, so the session is
recorded. `/model` inside the session lists Claude's models and the other engines' models together; choosing one
changes where the next request goes.

A plain session gets every tool of every server. Tools whose definition changed since they were approved are
flagged (slice 4), and a project can narrow what its sessions may use (slice 7).

### 4. See what happened

```text
$ aimux cost --today
SESSION                 STARTED BY   MODELS          CALLS   COST
fix login redirect      you          claude          212     $3.10
news                    schedule     codex           18      $0.31   estimated
research: vLLM routing  wingman      claude, codex   77      $1.92   2 tool calls not covered

$ aimux trace <session>            # model calls, tool calls and verdicts of one session, in order
$ aimux trace <session> --refused  # only what a sandbox or a rule refused
```

One record, read three ways:

| Question | View | Comes from |
|---|---|---|
| What did it cost | Cost | model-call lines, priced by the meter |
| What did it do | Trace | model-call and tool-call lines, in time order |
| What was it not allowed to do | Verdicts | lines written by a proxy that refused, and by the collector from a sandbox's OCSF log |

What aimux could not see is labelled "not covered", never shown as zero.

### 5. From the phone

Used as today. A session gains its model, its cost and who started it.

### 6. Agents, sandboxes and other machines

`aimux run` is for what Claude Code has no word for:

```text
$ aimux run reviewer                 # an agent definition: prompt, profile, tool sets, default model
$ aimux run research                 # a profile that runs in a sandbox
$ aimux run reviewer --on my-server  # the same definition, on another backend
```

A session started this way gets only the reviewed tool sets its definition names (`jira:readonly`,
`gh:readonly`), and only tools whose definition is still the approved one.

### 7. Work that runs without you

```text
$ aimux schedule add news "every day 07:00" --agent news-digest
$ aimux schedule list
NAME   WHEN             AGENT         LAST RUN         NEXT
news   every day 07:00  news-digest   ok, 4m, $0.31    tomorrow 07:00
```

A scheduled job is a session with a trigger instead of a person. It is in the same list and the same cost and
trace views. It has a time limit, a failure is reported, and a run that hangs does not stop the next one.

### 8. Upgrade

```text
$ aimux upgrade
```

One command whatever the install:

| Installed with | `aimux upgrade` does |
|---|---|
| The binary | Downloads the latest release, checks it against the published checksum, replaces the program, then moves the services |
| Homebrew | Says to run `brew upgrade` for the program, then moves the services. After a `brew upgrade` the services also follow by themselves |

"Moves the services" is what exists today in the open change: copy the build into each service's folder,
restart it, wait for it on the new build, and put the previous one back if it does not answer. `aimux version`
prints one version.

Three things it does not do yet and needs before it runs unasked: show what it will change first; test the new
build with one real model turn and one harmless MCP call, since a process that listens can still fail its first
request; and wait for streams in flight. Following a release automatically is for people who installed a release.
The owner's own services move only when he says so, which is what the promote is for.

### 9. When something is wrong

```text
$ aimux status
aimux 0.15.0    4 parts on, 1 needs attention

PART              STATE     DETAIL
Inference proxy   ok        anthropic, codex · last call 4s ago
MCP proxy         degraded  1 of 5 servers failed (slack)
Phone access      ok        2 devices
Schedules         ok        1 schedule, next 07:00

NEEDS ATTENTION                       FIX
MCP proxy: slack failed to start      aimux servers check slack
```

The parts summary sits above today's table, which stays available in full: its four service rows, builds and
exit codes are what scripts rely on, and a healthy host proxy must not hide a failed sandbox gateway behind one
"ok".

If the inference proxy is down, a session stops with that fix in its error. It is never sent direct without the
owner choosing so, because that changes what is recorded and what is billed.

### 10. Turn something off

```text
$ aimux disable inference-proxy
Inference proxy ... off   claude talks to Anthropic directly again; its settings were restored from the backup
```

Every part undoes exactly what it did, as `aimux mcp proxy uninstall` does today.

## How "see" surfaces in each interface

The three views in step 4 are the same three controller functions everywhere. Only the drawing differs.

**Terminal UI.** The session list gains two columns, and the existing split view gains two tabs.

```text
aimux   Inference proxy ok · MCP proxy 1 failed · Phone ok             [s]ervices [c]ost [S]chedules
  SESSION                   MODEL    STATE       COST    STARTED BY
> fix login redirect        claude   working     $3.10   you
  research: vLLM routing    codex    needs you   $1.92   wingman
  scratch                   -        idle        -       you          not covered

 fix login redirect                              [t]race  [c]ost  [v]erdicts
  14:02:11  model   claude-opus     in 41k  out 1.2k  first byte 1.9s   $0.21
  14:02:14  tool    Read            internal/auth/redirect.go            hook
  14:02:15  tool    jira_get_issue  jira                                 mcp proxy
  14:02:31  model   codex:sol       in 43k  out 300   first byte 3.1s   estimated
  14:02:33  refuse  network         api.example.com not in policy        sandbox
```

- Trace is today's trace pane with its transcript detail kept, and ledger lines joined to it by id. The ledger
  holds no arguments, paths or text, so a row's detail (the file a tool read, the text of a refusal) is fetched
  from the transcript or the sandbox log when the row is opened, by whoever is allowed to read those. Each row
  says where it came from (hook, MCP proxy, tool gateway, sandbox).
- A stretch of time with no evidence is shown as unknown. A count such as "2 tool calls not covered" is shown
  only when a second source saw them, for example the transcript; otherwise the session is marked not covered
  without a number.
- Verdicts is the same pane filtered to refusals. Opening a sandbox row shows the OCSF record it came from.
- Cost is today's cost view, reading the meter.
- Services is `aimux status` as a view, with restart and upgrade on a key.

**Dashboard.** The same three as tabs on a session page, and cost as its own page with grouping by day, project,
model and who started the session. Raw OCSF records are a download, not a page.

**Phone.** A session's info sheet gains cost, models used and counts: model calls, tool calls, refusals. A
refusal that stopped the session is an alert. No raw trace on the phone.

**Wingman's browser panel.** A finished question shows its cost and which stages ran on which model, read from
the same record by run id.

## What changes in each surface

The rule from the project guide holds: each new thing is one controller function, and every surface calls it.
Four functions carry everything below: parts, service status (exists), the meter, schedules.

| Surface | Stays | Gains | Loses |
|---|---|---|---|
| CLI | Every command that works today | `setup` as a menu of parts, `enable`, `disable`, `run`, `schedule`, `cost`, `models`; `status` by capability | Nothing. `model proxy serve`, `mcp proxy serve`, `collect` are hidden from help and kept for the background job and for debugging |
| Terminal UI | Agent list, preview, embedded session, trace view | A model column and a "not covered" mark on each row; a model and backend choice in the launcher; a services line in the header; a schedules view; scheduled sessions in the list with their trigger | Its own cost arithmetic: the cost view reads the meter, so it shows the same numbers as the CLI and adds measured, estimated or unpriced |
| Dashboard | Sessions, history, traces, insights | A setup page with the switches for each part (the natural home for them); a schedules page; cost by model and by who started the session; a services strip | The same duplicate cost arithmetic |
| Phone | Now, All, chat, queue, alerts | Model, cost and "started by" on a session; alerts for scheduled sessions; a new session can choose an agent and a model | Nothing. No setup on the phone: turning parts on stays on the machine |
| Browser panel (Wingman) | As it is | Its runs appear in cost and trace like any other session | Nothing |

In the terminal UI the change is mostly two columns and one header line:

```text
aimux   Models ok · MCP servers 1 failed · Phone ok                       [s]ervices [c]ost [S]chedules
  SESSION                   MODEL    STATE       COST    STARTED BY
> fix login redirect        claude   working     $3.10   you
  research: vLLM routing    codex    needs you   $1.92   wingman
  news                      codex    idle        $0.31   schedule 07:00
  scratch                   -        idle        -       you            not covered
```

The remote API changes by addition only: three fields on a session (`model`, `cost`, `startedBy`) and one
resource (`schedules`). Clients that do not know them keep working.

What the trace view shows gets wider without changing: today it parses each harness's transcript; the same view
reads ledger lines once they exist, so tool calls from hooks, model calls from the inference proxy and sandbox
verdicts appear in one timeline.

## Architecture

### Who keeps the services alive: generated job files first, a daemon only if needed

Recommended first step: keep the operating system as the supervisor, and generate each service's job file from
the registry, for launchd now and systemd when Linux has a user. That removes the hand-written and
differently-installed jobs without putting new code between the owner and the two proxies he depends on. Upgrading
a service when it is idle and one local status answer both work this way too.

A single daemon is the later option, described below. It buys one registration with the operating system, a
portable scheduler and one control endpoint. It costs owning restart back-off, recovery of child processes,
prevention of duplicates and its own upgrade. It is reconsidered when schedules or a second platform make the
need concrete, and only with a test showing that restarting the daemon keeps a model stream and an MCP
connection alive.

### The daemon, if it is built

`aimux daemon` is the only thing registered with the operating system: one launchd job on a Mac, one systemd unit
on Linux, the entrypoint of a container on a cluster. It does five things.

| Job | Replaces |
|---|---|
| Starts and watches the services, each still its own process | Five launchd jobs, installed five ways |
| Turns parts on and off | The per-service install commands |
| Upgrades services, each when idle, with the previous build kept | `aimux upgrade` over launchd, and the question of who follows an install |
| Starts sessions on a schedule or a trigger | Hand-written launchd jobs running `claude -p` |
| Answers one local API: status, parts, sessions, schedules | Each service's own status command as the first place to look |

The services stay separate processes. That keeps what the governance spec wanted from separation: restarting one
does not drop the others' connections, and one crashing does not take the rest down. What changes is who watches
them.

This is the answer to "is launchd the right approach". launchd is fine as the thing that keeps one process alive.
It is the wrong place to describe aimux's parts, because it exists on one operating system and knows nothing about
idleness, builds or parts.

The cost is real: a supervisor is code aimux then owns (restart with back-off, logs, a crash of the daemon itself).
It is kept small on purpose. It does not proxy traffic, so a daemon restart does not interrupt a session.

### Three kinds of background work, and what runs each

| Kind | Examples | Lives | Run by |
|---|---|---|---|
| A service | model proxy, MCP proxy, tool gateway, collector, phone API | as long as its part is on | the operating system's supervisor, from a generated job file |
| A session without a person | a scheduled job, a Wingman stage, a phone message continuing a session, an eval replay | from its trigger to its last turn | a backend, asked by the daemon |
| Housekeeping | naming sessions, refreshing search, reading sandbox logs | a few seconds, repeatedly | inside the service that needs it |

### Do we need a task API? No: sessions, with triggers

Every platform the backend spec compared (OpenClaw gateway, OpenAI's agents API, Claude's managed agents) uses the
same unit: an agent is a template, a session is a running instance. aimux already lists, names, searches, meters
and continues sessions. A second unit called "task" would have to be joined to sessions everywhere.

So:

- **Session**: the unit. Created, sent a prompt, followed, stopped. This is the `Sessions` interface from the
  backend spec, and its wire form is the published remote API (`/v1/sessions`).
- **Trigger**: what creates or continues a session when no person does. A time, a message from the phone, a queue
  item, later a webhook.
- **Run**: an id that groups the sessions of one piece of work, as the ledger already defines for a Wingman
  question. A scheduled job's run has one session; a Wingman run has several.

A trigger firing is recorded before anything is started: one line per occurrence with an idempotency key, its
outcome, and the sessions and turns it led to. A trigger can fail before any session exists, a phone message
continues a session that already exists, and a Wingman question spans several, so "the last run" and its cost
are read from the occurrence, not guessed from sessions. Retry, cancellation, overlap with a run still going,
and a run missed while the machine slept are each defined per trigger. The phone's chat queue already solves the
hardest case, a message that may or may not have been sent before a restart.

If aimux is ever asked to speak a task protocol to other agents, that is a translation at the edge, not a second
model inside.

### Backends: where a session runs

Unchanged from the backend spec: `local`, an OpenShell sandbox, an OpenClaw gateway, later a cluster control
plane, all behind one interface, chosen per agent definition or with `--on`. The one addition from this draft: a
session on any backend is wired to the doors when it is created, so its model calls and tool calls are recorded
the same way wherever it runs. A backend that cannot be wired is listed as not covered.

### Doors: what "backend is not just a harness" means

| Door | In front | Behind it | State |
|---|---|---|---|
| Inference proxy | any harness that speaks the Anthropic Messages API | Engines: Codex today; Anthropic passthrough; any OpenAI-compatible endpoint (a local model server, a model gateway) | Codex built. Engines are slice 8 |
| MCP proxy | any harness that speaks MCP, from the host or from a sandbox | MCP servers, started on demand, shared, pinned | Built as two processes (MCP proxy, tool gateway). One capability to the user |
| Hooks | the harness's built-in tools | nothing; they report | Slice 3 |

To the user the MCP proxy and the tool gateway are one part, the MCP proxy, with one command family
(`aimux servers`, `aimux tools`). Whether they stay two processes is an implementation choice.

### Versions

- **One release version** for the CLI and every service. `aimux version` prints one line.
- **Each contract has its own small version**: the ledger line, the health answer, the sessions API, each file two
  processes share. A reader accepts older versions. A reader that meets a newer one than it knows does not guess: it skips
  those records, says how many it skipped, and marks any total that spans them as incomplete. A writer that meets
  a newer one refuses to write. Together these keep the minutes of a mixed upgrade safe.
- **Separate products later, by evidence.** A piece is split out when someone wants it without the rest, or it
  needs a different stability promise. Because pieces talk only through the contracts, a separate binary from the
  same repository is a packaging change.

## One configuration, four ways to change it

The file is the statement of what should be on. The menu, the commands and the dashboard's switches all edit it,
and aimux makes the machine match it.

```yaml
# ~/.aimux/config.yaml
parts:
  inference-proxy:
    enabled: true
    engines:
      anthropic: {}                       # passthrough
      codex: {service_tier: priority}
  mcp-proxy:
    enabled: true                         # servers are in servers.yaml, reviewed sets in tool-sets.yaml
  phone-access:
    enabled: true
    allowed_logins: [me@example.com]
  sandboxes: {enabled: false}
  evals: {enabled: false}

schedules:
  news: {when: "every day 07:00", agent: news-digest}
```

| Way | Does |
|---|---|
| `aimux setup` | The menu; writes the file |
| `aimux enable <part>`, `aimux disable <part>` | One line of the file |
| The dashboard's setup page | The same switches |
| Editing the file, then `aimux setup --apply` | Makes the machine match; `--check` shows what would change and changes nothing |

Rules:

- **The file holds what the owner wants, never what aimux found or wrote.** Install manifests, backups, pins and
  paired devices stay where they are, outside it.
- **No secret is ever in it.** A key is a reference to the Keychain, as `servers.yaml` already does.
- **It is portable, with the two files it points at.** `config.yaml`, `servers.yaml` and `tool-sets.yaml` on a
  new machine, then `aimux setup --apply`, give the same setup. Secrets do not travel: each key is entered into
  that machine's Keychain. Tool approvals do not travel either: pins are made again there with
  `aimux tools approve`, because a pin vouches for what that machine's servers serve. On a cluster the three files
  are the configuration the container reads.
- **A project's own `.aimux/config.yaml` can narrow, never widen.** It may restrict tool sets, and choose among
  the engines the owner has already approved for that kind of content. It cannot turn on a part, and it cannot
  send a conversation to a provider that project has not been sent to before: the owner is asked the first time,
  for unattended runs as well. So cloning a repository can neither switch on phone access nor redirect a private
  conversation.
- **One switch can cover two policies.** The MCP proxy on the host warns about a changed tool; the tool gateway
  for sandboxes refuses it and hands out leases. Turning the part on does not merge those rules.
- **Today's keys keep working.** The file has about 25 top-level keys that grew one feature at a time, and the two
  proxies have none at all (flags and job files instead). Old keys are read as aliases of their new place.

One thing to get right before building: the commands must edit the file without destroying the owner's comments
and ordering.

## Where evals fit

The eval engine's design is in the research wiki (`wiki/agentic-evals/my-eval-loop/aimux-eval-engine-design.md`),
owned by the eval session and paused. It fits this shape without a new kind of thing: it is a part with no
service of its own. Turning it on adds a schedule, a few model calls after each session, and views.

| Eval stage | Lands on | Needs from the base |
|---|---|---|
| Capture | The session reader and the archive | One shared session reader (base item B5 below) |
| Detect, Cluster | Model calls made by aimux, recorded like any other | The ledger; they show in `aimux cost` as started by "evals" |
| Case identity | `case_key` from session id, turn id and an ordinal | The turn id (slice 1). It also joins a case to what its turn cost |
| Review | A queue: what the agent did, what the owner said, the proposed fix | A view in the terminal UI and the dashboard |
| Run (the Sunday replay) | A schedule whose sessions are started by "evals" | Schedules (stage 8). Until then the existing launchd job calls an aimux command |
| Run on other models | The same case started with another `--model` | Engines behind the inference proxy (stage 3) |
| Score, Calibrate | Scores kept separate; exported as ATIF | Nothing new |
| Report ("fix next") | One home: a view, and the weekly email | A view |
| Improve | Edits to rules, skills, hooks | Outside aimux |

Three places where the two designs strengthen each other:

- **Evals are the evidence the model catalog needs.** A case replayed on each engine, scored per failure type, is
  exactly "what each model is good at in this harness", which the routing design had no source for.
- **Evals cost money on every session.** Detect runs after each one. Because its calls are ledger lines, the cost
  is visible, and the allowance rule (slice 7) can give evals their own budget and a cheap engine.
- **The export rule is a rule.** "Secrets redacted, and approval per project before transcript text leaves the
  machine" belongs in the same rule check the proxies use, written as a verdict when it refuses.

Its five files (`corrections.jsonl`, `exclusions.jsonl`, `fixes.jsonl`, `failure-types.json`, the suite) already
follow the contract rule here: each record names its schema version, and files are append-only.

When it can resume, against the stages above:

| Eval phase | Earliest |
|---|---|
| 0 Reconcile, 1 Contracts | After stage 1, when the turn id exists |
| 2 Detect in shadow, 3 Migrate and review | With stage 4, in the eval session; needs the shared session reader |
| 4 One case end to end, 5 Switch over | Stage 6. The weekly job stays a launchd job calling aimux until schedules exist |
| 6 Compare models | Stage 6, feeding the model catalog |

## The clean base

Five things, each small, each taken from code that already works. Four of them block nothing. The fifth, the
shared session reader (B5), is needed before evals can detect corrections, so it is built in stage 4, ahead of
that eval work.

| Base | What it is | Why it comes first |
|---|---|---|
| B1. One registry | One table in code: each part, the services it needs, how each is started, probed and upgraded | Today that list is repeated in `status`, `doctor`, `setup`, `upgrade` and each installer, and they drift |
| B2. The service contract | Every service: one `serve` entry, the common health answer, the ledger writer, format-versioned files | Already the governance spec's "base". This names it as the rule for a new service |
| B3. Generated job files | Each service's launchd or systemd job written from the registry | Removes the hand-written jobs without new code between the owner and his proxies. A daemon replaces this only if a later need justifies it |
| B4. Sessions with triggers | `aimux run`, schedules, and the backend interface as the one way a session starts | Folds the entry wrapper, spawn, Wingman's runs and scheduled jobs into one path |
| B5. One session reader | Live, archived and Codex transcripts through one reader, with one redaction list | The dashboard, search, the phone API, handoff and evals each read transcripts their own way today. `internal/catalog` is its seed |

## Where we are

In the code and its commands the inference proxy is called the model proxy today (`aimux model proxy`,
`internal/modelproxy`). This draft uses the name the owner chose for what the user sees. Whether the command
and the package are renamed, with the old command kept as an alias, is a decision below.

| Piece | State | The owner depends on it daily |
|---|---|---|
| Session dashboard (terminal UI, web), session search, resume | Live | Yes |
| Model proxy with the Codex engine, stable and dev channels, promote script | Live, build 194 | Yes: Claude Code on Codex through `claude-codex` |
| MCP proxy: stdio servers shared, started on demand | Live | Yes: most MCP servers go through it |
| Tool gateway, tool sets, pins, sandboxed profiles, Wingman | Live, on a separate hand-kept build | Yes, for Wingman |
| Phone API with the web and iPhone clients | Live | Yes |
| Collector | Live | Indirectly |
| `aimux status`, the ledger line type and counting rule | Merged | New |
| Promote survives a cold start; `aimux upgrade` | Built and tested, waiting for the owner's word | No |
| The ledger files, the meter, hooks reporting, engines beyond Codex, rules | Designed (governance spec) | No |
| Sessions over named backends, agent definitions | Designed, partly built on `refactor/managed-sessions` | No |
| Parts, generated job files, schedules | This draft | No |

What works today is a set of good parts. What is missing is the layer that makes them one tool: one record,
one way to turn things on, one way to keep them current.

## From here to there

Rules that keep the daily setup safe at every stage:

- Every existing command keeps working. New commands are added in front of them.
- Nothing is wired into every session until it has run on the dev channel first.
- A service changes build only through an upgrade that puts the previous build back when the new one does not answer.
- Each stage is usable on its own; stopping after any of them leaves a working tool.

| Stage | What is built | What the owner gains | Roadmap items |
|---|---|---|---|
| 0. Land what is waiting | Promote retry; `aimux upgrade` for the model proxy and the collector; the MCP proxy on its own copy; the flaky tests | No service follows `go install` by accident; one command moves them | The agreed small work, in its agreed order |
| 1. One record | Ledger files written by the model proxy; a reader; a first meter | `aimux cost`: requests and tokens per session and model, for everything that passes the model proxy | Slice 1 |
| 2. One way in, and identity | The entry wrapper, first behind `claude-codex`; the registry of parts; `aimux setup` gains the parts menu over the existing installers, keeping `--json` and its non-interactive behaviour; `enable`, `disable`; job files generated from the registry | Sessions carry their identity; turning a piece on stops being four procedures | Slice 2, B1 |
| 3. A second engine, by choice | The Anthropic passthrough engine and a sandbox entrance, used only by sessions the owner routes through them; switching model by hand inside a session | Ordinary Claude sessions recorded when asked for; any model from `/model` in those sessions | Slice 8a |
| 4. The right model per task | Roles mapped to engines (helpers and background calls on another engine than the main conversation); fallback when a limit error comes back | Cheaper helpers without choosing by hand; work continues when one allowance runs out | Slices 8b, 8c |
| 4, in parallel. Evals resume | In the eval session: contracts, detection in shadow beside the current scripts, migration, the review queue. The weekly replay keeps its existing job, calling an aimux command | Corrections from Codex sessions are caught too; one home for "fix next" | Eval phases 0 to 3. Needs the turn id from stage 1 and the shared session reader (B5) |
| 5. See everything | Hooks report tool calls; the MCP proxy and tool gateway write lines; changed tools are flagged on the host | One timeline per session across model calls, built-in tools and MCP tools | Slices 3 and 4 |
| 6. Numbers, and "auto" | Full meter with prices and allowances; spans; rules; the model catalog fed by eval cases replayed on each engine; "auto" | Cost in every surface; a cap; a model chosen for the task, with the evidence shown | Slices 5, 6, 7, 8d to 8f; eval phases 4 to 6 |
| 7. Every session covered | The inference proxy wired once for all sessions | Plain `claude` is recorded; `claude-codex` becomes optional | New. Only after the checklist below |
| 8. Later | Schedules and triggers; `aimux run` with agent definitions; backends beyond local | The hand-written scheduled jobs go away | B4, the `managed-sessions` design |

Routing and evals come before the rest of the visibility work because the owner ranks them higher. That is
possible: neither needs the reporting hook or the other proxies' ledger lines. It has one cost. Routing by role
ships while tool calls are still only partly recorded, so what a helper did on its cheaper engine is visible as
model calls and cost, not yet as a full timeline.

Before stage 7, each of these has been shown on the dev channel:

- the subscription login, a streamed tool call with its result, and a conversation continued across two engines, all through the proxy;
- latency and error rate through the proxy measured against the direct route;
- a dead proxy: the session names the fix, and the owner can go direct again with one command that does not need any aimux service to be running;
- an upgrade that tests the new build with one real model turn and one harmless MCP call before switching, waits for streams in flight, and has been rolled back once on purpose;
- the sandbox entrance passing its own test, separately.

Native Codex sessions do not pass the inference proxy, which speaks the Anthropic Messages API. "Every session
covered" means every Claude Code session; native Codex stays covered by totals only and is labelled so.

## What each surface looks like along the way

| Surface | Today | After stages 1 to 3 | After stages 4 to 7 | Stage 8, later |
|---|---|---|---|---|
| CLI | Many service commands at the top level | `setup` menu, `enable`, `disable`, `cost`, `upgrade`, `status` by capability | `models`; `/model` switching needs no command | `run`, `schedule`; `serve` commands hidden from help |
| Terminal UI | Sessions, preview, trace from transcripts, its own cost view | A services line in the header; trace view also reads ledger lines | A model column, a cost column, "not covered" marks; cost view reads the meter | A schedules view; model and backend choice in the launcher |
| Dashboard | Sessions, history, costs, insights | A services strip | Cost by model and by who started the session | A setup page with the switches for each part; a schedules page |
| Phone (web and iPhone) | Now, All, chat, queue, alerts | Unchanged | Model and cost on a session | Scheduled sessions and their alerts; agent and model choice for a new session |
| Wingman (browser panel, its runs) | Its own build of the tool gateway; stages in sandboxes with no telemetry; its own router | Its model calls pass the sandbox entrance (stage 3), so a question has a cost in model calls | Its tool calls are ledger lines with the run id (stage 5), so a question has a full cost; the gateway runs the one build | Its stages are sessions created the same way as any other; its route function becomes "auto"'s second step |
| aimux's own MCP server (session search for agents) | Lists and reads sessions | Unchanged | Can answer what a session cost | Can list schedules |
| Status line (the HUD) | Reads a Codex usage file the proxy writes | Unchanged | Reads the meter instead | Unchanged |

## What this draft is unsure of

1. **Whether a daemon is ever needed.** Generated job files are recommended because upgrade-when-idle and one status answer
   do not need a daemon. Schedules are the feature
   most likely to bring it back, since something must be running to start a job on time.
2. **Idle-aware upgrade.** It needs every service to say whether it is busy. The model proxy can; the others have
   not been checked.
3. **Two processes behind the MCP proxy.** Merging the MCP proxy and the tool gateway would simplify the
   picture and is not examined here.
4. **How much of the `managed-sessions` branch still applies.** It predates the doors and the ledger. Its
   interfaces were read for this draft; its code was not.
5. **`aimux run` versus typing `claude`.** If the owner keeps typing `claude`, the entry wrapper has to be a shell
   integration, not a command, or sessions stay not covered.

## Decisions for the owner

1. **Parts as the top layer.** Are the six in `aimux setup` above the right ones, in the right words?
2. **Generated job files now, a daemon only when schedules or a second platform need one.** Recommended: yes.
3. **Sessions with triggers, no task API, with each trigger occurrence recorded.** Recommended: yes.
4. **Plain `claude` stays the everyday way in; `aimux run` is for agent definitions, sandboxes and other
   machines.** Recommended: yes. Wiring the inference proxy in front of every session is a separate, later
   decision with its own checklist (stage 7).
5. **Pull the passthrough engine and routing by role forward (stages 3 and 4), ahead of the reporting hook.** Recommended: yes,
   once the sandbox half of its test has an answer.
6. **Rename the model proxy to the inference proxy in commands and code, or only in what is shown.**
   Recommended: rename the command with the old one kept as an alias; leave the package name until a change
   touches it anyway.
7. **Who the second user is.** Someone who wants only the model proxy changes the order: the inference proxy becomes
   installable alone sooner.

# Stage 4: The Right Model per Task Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** In a session that goes through the model proxy, subagents run on the engine the owner chose for them without being told to each time, the ledger shows which kind of work each call was, and a plan limit is reported with what a fallback would have done. Continuing on another engine for real is not in this stage: it is stage 4b, planned once a real plan limit has been captured.

**Architecture:** Three small pieces around one new idea, the role of a call. For choosing, aimux does not route in the proxy: Claude Code has a variable that names the model its subagents use by default, so the owner's choice in the config file becomes that variable in the shell, and the proxy keeps picking the engine from the model name as it does today. For recording, the front that read the request hands the proxy a small set of facts about the call (`CallFacts`), and the role is decided from those, never from the request itself. For limits, the proxy only reports: it recognises a plan limit, writes what it would have done, and passes the answer on unchanged.

![The owner's subagent role in the config file becomes Claude Code's own subagent default in the shell. The session names its own model for the main conversation and the role's model for subagents. The proxy picks the engine from the model named, takes the facts of each call from the front that read it, and writes the engine and the role to the ledger; a plan limit on Anthropic is passed on unchanged and reported](images/stage-4-roles-and-limits.png)

**Tech Stack:** Go standard library. No new dependency.

**Spec:** `docs/superpowers/specs/2026-11-23-aimux-governance-metering-and-evals-design.md`, section 6 ("When the choice is made", "Routing per task, keeping context": levels 2 and the limit-error trigger). Settings shape: `docs/superpowers/specs/2026-11-25-parts-settings.md`, which task 2 extends.

## What the owner sees when it is done

```yaml
parts:
  inference-proxy:
    enabled: true
    engines:
      anthropic: {}
    models:                          # the one place a model name is written
      implement: codex:gpt-6.2-sol
      second-opinion: codex:gpt-6.2-astra
    roles:                           # a role names an entry above, never a model
      subagent.implement: implement  # a subagent kind, by its name in Claude Code
      subagent.review: second-opinion
    on_limit: report                 # say what a fallback would have done; change nothing
```

```text
$ aimux engine list
ENGINE     IN THE FILE  RUNNING  ...
ROLE                MODEL                              ENGINE
main                (the session's)                    -
subagent.implement  implement = codex:gpt-6.2-sol      codex
subagent.review     second-opinion = codex:gpt-6.2-astra  codex
subagent (others)   (the session's)                    -

$ aimux cost --by role
ROLE                ENGINE     CALLS  MEDIAN TIME  IN      OUT
main                anthropic  41     ...
subagent.implement  codex      56     ...
unknown             anthropic  39     ...
```

The main conversation is never moved by a role: it stays on the model the session is on, changed only by `/model` or by a plan limit the owner allowed to fall back.

## Starting values, and how they change

The starting values are from the model catalog in the design (section 6, "The model catalog"), which is public benchmarks, marked as such:

| Kind of work | Starts on | Why |
|---|---|---|
| The main conversation | the model the session is on | never moved by a role |
| Exploring and searching a codebase | the session's model | understanding an unfamiliar repository is where `gpt-6.2-sol` is weak |
| Implementation with a clear brief and tests as the gate | `codex:gpt-6.2-sol` | its strength, and it spends the ChatGPT plan, not the Claude allowance |
| Review | `codex:gpt-6.2-astra` | a second opinion from another vendor |
| Background and small helper calls | unchanged | no way to route them that leaves `/model haiku` alone |

So there is no single default for all subagents: one would send searches to the model that is weakest at them.

The owner's own ledger, 354 successful calls up to 2026-11-26, does not show `gpt-6.2-sol` as the quick one through the proxy: its first word came after 3.9 s at the median, against 1.1 s for `claude-fable-5-2`. The calls were different work, so this is not a comparison of the models. It is the reason no starting value is argued from speed.

A value is changed by the owner, in the file, on evidence. Three sources, in the order they arrive:

1. **The ledger, by role** (task 1): calls, time and tokens per role and model, from real work.
2. **The monitoring the collector already gathers:** tool errors and retries per session, joined to the role's model.
3. **The eval engine,** when it resumes: the owner's own cases replayed on each model, which is the only like-for-like comparison.

Models are replaced often. A role names an entry under `models`, so a new model is one changed line, and `codex:default` names whatever the proxy's current default Codex model is. `aimux model proxy doctor` warns when an entry names a model its engine no longer answers for.

## Turning it off

| For | How |
|---|---|
| Everything | no `roles` in the file, which is the default |
| One shell, and the sessions started from it | `AIMUX_ROLES=off` in that shell's environment: `aimux model proxy env` then sets no role |
| One task | name the model in the subagent call or its definition; a model named by hand always wins |

Claude Code reads these settings when a session starts, so a role cannot be switched in a session that is running: it is exited and resumed. Roles per project belong to the profiles work that follows this stage.

## Frontends

This stage is the CLI only, like the stages before it. Each operation is a function in `internal/controller` or `internal/parts` first, with its own tests, so the TUI and the dashboard can call it without copying logic, and each gap is added to the parity table in `.claude/CLAUDE.md`. The place these become visible outside a terminal is stage 5b, the admin view.

## What is known, and what is not

From the owner's ledger on 2026-11-26 (361 model calls through two proxies):

| Kind of call | Calls | How it is told apart today |
|---|---|---|
| Main conversation | 225 | has a session id, no agent header |
| Subagent | 56 | carries the agent header |
| Helper calls naming a small model (`claude-haiku-5-5`, `claude-sonnet-5-5`) inside a session on a larger one | 60 | not told apart: recorded as main |
| Background, with no session id | 0 seen | the rule exists; none appeared |

Two things follow. The ledger's "side" flag undercounts helper work. And those 60 calls cannot be labelled by their model alone: a session the owner moved to Haiku by hand looks the same as a helper call on Haiku. They are recorded as `unknown` unless task 0 finds something on the request that tells them apart.

From Claude Code's documentation ("Model configuration", environment variables):

- `CLAUDE_CODE_SUBAGENT_MODEL`: "the default model for subagents", an alias or a full model name. A model named per invocation or in a subagent's own definition takes precedence, which is the manual-choice rule this plan needs.
- `ANTHROPIC_DEFAULT_HAIKU_MODEL`: "the model to use for `haiku`, or background functionality". It is not used by this plan: it would also change what `/model haiku` and a subagent defined with `model: haiku` mean, which overrides a choice the owner made by hand. A `background` role is offered only if task 0 finds a way that does not.

Not known, and settled by task 0 before anything is built on it:

1. How a model is set per kind of subagent. A subagent's own definition can name its model, and the documentation says that takes precedence over `CLAUDE_CODE_SUBAGENT_MODEL`. Not known: whether a `codex:` name is accepted there and in the variable, whether the requests then reach the proxy naming it, whether a built-in subagent (Explore, general-purpose) can be given a model at all, and whether the request says which kind of subagent sent it.
2. Whether a helper call on a small model carries anything that tells it from a main conversation the owner moved to that model.
3. What a plan limit looks like on the wire: its status, its error type, and whether it can be told from a short rate limit, which Claude Code retries by itself. The two failures seen on 2026-11-26 were short ones (HTTP 429, answered in 0.2 s, the next call fine).

## Global Constraints

- A rule reads metadata only: the model named, the headers, the credentials, the role. Never the prompt.
- Manual choice comes first: a model named with `/model`, `--model`, per invocation or in a subagent's own definition is never overridden.
- Everything automatic ships first as a report. In this stage `on_limit` has only the `report` form.
- A role that cannot be told with certainty is `unknown`. It is never guessed, and an `unknown` call never advances a session's turn or produces a `switch` line.
- Nothing that decides an engine or a role, or writes a ledger line, takes a Messages request as its input. It takes `CallFacts`, which the Claude Code front fills today and a Codex front can fill later. One test runs the same decision on facts as each front would supply them.
- A request reaches an engine only when it carries what that engine requires. Codex spends the owner's allowance and needs the proxy key; a role or a report never widens that.
- The caller's credential is never stored, logged or sent to an engine it was not meant for.
- Defaults change nothing: with no `roles` and no `on_limit` in the file, every request is answered and recorded exactly as before this stage, except that its line carries a role.

## Review Focus

1. A short rate limit reported as a plan limit, which would make the report useless as the gate for stage 4b.
2. A role overriding a model the owner chose by hand for a subagent.
3. A `roles` entry naming an engine that is off, leaving every subagent call refused.
4. A helper call of one session given a role from another session's state.
5. An `unknown` call moving a session's turn number or writing a `switch` line.

Each has a test in the task that owns the code.

### Task 0: Evidence, read only

**Files:**
- Create: `docs/superpowers/specs/2026-11-26-roles-and-limits-evidence.md`

Nothing is built in this task. It answers the three open questions on a spare port, with real sessions, and the tasks below are corrected from what it finds before task 1 starts.

- [ ] **Step 1:** Start a proxy on a spare port with both engines. In a shell set by `aimux model proxy env --port`, in a scratch project with two subagent definitions of the test's own (one naming `model: codex:gpt-6.2-sol`, one naming none), run prompts that start each of them and a built-in one, first with and then without `CLAUDE_CODE_SUBAGENT_MODEL='codex:gpt-6.2-astra'` exported. Record from the ledger, per call: the model named, the engine, the agent header, and any header or metadata key (names only) that says which kind of subagent it is.
- [ ] **Step 2:** In a session on a large model, record every call that names a smaller one, with its headers and metadata keys (names only, never values or content). Then move a session to that smaller model by hand and record the same. State whether anything tells the two apart.
- [ ] **Step 3:** Write down, from the request history and from Claude Code's error documentation, what distinguishes a plan limit from a short rate limit. If no real plan limit can be captured, say so.

Done when the note states each answer with the run that showed it, and lists what stayed unknown.

### Task 1: The facts of a call, and its role in the ledger

**Files:**
- Create: `internal/modelproxy/callfacts.go`, `internal/modelproxy/role.go`, their tests
- Modify: `internal/ledger/line.go`, `internal/modelproxy/ledgerline.go`, `internal/modelproxy/turns.go`, `internal/controller/meter.go`, `cmd/aimux/cmd/cost.go`, their tests, `docs/ledger-identity.md`

**Interfaces:**
- Produces: `modelproxy.CallFacts{Session string; SessionFromBody bool; Agent string; Model string; Effort string; Harness string; StartsTurn bool; Prompts int; LastFingerprint uint64}`. `callFactsOfMessages(r *http.Request, request messageRequest) CallFacts` is the only function that reads a Messages request for these.
- Changes: `startModelCall` takes `CallFacts` in place of the request.
- Produces: `modelproxy.RoleOf(f CallFacts) string` returning `RoleMain`, `RoleSubagent`, `RoleBackground` or `RoleUnknown`; a subagent's role carries its kind (`subagent.<kind>`) when task 0 found the request says it, and is plain `subagent` otherwise. `CallFacts` gains `AgentKind string` for that.
- Produces: `ledger.Line.Role` as `aimux.role`. `Side` stays for earlier readers, true for subagent and background.

- [ ] **Step 1: Failing tests.** `RoleOf`: an agent id is a subagent; no session id in the body's metadata is background; a session id in the body and no agent id is main; whatever task 0 found for small-model helper calls, and `unknown` when it found nothing. An `unknown` call leaves the session's turn number and last engine as they were. The same facts, built once as the Messages front builds them and once by hand as a Responses front would, give the same role and the same ledger line.
- [ ] **Step 2: Implement.** Move the reading of the request into `callFactsOfMessages`; nothing else changes behaviour.
- [ ] **Step 3:** `aimux cost --by role`. Lines from before this stage have no role and are shown as `unknown`, with a note saying how many.
- [ ] **Step 4:** Full suite, docs, commit, pull request.

### Task 2: Roles and the limit report in the settings

**Files:**
- Modify: `internal/parts/settings.go`, `internal/parts/engines.go`, `docs/superpowers/specs/2026-11-25-parts-settings.md`, `cmd/aimux/cmd/engine.go`, their tests, `docs-site/src/content/docs/guides/parts.mdx`

**Interfaces:**
- Produces: `parts.PartSettings.Models map[string]string` (an entry's name to a model as the harness names it, or `codex:default`), `parts.PartSettings.Roles map[string]string` (role to an entry's name) and `parts.PartSettings.OnLimit string` (empty, or `report`).
- Produces: `parts.SetRole(file, role, entry string) error`, `parts.ClearRole(file, role string) error`, `parts.SetModel(file, entry, model string) error`.
- Produces: `controller.RolesView(s parts.Settings, running []string) []RoleRow`, the rows `aimux engine list` prints, so another frontend shows the same table.

- [ ] **Step 1: Failing tests** for the section: `subagent` and `subagent.<kind>` accepted; `main` refused with a sentence saying the main conversation is chosen in the session; any other role refused by name; a role naming an entry that `models` does not have, refused with the entry's name; a role naming a model directly, refused with a sentence saying to put it under `models`; an entry whose engine prefix names an engine that is not on, refused with the engine's name; `on_limit` taking only `report` in this stage, with a sentence saying that continuing on another engine comes later; a problem names its line and repeats no value.
- [ ] **Step 2: Implement** the parsing, the validation and the two edits, in the same text-patch way as the engine edits.
- [ ] **Step 3:** `aimux engine role set <role> <model>` and `aimux engine role clear <role>`, and the `ROLE` table in `aimux engine list`.
- [ ] **Step 4:** Full suite, docs, commit, pull request.

### Task 3: The shell carries the subagent default

**Files:**
- Modify: `internal/modelproxy/engines.go`, `cmd/aimux/cmd/model_proxy_env.go`, their tests, `docs-site/src/content/docs/advanced/model-proxy-channels.mdx`

**Interfaces:**
- Consumes: `parts.PartSettings.Roles`.
- Produces: `modelproxy.SessionEnvRoles(roles, models map[string]string, getenv func(string) string) (lines []string, kept []string)`.

How a role per kind of subagent reaches Claude Code is whatever task 0 found to work. If only a subagent's own definition can carry it, aimux does not edit the owner's definition files: `aimux engine list` shows, for each definition it finds, the model it names beside the model its role asks for, and says which differ.

- [ ] **Step 1: Failing tests.** With `AIMUX_ROLES=off` in the shell, no role line is printed and stderr says so; with plain `subagent` set, the lines export `CLAUDE_CODE_SUBAGENT_MODEL` with the entry's model; a value the shell already holds is left alone and named in `kept`; `--off` unsets only what aimux set, by the same marker rule as the picker entry; a role whose engine the running proxy does not have is not exported, and stderr says why; `aimux model proxy env --dry-run` prints what would be set and for which role, without the key and without setting anything.
- [ ] **Step 2: Implement.**
- [ ] **Step 3:** Full suite, docs, commit, pull request.

### Task 4: A plan limit is recognised and reported

**Files:**
- Create: `internal/modelproxy/limit.go`, `internal/modelproxy/limit_test.go`
- Modify: `internal/modelproxy/anthropic.go`, `internal/modelproxy/engines.go`, `internal/ledger/line.go`, `cmd/aimux/cmd/model_proxy.go`, `internal/controller/proxydoctor.go`, their tests, docs

**Interfaces:**
- Produces: `modelproxy.limitKind(status int, errorType string, header http.Header) string` returning `limitNone`, `limitShort` or `limitPlan`, by the rule task 0 wrote down. The engine's own code classifies its refusal; the proxy only reads the result.
- Produces: `ledger.Line.Limit` as `aimux.limit` (`short` or `plan`) on a failed model call.
- Produces: `aimux model proxy serve --on-limit report`, set from the settings by `ProxyArgs`.

- [ ] **Step 1: Failing tests,** each with a real handler and a stand-in upstream: a plan limit is passed on to the session byte for byte and its line carries `aimux.limit: plan`; a short rate limit carries `short`; with `on_limit: report` a plan limit also writes one line saying what a fallback would have needed (a fresh turn, no tool result outstanding, the proxy key present, the engine it would have gone to) and whether this request met each; nothing is ever sent to another engine; with no `on_limit` no such line is written.
- [ ] **Step 2: Implement.** The answer the session receives is never changed in this stage.
- [ ] **Step 3:** `aimux model proxy doctor` reads the limit kind where it has one, in place of guessing from the status, and gains a `models` check: an entry whose model its engine refused in the last hour, or whose engine is off, is a warning with the entry's name.
- [ ] **Step 4:** Full suite, docs, commit, pull request.

### Task 5: The close, on the owner's machine

- [ ] The owner sets the starting values above and runs a session on a Claude model that starts an implementation subagent and a search. Done when `aimux cost --by role` shows the main conversation and the search on Anthropic and the implementation subagent on Codex in one session, with nothing typed to make it so.
- [ ] After a week, the ledger by role is read against the starting values, and the catalog's evidence for each row is changed from public to measured or left marked public.
- [ ] `on_limit: report` stays on. The tracker is updated with what was seen.

## Stage 4b, not planned here: continuing on another engine

It is planned only after a real plan limit has been captured and the report has told it apart correctly. That plan has to settle, with tests, what this one leaves open:

- **Who may be sent where.** A fallback repeats the check a direct request gets: Codex only with the proxy key. Whether a setting for the whole machine may send every project's conversation to a second provider, or each project is approved once, is the owner's decision.
- **The decision comes before the first byte.** The forwarder writes the status and starts the body before it returns, so the choice has to be made on the upstream's refusal before anything is written.
- **What a fresh turn is.** Not "the last message holds no tool result": tool results can be split across messages. A request qualifies only when no tool call is outstanding anywhere in it, and the other engine can do what the request asks for (Codex has no provider-run tools such as web search).
- **How long a fallback lasts.** If Codex answers with a tool call, the next request carries its result and still names a Claude model. The turn has to finish on the engine that started answering it.
- **What is recorded.** The model asked for, the model that answered, the two attempts linked, and the cost of the other engine reading the conversation once.

## What this stage does not do

- It does not choose a model by the kind of task, by cost or by what worked. That is "auto", stage 6.
- It does not continue a session on another engine when a limit comes back. It reports, and stage 4b does it.
- It does not give background or small helper calls an engine of their own, unless task 0 finds a way that leaves `/model haiku` meaning what it says.
- It does not cover the Codex CLI as a harness. The role and the fallback are written against neutral inputs so that front can use them when it is built.

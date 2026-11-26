# Stage 4: The Right Model per Task Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** In a session that goes through the model proxy, subagents run on the engine the owner chose for them without being told to each time, the ledger shows which kind of work each call was, and a plan limit is reported with what a fallback would have done. Continuing on another engine for real is not in this stage: it is stage 4b, planned once a real plan limit has been captured.

**Architecture:** Three small pieces around one new idea, the role of a call. For choosing, aimux does not route in the proxy: Claude Code has a variable that names the model its subagents use by default, so the owner's choice in the config file becomes that variable in the shell, and the proxy keeps picking the engine from the model name as it does today. For recording, the front that read the request hands the proxy a small set of facts about the call (`CallFacts`), and the role is decided from those, never from the request itself. For limits, the proxy only reports: it recognises a plan limit, writes what it would have done, and passes the answer on unchanged.

![The owner's subagent role in the config file becomes Claude Code's own subagent default in the shell. The session names its own model for the main conversation and the role's model for subagents. The proxy picks the engine from the model named, takes the facts of each call from the front that read it, and writes the engine and the role to the ledger; a plan limit on Anthropic is passed on unchanged and reported](images/stage-4-roles-and-limits.png)

**Tech Stack:** Go standard library. No new dependency.

**Evidence:** `docs/superpowers/specs/2026-11-26-roles-and-limits-evidence.md` (task 0, done on 2026-11-26). The sections below are corrected from it; where it overturned the first draft, that is said.

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
      subagent: implement            # the owner's own subagents that name no model
    on_limit: report                 # say what a fallback would have done; change nothing
```

```text
$ aimux engine list
ENGINE     IN THE FILE  RUNNING  ...
ROLE      MODEL                          ENGINE
main      (the session's)                -
subagent  implement = codex:gpt-6.2-sol  codex

SUBAGENT        DEFINED IN         ITS MODEL                RUNS ON
reviewer        ~/.claude/agents   codex:gpt-6.2-astra      codex
implementer     .claude/agents     (none: the role's)       codex
Explore         built in           (the session's)          -

$ aimux model proxy status
...
Claude allowance      36% of the week used, resets Tue 04:00

$ aimux cost --by role
ROLE      ENGINE     CALLS  MEDIAN TIME  IN      OUT
main      anthropic  41     ...
subagent  codex      56     ...
```

The main conversation is never moved by a role: it stays on the model the session is on, changed only by `/model` or by a plan limit the owner allowed to fall back.

## Starting values, and how they change

The starting values are from the model catalog in the design (section 6, "The model catalog"), which is public benchmarks, marked as such:

| Kind of work | Starts on | How it is set | Why |
|---|---|---|---|
| The main conversation | the model the session is on | `/model` | never moved by a role |
| Exploring and searching a codebase | the session's model | nothing to set: the built-in Explore subagent ignores the subagent default (evidence, run 2) | understanding an unfamiliar repository is where `gpt-6.2-sol` is weak |
| The owner's own subagents that name no model | `codex:gpt-6.2-sol` | `roles.subagent` | implementation with a clear brief; spends the ChatGPT plan, not the Claude allowance |
| Review | `codex:gpt-6.2-astra` | `model:` in that subagent's own definition, which wins over the role (evidence, run 2) | a second opinion from another vendor |
| Sessions other programs start | unchanged | | each is a session of its own, not a helper call |

The first draft had no single default for subagents, for fear it would send searches to the model weakest at them. The evidence removed that worry: the default does not reach Explore. A kind of subagent that should differ from the default says so in its own definition, where Claude Code reads it; aimux shows those and does not edit them.

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

| Kind of call | Calls | How it is told apart |
|---|---|---|
| Main conversation | 285 | a session id in the body, no agent header |
| Subagent | 56 | the agent header, on every one of its calls |
| Background, with no session id | 0 seen | the rule exists; none appeared, in the ledger or in task 0 |

The first draft counted 60 calls on a small model as helper calls recorded wrongly as main. Task 0 showed they were whole sessions of their own, each with its own session id, started by other programs on the machine. Nothing is mislabelled, and no call inside a session named a smaller model than the session's.

From Claude Code's documentation ("Model configuration", environment variables):

- `CLAUDE_CODE_SUBAGENT_MODEL`: "the default model for subagents", an alias or a full model name. A model named per invocation or in a subagent's own definition takes precedence, which is the manual-choice rule this plan needs.
- `ANTHROPIC_DEFAULT_HAIKU_MODEL`: "the model to use for `haiku`, or background functionality". It is not used by this plan: it would also change what `/model haiku` and a subagent defined with `model: haiku` mean, which overrides a choice the owner made by hand. A `background` role is offered only if task 0 finds a way that does not.

Settled by task 0:

1. A subagent's definition can name a `codex:` model, and it wins over the default. `CLAUDE_CODE_SUBAGENT_MODEL` accepts a `codex:` name and moves the owner's own subagents that name none. It does not move the built-in Explore subagent.
2. A request does not say which kind of subagent sent it: it carries an agent id only. So the ledger's role is `subagent`, with the id, and the kind is not recorded.
3. Every answer from Anthropic on the owner's login carries the state of his plan's allowance in its headers (`anthropic-ratelimit-unified-*`): whether the plan allows the request, which window binds, the share used, when it resets.

Still not known: what those headers say when the plan refuses, since no limit occurred; and what an interactive session sends that a non-interactive one did not.

## Global Constraints

- A rule reads metadata only: the model named, the headers, the credentials, the role. Never the prompt.
- Manual choice comes first: a model named with `/model`, `--model`, per invocation or in a subagent's own definition is never overridden.
- Everything automatic ships first as a report. In this stage `on_limit` has only the `report` form.
- A role that cannot be told with certainty is `unknown`. It is never guessed. After task 0 that is only a line written before this stage, which has no role.
- Nothing that decides an engine or a role, or writes a ledger line, takes a Messages request as its input. It takes `CallFacts`, which the Claude Code front fills today and a Codex front can fill later. One test runs the same decision on facts as each front would supply them.
- A request reaches an engine only when it carries what that engine requires. Codex spends the owner's allowance and needs the proxy key; a role or a report never widens that.
- The caller's credential is never stored, logged or sent to an engine it was not meant for.
- Defaults change nothing: with no `roles` and no `on_limit` in the file, every request is answered and recorded exactly as before this stage, except that its line carries a role.

## Review Focus

1. A short rate limit reported as a plan limit, which would make the report useless as the gate for stage 4b.
2. A role overriding a model the owner chose by hand for a subagent.
3. A `roles` entry naming an engine that is off, leaving every subagent call refused.
4. A helper call of one session given a role from another session's state.
5. An allowance read from one login shown as another's: the figure belongs to the credential that carried the request.

Each has a test in the task that owns the code.

### Task 0: Evidence, read only

Done on 2026-11-26: `docs/superpowers/specs/2026-11-26-roles-and-limits-evidence.md`.

### Task 1: The facts of a call, and its role in the ledger

**Files:**
- Create: `internal/modelproxy/callfacts.go`, `internal/modelproxy/role.go`, their tests
- Modify: `internal/ledger/line.go`, `internal/modelproxy/ledgerline.go`, `internal/modelproxy/turns.go`, `internal/controller/meter.go`, `cmd/aimux/cmd/cost.go`, their tests, `docs/ledger-identity.md`

**Interfaces:**
- Produces: `modelproxy.CallFacts{Session string; SessionFromBody bool; Agent string; Model string; Effort string; Harness string; StartsTurn bool; Prompts int; LastFingerprint uint64}`. `callFactsOfMessages(r *http.Request, request messageRequest) CallFacts` is the only function that reads a Messages request for these.
- Changes: `startModelCall` takes `CallFacts` in place of the request.
- Produces: `modelproxy.RoleOf(f CallFacts) string` returning `RoleMain`, `RoleSubagent` or `RoleBackground`. The kind of subagent is not on the request, so it is not part of the role.
- Produces: `ledger.Line.Role` as `aimux.role`. `Side` stays for earlier readers, true for subagent and background.

- [ ] **Step 1: Failing tests.** `RoleOf`: an agent id is a subagent; no session id in the body's metadata is background; a session id in the body and no agent id is main, whatever model it names. The same facts, built once as the Messages front builds them and once by hand as a Responses front would, give the same role and the same ledger line.
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
- Produces: `controller.SubagentModels(dirs []string, s parts.Settings) []SubagentRow`: each subagent definition found (`~/.claude/agents`, the project's `.claude/agents`), the model it names if any, and the engine that would answer it. It reads the definitions' front matter and changes nothing.

- [ ] **Step 1: Failing tests** for the section: `subagent` accepted; `subagent.<anything>` refused with a sentence saying a kind of subagent names its model in its own definition; `main` refused with a sentence saying the main conversation is chosen in the session; any other role refused by name; a role naming an entry that `models` does not have, refused with the entry's name; a role naming a model directly, refused with a sentence saying to put it under `models`; an entry whose engine prefix names an engine that is not on, refused with the engine's name; `on_limit` taking only `report` in this stage, with a sentence saying that continuing on another engine comes later; a problem names its line and repeats no value.
- [ ] **Step 2: Implement** the parsing, the validation and the two edits, in the same text-patch way as the engine edits.
- [ ] **Step 3:** `aimux engine role set <role> <model>` and `aimux engine role clear <role>`, and the `ROLE` table in `aimux engine list`.
- [ ] **Step 4:** Full suite, docs, commit, pull request.

### Task 3: The shell carries the subagent default

**Files:**
- Modify: `internal/modelproxy/engines.go`, `cmd/aimux/cmd/model_proxy_env.go`, their tests, `docs-site/src/content/docs/advanced/model-proxy-channels.mdx`

**Interfaces:**
- Consumes: `parts.PartSettings.Roles`.
- Produces: `modelproxy.SessionEnvRoles(roles, models map[string]string, getenv func(string) string) (lines []string, kept []string)`.

Claude Code prints one diagnostic line for each call on a model name it does not know, `codex:` names included; the call is answered all the same. Whether an entry in its settings stops that line is tried in this task, and the lines are left alone if it would mean writing to the owner's settings file.

- [ ] **Step 1: Failing tests.** With `AIMUX_ROLES=off` in the shell, no role line is printed and stderr says so; with plain `subagent` set, the lines export `CLAUDE_CODE_SUBAGENT_MODEL` with the entry's model; a value the shell already holds is left alone and named in `kept`; `--off` unsets only what aimux set, by the same marker rule as the picker entry; a role whose engine the running proxy does not have is not exported, and stderr says why; `aimux model proxy env --dry-run` prints what would be set and for which role, without the key and without setting anything.
- [ ] **Step 2: Implement.**
- [ ] **Step 3:** Full suite, docs, commit, pull request.

### Task 4: A plan limit is recognised and reported

**Files:**
- Create: `internal/modelproxy/limit.go`, `internal/modelproxy/limit_test.go`
- Modify: `internal/modelproxy/anthropic.go`, `internal/modelproxy/engines.go`, `internal/ledger/line.go`, `cmd/aimux/cmd/model_proxy.go`, `internal/controller/proxydoctor.go`, their tests, docs

**Interfaces:**
- Produces: `modelproxy.allowanceOf(header http.Header) (Allowance, bool)` reading `anthropic-ratelimit-unified-*`: `Allowance{Status string; Window string; Used float64; ResetsAt time.Time}`. It keeps numbers and the plan's own words, nothing that identifies the account.
- Produces: `modelproxy.limitKind(status int, a Allowance, known bool) string` returning `limitNone`, `limitShort` or `limitPlan`: a failed call is a plan limit only when the plan's own status says it is not allowed; a 429 without that is short. The engine's own code classifies its refusal; the proxy only reads the result.
- Produces: `Health.Allowance` and the same in the keyless answer, the latest seen per engine, and a `Claude allowance` row in `aimux model proxy status`.
- Produces: `ledger.Line.Limit` as `aimux.limit` (`short` or `plan`) on a failed model call.
- Produces: `aimux model proxy serve --on-limit report`, set from the settings by `ProxyArgs`.

- [ ] **Step 1: Failing tests,** each with a real handler and a stand-in upstream: an answer's allowance headers reach the session unchanged and the health answer then reports the share used and the reset time; an answer with none leaves the allowance unknown, not zero; a plan limit is passed on to the session byte for byte and its line carries `aimux.limit: plan`; a short rate limit carries `short`; with `on_limit: report` a plan limit also writes one line saying what a fallback would have needed (a fresh turn, no tool result outstanding, the proxy key present, the engine it would have gone to) and whether this request met each; nothing is ever sent to another engine; with no `on_limit` no such line is written.
- [ ] **Step 2: Implement.** The answer the session receives is never changed in this stage. The value the plan's status holds when it refuses has not been seen: the rule treats anything other than `allowed` on a failed call as a plan limit, records the word it saw, and the report is what shows whether that was right.
- [ ] **Step 3:** `aimux model proxy doctor` reads the limit kind where it has one, in place of guessing from the status, and gains a `models` check: an entry whose model its engine refused in the last hour, or whose engine is off, is a warning with the entry's name.
- [ ] **Step 4:** Full suite, docs, commit, pull request.

### Task 5: The close, on the owner's machine

- [ ] The owner sets the starting values above and runs a session on a Claude model that starts one of his own subagents and a search. Done when the ledger shows the main conversation and the search on Anthropic and his subagent on Codex in one session, with nothing typed to make it so, and `aimux model proxy status` shows his Claude allowance.
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

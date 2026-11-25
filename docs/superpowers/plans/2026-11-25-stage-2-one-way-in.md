# Stage 2: one statement of what is on, and one way in

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One file says which parts of aimux are on and how; aimux writes each service's job file from it; and one line points a shell's sessions at aimux. After this stage, turning the Anthropic engine on is `aimux engine enable anthropic`, not an edit of a launchd file.

**Architecture:** A registry in code lists every part and the services it needs: label, port, arguments, health probe. `status`, `upgrade`, `doctor` and `setup` read it where each keeps its own list today. A `parts:` section in `~/.aimux/config.yaml` holds what the owner wants. `aimux setup --apply` compares each service's job file with what the registry and the file produce, and changes only what differs, through the restart-check-roll-back path `aimux upgrade` already has. The operating system stays the supervisor; no daemon.

**Tech Stack:** Go, cobra, `gopkg.in/yaml.v3` (its `Node` tree gives each value's line and column; the edit itself is on the source text), launchd through `internal/mcpproxy.Agent`. No new dependency.

**Spec:** `docs/superpowers/specs/2026-11-25-parts-settings.md` (the shape of the `parts:` section, and the rules it follows: where this plan and that page differ, the page is right); `docs/superpowers/specs/2026-11-25-aimux-product-shape-draft.md` ("Turn on what you want", "Who keeps the services alive", "One configuration, four ways to change it", base items B1 and B3); `docs/superpowers/specs/2026-11-23-aimux-governance-metering-and-evals-design.md` ("The entry wrapper"). Tracker: `docs/superpowers/plans/governance-roadmap.md`, stage 2.

<!-- no-images: one table of tasks and one of decisions; the draft carries the figures -->

## What stages 1 and 3 already did for this one

The tracker's stage 2 was written before them. Two of its pieces arrived early:

- **Identity.** "A background call carries its session in the ledger" is the stage's first done-when line. Since stage 1 the proxy reads the session from the header Claude Code sends on every request, a subagent's and a child session's included (`../specs/2026-11-25-turn-identity.md`, `../specs/2026-11-25-second-engine-evidence.md`). What is left is to show it on a day of real use, which task 6 does.
- **Half of the entry wrapper.** `aimux model proxy env` already points a shell at the proxy for both engines. What is left is the Codex-only form the owner uses every day (`claude-codex`, a shell function he maintains), and telemetry.

So this stage is mostly the registry, the file and the job files.

## Global Constraints

- The owner runs anything that restarts a live service. `--apply` on a job that is running says what will restart and asks, unless `--yes` is given.
- With no `parts:` section in the file, aimux changes no job file. Nothing moves until the owner asks. A part the section does not name stays unmanaged too: naming the inference proxy does not make aimux touch the MCP proxy or the collector. `enabled: false` is the only thing that means "off".
- aimux changes only what it owns in a job file: the program, and the arguments that come from the settings. Every other key and every other argument stays as it is (an environment, a custom port, `--codex-command`, log paths). A file it cannot read that way, one with a separate `Program` key for example, is refused with the reason, not rewritten. The file as it was before aimux first changed it is kept.
- A job file whose arguments already match is not rewritten and its service is not restarted. Taking a job under management costs no restart by itself.
- A job that fails its health check after an apply gets its previous job file back and is started again from it. `aimux upgrade` cannot do this today: when the program is already current it restarts without keeping anything to go back to. Task 3 builds this path and proves it on a job of its own before any live job is touched.
- The file holds what the owner wants, never what aimux found or wrote, and never a secret.
- A command that edits the file changes the bytes of one value and nothing else: comments, order, indentation, line endings and unknown keys stay. Re-encoding the file from a parsed tree does not give that (it moves comments and normalises indentation), so the edit patches the source text at the value's position and parses the result again to check it. A value reached through an anchor or a merge key is refused, with the line, since changing it would change other places too.
- Today's flags and job files keep working. A proxy started by hand with `--anthropic` behaves as before.
- The tool gateway's job is not touched: it runs Wingman's copy, and changes to it are agreed with the Wingman session first.
- The commands stay `aimux model proxy` and `aimux mcp proxy`. The part is shown as "Inference proxy", with the id `inference-proxy`; renaming the command is not in this stage.
- Controller first: every operation is a function in a core package with its own tests; the commands are thin. No mocks: job files are tested against the real launchd on a job of the test's own, behind the `launchdlive` build tag, as `aimux upgrade` is.
- Each task is one pull request, names "Stage 2" at the top of its description, and gets at most two pre-push review rounds.

## Review Focus

- A config file with comments, anchors, tabs, a `parts:` key written in flow style, or no trailing newline: an edit changes one value and nothing else.
- A job file with keys the generator does not know (an environment variable the owner added): they survive an apply, or the apply stops and says which.
- Two `aimux setup --apply` at once, or one during `aimux upgrade`: one waits or refuses; no half-written job file.
- A service that is off in the file and running: `--check` says so, `--apply` stops it only when told (`disable`), never as a side effect of something else.
- The file edited by hand into something invalid (an unknown part, an upstream with a bad address): `--check` names the line and changes nothing.

## Tasks

| # | Pull request | The owner can then |
|---|---|---|
| 1 | The registry of parts and services | Nothing new. `status` and `upgrade` read one table |
| 2 | The `parts:` section: read it, and edit it without losing comments | `aimux setup --check`, which prints what the file says and changes nothing |
| 3 | Job files written from the registry and the file | `aimux setup --apply`: the model proxy's job file is one aimux wrote |
| 4 | `aimux engine enable`, `disable`, `add`, `remove`, `list`; `aimux enable`, `disable` for parts | Turn the Anthropic engine or the gateway on with one command |
| 5 | The parts menu in `aimux setup` | Pick parts from a list |
| 6 | The Codex-only entry line, telemetry, and the close | Replace the body of `claude-codex` with one line |

### Task 1: The registry

**Files:**
- Create: `internal/parts/registry.go`, `internal/parts/registry_test.go`, `internal/parts/boundary_test.go`
- Modify: `cmd/aimux/cmd/upgrade.go` (`upgradeServices`, `notMoved`), `cmd/aimux/cmd/status_probes.go` (labels, addresses, fix commands), `.claude/CLAUDE.md`

**Interfaces:**
- Produces:

```go
package parts // imports nothing from the repository but internal/modelproxy and internal/mcpproxy constants

type Service struct {
	ID      string // "model-proxy": what `aimux upgrade` is given
	Name    string // "model proxy": what a table shows
	Label   string // the launchd label, "com.aimux.model-proxy"
	Port    int      // the default. Where a running service is found stays each service's own rule: the MCP
	                 // proxy's recorded port, the collector's configured one, the gateway's socket
	Serve   []string // arguments after the program, before any from settings: {"model","proxy","serve"}
	Managed bool     // false: aimux upgrade and setup leave its job alone (the tool gateway)
	Update  string   // for an unmanaged service, the command that updates it
}

type Part struct {
	ID, Name, About string    // "inference-proxy", "Inference proxy", one line for the menu
	Services        []string  // service ids
	Always          bool      // the sessions dashboard: on, with no service
}

func Services() []Service            // in upgrade order
func ServiceByID(id string) (Service, bool)
func Parts() []Part                  // in menu order
```

- [ ] **Step 1: Write the failing tests.** `TestServices_AreTheOnesStatusAndUpgradeKnow` pins ids, labels and ports against today's literals (`com.aimux.model-proxy` 7385, `com.aimux.mcp-proxy` 7381, `com.azaalouk.aimux-collect` 4318, `com.aimux.tool-gateway` 7390 unmanaged). `TestStatusAndUpgradeReadTheRegistry` fails when `upgrade.go` or `status_probes.go` holds a launchd label literal. `doctor` and the installers keep their own for now; moving them is listed under "Not in this stage", so the registry is the one list for status, upgrade and setup, not yet for everything.
- [ ] **Step 2: Implement, then replace the two tables.** `aimux status` and `aimux upgrade --dry-run --json` print the same bytes before and after; `TestStatus_*` and `TestUpgrade_*` pass unchanged.
- [ ] **Step 3: Full suite, commit, pull request.**

### Task 2: The `parts:` section

The shape, every key's meaning when it is left out, and the rules are in `../specs/2026-11-25-parts-settings.md`. This task implements that page. Two things from it reach beyond the file: a model naming a disabled or unknown engine is refused by the proxy and never answered by another engine (today it falls through when no upstream is configured), and the proxy is handed a key reference, never a key. Both are built here, in the proxy, with their tests.

**Files:**
- Create: `internal/parts/settings.go`, `internal/parts/settings_test.go`, `internal/parts/edit.go`, `internal/parts/edit_test.go`, `internal/parts/testdata/*.yaml`
- Modify: `cmd/aimux/cmd/setup.go` (`--check`), docs

**Interfaces:**
- Produces:

```go
// Settings is what the owner wants on. The zero value is "nothing stated": aimux manages no job.
type Settings struct {
	Stated bool                       // the file has a parts: section
	Parts  map[string]PartSettings
}
type PartSettings struct {
	Enabled bool
	Engines map[string]Engine         // inference-proxy only: "anthropic", "codex", or a named upstream.
	                                  // Codex is built in: it can be given a tier, never removed
}
type Engine struct {
	Type        string         // "codex", "anthropic", "messages"
	Enabled     *bool          // nil: on, for an engine that is listed
	URL         string         // a messages engine
	Key         *secretref.Ref // a messages engine; nil: the Keychain item <name>-key
	ServiceTier string         // codex
	Binary      string         // codex
}

func Load(path string) (Settings, error)             // a missing file or section is the zero value
func (s Settings) Validate() []Problem               // unknown part, bad address, a name the proxy would refuse
func (s Settings) Args(service Service) []string     // the model proxy: --anthropic, --messages-upstream n=a, --service-tier

// Set changes one value under parts: and nothing else in the file. path is like
// {"inference-proxy","engines","anthropic"}; a nil value removes the key.
func Set(file string, path []string, value any) error
```

- [ ] **Step 1: Write the failing tests** from fixtures: `TestLoad_*` (absent, empty, full, an old file with none of it); `TestArgs_*` (each engine alone and together, in a fixed order so a job file does not change when nothing did); `TestSet_KeepsEverythingElse` (comments above and beside the key, flow style, tabs in comments, no trailing newline, CRLF: the bytes outside the changed value are identical); `TestSet_RefusesAValueBehindAnAnchorOrMerge`; `TestSet_CreatesTheSectionAtTheEnd`; `TestSet_TheResultParsesToWhatWasAsked`; `TestValidate_*` (removing `codex` is a problem, not a setting).
- [ ] **Step 2: Implement, add `aimux setup --check`** (prints each part, on or off, and what `--apply` would change; exits 1 when something would change; changes nothing).
- [ ] **Step 3: Full suite, docs, commit, pull request.**

### Task 3: Job files from the registry and the file

This is the step that touches a service the owner uses every day. It is built and proven on a job of its own first.

**Files:**
- Create: `internal/parts/jobfile.go`, `internal/parts/jobfile_test.go`, `internal/controller/apply.go`, `internal/controller/apply_test.go`, `cmd/aimux/cmd/setup_apply_live_test.go` (build tag `launchdlive`)
- Modify: `cmd/aimux/cmd/setup.go` (`--apply`, `--yes`), `internal/mcpproxy/launchd.go` (a checked install that takes arguments), docs

**Interfaces:**
- Produces:

```go
// Owned splits a job file into what aimux decides and what it leaves alone.
// It fails on a file it cannot split safely, naming why.
func Owned(existing []byte, service Service) (program string, owned, other []string, err error)

// WithArgs is existing with its owned arguments replaced, and every other byte as it was.
func WithArgs(existing []byte, service Service, owned []string) ([]byte, error)

// NewJobFile is the job for a service that has none yet.
func NewJobFile(service Service, program string, args []string, home string) []byte

// PlanParts says what would change. ApplyParts runs one plan; check and apply share the planner.
func PlanParts(home string, s parts.Settings) ([]JobChange, error)
func ApplyParts(home string, plan []JobChange, opts ApplyOptions) error
type JobChange struct {
	Service  string
	Action   string   // "none", "adopt" (record it, no restart), "change arguments", "start", "stop"
	Was, Now []string // the owned arguments before and after
	Restarts bool
}
```

- What aimux has taken under management is recorded outside the job file, in `~/.aimux/services/<id>/job.json`: the hash of the file as aimux last left it, and the path of the copy kept from before. A file that no longer matches its hash was edited by hand; the plan says so and stops for that service.
- `ApplyParts` takes the locks `aimux upgrade` takes, and plans again under them: if the settings or a job file changed since the plan was shown, it stops and shows the new plan.
- A change of arguments is: write the new file beside the old one, reload the job, wait for the service's health answer, and on failure put the old file back and reload again. It starts from `mcpproxy.Agent.InstallChecked`, not from `UpgradeServices`.
- The MCP proxy's enable and disable are not a job start and stop: its installer also rewrites Claude Code's server entries and hooks, and knows how to put them back. For that part the plan's action is to run that installer or its uninstall.

- [ ] **Step 1: Write the failing tests.** `TestOwned_*` and `TestWithArgs_*` from fixtures of the three job files as they are today, plus one with a custom port and `--codex-command`, one with a `Program` key (refused), one with unknown keys (kept). `TestPlanParts_NothingStated` and `TestPlanParts_OnlyTheNamedPart`: zero actions for a part the file does not name. `TestPlanParts_AdoptCostsNoRestart`. `TestApplyParts_AnArgumentThatBreaksTheServiceIsPutBack`: same program, a bad argument, the service never answers; the old file is back and the service runs. `TestApplyParts_StopsWhenThePlanIsStale`. `TestSetupApplyLive_AgainstLaunchd`: all of that against the real launchd on a job of the test's own.
- [ ] **Step 2: Implement.** Run the full suite and the tagged live test.
- [ ] **Step 3: The owner runs it on the stable model proxy, between turns:** add `parts: {inference-proxy: {enabled: true}}` to the file, `aimux setup --check`, then `aimux setup --apply`. Expected: one action, "model proxy: adopt", no restart, and zero actions for the MCP proxy and the collector. Then one Codex turn, to see nothing changed.
- [ ] **Step 4: Full suite, docs, commit, pull request.**

### Task 4: `aimux engine` and `aimux enable`

**Files:**
- Create: `cmd/aimux/cmd/engine.go`, `cmd/aimux/cmd/engine_test.go`, `cmd/aimux/cmd/enable.go`, `cmd/aimux/cmd/enable_test.go`
- Modify: `internal/controller/apply.go`, `internal/e2e/` (the new commands), docs, README

**Interfaces:**
- Produces: `aimux engine list` (each engine of the inference proxy, on or off, with what the running proxy's `/healthz` says beside it); `aimux engine enable <name>` and `aimux engine disable <name>`, which switch an engine that exists (`anthropic`, or a named upstream already added) and keep its address; `aimux engine add <name>=<address>`, which tells aimux a named upstream exists and turns it on; `aimux engine remove <name>`, which forgets it. Two verbs for two things: Anthropic is built in, so there is nothing to add, only to switch on. `disable codex` and `remove codex` are refused before the file is touched, and so are `add anthropic` and `add codex`, with the command that was meant; `aimux enable <part>`; `aimux disable <part>`. Each edits the file with `parts.Set`, prints what `--apply` would do, and applies after a yes (`--yes` to skip the question, `--no-apply` to only edit). `--json` on all.
- `aimux engine add <name>=<address>` checks that the secret-store entry `<name>-key` exists, without reading it, and says how to create it when it does not.
- [ ] **Step 1: Write the failing tests**, the e2e ones included: enable, disable, add, list, remove, each with `--no-apply` against a temp home; a named upstream disabled and enabled again keeps its address; a bad name and a bad address refused before the file is touched; `disable inference-proxy` stops the job and leaves the file saying so; `enable mcp-proxy` and `disable mcp-proxy` run the MCP proxy's own install and uninstall, and a tool answers through the proxy after one and directly after the other.
- [ ] **Step 2: Implement. Step 3: The owner runs** `aimux engine enable anthropic` on his machine. Expected: the stable proxy restarts with `--anthropic`, and `aimux model proxy env` now succeeds against it. This is also the last step of stage 3.
- [ ] **Step 4: Full suite, docs, commit, pull request.**

### Task 5: The parts menu

**Files:**
- Modify: `cmd/aimux/cmd/setup.go`, `cmd/aimux/cmd/setup_test.go`, docs

- `aimux setup` on a terminal opens with the list of parts, on or off, one line each, and "Turn on:" taking numbers. What it does today (the sandbox, tools, router, browser and gateway checks) runs for the parts that are on, under the part it belongs to. Without a terminal, and with `--json`, it behaves as today plus a `parts` list; the schema version goes up by one.
- Parts that are listed and not built yet (schedules, evals) are shown as "not available yet" and cannot be chosen. The tool gateway is shown as managed by Wingman and cannot be chosen either.
- [ ] **Step 1: Write the failing tests** against a scripted terminal: the list, choosing two, a wrong number, an empty answer, `--json`.
- [ ] **Step 2: Implement. Step 3: Full suite, docs, commit, pull request.**

### Task 6: The Codex-only line, telemetry, and the close

**Files:**
- Modify: `cmd/aimux/cmd/model_proxy_env.go`, `internal/modelproxy/engines.go` (`SessionEnv`), tests, docs, the tracker

- `aimux model proxy env --codex [model]` prints what the owner's `claude-codex` function sets today: the proxy's address, the key as the API key, and the model pinned to `codex:<model>` (default `gpt-6.2-sol`). It needs no `--anthropic` on the proxy, and it still asks the listener to prove it holds the key: `CheckSessionProxy` is split so the proof stands alone and the engine check is separate. `--off` undoes both forms: the base URL, the API key variable when it holds the proxy key, and the pinned model.
- Both forms also print the collector's telemetry variables when the collector answers, so a session started this way is in the collector's totals.
- [ ] **Step 1: Tests, implement.** The tests include one shell going Codex-only, then both engines, then off, with no variable left holding the proxy key at the end; values with quotes in them; and the collector not answering.
- [ ] **Step 2: The owner replaces the body of `claude-codex`** with `lines="$(aimux model proxy env --codex "$@")" && eval "$lines"`, keeping his HUD lines. aimux does not edit his shell file.
- [ ] **Step 3: Close the stage.** Start one session through the entry line that makes a subagent call, and find its new line in the stable ledger with `aimux.side` set and the session's id: a new line for a call made on purpose, not the absence of bad lines. Check the collector received that session. `aimux setup --check` reports nothing to change, and `~/.aimux/services/model-proxy/job.json` exists. Mark stage 2 done in the tracker.

## Decisions for the owner

| # | Decision | Recommended | The other way |
|---|---|---|---|
| 1 | How an engine is turned on | Decided: `enable` and `disable` switch an engine that exists (`aimux engine enable anthropic`); `add` and `remove` are for a named upstream, which needs an address (`aimux engine add enmaas=<address>`) | A flag on `aimux enable inference-proxy`; fewer commands, harder to read back |
| 2 | What aimux does with a job file that exists | Changes only the program and the arguments that come from the settings; keeps everything else; refuses a file it cannot split | Rewrites the whole file from the registry; one known shape, and it would drop anything set by hand (a port, `--codex-command`, the service's PATH) |
| 3 | Who owns the shell function | Decided: the owner keeps `claude-codex`; its body becomes one line that calls aimux | aimux installs its own shell integration; one more thing that edits `~/.zshrc` |

## Where a second opinion changed this plan

Codex (gpt-6.2-astra) reviewed the first draft. It agreed with decisions 1 and 3 and disagreed with 2, which is now its recommendation. From its findings the plan took: a roll-back path of its own for a change of arguments, since `aimux upgrade` has none when the program is already current; taking a job under management without a restart; a part that is not named staying unmanaged; editing the file's text and not re-encoding it; ports in the registry as defaults only; Codex as built in; the MCP proxy's enable and disable going through its installer; and a close that needs a new ledger line for a call made on purpose.

## Not in this stage

- `doctor` and the installers reading the registry. They keep their own lists until a change touches them.

- A daemon, and systemd job files. The registry is written so a second generator can be added.
- The tool gateway's job, and merging the two processes behind the MCP proxy.
- The dashboard's setup page. It reads the same file later.
- Renaming `aimux model proxy` to `aimux inference proxy`.
- A project's `.aimux/config.yaml` narrowing parts or engines. That comes with routing, in stage 4.
- Putting the inference proxy in front of every session. That is stage 7, behind its checklist.

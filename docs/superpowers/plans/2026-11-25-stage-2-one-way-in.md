# Stage 2: one statement of what is on, and one way in

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One file says which parts of aimux are on and how; aimux writes each service's job file from it; and one line points a shell's sessions at aimux. After this stage, turning the Anthropic engine on is `aimux engine add anthropic`, not an edit of a launchd file.

**Architecture:** A registry in code lists every part and the services it needs: label, port, arguments, health probe. `status`, `upgrade`, `doctor` and `setup` read it where each keeps its own list today. A `parts:` section in `~/.aimux/config.yaml` holds what the owner wants. `aimux setup --apply` compares each service's job file with what the registry and the file produce, and changes only what differs, through the restart-check-roll-back path `aimux upgrade` already has. The operating system stays the supervisor; no daemon.

**Tech Stack:** Go, cobra, `gopkg.in/yaml.v3` (its `Node` tree, to edit the file without losing comments), launchd through `internal/mcpproxy.Agent`. No new dependency.

**Spec:** `docs/superpowers/specs/2026-11-25-aimux-product-shape-draft.md` ("Turn on what you want", "Who keeps the services alive", "One configuration, four ways to change it", base items B1 and B3); `docs/superpowers/specs/2026-11-23-aimux-governance-metering-and-evals-design.md` ("The entry wrapper"). Tracker: `docs/superpowers/plans/governance-roadmap.md`, stage 2.

<!-- no-images: one table of tasks and one of decisions; the draft carries the figures -->

## What stages 1 and 3 already did for this one

The tracker's stage 2 was written before them. Two of its pieces arrived early:

- **Identity.** "A background call carries its session in the ledger" is the stage's first done-when line. Since stage 1 the proxy reads the session from the header Claude Code sends on every request, a subagent's and a child session's included (`../specs/2026-11-25-turn-identity.md`, `../specs/2026-11-25-second-engine-evidence.md`). What is left is to show it on a day of real use, which task 6 does.
- **Half of the entry wrapper.** `aimux model proxy env` already points a shell at the proxy for both engines. What is left is the Codex-only form the owner uses every day (`claude-codex`, a shell function he maintains), and telemetry.

So this stage is mostly the registry, the file and the job files.

## Global Constraints

- The owner runs anything that restarts a live service. `--apply` on a job that is running says what will restart and asks, unless `--yes` is given.
- With no `parts:` section in the file, aimux changes no job file. Nothing moves until the owner asks.
- A job file aimux did not write is never overwritten silently: the first apply shows the difference and keeps the old file beside the new one.
- A job that fails its health check after an apply gets its previous job file and its previous program back, as `aimux upgrade` does for the program today.
- The file holds what the owner wants, never what aimux found or wrote, and never a secret.
- A command that edits the file keeps the owner's comments, order and unknown keys, byte for byte outside the value it changes.
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
| 4 | `aimux engine add`, `remove`, `list`; `aimux enable`, `disable` | Turn the Anthropic engine or the gateway on with one command |
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
	Port    int
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

- [ ] **Step 1: Write the failing tests.** `TestServices_AreTheOnesStatusAndUpgradeKnow` pins ids, labels and ports against today's literals (`com.aimux.model-proxy` 7385, `com.aimux.mcp-proxy` 7381, `com.azaalouk.aimux-collect` 4318, `com.aimux.tool-gateway` 7390 unmanaged). `TestNoServiceLabelOutsideTheRegistry` walks `cmd/` and `internal/` and fails on a launchd label literal anywhere else, tests excepted.
- [ ] **Step 2: Implement, then replace the two tables.** `aimux status` and `aimux upgrade --dry-run --json` print the same bytes before and after; `TestStatus_*` and `TestUpgrade_*` pass unchanged.
- [ ] **Step 3: Full suite, commit, pull request.**

### Task 2: The `parts:` section

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
	Engines map[string]Engine         // inference-proxy only: "anthropic", "codex", or a named upstream
}
type Engine struct {
	Address     string // a named upstream's address; empty for anthropic and codex
	ServiceTier string // codex only
}

func Load(path string) (Settings, error)             // a missing file or section is the zero value
func (s Settings) Validate() []Problem               // unknown part, bad address, a name the proxy would refuse
func (s Settings) Args(service Service) []string     // the model proxy: --anthropic, --messages-upstream n=a, --service-tier

// Set changes one value under parts: and nothing else in the file. path is like
// {"inference-proxy","engines","anthropic"}; a nil value removes the key.
func Set(file string, path []string, value any) error
```

- [ ] **Step 1: Write the failing tests** from fixtures: `TestLoad_*` (absent, empty, full, an old file with none of it); `TestArgs_*` (each engine alone and together, in a fixed order so a job file does not change when nothing did); `TestSet_KeepsEverythingElse` (comments above and beside the key, anchors, flow style, no trailing newline, CRLF: the bytes outside the changed value are identical); `TestSet_CreatesTheSectionAtTheEnd`; `TestValidate_*`.
- [ ] **Step 2: Implement, add `aimux setup --check`** (prints each part, on or off, and what `--apply` would change; exits 1 when something would change; changes nothing).
- [ ] **Step 3: Full suite, docs, commit, pull request.**

### Task 3: Job files from the registry and the file

**Files:**
- Create: `internal/parts/jobfile.go`, `internal/parts/jobfile_test.go`, `internal/controller/apply.go`, `internal/controller/apply_test.go`, `cmd/aimux/cmd/setup_apply_live_test.go` (build tag `launchdlive`)
- Modify: `cmd/aimux/cmd/setup.go` (`--apply`, `--yes`), `internal/mcpproxy/launchd.go` (the plist writer takes arguments and an environment), docs

**Interfaces:**
- Produces:

```go
// JobFile is the launchd job for service running program with the settings' arguments. keep is what an existing
// file holds that the generator does not decide: its environment variables. Nothing else is carried over.
func JobFile(service Service, program string, args []string, keep Kept) []byte
func ReadKept(existing []byte) (Kept, error)

// ApplyParts makes each managed service's job match. With Check it only reports.
func ApplyParts(home string, s parts.Settings, opts ApplyOptions) ([]JobChange, error)
type JobChange struct {
	Service string
	Action  string   // "none", "write", "write and restart", "start", "stop"
	Was, Now []string // the arguments before and after
	Adopted bool     // the file was not one aimux wrote; the old one is kept as <label>.plist.before-aimux
}
```

- An apply that changes a running job goes through `UpgradeServices`' restart: reload, wait for the service's health answer, and on failure put back the previous job file and reload again.
- A job file aimux wrote carries a comment line saying so; that line is how `Adopted` is decided.
- [ ] **Step 1: Write the failing tests.** `TestJobFile_*`: the model proxy's file for each settings combination, compared with fixtures; the collector's and the MCP proxy's equal what is installed today apart from the marker line. `TestApplyParts_Check`: reports and writes nothing. `TestApplyParts_NothingStated`: zero value, no change. `TestApplyParts_RollsBackTheJobFile`: a health probe that never answers. `TestSetupApplyLive_AgainstLaunchd`: a job of the test's own, arguments changed, restarted, rolled back.
- [ ] **Step 2: Implement.**
- [ ] **Step 3: The owner runs it:** `aimux setup --check`, then `aimux setup --apply`, on his stable model proxy. Expected: one change, "model proxy: adopted", the same arguments as today, a restart, status ok.
- [ ] **Step 4: Full suite, docs, commit, pull request.**

### Task 4: `aimux engine` and `aimux enable`

**Files:**
- Create: `cmd/aimux/cmd/engine.go`, `cmd/aimux/cmd/engine_test.go`, `cmd/aimux/cmd/enable.go`, `cmd/aimux/cmd/enable_test.go`
- Modify: `internal/controller/apply.go`, `internal/e2e/` (the new commands), docs, README

**Interfaces:**
- Produces: `aimux engine list` (each engine of the inference proxy, on or off, with what the running proxy's `/healthz` says beside it); `aimux engine add anthropic`; `aimux engine add <name>=<address>`; `aimux engine remove <name>`; `aimux enable <part>`; `aimux disable <part>`. Each edits the file with `parts.Set`, prints what `--apply` would do, and applies after a yes (`--yes` to skip the question, `--no-apply` to only edit). `--json` on all.
- `aimux engine add <name>=<address>` checks that the secret-store entry `<name>-key` exists, without reading it, and says how to create it when it does not.
- [ ] **Step 1: Write the failing tests**, the e2e ones included: add, list, remove, each with `--no-apply` against a temp home; a bad name and a bad address refused before the file is touched; `disable inference-proxy` stops the job and leaves the file saying so.
- [ ] **Step 2: Implement. Step 3: The owner runs** `aimux engine add anthropic` on his machine. Expected: the stable proxy restarts with `--anthropic`, and `aimux model proxy env` now succeeds against it. This is also the last step of stage 3.
- [ ] **Step 4: Full suite, docs, commit, pull request.**

### Task 5: The parts menu

**Files:**
- Modify: `cmd/aimux/cmd/setup.go`, `cmd/aimux/cmd/setup_test.go`, docs

- `aimux setup` on a terminal opens with the list of parts, on or off, one line each, and "Turn on:" taking numbers. What it does today (the sandbox, tools, router, browser and gateway checks) runs for the parts that are on, under the part it belongs to. Without a terminal, and with `--json`, it behaves as today plus a `parts` list; the schema version goes up by one.
- Parts that are listed and not built yet (schedules, evals) are shown as "not available yet" and cannot be chosen.
- [ ] **Step 1: Write the failing tests** against a scripted terminal: the list, choosing two, a wrong number, an empty answer, `--json`.
- [ ] **Step 2: Implement. Step 3: Full suite, docs, commit, pull request.**

### Task 6: The Codex-only line, telemetry, and the close

**Files:**
- Modify: `cmd/aimux/cmd/model_proxy_env.go`, `internal/modelproxy/engines.go` (`SessionEnv`), tests, docs, the tracker

- `aimux model proxy env --codex [model]` prints what the owner's `claude-codex` function sets today: the proxy's address, the key as the API key, and the model pinned to `codex:<model>` (default `gpt-6.2-sol`). It needs no `--anthropic` on the proxy. `--off` undoes both forms.
- Both forms also print the collector's telemetry variables when the collector answers, so a session started this way is in the collector's totals.
- [ ] **Step 1: Tests, implement.**
- [ ] **Step 2: The owner replaces the body of `claude-codex`** with `lines="$(aimux model proxy env --codex "$@")" && eval "$lines"`, keeping his HUD lines. aimux does not edit his shell file.
- [ ] **Step 3: Close the stage.** After a day of use: every `aimux.side` line in the stable ledger has a session (`jq` over `~/.aimux/ledger/model-proxy.jsonl`); `aimux setup --check` reports nothing to change; the model proxy's job file carries aimux's marker. Mark stage 2 done in the tracker.

## Decisions for the owner

| # | Decision | Recommended | The other way |
|---|---|---|---|
| 1 | How an engine is turned on | `aimux engine add anthropic`, `aimux engine add enmaas=<address>` | A flag on `aimux enable inference-proxy`; fewer commands, harder to read back |
| 2 | What the first apply does with the job file that exists | Keeps its environment variables as they are, replaces the rest, keeps the old file beside it | Writes a minimal environment; cleaner, and likely to break `codex-lean` on the service's PATH |
| 3 | Who owns the shell function | The owner keeps `claude-codex`; its body becomes one line that calls aimux | aimux installs its own shell integration; one more thing that edits `~/.zshrc` |

## Not in this stage

- A daemon, and systemd job files. The registry is written so a second generator can be added.
- The tool gateway's job, and merging the two processes behind the MCP proxy.
- The dashboard's setup page. It reads the same file later.
- Renaming `aimux model proxy` to `aimux inference proxy`.
- A project's `.aimux/config.yaml` narrowing parts or engines. That comes with routing, in stage 4.
- Putting the inference proxy in front of every session. That is stage 7, behind its checklist.

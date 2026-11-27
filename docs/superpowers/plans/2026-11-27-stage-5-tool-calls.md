# Stage 5: Tool Calls in the Ledger Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One session's model calls and tool calls can be read in order from the ledger, each with where the record came from, so the owner sees what his agents did and stage 6 can tell one kind of work from another.

**Architecture:** Claude Code already sends a telemetry event for every tool call to aimux's collector. The collector copies a fixed list of facts from each such event into a ledger line, and nothing else from it. It is the one writer of `collector.jsonl`. The MCP proxy writes a second, lesser line for each MCP call it carries, which names the MCP tool (telemetry does not). A reader in the controller joins the lines of one session into a timeline and says what it could not see. No hook is changed.

**Why telemetry and not a hook.** Measured on one session observed three ways (evidence note, second run): transcript, hooks and telemetry each saw the same eleven tool calls. Telemetry costs nothing per call, cannot disturb the hook the owner's permission alerts run on, has no trouble with a large tool result, and alone carries the subagent that started a subagent, the model call that asked for a tool call, and the model calls of a session that does not go through the proxy. Its two gaps are known: an event the collector was not there to receive is lost, and an MCP tool is named only `mcp_tool` unless tool details are switched on. The first is shown by comparing the ids in the ledger with the ids in the session's transcript, which is on disk. A reporting hook is a later task, taken only if a week of real use shows the gaps matter.

**This is a pilot on evidence from one session and one version of Claude Code (2.1.332).** It showed that the approach works; it did not show that it holds for a resumed session, an interactive one, or a session that ends abruptly. Task 1 measures those before anything is written to the owner's ledger, and the owner's close compares a week of his sessions against their transcripts.

**A change from the design, for the owner to accept or refuse.** The design has a hook report each tool call through the collector, with a spool for when the collector is down. This plan has no reporting hook. What is given up, plainly:

- Tool calls made while the collector is down are lost for good; a hook's spool would have kept them. The timeline says how many are missing by comparing with the transcript, and cannot recover them.
- A session with telemetry switched off is not recorded at all.
- A hook is told the exact name of an MCP tool, whether a failure was an interrupt, and the kind of subagent on every tool event. Telemetry gives the first only with tool details on, does not give the second, and gives the third on model calls and at the subagent's end.

**The trace and the ledger are not the same thing.** The trace holds everything a session did, content included, in the collector's memory, as the harness reports it about itself. The ledger holds names, counts, timings and outcomes, on disk, with no content, and is the only record of what aimux itself did: which engine answered, a switch, a limit, later a routing choice. The trace is for "what exactly happened in this session"; the ledger is for "how much, on what, by whom, and is anything missing", over time and in a form that can be kept and shown. This plan builds the tool-call part of the ledger from the trace's events: the trace feeds the ledger and does not replace it.

**Tech Stack:** Go; the existing `internal/ledger`, `internal/otel` (collector), `internal/mcpproxy`, `internal/controller`.

**Spec:** `docs/superpowers/specs/2026-11-23-aimux-governance-metering-and-evals-design.md` (sections 1 to 3), `docs/ledger-identity.md`, `docs/superpowers/specs/2026-11-25-turn-identity.md`. Evidence: `docs/superpowers/specs/2026-11-27-tool-call-evidence.md`.

## Global Constraints

- A line holds names, counts, timings and outcomes. Never a prompt, an answer, a tool's arguments, a command, a file path, an error text, an email address or an account id.
- The collector builds each line afresh from the fields this plan names, each checked for its own form: an id must look like an id of its kind (a session id, a `toolu_` id, a `req_` id, an agent id), a duration must be a number, an error type must be one of a fixed list or it is written `other`. A value that fails its check is left out and counted, never written. A field this plan does not name is never read.
- The sessions aimux starts have prompt logging and tool details switched on (`OTELEnv` sets `OTEL_LOG_USER_PROMPTS=1` and `OTEL_LOG_TOOL_DETAILS=1`, for the trace view), so the events reaching the collector do carry prompts, commands and arguments. That is why the rule above is a tested one: there is a test with real events that hold all of them, asserting none of it is in the ledger file or in anything the collector logs about a line it refused.
- Nothing about how the collector receives, stores or exports spans changes: traces, the trace view and export keep working as they do.
- Schema stays `aimux.ledger/1`. New fields are optional, and a reader that does not know them ignores them.
- `aimux cost` and `GET /api/meter` keep their figures: a tool-call line is never counted as a model call, no model-call line is written from telemetry in this plan, and a gap in the collector's file is reported as the collector's.
- What the ledger did not see is said, never left out: tool calls the transcript has and the ledger does not, a session with no telemetry, an MCP call with no name. Where the transcript is gone, the timeline says coverage is not known.
- The tool gateway is not changed in this plan (the Wingman session's side; later, coordinated).
- Controller first; no mocks; unit, CLI and API tests; docs in the same push.

## What a tool-call line holds

```json
{"schema":"aimux.ledger/1","kind":"tool_call","writer":"collector","seq":812,"event_id":"toolu_01Vu…:tool_result",
 "at":"2026-11-27T19:02:11Z","session":"4a09fd61-…","aimux.prompt_id":"c16292f4-…","call":"toolu_01Vu…",
 "aimux.harness":"claude-code","aimux.role":"subagent","aimux.agent":"a896f925…","aimux.agent_parent":"a1c3e350…",
 "aimux.asked_by":"req_011ChU5A…","gen_ai.tool.name":"Bash","aimux.duration_ms":2453,
 "aimux.outcome":"error","aimux.error_type":"ShellError"}
```

| Field | From the telemetry event's | Note |
|---|---|---|
| `at` | `event.timestamp` | when the call ended, not when the line was written; the timeline orders by it |
| `session`, `call` | `session.id`, `tool_use_id` | |
| `aimux.prompt_id` | `prompt.id` | the harness's own id for the turn. `turn` is left out: it is the proxy's ordinal, which a collector line does not know, and the capture already has a call where the two disagree. The two are never mixed in one field |
| `aimux.agent`, `aimux.agent_parent` | `agent_id`, `parent_agent_id` | |
| `aimux.asked_by` | `request_id` | the model call that asked for this tool call; an opaque id, kept only for that join |
| `gen_ai.tool.name`, `aimux.tool.server` | `tool_name`; for an MCP tool, the two names inside `tool_parameters` when tool details are on | `mcp_tool` and no server when they are not. Only those two names are taken from `tool_parameters`, each checked as a name; nothing else in it is read |
| `aimux.duration_ms`, `aimux.outcome`, `aimux.error_type` | `duration_ms`, `success`, `error_type` | outcome `ok` or `error`; the type is one of a fixed list of classes (`ShellError`, …) or `other` |

A name is written only when it looks like one (at most 128 characters of letters, digits, `-`, `_`, `.`, `:`); anything else becomes `other`.

The kind of a subagent is not on a tool-call line. It is its own line, written once per subagent when the collector first learns it (from a model call's `agent.name`, or from `subagent_completed`):

```json
{"schema":"aimux.ledger/1","kind":"agent","writer":"collector","session":"4a09fd61-…","aimux.agent":"a896f925…",
 "aimux.agent_parent":"a1c3e350…","aimux.agent_type":"general-purpose"}
```

A reader finds a subagent's kind from that line whenever it reads, so a collector that restarted, or an event that came late, does not leave a call without its kind for good.

## Review Focus

- Telemetry with prompt logging and tool details switched on: no prompt, command or argument reaches a line.
- The collector is restarted in the middle of a session: the lines before and after are there once each, and the timeline says how many tool calls the transcript has that the ledger does not.
- A session resumed after a restart, and one continued after `/clear`: no line is dropped as a repeat of another session's, and none is written twice.
- The same event arrives twice (the exporter retries a batch): one line.
- Two subagents at once, and a subagent started by a subagent: each call carries its own agent and its parent.
- A session that does not go through the model proxy: its tool calls are shown, and the timeline says its model calls were not seen.

## Tasks

### Task 1: Finish the evidence

**Files:** Modify `docs/superpowers/specs/2026-11-27-tool-call-evidence.md`.

- [ ] Repeat the three-way count (transcript, hooks, telemetry) on: an interactive session, a session resumed after a restart, a session continued after `/clear`, and one whose process is killed right after its last tool call. Record what agrees and what does not.
- [ ] Stop the capture for 3, 10 and 30 seconds in the middle of a session: what arrives and what is lost.
- [ ] With tool details on, as the owner's sessions have them: where exactly are an MCP tool's server and name, and are they there for every MCP call?
- [ ] A tool call the owner refuses at a permission prompt: is there a `tool_result`, or only a `tool_decision`? The count against the transcript must not call those missing.
- [ ] Send real events as protobuf to the installed collector on both of its paths (`/v1/logs` and `/`): does the session id it files them under equal Claude Code's `session.id`, or an alias of aimux's?
- [ ] Record the results. If a kind of session loses tool calls that the transcript comparison cannot show, stop and bring it to the owner.

### Task 2: The collector is a part of aimux

The ledger now depends on the collector, and nothing in aimux's settings says it should run: it is in `aimux status` and `aimux upgrade`, and not in the parts list, the config, or `aimux setup`. Its job is still named `com.azaalouk.aimux-collect`, from before the others were `com.aimux.*`.

**Files:** Modify `internal/parts/registry.go`, `internal/parts/settings.go`, `internal/parts/jobfile.go`, `internal/controller/partsmenu.go`, `internal/controller/upgrade.go`, `cmd/aimux/cmd/setup.go`, `cmd/aimux/cmd/collect.go`; tests beside them.

**Interfaces:** Produces a part `collector` ("Collector": "receives your sessions' telemetry: traces, and the tool calls in the ledger"), with service `collector`; settings `parts.collector.enabled`, `parts.collector.port`, `parts.collector.ledger` (true by default); the service's label `com.aimux.collector`, with `LegacyLabels []string{"com.azaalouk.aimux-collect"}` on `parts.Service`.

- [ ] Test: the part is in the list `aimux setup` shows, and `aimux enable collector` and `aimux disable collector` state it in the config and plan the change like the two proxies.
- [ ] Test: `aimux setup --check` on a machine whose collector runs under the old label reports it as taken under management with a rename, and changes nothing; `--apply` writes the new job, starts it, waits for it to answer, and only then removes the old one. If the new one does not answer, the old job is put back and started, and the command says so and exits 1.
- [ ] Test: `aimux status` and `aimux upgrade collector` find the collector under either label, and never report two.
- [ ] Test: `parts.collector.ledger: false` starts the collector without a ledger writer, and its health says `ledger_off`.
- [ ] Test (build tag `launchdlive`): the rename against the real launchd on a job of its own.
- [ ] Implement. The owner runs the apply on his machine; it restarts the collector.

### Task 3: The line

**Files:** Modify `internal/ledger/line.go`, `internal/ledger/count.go`; tests beside them.

**Interfaces:** Produces on `ledger.Line`: `PromptID` (`aimux.prompt_id`), `ToolName` (`gen_ai.tool.name`), `ToolServer` (`aimux.tool.server`), `AgentType` (`aimux.agent_type`), `AgentParent` (`aimux.agent_parent`), `AskedBy` (`aimux.asked_by`), `ErrorType` (`aimux.error_type`); `KindAgent = "agent"`; `OutcomeError = "error"`; `ledger.Name(s string) string` (the name rule above).

- [ ] Test: a line with the new fields round-trips; a line without them reads as before.
- [ ] Test: `Name` keeps `general-purpose` and `search_messages`, and gives `other` for a name with a space, a slash, or 129 characters.
- [ ] Test: a build from before this plan reads a file that holds `agent` lines and the new fields: `aimux cost` gives the same figures (the meter keeps model calls only).
- [ ] Test: `Count` still drops a repeated event id, and a secondary line whose call a primary line has.
- [ ] Implement.

### Task 4: The collector writes a line per tool call

**Files:** Create `internal/otel/toolcalls.go`; modify `internal/otel/receiver.go` (one call where a log event is handled, and health); `internal/ledger/writer.go`; tests beside them.

**Interfaces:** Consumes task 3. Produces `otel.ToolCallLine(attrs map[string]any) (ledger.Line, bool)` and `otel.AgentLine(attrs map[string]any) (ledger.Line, bool)`, pure functions from one event's attributes to a line; `(*ledger.Writer).Append(ledger.Line) error`, which says when a line is not on disk. The collector's health gains `ledger_seq`, `ledger_failed`, `ledger_refused` (values that failed their check), `ledger_off`.

- [ ] Test: the attributes of a real `tool_result` event (the names in the evidence note) give the line above; `success` `false` gives outcome `error` with its `error_type`; an `error_type` not on the list gives `other`.
- [ ] Test: from events holding `prompt`, `prompt_text`, `tool_input`, `tool_parameters`, `user.email`, `user.id`, `user.account_uuid` and `organization.id`, none of their values is in the ledger file, except an MCP tool's server and name. A value put where an id belongs (a sentence as `tool_use_id`, a path as `agent_id`, a secret-looking word as `tool_name`) is left out or written `other`, and is not in the collector's log either.
- [ ] Test: the same event received twice is one line: at once, after a restart of the collector, and when the first copy is further back in the file than the tail read at start (the collector keeps the ids it wrote for a session in memory and reads the whole of that session's lines back when it first sees the session again).
- [ ] Test: an `agent` line is written once per subagent, from whichever of `api_request` and `subagent_completed` comes first, and again only if a later event names a different kind.
- [ ] Test: a failed append is counted in `ledger_failed`, does not stop spans from being stored, and the same event sent again is then written; a second collector on the same file is refused by the writer's lock and says so in health; the TUI's own receiver, which has no ledger, writes none and says `ledger_off`.
- [ ] Test: the span the collector stores for the event is what it stored before this change, compared field by field.
- [ ] Implement.

### Task 5: The meter stays right

**Files:** Modify `internal/controller/meter.go`, its tests; `cmd/aimux/cmd/cost.go`.

- [ ] Test: with tool-call lines and a gap in `collector.jsonl`, `aimux cost` shows the same requests and tokens as without them, and `incomplete` names the writer with the gap.
- [ ] Test: `aimux cost --by tool` gives calls, failures and time per tool.
- [ ] Implement. Release tasks 2 to 5 and run `aimux upgrade`: from here the owner's ledger holds his tool calls.

### Task 6: The MCP proxy's lesser line

**Files:** Modify `internal/mcpproxy/pool.go`, `internal/mcpproxy/server.go`, `internal/mcpproxy/daemon.go`; create `internal/mcpproxy/ledgerline.go`; tests beside them.

**Interfaces:** Consumes task 3. Produces one `tool_call` line per `tools/call` in `mcp-proxy.jsonl`, marked `secondary`, with `aimux.tool.server`, `gen_ai.tool.name`, duration, outcome, and the call id when task 1 found one in the request.

- [ ] Test: a call through the proxy writes one secondary line; a failed start, a timeout, a result marked as an error, and a call the proxy refuses before forwarding each give outcome `error`; the arguments are not in the line.
- [ ] Test: the MCP proxy's line is never joined to a telemetry line by time or by process: two sessions calling the same tool at once would be mixed up. It is shown in the timeline only when the request carried the harness's call id; otherwise it is counted as a call the proxy carried for an unknown session.
- [ ] Implement; `aimux mcp proxy status` reports `ledger_seq` and whether the ledger is off.

### Task 7: The timeline

**Files:** Create `internal/controller/timeline.go`, `cmd/aimux/cmd/timeline.go`; modify `internal/frontend/web/handlers.go`, `server.go`, `smoke_test.go`; tests beside them.

**Interfaces:** Produces `controller.Timeline(dir, session string) (TimelineReport, error)` with `Entries []TimelineEntry{At, Turn, PromptID, Kind, Agent, AgentParent, AgentType, What, AskedBy, DurationMS, Outcome, Source}` and `NotSeen []string`. `controller.TranscriptToolIDs(file string) ([]string, error)` reads the ids of the tool calls in a transcript and nothing else of it. CLI `aimux timeline <session>` (`--json`; a prefix of the id accepted). Web `GET /api/timeline?session=`.

- [ ] Test: a session with model calls, built-in tool calls, an MCP call, two subagents at once and a nested one comes out in the order things happened (by `at`), each entry naming its source; a tool call is shown under the model call that asked for it when `asked_by` matches one, and on its own when not.
- [ ] Test: tool calls the transcript has and the ledger does not are one `NotSeen` line with their number; calls refused at a permission prompt are counted apart, as task 1 found them; with no transcript on disk the line says coverage is not known.
- [ ] Test: a session with no telemetry says so; a session outside the proxy says its model calls were not seen.
- [ ] Test: a model call from the proxy and a tool call from the collector sit in one timeline by time; the tool call is drawn under a model call only where its `asked_by` equals an id the proxy's line carries, and today's proxy lines carry none, so the test pins that nothing is drawn by guess.
- [ ] Test: an ambiguous prefix is an error naming the candidates, as `aimux cost --session` does.
- [ ] Test (e2e): `aimux timeline <id> --json` validates; (smoke) the endpoint answers.
- [ ] Implement.

### Task 8: What kind of work a subagent did

**Files:** Create `internal/controller/subagentwork.go`, test beside it; modify `cmd/aimux/cmd/cost.go`.

**Interfaces:** Produces `controller.SubagentWork(dir string, since time.Time) []SubagentRun{Session, Agent, AgentParent, AgentType string; ModelCalls, ToolCalls int; Tokens int64; Did string}`, the kind taken from the subagent's `agent` line and `unknown` when there is none, where `Did` is `asked to change files` (an Edit, Write, MultiEdit or NotebookEdit that did not fail), `gathers` (WebSearch, WebFetch or an MCP tool), `runs commands` (Bash, no write tool) or `reads` (the rest). The label says what tools a run used, not what it achieved: a shell command can write a file, and that is not seen.

- [ ] Test: runs with each mix of tools get the right label; a run with model calls and no tool lines is labelled `not seen`.
- [ ] Test: `aimux cost --by subagent` prints kind of subagent, what it did, runs, model calls and tokens.
- [ ] Implement. This replaces `scripts/helper-work.py` as the source for stage 6. The script stays for comparison until the two agree on the same week and the same runs (`general-purpose` only, the script's two write labels counted as one, failed write attempts counted the same way on both sides, runs with no tool lines set aside).

### Task 9: Docs, tracker, and the owner's close

- [ ] Docs: the ledger page; the cost guide (`--by tool`, `--by subagent`, `aimux timeline`); what the collector copies from telemetry and never copies; the parity table.
- [ ] Tracker: stage 5 row; the unverified items 2, 4 and 14 with what was found; the change from the design in the decision log.
- [ ] The owner's close, after a week of normal use: for three of his sessions, the timeline's own count of what is missing against the transcript is zero, or small and explained; `aimux timeline` on one shows its model calls, built-in tools and MCP tools in order; `aimux cost --by subagent` agrees with the script as task 8 says.
- [ ] Then decide, from that week: is a reporting hook needed for what telemetry lost? The rules for it are kept in the evidence note (alert first, report second, its own switch, the reading side first, a reader that does not need the whole input).

## Not in this plan

- A hook that reports tool calls (see task 9), and any hook that refuses one.
- The tool gateway's lines (sandboxed sessions): coordinated with the Wingman session, after this.
- Model-call lines written from telemetry, for sessions that do not go through the proxy. The proxy gives each call an id of its own, so the two records of one call cannot be told to be the same, and a reader from before this plan would add both. It waits until the proxy's line carries the upstream's request id for every engine and the pairing is proven call by call.
- Codex's telemetry: after Claude Code works, as the design orders it.
- Sandbox verdicts from OpenShell's OCSF log (what a sandbox was allowed and refused): the collector's next reader after this one, and the input for policy from sessions. It belongs with the sandbox work (4d and 5d on the tracker), since it says nothing about model calls or cost. Exporting the ledger as OCSF, for tools that read it, is later still.
- Whether a file changed was code or a document: that needs the file's name, which a line may not hold.
- The TUI and dashboard views of the timeline (stage 5b).

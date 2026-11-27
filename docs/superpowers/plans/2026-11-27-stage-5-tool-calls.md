# Stage 5: Tool Calls in the Ledger Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One session's model calls and tool calls can be read in order from the ledger, each with where the record came from, so the owner sees what his agents did and stage 6 can tell one kind of work from another.

**Architecture:** Claude Code already sends a telemetry event for every tool call to aimux's collector. The collector copies a fixed list of facts from each such event into a ledger line, and nothing else from it. It is the one writer of `collector.jsonl`. The MCP proxy writes a second, lesser line for each MCP call it carries, which names the MCP tool (telemetry does not). A reader in the controller joins the lines of one session into a timeline and says what it could not see. No hook is changed.

**Why telemetry and not a hook.** Measured on one session observed three ways (evidence note, second run): transcript, hooks and telemetry each saw the same eleven tool calls. Telemetry costs nothing per call, cannot disturb the hook the owner's permission alerts run on, has no trouble with a large tool result, and alone carries the subagent that started a subagent, the model call that asked for a tool call, and the model calls of a session that does not go through the proxy. Its two gaps are known: an event the collector was not there to receive is lost, and an MCP tool is named only `mcp_tool`. The first is shown (each event has a sequence number per session), the second is filled from the MCP proxy's line. A reporting hook is a later task, taken only if a week of real use shows the gaps matter.

**A change from the design, for the owner to accept or refuse.** The design has a hook report each tool call through the collector, with a spool for when the collector is down. This plan has no reporting hook. What is given up: tool calls made while the collector is down (it restarts in seconds on an upgrade; the ledger says how many events are missing), and sessions with telemetry switched off.

**Tech Stack:** Go; the existing `internal/ledger`, `internal/otel` (collector), `internal/mcpproxy`, `internal/controller`.

**Spec:** `docs/superpowers/specs/2026-11-23-aimux-governance-metering-and-evals-design.md` (sections 1 to 3), `docs/ledger-identity.md`, `docs/superpowers/specs/2026-11-25-turn-identity.md`. Evidence: `docs/superpowers/specs/2026-11-27-tool-call-evidence.md`.

## Global Constraints

- A line holds names, counts, timings and outcomes. Never a prompt, an answer, a tool's arguments, a command, a file path, an error text, an email address or an account id.
- The collector copies from a telemetry event only the fields this plan names. A field it does not name is never written, whatever the session's telemetry settings send (`OTEL_LOG_USER_PROMPTS`, `OTEL_LOG_TOOL_DETAILS`). There is a test with both switched on.
- Nothing about how the collector receives, stores or exports spans changes: traces, the trace view and export keep working as they do.
- Schema stays `aimux.ledger/1`. New fields are optional, and a reader that does not know them ignores them.
- `aimux cost` and `GET /api/meter` keep their figures: a tool-call line is never counted as a model call, a model call seen by both the proxy and telemetry is counted once, and a gap in the collector's file is reported as the collector's.
- What the ledger did not see is said, never left out: events missing from a session's sequence, a session with no telemetry, an MCP call with no name.
- The tool gateway is not changed in this plan (the Wingman session's side; later, coordinated).
- Controller first; no mocks; unit, CLI and API tests; docs in the same push.

## What a tool-call line holds

```json
{"schema":"aimux.ledger/1","kind":"tool_call","writer":"collector","seq":812,"event_id":"toolu_01Vu…:tool_result",
 "at":"2026-11-27T19:02:11Z","session":"4a09fd61-…","turn":"c16292f4-…","call":"toolu_01Vu…",
 "aimux.harness":"claude-code","aimux.role":"subagent","aimux.agent":"a896f925…","aimux.agent_parent":"a1c3e350…",
 "aimux.agent_type":"general-purpose","aimux.asked_by":"req_011ChU5A…",
 "gen_ai.tool.name":"Bash","aimux.duration_ms":2453,"aimux.outcome":"error","aimux.error_type":"ShellError"}
```

| Field | From the telemetry event's | Note |
|---|---|---|
| `at` | `event.timestamp` | when the call ended, not when the line was written; the timeline orders by it |
| `session`, `call` | `session.id`, `tool_use_id` | |
| `turn` | `prompt.id` | the harness's own id for the turn, kept as it is; left out when absent |
| `aimux.agent`, `aimux.agent_parent` | `agent_id`, `parent_agent_id` | |
| `aimux.agent_type` | the `agent.name` of that agent's model calls, or `agent_type` of its `subagent_completed` | left out until one of them has been seen |
| `aimux.asked_by` | `request_id` | the model call that asked for this tool call |
| `gen_ai.tool.name` | `tool_name` | `mcp_tool` for an MCP tool; its real name comes from the MCP proxy's line |
| `aimux.duration_ms`, `aimux.outcome`, `aimux.error_type` | `duration_ms`, `success`, `error_type` | outcome `ok` or `error`; the type is a class such as `ShellError`, never a message |

A name is written only when it looks like one (at most 128 characters of letters, digits, `-`, `_`, `.`, `:`); anything else becomes `other`.

The turn on a tool-call line is the harness's id; on a model-call line from the proxy it is the proxy's number. The timeline joins the two by the telemetry model-call lines of task 5, which carry both the harness's turn id and the request the proxy also saw.

## Review Focus

- Telemetry with prompt logging and tool details switched on: no prompt, command or argument reaches a line.
- The collector is restarted in the middle of a session: the lines before and after are there once each, and the timeline says how many events are missing between them.
- The same event arrives twice (the exporter retries a batch): one line.
- Two subagents at once, and a subagent started by a subagent: each call carries its own agent and its parent.
- A session that does not go through the model proxy: its tool calls and its model calls are both shown, the model calls marked as seen by telemetry only.

## Tasks

### Task 1: Finish the evidence

**Files:** Modify `docs/superpowers/specs/2026-11-27-tool-call-evidence.md`.

- [ ] Does an MCP request reaching the MCP proxy carry the `tool_use_id` (`_meta`, a header)? Record it. If not, record what does join an MCP proxy call to the telemetry's `mcp_tool` event: the session's owning process and the time, and how reliably.
- [ ] Restart the capture for 3, 10 and 30 seconds in the middle of a session: what arrives, what is lost, and whether the sequence numbers show the hole.
- [ ] End a session at once after its last tool call: does the last batch arrive?
- [ ] Record the results. If a short restart loses events without a visible hole, stop and bring it to the owner.

### Task 2: The line

**Files:** Modify `internal/ledger/line.go`, `internal/ledger/count.go`; tests beside them.

**Interfaces:** Produces on `ledger.Line`: `ToolName` (`gen_ai.tool.name`), `ToolServer` (`aimux.tool.server`), `AgentType` (`aimux.agent_type`), `AgentParent` (`aimux.agent_parent`), `AskedBy` (`aimux.asked_by`), `ErrorType` (`aimux.error_type`); `OutcomeError = "error"`; `ledger.Name(s string) string` (the name rule above); `ledger.SplitTool(name string) (server, tool string)`.

- [ ] Test: a line with the new fields round-trips; a line without them reads as before.
- [ ] Test: `Name` keeps `general-purpose` and `mcp__slack__search`, and gives `other` for a name with a space, a slash, or 129 characters. `SplitTool("mcp__slack__search_messages")` is `("slack","search_messages")`; `SplitTool("Bash")` is `("","Bash")`.
- [ ] Test: `Count` still drops a repeated event id, and a secondary line whose call a primary line has.
- [ ] Implement.

### Task 3: The collector writes a line per tool call

**Files:** Create `internal/otel/toolcalls.go`; modify `internal/otel/receiver.go` (one call where a log event is handled, and health); `internal/ledger/writer.go`; tests beside them.

**Interfaces:** Consumes task 2. Produces `otel.ToolCallLine(attrs map[string]any, kinds AgentKinds) (ledger.Line, bool)`, a pure function from one event's attributes to a line; `AgentKinds`, what kind each agent of a session is, learned from `api_request` and `subagent_completed` events and forgotten an hour after a session's last event; `(*ledger.Writer).Append(ledger.Line) error`, which says when a line is not on disk. The collector's health gains `ledger_seq`, `ledger_failed`, `ledger_off`.

- [ ] Test: the attributes of a real `tool_result` event (the names in the evidence note) give the line above; `success` `false` gives outcome `error` with its `error_type`.
- [ ] Test: with `prompt`, `tool_input`, `tool_parameters`, `user.email`, `user.id`, `user.account_uuid` and `organization.id` among the attributes, none of their values is anywhere in the line. The test fails if `ToolCallLine` reads any attribute not on its list.
- [ ] Test: the same event received twice is one line, also across a restart of the collector (what was written is read back from the file's tail at start).
- [ ] Test: a tool call from an agent whose kind is not yet known is written without a kind, not held back.
- [ ] Test: a failed append is counted in `ledger_failed` and does not stop spans from being stored; a second collector on the same file is refused by the writer's lock and says so in health; the TUI's own receiver, which has no ledger, writes none and says `ledger_off`.
- [ ] Test: the span the collector stores for the event is byte for byte what it stored before this change.
- [ ] Implement.

### Task 4: The meter stays right

**Files:** Modify `internal/controller/meter.go`, its tests; `cmd/aimux/cmd/cost.go`.

- [ ] Test: with tool-call lines and a gap in `collector.jsonl`, `aimux cost` shows the same requests and tokens as without them, and `incomplete` names the writer with the gap.
- [ ] Test: `aimux cost --by tool` gives calls, failures and time per tool.
- [ ] Implement. Release tasks 2 to 4 and run `aimux upgrade`: from here the owner's ledger holds his tool calls.

### Task 5: Model calls the proxy did not see

**Files:** Modify `internal/otel/toolcalls.go`, `internal/ledger/count.go`, `internal/controller/meter.go`; tests beside them.

**Interfaces:** Produces `otel.ModelCallLine(attrs map[string]any) (ledger.Line, bool)`: one `model_call` line per `api_request` event, marked `secondary`, with session, the harness's turn id, agent, parent, kind, the request id as `call`, model, tokens and duration. `ledger.Count` drops it when the proxy wrote a line for the same call of the same session, and keeps it otherwise.

- [ ] Task 1 of this plan's evidence said the proxy's ledger and the telemetry agree on the number of model calls for a session. First find what the two lines share to be matched on (the request id the upstream answered with; today the proxy mints its own call id), and record it. If nothing is shared, the proxy's line gains the upstream's request id as a new optional field, written from the answer's header.
- [ ] Test: a session through the proxy is counted once, with the proxy's figures; a session outside it is counted from telemetry, and `aimux cost` marks those rows `seen by telemetry only`.
- [ ] Test: no cost figure from telemetry (`cost_usd`) is written: the ledger holds tokens, and prices come later from one table.
- [ ] Implement.

### Task 6: The MCP proxy's lesser line

**Files:** Modify `internal/mcpproxy/pool.go`, `internal/mcpproxy/server.go`, `internal/mcpproxy/daemon.go`; create `internal/mcpproxy/ledgerline.go`; tests beside them.

**Interfaces:** Consumes task 2. Produces one `tool_call` line per `tools/call` in `mcp-proxy.jsonl`, marked `secondary`, with `aimux.tool.server`, `gen_ai.tool.name`, duration, outcome, and the call id when task 1 found one in the request.

- [ ] Test: a call through the proxy writes one secondary line; a failed start, a timeout, a result marked as an error, and a call the proxy refuses before forwarding each give outcome `error`; the arguments are not in the line.
- [ ] Test: joined to the telemetry's `mcp_tool` line by the rule task 1 settled, the timeline shows the server and the tool; where the join is not certain, it shows `mcp_tool` and says the name is not known.
- [ ] Implement; `aimux mcp proxy status` reports `ledger_seq` and whether the ledger is off.

### Task 7: The timeline

**Files:** Create `internal/controller/timeline.go`, `cmd/aimux/cmd/timeline.go`; modify `internal/frontend/web/handlers.go`, `server.go`, `smoke_test.go`; tests beside them.

**Interfaces:** Produces `controller.Timeline(dir, session string) (TimelineReport, error)` with `Entries []TimelineEntry{At, Turn, Kind, Agent, AgentParent, AgentType, What, AskedBy, DurationMS, Outcome, Source}` and `NotSeen []string`. CLI `aimux timeline <session>` (`--json`; a prefix of the id accepted). Web `GET /api/timeline?session=`.

- [ ] Test: a session with model calls, built-in tool calls, an MCP call, two subagents at once and a nested one comes out in the order things happened (by `at`), each entry naming its source; a tool call is shown under the model call that asked for it when `asked_by` matches one, and on its own when not.
- [ ] Test: a hole in a session's event sequence is one `NotSeen` line saying how many events are missing and between which times.
- [ ] Test: a session with no telemetry says so; a session outside the proxy shows its model calls as seen by telemetry only.
- [ ] Test: an ambiguous prefix is an error naming the candidates, as `aimux cost --session` does.
- [ ] Test (e2e): `aimux timeline <id> --json` validates; (smoke) the endpoint answers.
- [ ] Implement.

### Task 8: What kind of work a subagent did

**Files:** Create `internal/controller/subagentwork.go`, test beside it; modify `cmd/aimux/cmd/cost.go`.

**Interfaces:** Produces `controller.SubagentWork(dir string, since time.Time) []SubagentRun{Session, Agent, AgentParent, AgentType string; ModelCalls, ToolCalls int; Tokens int64; Did string}` where `Did` is `asked to change files` (an Edit, Write or NotebookEdit that did not fail), `gathers` (WebSearch, WebFetch or an MCP tool), `runs commands` (Bash, no write tool) or `reads` (the rest). The label says what tools a run used, not what it achieved: a shell command can write a file, and that is not seen.

- [ ] Test: runs with each mix of tools get the right label; a run with model calls and no tool lines is labelled `not seen`.
- [ ] Test: `aimux cost --by subagent` prints kind of subagent, what it did, runs, model calls and tokens.
- [ ] Implement. This replaces `scripts/helper-work.py` as the source for stage 6. The script stays for comparison until the two agree on the same week and the same runs (`general-purpose` only, the script's two write labels counted as one, runs with no tool lines set aside).

### Task 9: Docs, tracker, and the owner's close

- [ ] Docs: the ledger page; the cost guide (`--by tool`, `--by subagent`, `aimux timeline`); what the collector copies from telemetry and never copies; the parity table.
- [ ] Tracker: stage 5 row; the unverified items 2, 4 and 14 with what was found; the change from the design in the decision log.
- [ ] The owner's close, after a week of normal use: for three of his sessions, the number of tool calls in the ledger equals the number in the transcript, or the timeline says how many are missing; `aimux timeline` on one shows its model calls, built-in tools and MCP tools in order; `aimux cost --by subagent` agrees with the script as task 8 says.
- [ ] Then decide, from that week: is a reporting hook needed for what telemetry lost? The rules for it are kept in the evidence note (alert first, report second, its own switch, the reading side first, a reader that does not need the whole input).

## Not in this plan

- A hook that reports tool calls (see task 9), and any hook that refuses one.
- The tool gateway's lines (sandboxed sessions): coordinated with the Wingman session, after this.
- Codex's telemetry: after Claude Code works, as the design orders it.
- Whether a file changed was code or a document: that needs the file's name, which a line may not hold.
- The TUI and dashboard views of the timeline (stage 5b).

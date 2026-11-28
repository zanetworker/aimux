# Stage 5: Tool Calls in the Ledger Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Everything a session does is tied to its session and its agent in one record: its model calls and its tool calls can be read in order from the ledger, each saying how it was learned, so the owner sees what his agents did and stage 6 can tell one kind of work from another.

**Architecture:** The model proxy is the record. Every request a harness sends to a model carries the tool results that just came back and the answer that asked for them; the proxy, which already writes a line per model call, writes a line per finished tool call from that, into the same file. Telemetry, through the collector, adds only what the proxy cannot know: how long a tool really took, and what kind a subagent is. Session and agent facts live on lines of their own, so a call line carries ids and nothing about who or where. Nothing depends on a hook, and nothing in the record depends on the collector being up.

**How this plan got here.** Four evidence runs (`2026-11-27-tool-call-evidence.md`): the transcript, the hooks, the telemetry and the traffic through the proxy each showed the same tool calls. A hook costs a process per call on the hook the owner's permission alerts use. Telemetry loses calls in any collector outage, however short. The proxy loses nothing a session did not lose itself, already owns the model-call lines, and is the only source that has a run's history in hand when it would choose a model (stage 6).

**The trace and the ledger are not the same thing.** The trace holds everything a session did, content included, in the collector's memory, as the harness reports it about itself. The ledger holds names, ids, timings and outcomes, on disk, with no content, and is the only record of what aimux itself did. The trace is for "what exactly happened in this session"; the ledger is for "how much, on what, by whom, and is anything missing".

**Tech Stack:** Go; the existing `internal/ledger`, `internal/modelproxy`, `internal/otel` (collector), `internal/parts`, `internal/controller`.

**Spec:** `docs/superpowers/specs/2026-11-23-aimux-governance-metering-and-evals-design.md` (sections 1 to 3), `docs/ledger-identity.md`, `docs/superpowers/specs/2026-11-25-turn-identity.md`. Evidence: `docs/superpowers/specs/2026-11-27-tool-call-evidence.md`.

## Identity: what everything hangs from

Four requirements from the owner shape this: everything connects to a session and an agent; more harnesses will come; some sessions run in a sandbox and some do not; and agent identity and acting on someone's behalf come later.

```text
session line   one per session    harness, its version, where it runs (host or sandbox, and which sandbox)
                                  later: who the session acts for, and as whom
agent line     one per subagent   its parent agent, its kind
call lines     model calls and tool calls: session id, agent id, their own ids. Nothing about who or where.
```

- **A call line carries ids only.** Who a session belongs to, which harness drives it and where it runs are facts of the session, written once on its `session` line. A fact that arrives later, such as an identity, is added there and reaches every call of the session without one call line changing.
- **An agent is named by the harness's own id** (`aimux.agent`), with its parent (`aimux.agent_parent`). The main conversation is the agent `root`, said outright, so that it is never confused with the harness's side work (`background`) or with a call whose agent is not known (left out). An id means something only within its session and harness; nothing reads one without both. A subagent's kind is on its `agent` line.
- **Harnesses differ at the edge, not in the line.** The proxy already reads a request into neutral facts (`CallFacts`) before anything is written; tool facts join them. Another front (the Responses API, for the Codex CLI) fills the same facts. A line never has a field that only one harness can fill; what a harness cannot supply is left out.
- **Where a session runs is a fact of the session:** `aimux.place` is `host` or `sandbox`, with `aimux.sandbox` (the sandbox's id) for the second. A sandboxed session's lines are the same lines. A place is written only on evidence, never assumed: this plan writes `host` when the request came from this machine's own loopback and leaves it out otherwise. A session can move (resumed elsewhere), so a `session` line is appended again when a fact of it changes, each with its time, and a reader takes the one in force at the time of a call.
- **Every line says how it was learned** (`aimux.learned`): `seen` when aimux carried the call itself (the proxy), `reported` when the harness said so (telemetry), `verified` when a supervisor outside the agent recorded it (a sandbox's OCSF log, later). On the host the first two usually agree. In a sandbox the difference is the point: what an agent says about itself is a claim.
- **Ids that let records be matched for certain, never by time:** the harness's tool call id; the upstream's request id on a model-call line (the id telemetry also carries); and, reserved for sandboxes, OpenShell's request id (`aimux.place_request`), which its audit records and its receipt header share.
- **Reserved for later, not built:** `aimux.principal` (who is acted for) and `aimux.actor` (the identity held). They will live on the `session` line and, since a subagent may one day act for someone else than its parent, on the `agent` line too, each with the time from which it holds. A supervisor's "allowed" verifies that an action was authorised, not who asked; the two are kept apart.

## Global Constraints

- A line holds names, ids, counts, timings and outcomes. Never a prompt, an answer, a tool's arguments, a command, a file path, an error text, an email address or an account id.
- The proxy and the collector both handle content, because it flows through them. Each builds a line afresh from named fields, each checked for its own form (an id must look like an id of its kind, a name like a name, a duration like a number). A value that fails its check is left out and counted, never written, and not logged. There are tests with real requests and real events that hold prompts, commands and arguments.
- Reading tool blocks must not slow a request or change it: the proxy forwards the same bytes it forwards today, and the time to the first byte does not grow by more than a millisecond at the median for a 5 MB request. Measured before it is on.
- A failure to read or to write a tool line never fails the model request.
- Schema stays `aimux.ledger/1`. New fields and kinds are optional, and a build from before this plan reading a newer file gives the same figures for `aimux cost`.
- What the ledger did not see is said, never left out.
- The tool gateway and the profile code are not changed (the Wingman session's side; later, coordinated).
- Controller first; no mocks; unit, CLI and API tests; docs in the same push.

## The lines

```json
{"kind":"session","writer":"model-proxy","session":"4a09fd61-…","aimux.harness":"claude-code","aimux.harness_version":"2.1.332","aimux.place":"host","aimux.learned":"seen"}
{"kind":"agent","writer":"model-proxy","session":"4a09fd61-…","aimux.agent":"a896f925…","aimux.agent_parent":"a1c3e350…","aimux.learned":"seen"}
{"kind":"model_call","writer":"model-proxy","session":"4a09fd61-…","aimux.agent":"a896f925…","turn":"2","call":"mc_9f…","aimux.request":"req_011ChU5A…", "…":"as today"}
{"kind":"tool_call","writer":"model-proxy","event_id":"toolu_01Vu…:returned","at":"…","session":"4a09fd61-…","aimux.agent":"a896f925…","turn":"2",
 "call":"toolu_01Vu…","aimux.asked_by":"mc_9f…","gen_ai.tool.name":"search_messages","aimux.tool.server":"slack","aimux.outcome":"error","aimux.learned":"seen"}
{"kind":"tool_call","writer":"collector","secondary":true,"session":"4a09fd61-…","call":"toolu_01Vu…","aimux.duration_ms":2453,"aimux.error_type":"ShellError","aimux.prompt_id":"c16292f4-…","aimux.learned":"reported"}
{"kind":"agent","writer":"collector","session":"4a09fd61-…","aimux.agent":"a896f925…","aimux.agent_type":"general-purpose","aimux.learned":"reported"}
```

- A tool call has two moments, and the proxy sees both.
  - **Asked:** the model's answer names a tool and gives it an id. The proxy notes, for that id, the model call that asked, its agent and its turn. This is what `aimux.asked_by` is, bound to the id at the moment it is issued, never worked out afterwards from "the last answer" (a side call in between would make that wrong).
  - **Returned:** a later request carries the result. The results that count are those that come **after the last answer in the request**, wherever they sit: Claude Code sends the results of tools that ran together in separate messages, in any order, and its hooks add messages of their own after them (the proxy's own regression test, `TestSessionContinuesToolResultsSplitOverMessages`, pins that shape). Each is matched to its ask by id. Which messages carry returns is the harness adapter's to say, not a rule about position.
- A line is written when a call returns. A call that was asked for and never returned (the session ended, the owner interrupted or refused it) is written when the session's next request shows no result for it, or when the session goes quiet, with outcome `not returned`: asked, result unknown.
- A result whose ask the proxy did not see (the proxy restarted, a compaction removed the answer that named it) is written with its id and no name, agent or `asked_by`, rather than dropped.
- **One line per call on disk**, not only when counted: the proxy keeps the ids it has written for a session, and when it first sees a session after a restart it reads that session's ids back from its own file before writing. A resumed session's old results come before its last answer, so they are not taken as new; the read-back is the guard for the cases position cannot decide.
- `at` is when the request carrying the result arrived. Tools that ran together arrive together, so their order among themselves is not known from the proxy; the timeline says so, and uses the harness's own times where telemetry gave them.
- `seen` means the proxy carried the call. Whether the tool succeeded is what the harness put in the result: the proxy saw the claim pass, it did not watch the tool.
- An MCP tool's name is split into server and tool (`mcp__slack__search_messages`).
- The collector's lines are `secondary`: they add a duration, a class of failure, the harness's turn id and a subagent's kind to a call the proxy recorded, and are never counted as calls. A reader merges them by session and call id.

## Review Focus

- A request whose tool blocks hold prompts, commands and megabytes of output: none of it is in the ledger file or the proxy's log, and the request is not slowed.
- The proxy is restarted in the middle of a session, and a session is resumed with a long history: no call from before is written again.
- The same request is sent twice (a retry): one line per tool call.
- Two subagents at once, and a subagent started by a subagent: each call carries its own agent, and each agent line its parent.
- The collector is down for a day: every tool call is still in the ledger, without durations.
- A session that does not go through the proxy: the timeline says its calls were not seen, and does not show an empty session as an idle one.

## Tasks

### Task 1: Finish the evidence

**Files:** Modify `docs/superpowers/specs/2026-11-27-tool-call-evidence.md`.

- [ ] Through the real proxy code, not a recorder, for both engines (Anthropic forwarded, Codex translated): capture real requests for several tools at once (results in separate messages, with a hook's reminder after them), a compaction, the first request of a resumed session, an interrupted tool, a refused tool, and a session that ends right after a tool. For each, record which results follow the last answer, and whether every ask has a return.
- [ ] How asks arrive in each answer form the proxy handles: a streamed Anthropic answer, a non-streamed one, a compressed one, one cut off mid-stream, and a Codex answer after translation (where MCP tools are renamed and errors become text): at which point are the harness's own id and name still there to be read?
- [ ] Which header carries the upstream's request id on an Anthropic answer, and is there one for Codex? Does it equal the `request_id` telemetry reports for the same call?
- [ ] Does an MCP request reaching the MCP proxy carry the harness's tool call id?
- [ ] The cost of reading tool blocks from a 50 KB, a 1 MB and a 5 MB request, 200 runs each.
- [ ] Record the results. If any case leaves a call that would be written twice, missed, or given to the wrong agent or model call, stop and bring it to the owner: task 4 rests on it.

### Task 2: The collector is a part of aimux

Asked for by the owner. Independent of the record: it can ship at any point before task 6.

**Files:** Modify `internal/parts/registry.go`, `internal/parts/settings.go`, `internal/parts/jobfile.go`, `internal/controller/partsmenu.go`, `internal/controller/upgrade.go`, `cmd/aimux/cmd/setup.go`, `cmd/aimux/cmd/collect.go`; tests beside them.

**Interfaces:** Produces a part `collector` ("Collector": "receives your sessions' telemetry: traces, and what the ledger learns from them"), with service `collector`; settings `parts.collector.enabled`, `parts.collector.port`, `parts.collector.ledger` (true by default); the service's label `com.aimux.collector`, with `LegacyLabels []string{"com.azaalouk.aimux-collect"}` on `parts.Service`.

- [ ] Test: the part is in the list `aimux setup` shows, and `aimux enable collector` and `aimux disable collector` state it in the config and plan the change like the two proxies.
- [ ] Test: `aimux setup --check` on a machine whose collector runs under the old label reports a rename and changes nothing; `--apply` writes the new job, starts it, waits for it to answer, and only then removes the old one. If the new one does not answer, the old job is put back and started, and the command says so and exits 1.
- [ ] Test: `aimux status` and `aimux upgrade collector` find the collector under either label, and never report two.
- [ ] Test: both jobs want the same port, and the collector today does not report that it failed to take it. The rename stops the old one first, starts the new one, and checks that the process answering on the port is the new job's (by its pid), not merely that something answers; the collector fails its start when it cannot take its port.
- [ ] Test (build tag `launchdlive`): the rename against the real launchd on a job of its own.
- [ ] Implement. The owner runs the apply on his machine; it restarts the collector.

### Task 3: The lines

**Files:** Modify `internal/ledger/line.go`, `internal/ledger/count.go`, `docs/ledger-identity.md`; tests beside them.

**Interfaces:** Produces `KindSession = "session"`, `KindAgent = "agent"`; on `ledger.Line`: `HarnessVersion` (`aimux.harness_version`), `Place` (`aimux.place`), `Sandbox` (`aimux.sandbox`), `PlaceRequest` (`aimux.place_request`), `Learned` (`aimux.learned`), `AgentParent` (`aimux.agent_parent`), `AgentType` (`aimux.agent_type`), `Request` (`aimux.request`), `AskedBy` (`aimux.asked_by`), `ToolName` (`gen_ai.tool.name`), `ToolServer` (`aimux.tool.server`), `ErrorType` (`aimux.error_type`), `PromptID` (`aimux.prompt_id`); `LearnedSeen`, `LearnedReported`, `LearnedVerified`; `PlaceHost`, `PlaceSandbox`; `OutcomeError = "error"`; `ledger.Name(s string) string`; `ledger.SplitTool(name string) (server, tool string)`.

- [ ] Test: each new line round-trips; a line without the new fields reads as before.
- [ ] Test: `Count` counts a tool call once when the proxy's line and a secondary line share its id **within the same session**, never across sessions; a secondary line with no line from the proxy is reported as an orphan and is not counted as a call; the MCP proxy's existing counting rule holds as it is; a `session` or `agent` line is never counted as a call.
- [ ] Test: a build from before this plan reads a file holding the new kinds and fields: `aimux cost` gives the same figures.
- [ ] Test: `Name` and `SplitTool` on names with `__` inside the tool part, a space, a slash, 129 characters.
- [ ] Implement, and write the identity section above into `docs/ledger-identity.md` as the rule.

### Task 4: The proxy writes session, agent and tool-call lines

**Files:** Modify `internal/modelproxy/callfacts.go`, `internal/modelproxy/ledgerline.go`, `internal/modelproxy/turns.go`; create `internal/modelproxy/toolfacts.go`; tests beside them, with requests in `internal/modelproxy/testdata/tools/`.

**Interfaces:** Consumes task 3. Produces on `CallFacts`: `ToolsReturned []ToolReturn{CallID string; Failed bool}` (the results after the last answer, as the front's adapter finds them), `AgentParent`, `HarnessVersion`; `ToolAsk{CallID, Name, ModelCall, Agent, Turn string}` read from each answer as it passes, beside the usage the proxy already reads there; `toolTable`, which binds asks to returns per session, remembers what was written, and reads a session's written ids back from the file when it first meets the session. The model-call line gains `aimux.request` from the answer's request id header. `count_tokens` and other requests that are not conversations are not read.

- [ ] Test: from real request shapes (one tool, three at once, an MCP tool, a failed tool, a subagent's, a nested subagent's, the first request of a resumed session with fifty old results in its history) exactly the new calls are written, each once, with the right agent and outcome.
- [ ] Test: no part of a tool's input or result is in the ledger file or in anything the proxy logs: the requests hold marked strings, and the test searches for them.
- [ ] Test: a session's first request writes one `session` line, a subagent's first request one `agent` line with its parent; neither is written again while the proxy runs, and after a restart they are written again and `Count` holds them to one.
- [ ] Test: a request sent twice, at once and one after the other, and again after a restart of the proxy, leaves one line per tool call in the file itself.
- [ ] Test: an ask with no return is written `not returned` once the session moves on without it; a return with no ask is written with its id alone.
- [ ] Test: a side call between an ask and its return does not change which model call the tool is tied to; the main conversation's calls carry `root`, the harness's side work `background`.
- [ ] Test: a tool block that is not what it should be (no id, an id that is a sentence, a secret-shaped word where a name belongs) writes nothing of that value and is counted in health as `ledger_refused`. Only the id, the name and the failed flag are decoded; a block's input and result are skipped without being held in memory.
- [ ] Test: a failed or stalled write of a tool line does not fail or delay the model request: lines are handed to the writer off the request's path, and what could not be handed over is counted as dropped. The bytes forwarded upstream are the bytes received.
- [ ] Measure under load, not only alone: ten 5 MB requests at once, with the writer stalled, the time the client waits at the 99th percentile and the memory used.
- [ ] Test: both engines write the same lines for the same conversation.
- [ ] Measure as task 1 set out, in the real proxy; stop and bring it to the owner if the budget in Global Constraints is exceeded.
- [ ] Implement.

### Task 5: The meter stays right

**Files:** Modify `internal/controller/meter.go`, its tests; `cmd/aimux/cmd/cost.go`.

- [ ] Test: with the new lines in the file, `aimux cost` shows the same requests and tokens as without them.
- [ ] Test: `aimux cost --by tool` gives calls and failures per tool, MCP tools under their server, and time where a duration is known.
- [ ] Implement. Release tasks 3 to 5 and run `aimux upgrade`: from here the owner's ledger holds his tool calls.

### Task 6: What only the harness knows, from telemetry

**Files:** Create `internal/otel/ledgerfacts.go`; modify `internal/otel/receiver.go` (one call where a log event is handled, and health); tests beside them.

**Interfaces:** Consumes tasks 2 and 3. Produces `otel.ToolFactsLine(attrs map[string]any) (ledger.Line, bool)` (a secondary line: duration, error class, the harness's turn id) and `otel.AgentKindLine(attrs map[string]any) (ledger.Line, bool)`, pure functions from one event's attributes; the collector's health gains `ledger_seq`, `ledger_failed`, `ledger_refused`, `ledger_off`.

- [ ] Test: a real `tool_result` event gives a secondary line with its duration and error class; an `api_request` or `subagent_completed` naming a kind gives one `agent` line per subagent.
- [ ] Test: from events holding prompts, tool input, tool parameters, the account's email and ids, none of it is in the ledger file or the collector's log.
- [ ] Test: the same event twice is one line; the span the collector stores for an event is what it stored before this change.
- [ ] Test: with the collector off, the timeline of task 7 is complete and says durations and kinds are not known.
- [ ] Implement.

### Task 7: The timeline

**Files:** Create `internal/controller/timeline.go`, `cmd/aimux/cmd/timeline.go`; modify `internal/frontend/web/handlers.go`, `server.go`, `smoke_test.go`; tests beside them.

**Interfaces:** Produces `controller.Timeline(dir, session string) (TimelineReport, error)` with `Session{Harness, HarnessVersion, Place, Sandbox}`, `Agents []TimelineAgent{ID, Parent, Type}`, `Entries []TimelineEntry{At, Turn, Kind, Agent, What, Server, AskedBy, DurationMS, Outcome, Learned}` and `NotSeen []string`. CLI `aimux timeline <session>` (`--json`; a prefix of the id accepted). Web `GET /api/timeline?session=`.

- [ ] Test: a session with model calls, built-in tools, an MCP tool, two subagents at once and a nested one comes out in order, each tool call under the model call that asked for it, each entry saying how it was learned.
- [ ] Test: a tool call with a secondary line shows its duration; one without says the duration is not known.
- [ ] Test: a session with no lines from the proxy says its calls were not seen; an ambiguous prefix is an error naming the candidates.
- [ ] Test (e2e): `aimux timeline <id> --json` validates; (smoke) the endpoint answers.
- [ ] Implement.

### Task 8: What kind of work a subagent did

**Files:** Create `internal/controller/subagentwork.go`, test beside it; modify `cmd/aimux/cmd/cost.go`.

**Interfaces:** Produces `controller.SubagentWork(dir string, since time.Time) []SubagentRun{Session, Agent, AgentParent, AgentType string; ModelCalls, ToolCalls int; Tokens int64; Did string}`, the kind from the subagent's `agent` lines and `unknown` when none names one, where `Did` is `asked to change files` (an Edit, Write, MultiEdit or NotebookEdit that did not fail), `gathers` (WebSearch, WebFetch or an MCP tool), `runs commands` (Bash, no write tool) or `reads` (the rest). The label says what tools a run used, not what it achieved.

- [ ] Test: runs with each mix of tools get the right label; `aimux cost --by subagent` prints kind, what it did, runs, model calls and tokens.
- [ ] Implement. This replaces `scripts/helper-work.py` as the source for stage 6; the script stays for comparison until the two agree on the same week and the same runs.

### Task 9: Docs, tracker, and the owner's close

- [ ] Docs: `docs/ledger-identity.md`; the cost guide (`--by tool`, `--by subagent`, `aimux timeline`); what the proxy and the collector read and never keep; the parity table.
- [ ] Tracker: stage 5 row; the unverified items with what was found; the decisions (the proxy is the record; identity on session and agent lines; how a line was learned).
- [ ] The owner's close, after a week of normal use: for three of his sessions, one of them ended in the middle of a tool, the tool calls in the ledger equal those in the transcript by id, by agent and by the model call that asked; `aimux timeline` on one shows its model calls, built-in tools and MCP tools in order; `aimux cost --by subagent` agrees with the script.

## Not in this plan

- Sessions that do not go through the proxy (scheduled jobs): their tool calls could be added from telemetry or their transcripts, marked `reported`. Decided after the week, when it is known how many there are.
- Sandboxed sessions. The path there is OpenShell's own: its trace relay carries a sandbox's telemetry out stamped with the sandbox id, its request id ties an audit record to a call, and its OCSF log holds what was allowed and refused, to become `verified` lines. Two pieces aimux would need are still open upstream (the relay for logs, and JSON). It belongs with the parked sandbox work (4d, 5d).
- Agent identity and acting on someone's behalf: the `session` line is where they will live.
- Another harness's front (the Codex CLI through the Responses API): it fills the same facts; stage 8.
- A hook that reports or refuses a tool call.
- The MCP proxy writing lines of its own: the proxy's line already names the MCP tool and its server, so the MCP proxy's record would add nothing to a session's story until it can know the session.
- The TUI and dashboard views of the timeline (stage 5b).

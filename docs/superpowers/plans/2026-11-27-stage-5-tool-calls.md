# Stage 5: Tool Calls in the Ledger Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One session's model calls and tool calls can be read in order from the ledger, each with where the record came from, so the owner sees what his agents did and stage 6 can tell one kind of work from another.

**Architecture:** The hook that already runs after every tool call (`aimux remote hooks notify`) leaves the facts of the call as one small file in a spool folder, and does nothing else for the ledger. The collector, the one writer of `collector.jsonl`, picks the files up within two seconds, writes a line for each and deletes a file only once its line is on disk. The MCP proxy writes a second, lesser line for each MCP call it carries, so a call that no hook reported is still seen. A reader in the controller joins the lines of one session into a timeline.

**One change from the design, for the owner to accept or refuse.** The design has the hook post each event to the collector and spool only when the collector is down. This plan spools always. Reasons: the hook never waits on the network (a collector that accepts a connection and hangs would add its whole timeout to every tool call); there is one path to test, not two; and nothing depends on the collector saying "written", which today's endpoint cannot say (it answers 200 after storing a span, and an older collector or the TUI's own receiver on that port would answer the same). The cost: a tool call reaches the ledger up to two seconds after it happened.

**Tech Stack:** Go; the existing `internal/ledger`, `internal/waiting`, `internal/otel` (collector), `internal/mcpproxy`, `internal/controller`.

**Spec:** `docs/superpowers/specs/2026-11-23-aimux-governance-metering-and-evals-design.md` (sections 1 to 3), `docs/ledger-identity.md`, `docs/superpowers/specs/2026-11-25-turn-identity.md`. Evidence: `docs/superpowers/specs/2026-11-27-tool-call-evidence.md`.

## Global Constraints

- A line holds names, counts, timings and outcomes. Never a prompt, an answer, a tool's arguments, a command, a file path or an error text.
- A hook never owns a ledger file. It hands the event to the collector, or spools it.
- The hook reports and never refuses. It always exits 0, prints nothing, and reads at most 1 MiB of input.
- The hook's added cost is measured before it is on for every session: with a large tool result, a long transcript, and an unwritable spool. Budget: the median grows by at most 10 ms and the 95th percentile by at most 20 ms.
- The hook is what the owner's permission alerts run on. It applies the alert first, exactly as today, and reports second; a failure to report never changes the alert, the exit code or the output. Reporting has its own switch (`AIMUX_HOOK_LEDGER=off`, and `hooks.ledger: false` in the config), off until task 4's measurements pass.
- What reads the lines ships before what writes them: the collector and the meter's changes are released and running before the hook starts to report.
- Schema stays `aimux.ledger/1`. New fields are optional and a reader that does not know them ignores them.
- `aimux cost` and `GET /api/meter` keep their figures: a tool-call line is never counted as a model call, and a gap in the collector's file is reported as the collector's, not as missing model calls.
- The tool gateway is not changed in this plan (it is the Wingman session's side; its lines are a later task, coordinated).
- Controller first; no mocks; unit, CLI and API tests; docs in the same push.

## What a tool-call line holds

```json
{"schema":"aimux.ledger/1","kind":"tool_call","writer":"collector","seq":812,"event_id":"toolu_01Vu…:PostToolUse",
 "at":"2026-11-27T19:02:11Z","session":"4a09fd61-…","turn":"7","call":"toolu_01Vu…",
 "aimux.harness":"claude-code","aimux.role":"subagent","aimux.agent":"ad87a3e2…","aimux.agent_type":"general-purpose",
 "gen_ai.tool.name":"Bash","aimux.duration_ms":2453,"aimux.outcome":"ok"}
```

New fields on `ledger.Line`: `gen_ai.tool.name`; `aimux.tool.server` (the MCP server, for an MCP tool); `aimux.agent_type`. New outcomes beside `ok`: `error`, `interrupted`. For an MCP tool the name is split: `mcp__slack__search_messages` becomes server `slack`, tool `search_messages`.

- `at` is when the call ended as the hook saw it, not when the collector wrote the line, and the timeline orders by it.
- `turn` is a string, as on every line, and is left out when it is not known for certain; a reader never guesses it.
- A tool, server or subagent type is written only when it looks like a name (at most 128 characters of letters, digits, `-`, `_`, `.`, `:`); anything else is written as `other`. The owner's own names for his MCP servers and subagents are kept: they are his, on his machine, and they are what the timeline is for.

## Review Focus

- A hook input larger than 1 MiB (a big tool result): the line is still written from the fields at the front; nothing is truncated into a wrong value.
- The collector is down for an hour: every call made meanwhile is in the ledger afterwards, once each, and the timeline shows them where they happened.
- The same event arrives twice (a spool file read twice, a crash between the write and the delete): one line.
- Two subagents run at once in one session: each call carries its own agent, and the timeline does not interleave them wrongly.
- A session that does not go through the model proxy (a scheduled job): its tool calls are recorded, and the timeline says its model calls were not seen.

## Tasks

### Task 1: Finish the evidence

**Files:** Modify `docs/superpowers/specs/2026-11-27-tool-call-evidence.md`.

- [ ] Capture an MCP tool call's hook events (field names only) and what the MCP proxy receives for the same call: is the `tool_use_id` anywhere in the request (`_meta`, a header)? Also an MCP call whose result is an error, and one the proxy refuses before it reaches a server.
- [ ] Record the order of the fields as Claude Code writes them, for a small result and a 5 MB one: do the identifying fields (`session_id`, `tool_use_id`, `tool_name`, `agent_id`, `duration_ms`) come before `tool_input` and `tool_response`, or after? Today's hook decodes the whole object under a 1 MiB cap, so a large result fails it; say whether that already loses permission-alert events.
- [ ] In one real session through the proxy, compare the proxy's turn number per call with (a) the count of user prompts in the transcript at the hook, (b) `prompt_id` changes. Record which agrees, including: a subagent still working after the main conversation moved to the next turn, a session resumed after a restart, and one continued after a compaction.
- [ ] Capture two subagents running at once, and a subagent starting a subagent.
- [ ] Record the results. If the turn rule does not agree, stop and bring it to the owner: task 3 depends on it.

### Task 2: The line

**Files:** Modify `internal/ledger/line.go`, `internal/ledger/count.go`; tests beside them.

**Interfaces:** Produces `Line.ToolName string` (`gen_ai.tool.name`), `Line.ToolServer string` (`aimux.tool.server`), `Line.AgentType string` (`aimux.agent_type`), `OutcomeError = "error"`, `OutcomeInterrupted = "interrupted"`, and `ledger.SplitTool(name string) (server, tool string)`.

- [ ] Test: a line with the new fields round-trips; a line without them reads as before; `SplitTool("mcp__slack__search_messages")` is `("slack","search_messages")`, `SplitTool("Bash")` is `("","Bash")`, a name with `__` inside the tool part keeps it.
- [ ] Test: `Count` still drops a repeated event id and a secondary line whose call a primary line has.
- [ ] Implement; run `go test ./internal/ledger/`.

### Task 3: The collector writes tool-call lines

**Files:** Modify `internal/ledger/writer.go`; create `internal/otel/toolcalls.go`, `internal/ledger/spool.go`; modify `internal/otel/receiver.go` (health only); tests beside them.

**Interfaces:** Consumes task 2. Produces `ledger.ToolEvent{Session, Agent, AgentType, ToolName, CallID, HookEvent, Turn string; DurationMS int64; Failed, Interrupted bool; At time.Time}`; `ledger.Spool(dir string, e ToolEvent) error` (a temporary file, then a rename, so a reader never sees half a file; 0600 in a 0700 folder); `ledger.DrainSpool(dir string, write func(ToolEvent) error) (written, left int, err error)`; `(*Writer).Append(Line) error`, which says when the line is not on disk (`Write` keeps its behaviour for the model proxy). The collector's health gains `ledger_seq`, `ledger_failed`, `ledger_off`, `spool_waiting`, `spool_oldest`. `POST /v1/hooks` is not changed.

- [ ] Test: a spooled `PostToolUse` event becomes one `tool_call` line in `collector.jsonl` with the fields above; `PostToolUseFailure` gives outcome `error`, or `interrupted` when it was an interrupt.
- [ ] Test: the same event spooled twice is one line, also when the collector restarted between the two (what was already written is read back from the file's tail at start).
- [ ] Test: a crash between writing a line and deleting its spool file does not give a second line on the next start.
- [ ] Test: a failed append (a full disk, stood in for by a file made read-only) leaves the spool file and counts in `ledger_failed`.
- [ ] Test: files are written in the order of their `At`; a torn or foreign file is left where it is and counted, never deleted.
- [ ] Test: a second collector on the same ledger file is refused by the writer's lock, says so in health, and deletes nothing from the spool.
- [ ] Test: with 10,000 files waiting the drain takes them in batches and health shows the oldest one's age; past 50,000 the hook stops adding and counts what it dropped (`spool_dropped` in the hooks log), so a collector left off for weeks cannot fill the disk.
- [ ] Implement; the collector drains at start and every 2 s.

### Task 4: The meter stays right

**Files:** Modify `internal/controller/meter.go`, its tests; `cmd/aimux/cmd/cost.go`.

- [ ] Test: with tool-call lines and a gap in `collector.jsonl`, `aimux cost` shows the same requests and tokens as without them, and `incomplete` names the writer with the gap.
- [ ] Test: `aimux cost --by tool` gives calls, failures and time per tool, MCP tools under their server.
- [ ] Implement. Release tasks 2 to 4 and run `aimux upgrade` before task 5 is switched on.

### Task 5: The hook reports

**Files:** Modify `internal/waiting/hook.go`, `cmd/aimux/cmd/remote_hooks.go`; tests beside them.

**Interfaces:** Consumes `ledger.ToolEvent` and `ledger.Spool`. Produces `waiting.ToolEventOf(input) (ledger.ToolEvent, bool)`.

- [ ] Test: the input is read once and the permission alert is applied before anything about the ledger; with the spool folder unwritable, the alert's marker is set and cleared exactly as today, the exit code is 0 and nothing is printed.
- [ ] Test: the existing tests of the hook pass untouched, and the real `notify` command is run through: a permission prompt set and cleared, a subagent finishing while the main conversation waits at a prompt.
- [ ] Test: from input with the field names of the evidence note the hook spools a `ToolEvent`; no part of `tool_input`, `tool_response` or `error` is in the file.
- [ ] Test: the identifying fields are read whatever their place in the object and however large the values around them (the reader walks the top-level keys and skips the values it does not want, without holding them); a nested key of the same name inside `tool_input` is not taken for the real one. An input that ends before its fields gives no event and one line in the hooks log.
- [ ] Test: with reporting switched off, nothing is spooled and the hook does what it does today.
- [ ] Implement. The turn comes from the rule task 1 settled, and is left out when that rule cannot be sure.
- [ ] Measure and record in the evidence note, 200 runs each: the hook before and after, with a small and a 5 MB result, a short and a 50 MB transcript, the spool writable and not. Stop and bring it to the owner if the budget in Global Constraints is exceeded.
- [ ] Switch reporting on for the owner's machine only after that.

### Task 6: The MCP proxy's lesser line

**Files:** Modify `internal/mcpproxy/pool.go`, `internal/mcpproxy/daemon.go`; create `internal/mcpproxy/ledgerline.go`; tests beside them.

**Interfaces:** Consumes task 2. Produces one `tool_call` line per `tools/call` in `mcp-proxy.jsonl`, marked `secondary`, with server, tool, duration, outcome, and the call id when task 1 found one in the request.

- [ ] Test: a call through the proxy writes one secondary line; a failed start, a timeout, a tool result marked as an error, and a call the proxy refuses before forwarding each give outcome `error`; the arguments are not in the line.
- [ ] Test: with a call id, `ledger.Count` drops the secondary line when the hook's line has the same id; without one, it is counted as unmatched and said so. The proxy knows no session, so a lone secondary line is never shown as belonging to one.
- [ ] Implement; `aimux mcp proxy status` reports `ledger_seq` and whether the ledger is off.

### Task 7: The timeline

**Files:** Create `internal/controller/timeline.go`, `cmd/aimux/cmd/timeline.go`; modify `internal/frontend/web/handlers.go`, `server.go`, `smoke_test.go`; tests beside them.

**Interfaces:** Produces `controller.Timeline(dir string, session string) (TimelineReport, error)` with `Entries []TimelineEntry{At, Turn, Kind, Who (main or the subagent's id and type), What (model or tool, with server), DurationMS, Outcome, Source (writer)}` and `NotSeen []string` (what the ledger cannot vouch for: no model calls seen, a gap in a writer's file, secondary lines with no primary). CLI `aimux timeline <session>` (`--json`, a prefix of the id accepted); web `GET /api/timeline?session=`.

- [ ] Test: a session with model calls, built-in tool calls, an MCP call and a subagent comes out in the order things happened (by `at`, not by the order the lines were written), each entry naming its source; two subagents' entries carry their own ids.
- [ ] Test: an entry says which subagent made a call, never which model call asked for a tool call: the proxy gives each model call an id of its own, so that link is not known, and the timeline does not draw it.
- [ ] Test: a session whose model calls did not pass the proxy lists its tool calls and says model calls were not seen.
- [ ] Test: an ambiguous prefix is an error naming the candidates, as `aimux cost --session` does.
- [ ] Test (e2e): `aimux timeline <id> --json` validates; (smoke) the endpoint answers.
- [ ] Implement.

### Task 8: What kind of work a subagent did

**Files:** Create `internal/controller/helperwork.go`, test beside it; modify `cmd/aimux/cmd/cost.go`.

**Interfaces:** Produces `controller.HelperWork(dir string, since time.Time) []HelperRun{Session, Agent, AgentType string; ModelCalls, ToolCalls int; Tokens int64; Did string}` where `Did` is `asked to change files` (an Edit, Write or NotebookEdit that did not fail), `gathers` (WebSearch, WebFetch or an MCP tool), `runs commands` (Bash, no write tool) or `reads` (the rest), by the tools on its lines. The label says what tools a run used, not what it achieved: a shell command can write a file, and that is not seen.

- [ ] Test: runs with each mix of tools get the right label; a model call is joined to its run by session and agent; a run with model calls and no tool lines is labelled `not seen`.
- [ ] Test: `aimux cost --by helper` prints kind of subagent, what it did, runs, model calls and tokens.
- [ ] Implement. This replaces `scripts/helper-work.py` as the source for stage 6. The script stays for comparison until the two agree on the same week and the same runs (`general-purpose` only, the script's two write labels counted as one, runs with no tool lines set aside).

### Task 9: Docs, tracker, and the owner's close

- [ ] Docs: the ledger page, the cost guide (`--by tool`, `--by helper`, `aimux timeline`), the remote hooks page (what the hook now reports and never reads), the parity table.
- [ ] Tracker: stage 5 row, the unverified items 2, 4 and 14 with what was found, and the change from the design (the hook always spools) in the decision log.
- [ ] The owner's close: after a day of normal use, his permission alerts behaved as before; `aimux timeline` on one of his sessions shows its model calls, built-in tools and MCP tools in order; the number of tool calls in the ledger for that session equals the number in its transcript; and `aimux cost --by helper` agrees with `scripts/helper-work.py` on the share of runs per kind of work, compared as task 8 says.

## Not in this plan

- The tool gateway's lines (sandboxed sessions): coordinated with the Wingman session, after this.
- Codex's hooks: after Claude Code works, as the design orders it.
- A hook that refuses a tool call: the owner's open decision; nothing here depends on it.
- Whether a file changed was code or a document. That needs the file's name, which a line may not hold. If stage 6 needs it, the hook could report only a class (`code`, `document`, `other`) worked out from the name and never the name; that is the owner's decision.
- The TUI and dashboard views of the timeline (stage 5b).

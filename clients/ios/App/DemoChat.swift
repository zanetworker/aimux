import Foundation
import AimuxKit

#if DEBUG
/// Invented chats for `-demo chat`, `chat-approval`, `chat-readonly` and `chat-terminal`: demo-2 working (thought,
/// plan, steps), demo-13 waiting for an approval, demo-16 open on the Mac with an Arabic exchange; with
/// `chat-terminal`, demo-1 open on the Mac and ready to type into, demo-17 showing a permission prompt there
/// (busy and typeable once answered), demo-18 refused because the Mac is in use,
/// demo-19 with text in its input box (Replace it).
enum DemoChat {
    struct Detail: Sendable { let turns: [Turn]; let stats: SessionStats? }

    static let details: [String: Detail] = [
        "demo-2": Detail(turns: [
            Turn(number: 1, time: DemoData.ago(minutes: 40), user: "why does the reconnect test fail on CI only?",
                 reply: "The test waits a fixed `200ms` for the second connection, and the CI runners are slower. "
                     + "I would wait for the reconnect event instead:\n"
                     + "```go\nwaitFor(t, client.Reconnected, 5*time.Second)\n```",
                 tools: [])
        ], stats: stats(cost: 0.42, tokens: [1_204_000, 34_200, 880_000, 61_500], turns: 14, minutes: 72,
                        branch: "fix/sse-reconnect")),
        "demo-13": Detail(turns: [], stats: stats(cost: 0.004, tokens: [812, 96, 0, 0], turns: 1,
                                                  minutes: 2, branch: "main")),
        "demo-16": Detail(turns: [
            Turn(number: 1, time: DemoData.ago(minutes: 760), user: "لخص النقاط الرئيسية من اجتماع الفريق",
                 reply: "النقاط الرئيسية: موعد الإصدار القادم يوم الخميس، ومراجعة خطة الاختبار قبل ذلك، "
                     + "وتحديث صفحة التوثيق.", tools: []),
            Turn(number: 2, time: DemoData.ago(minutes: 722), user: "and the action items in English?",
                 reply: "Three action items: confirm the Thursday release date, review the test plan by Wednesday, "
                     + "and update the docs page.",
                 tools: [TurnTool(name: "Read", snippet: "notes/2026-10-26.md", ok: true)])
        ], stats: nil),
        "demo-1": Detail(turns: [
            Turn(number: 1, time: DemoData.ago(minutes: 4),
                 user: "Propose the app structure and the first screen to build",
                 reply: "Three tabs: Now, All and Search, with the logic in a framework the tests can reach. "
                     + "Start with Now: it is the screen you open most.",
                 tools: [TurnTool(name: "Read", snippet: "docs/brief.md", ok: true)])
        ], stats: nil),
        "demo-17": Detail(turns: [
            Turn(number: 1, time: DemoData.ago(minutes: 12), user: "check the open items",
                 reply: "Two items are open: the changelog entry and the signed tag. I will write the changelog first.",
                 tools: [])
        ], stats: nil),
        "demo-18": Detail(turns: [
            Turn(number: 1, time: DemoData.ago(minutes: 680), user: "fix the sidebar",
                 reply: "The sidebar now folds sections you are not reading.", tools: [])
        ], stats: nil)
    ]

    /// Every demo session's repository, so references link: `github.com/acme/<folder>` on main.
    static func repo(_ project: String) -> SessionRepo {
        SessionRepo(web: "https://github.com/acme/" + (project.split(separator: "/").last.map(String.init) ?? "app"),
                    branch: "main", root: project)
    }

    /// What `-demo chat-terminal` says about each session open on the Mac; a prompt answered here goes away.
    static func terminal(_ id: String) -> TerminalStatus? {
        switch id {
        case "demo-1":
            return TerminalStatus(status: "idle", canType: true, reason: nil, prompt: nil)
        case "demo-17":
            let answered = DemoData.answered.withLock { $0.contains(releasePrompt.promptId) }
            // Once answered Claude works on, and a message typed meanwhile waits in its input box.
            return answered ? TerminalStatus(status: "busy", canType: true, reason: nil, prompt: nil)
                : TerminalStatus(status: "busy", canType: false, reason: "not_at_input", prompt: releasePrompt)
        case "demo-18":
            return TerminalStatus(status: "idle", canType: false, reason: "mac_in_use", prompt: nil)
        case "demo-19":  // Claude Code's grey suggestion (or a draft) sits in its input box
            return TerminalStatus(status: "idle", canType: false, reason: "not_at_input", prompt: nil, boxHasText: true)
        default:
            return nil
        }
    }

    private static let releasePrompt = TerminalPrompt(
        promptId: "demo-prompt-17", question: "Do you want to make this edit to CHANGELOG.md?",
        detail: "Edit file\nCHANGELOG.md\n  12 +- Release checklist covers the docs site",
        options: [TerminalOption(key: "1", label: "Yes"),
                  TerminalOption(key: "2", label: "Yes, allow all edits during this session"),
                  TerminalOption(key: "3", label: "No, and tell Claude what to do differently")])

    static let events: [String: [SessionEvent]] = [
        "demo-2": working,
        "demo-13": approval
    ]

    private static var working: [SessionEvent] {
        let turn = Turn2(id: "r2", minutesAgo: 1.5)
        return [
            turn.start("run the remote client tests again and tell me which ones still fail"),
            turn.update(1, "agent_thought_chunk", text: "The flaky one waits on a timer; check the others first."),
            turn.update(9, "plan", entries: [("Read the failing test", "completed"),
                                             ("Wait for the reconnect event", "completed"),
                                             ("Run the remote tests", "in_progress"),
                                             ("Report what still fails", "pending")]),
            // c1 has no detail (a plain line); the others open to their command, error or edit.
            turn.tool(10, "c1", "remote/stream_test.go", kind: "read", status: "completed"),
            turn.tool(14, "c2", "retryAfter", kind: "search", status: "completed",
                      rawInput: ["pattern": "retryAfter\\(", "path": "remote"]),
            turn.tool(22, "c3", "remote/stream.go", kind: "edit", status: "completed",
                      rawInput: ["file_path": "remote/stream.go"],
                      content: [ToolContentItem(type: "diff", oldText: streamOld, newText: streamNew)]),
            turn.tool(31, "c4", "remote/stream_test.go", kind: "edit", status: "completed",
                      rawInput: ["file_path": "remote/stream_test.go", "old_string": testOld, "new_string": testNew]),
            turn.tool(38, "c5", "go vet ./remote/...", kind: "execute", status: "failed",
                      rawInput: ["command": "go vet ./remote/..."],
                      content: [ToolContentItem(type: "content", text: "remote/stream.go:88:2: unreachable code"),
                                ToolContentItem(type: "content", text: "exit status 1")]),
            turn.tool(50, "c6", "go test ./remote/...", kind: "execute", status: "in_progress",
                      rawInput: ["command": "go test ./remote/... -run 'TestReconnect|TestResume' -count=1 -race",
                                 "description": "Run the remote client tests"])
        ]
    }

    private static let streamOld = "\tcase <-time.After(200 * time.Millisecond):\n\t\treturn errTimeout\n"
    private static let streamNew = "\tcase <-c.reconnected:\n\t\treturn nil\n"
        + "\tcase <-ctx.Done():\n\t\treturn ctx.Err()\n"
    /// A long edit, so the detail stops at 40 lines.
    private static let testOld = (1...12).map { "\tcheckStep(t, \($0))\n" }.joined()
    private static let testNew = (1...32).map { "\twaitFor(t, step(\($0)))\n" }.joined()

    private static var approval: [SessionEvent] {
        let turn = Turn2(id: "a1", minutesAgo: 2)
        let options = [
            PermissionOption(optionId: "reject", name: "Deny", kind: "reject_once"),
            PermissionOption(optionId: "allow-always", name: "Always", kind: "allow_always"),
            PermissionOption(optionId: "allow-once", name: "Allow", kind: "allow_once")
        ]
        return [
            turn.start("plan the schema change"),
            turn.update(1, "agent_thought_chunk", text: "Read the migration, then check who still uses the column."),
            turn.tool(15, "c1", "migrations/0042_orders.sql", kind: "read", status: "completed"),
            turn.tool(21, "c2", "order_ref", kind: "search", status: "completed"),
            turn.update(30, "agent_message_chunk",
                        text: "The migration adds `orders.region` and backfills it in batches of 5,000. "
                            + "Nothing reads `order_ref` any more, so it can go in the next release. "
                            + "Running it on staging first."),
            turn.approval(40, Approval(approvalId: "ap-demo-13",
                                       title: "Run the migration against the staging database",
                                       options: options, command: "make migrate ENV=staging"))
        ]
    }

    /// `tokens`: in, out, cache read, cache write.
    private static func stats(cost: Double, tokens: [Int], turns: Int, minutes: Double,
                              branch: String) -> SessionStats? {
        let (tokensIn, tokensOut, cacheRead, cacheWrite) = (tokens[0], tokens[1], tokens[2], tokens[3])
        let started = ISO8601DateFormatter().string(from: DemoData.ago(minutes: minutes + 5))
        let json = #"{"costUsd":\#(cost),"tokensIn":\#(tokensIn),"tokensOut":\#(tokensOut),"# +
            #""cacheReadTokens":\#(cacheRead),"cacheWriteTokens":\#(cacheWrite),"turns":\#(turns),"# +
            #""startTime":"\#(started)","durationMin":\#(minutes),"model":"claude-opus-4","# +
            #""gitBranch":"\#(branch)"}"#
        return try? APIDecoder.decode(SessionStats.self, from: Data(json.utf8))
    }

    /// Builds one turn's events, numbered in order, `seconds` after the turn started.
    private struct Turn2 {
        let id: String
        let started: Date
        private let seq = Counter()

        init(id: String, minutesAgo: Double) {
            self.id = id
            started = DemoData.ago(minutes: minutesAgo)
        }

        func time(_ seconds: Double) -> Date { started.addingTimeInterval(seconds) }

        func start(_ text: String) -> SessionEvent {
            SessionEvent(seq: seq.next(), turn: id, kind: .turnStarted, time: started, data: .turnStarted(text: text))
        }

        func update(_ seconds: Double, _ kind: String, text: String? = nil,
                    entries: [(String, String)]? = nil) -> SessionEvent {
            let update = SessionUpdate(sessionUpdate: kind, text: text,
                                       entries: entries?.map { UpdatePlanEntry(content: $0.0, status: $0.1) })
            return SessionEvent(seq: seq.next(), turn: id, kind: .update, time: time(seconds),
                                data: .update(sessionUpdate: kind, title: nil), update: update)
        }

        func approval(_ seconds: Double, _ approval: Approval) -> SessionEvent {
            SessionEvent(seq: seq.next(), turn: id, kind: .approval, time: time(seconds), data: .approval(approval))
        }

        func tool(_ seconds: Double, _ callID: String, _ title: String, kind: String, status: String,
                  rawInput: [String: String]? = nil, content: [ToolContentItem]? = nil) -> SessionEvent {
            let update = SessionUpdate(sessionUpdate: "tool_call", title: title, toolCallId: callID, kind: kind,
                                       status: status, rawInput: rawInput, toolContent: content)
            return SessionEvent(seq: seq.next(), turn: id, kind: .update, time: time(seconds),
                                data: .update(sessionUpdate: "tool_call", title: title), update: update)
        }
    }

    private final class Counter {
        private var value = 0
        func next() -> Int {
            value += 1
            return value
        }
    }
}
// The `-demo chat-format` chat, kept apart so each type stays short.
extension DemoChat {
    /// `-demo chat-format`: demo-4's chat shows every Markdown block, references, a 12-column table, a
    /// 300-character code line, media cards and a two-column table of wrapping cells, over five turns.
    static func formatted(_ id: String) -> Detail? {
        guard id == "demo-4" else { return nil }
        return Detail(turns: [
            Turn(number: 1, time: DemoData.ago(minutes: 30), user: formatAsk, reply: formatReplyOne, tools: []),
            Turn(number: 2, time: DemoData.ago(minutes: 20), user: "Show the config and compare every region",
                 reply: formatReplyTwo, tools: []),
            Turn(number: 3, time: DemoData.ago(minutes: 10), user: "Anything else?", reply: formatReplyThree,
                 tools: []),
            Turn(number: 4, time: DemoData.ago(minutes: 5), user: "Any recordings?", reply: formatReplyMedia,
                 tools: []),
            Turn(number: 5, time: DemoData.ago(minutes: 2), user: "Which images run there?",
                 reply: formatReplyImages, tools: [])
        ], stats: nil)
    }

    private static let formatAsk = """
        Why does the gateway reject the new cert? It fails on `edge-proxy` with:
        ```
        x509: certificate signed by unknown authority
        ```
        - `make test` passes locally
        - `ls` shows the new bundle

        Check **both** clusters.
        """

    private static let formatReplyOne = """
        ## Certificate rotation

        The new leaf is fine; the gateway still trusts the **old intermediate**. It was pinned in \
        `internal/tls/pool.go:88` by a1b2c3d (see #42 and EDGE-311), so *nothing* reloads it and the \
        ~~rotation job~~ restart never happens.

        ### What I changed

        1. Load the bundle from `deploy/ca/bundle.pem` at start
        2. Reload it on `SIGHUP`, with a **retry budget** of 3
           - first attempt waits 50 ms
           - the last one logs `ca reload failed`
        3. Kept the old pool as a fallback for one release

        - [x] Tests pass locally
        - [ ] Rotate staging, then production

        > The intermediate expires on Friday, so production must reload before then. A lazy continuation
        joins the quote.
        """

    private static let formatReplyTwo = """
        The reload, in `internal/tls/reload.go`:

        ```go
        func (p *Pool) Reload(ctx context.Context) error {
            bundle, err := os.ReadFile(p.path)
            if err != nil {
                return fmt.Errorf("read bundle: %w", err)
            }
            return p.swap(bundle)
        }
        ```

        ```
        \(longLine)
        ```

        | Region | Cluster | Cert | Issuer | Expires | Rotated | Pods | Ready | Errors | p50 | p99 | Status |
        | --- | --- | --- | --- | --- | :---: | ---: | ---: | ---: | ---: | ---: | --- |
        | eu-west | edge-1 | leaf-7 | int-2 | Nov 3 | yes | 12 | 12 | 0 | 4 ms | 19 ms | ok |
        | us-east | edge-2 | leaf-7 | int-2 | Nov 3 | yes | 16 | 15 | 2 | 5 ms | 31 ms | reloading |
        | ap-south | edge-3 | leaf-6 | int-1 | Oct 30 | no | 8 | 8 | 41 | 6 ms | 88 ms | old chain |

        | Check | Before | After |
        | :--- | :---: | ---: |
        | Handshakes | failing | 0 errors |
        | Reload time | restart | 0.4 s |
        """

    private static let formatReplyThree = """
        ---

        #### Follow-ups

        ##### Staging

        ###### Owner: platform team

        Docs: https://example.com/docs/tls-rotation and [the runbook](https://example.com/runbook). \
        The `javascript:` link [here](javascript:alert(1)) stays text.

        ملاحظة: يجب **إعادة تحميل** الشهادة قبل يوم الجمعة.
        """

    private static let formatReplyMedia = """
        The walkthrough and the handshake capture:

        https://www.youtube.com/watch?v=dQw4w9WgXcQ

        [Rotation walkthrough, recorded on staging with the new bundle and the reload on SIGHUP](\(walkthrough))

        ![Handshake diagram](https://example.com/docs/handshake.png)

        https://example.com/audio/standup-notes.m4a

        Inline, https://youtu.be/abc123 stays a link.
        """

    /// Two columns whose cells wrap to several lines, a ragged last row, then text after the table.
    private static let formatReplyImages = """
        | Component | Image |
        |---|---|
        | Gateway | `quay.io/example-org/edge-gateway-controller:v2.14.0-rc.3-build.20261028.1` |
        | Supervisor | `registry.example.com/platform/edge-supervisor@sha256:4f9a0c1d2e3b4a5f6e7d8c9b0a1f2e3d` |
        | Notes | Rotated on staging first, then production, after the reload test passed on both clusters |
        | Sandbox |

        All images are pinned by digest, and nothing was changed.
        """

    private static let walkthrough = "https://example.com/media/rotation-walkthrough.mp4"

    private static let longLine = "export CA_BUNDLE_CHAIN="
        + String(repeating: "MIIDdzCCAl+gAwIBAgIEAgAAuTAN", count: 10)
        + " # one long line"
}

// The `-demo chat-items` chat: a wrapped user message and formatted thought, plan and steps.
extension DemoChat {
    /// `-demo chat-items`: demo-2 opens with a slash command, a bash-mode command and its output; its last history
    /// turn was typed on the Mac while Claude worked, with a paste and an image; its running turn has a thought with
    /// inline code, a plan with a link and steps that are paths.
    static func items(_ id: String) -> Detail? {
        guard id == "demo-2" else { return nil }
        let command = "<command-name>/review-tools:tidy</command-name>\n<command-message>review-tools:tidy"
            + "</command-message>\n<command-args>keep the chart notes short</command-args>"
        let output = "<bash-stdout>legend.ts: ok\naxis.ts: ok</bash-stdout><bash-stderr>warning: 1 rule skipped"
            + "</bash-stderr>"
        let user = "The user sent a new message while you were working:\n[Image #1] the chart legend overlaps the "
            + "axis here\n<pasted_content id=\"7012\">\n.legend { position: absolute; }\n.axis { left: 0; }\n"
            + "</pasted_content>\n"
        return Detail(turns: [
            Turn(number: 1, time: DemoData.ago(minutes: 9), user: command,
                 reply: "The chart notes are down to three lines.", tools: []),
            Turn(number: 2, time: DemoData.ago(minutes: 8), user: "<bash-input>npm run lint</bash-input>",
                 reply: "", tools: []),
            Turn(number: 3, time: DemoData.ago(minutes: 8), user: output, reply: "", tools: []),
            Turn(number: 4, time: DemoData.ago(minutes: 5), user: user,
                 reply: "The legend is absolutely placed over the axis; I will move it into the flow.",
                 tools: [TurnTool(name: "Read", snippet: "/Users/me/src/aimux/src/chart/legend.ts", ok: true),
                         TurnTool(name: "Grep", snippet: "**/*.css", ok: true),
                         TurnTool(name: "Edit", snippet: "src/chart/__init__.py", ok: true),
                         TurnTool(name: "Bash", snippet: "npm test -- legend", ok: false)])
        ], stats: nil)
    }

    static var itemEvents: [String: [SessionEvent]] {
        let turn = Turn2(id: "i2", minutesAgo: 1)
        return ["demo-2": [
            turn.start("now check the tooltip too"),
            turn.update(1, "agent_thought_chunk", text: "The tooltip reads `chart.offsetLeft`, which is *stale* "
                        + "after the move; see src/chart/tooltip.ts:42."),
            turn.update(4, "plan", entries: [("Move `.legend` into the flow", "completed"),
                                             ("Check the [layout guide](https://example.com/guide/layout)",
                                              "in_progress"),
                                             ("Run **all** chart tests", "pending")]),
            turn.tool(6, "c1", "Read `src/chart/tooltip.ts`", kind: "read", status: "completed"),
            turn.tool(8, "c2", "src/chart/tooltip.ts", kind: "edit", status: "completed"),
            turn.tool(10, "c3", "npm test -- tooltip", kind: "execute", status: "in_progress")
        ]]
    }
}
#endif

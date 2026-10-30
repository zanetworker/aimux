import Foundation
import SwiftUI
import Synchronization
import AimuxKit

/// Launch arguments that open screens and states directly, for screenshots. DEBUG only.
enum DebugHooks {
    #if DEBUG
    /// -server <address>: use this server instead of the saved one.
    static var serverOverride: String? { UserDefaults.standard.string(forKey: "server") }
    /// -openSettings YES: open the server sheet at launch.
    static var openSettings: Bool { UserDefaults.standard.bool(forKey: "openSettings") }
    /// -query <text>: start the search tab with this search.
    static var initialQuery: String? { UserDefaults.standard.string(forKey: "query") }
    /// -project <path>: start All filtered to this project.
    static var initialProject: String? { UserDefaults.standard.string(forKey: "project") }
    /// -collapse <path>: start Now with this working folder closed (folders start open).
    static var collapsedProject: String? { UserDefaults.standard.string(forKey: "collapse") }
    /// -scrollToEnd YES: open lists scrolled to the bottom, for screenshots of the lower sections.
    static var scrollToEnd: Bool { UserDefaults.standard.bool(forKey: "scrollToEnd") }
    /// -tab now|all|search: start on this tab.
    static var initialTab: AppTab {
        UserDefaults.standard.string(forKey: "tab").flatMap(AppTab.init(rawValue:)) ?? .now
    }
    /// -demo list|twenty|finding|error|empty|slow|chat|chat-approval|chat-readonly|chat-terminal|chat-format|
    /// chat-items|chat-queue|chat-long: sample data without a server (finding: Last on your Mac, pins, folders,
    /// descriptions and a fake Update summary; chat-queue: the terminal sessions of chat-terminal, and
    /// queues; chat-long: every session a long terminal chat whose latest 30 turns move on by one each read).
    static var demoSource: (any AppSource)? {
        UserDefaults.standard.string(forKey: "demo").map(DemoSource.init(mode:))
    }
    /// The demo's event stream: open, and quiet.
    static var demoStream: (any EventStreaming)? { demoSource == nil ? nil : DemoStream() }
    /// -deviceLogin pairing|revoked|keychanged|locked|refused|live: the demo behind a device login in this state
    /// (refused: locked after an unlock the Mac refused).
    @MainActor static let demoLogin: DeviceLogin? = {
        let states: [String: DeviceLoginState] = ["pairing": .unpaired(nil), "revoked": .unpaired(.revoked),
                                                  "keychanged": .unpaired(.keyChanged), "locked": .locked,
                                                  "refused": .locked,
                                                  "live": .live]
        guard demoSource != nil, let name = UserDefaults.standard.string(forKey: "deviceLogin"),
              let state = states[name] else { return nil }
        let login = DeviceLogin(baseURL: URL(string: "https://my-mac.tail1234.ts.net:8443")!,
                                keys: SoftwareKeyStore(), defaults: UserDefaults(suiteName: "aimux-demo-login")!)
        login.showForDemo(state, refusedUnlock: name == "refused")
        return login
    }()
    /// -openChat <id>: start Now with this demo session's chat open.
    static var initialPath: [Session] {
        guard let id = UserDefaults.standard.string(forKey: "openChat") else { return [] }
        return (DemoData.list + DemoFinding.sessions).filter { $0.id == id }
    }
    /// -openInfo YES: open the session info sheet with the chat.
    static var openInfo: Bool { UserDefaults.standard.bool(forKey: "openInfo") }
    /// -editField title|description: with the info sheet open, start editing this field.
    static var editField: MetaField? {
        UserDefaults.standard.string(forKey: "editField").flatMap { MetaField(rawValue: $0) }
    }
    /// -openSteps YES: show every steps group and thought opened.
    static var openSteps: Bool { UserDefaults.standard.bool(forKey: "openSteps") }
    /// -openStepDetails 2,4: with the steps open, open these steps' details (by index in their group).
    static var openStepDetails: Set<Int> {
        Set((UserDefaults.standard.string(forKey: "openStepDetails") ?? "").split(separator: ",").compactMap {
            Int($0.trimmingCharacters(in: .whitespaces))
        })
    }
    /// -openQueueEdit <item id>: open this queued message for editing when the chat opens.
    static var openQueueEdit: String? { UserDefaults.standard.string(forKey: "openQueueEdit") }
    /// -chatAnchor top|center|bottom: where a chat opens (default bottom), for screenshots of long replies.
    static var chatAnchor: UnitPoint {
        switch UserDefaults.standard.string(forKey: "chatAnchor") {
        case "top": .top
        case "center": .center
        default: .bottom
        }
    }
    #else
    static let serverOverride: String? = nil
    static let openSettings = false
    static let initialQuery: String? = nil
    static let initialProject: String? = nil
    static let collapsedProject: String? = nil
    static let scrollToEnd = false
    static let initialTab = AppTab.now
    static let demoSource: (any AppSource)? = nil
    static let demoStream: (any EventStreaming)? = nil
    @MainActor static let demoLogin: DeviceLogin? = nil
    static let initialPath: [Session] = []
    static let openInfo = false
    static let editField: MetaField? = nil
    static let openSteps = false
    static let openStepDetails: Set<Int> = []
    static let chatAnchor = UnitPoint.bottom
    static let openQueueEdit: String? = nil
    #endif
}

#if DEBUG
/// Made-up sessions covering every Now case: `list` (an approval, a failure, a finished turn, two working,
/// four on the Mac in three folders, one Arabic title) and `twenty` (3 need you, 2 finished, 20 working in 7 projects).
private struct DemoSource: AppSource {
    let mode: String

    func sessions(limit: Int) async throws -> [Session] {
        switch mode {
        case "error": throw APIError.http(status: 502)
        case "empty": return []
        case "slow":
            try await Task.sleep(for: .seconds(600))
            return []
        case "twenty": return Array(DemoData.twenty.prefix(limit))
        case "finding": return Array(DemoFinding.sessions.prefix(limit))
        default: return Array(DemoData.list.prefix(limit))
        }
    }

    func sessionList(limit: Int) async throws -> SessionList {
        if isFinding { return DemoFinding.list }
        return SessionList(sessions: try await sessions(limit: limit))
    }

    private var isFinding: Bool { mode == "finding" }

    func editMeta(sessionID: String, _ change: MetaChange) async throws -> SessionMeta {
        try await Task.sleep(for: .milliseconds(200))
        return try DemoFinding.edit(sessionID, change)
    }

    func describe(sessionID: String) async throws -> DescribeResult {
        try await DemoFinding.describe(sessionID)
    }

    private var isChat: Bool { mode.hasPrefix("chat") }
    private var hasQueue: Bool { mode == "chat-queue" }

    func eventSnapshot(sessionID: String) async throws -> EventPage {
        let chat = mode == "chat-items" ? DemoChat.itemEvents[sessionID] : isChat ? DemoChat.events[sessionID] : nil
        var events = chat ?? DemoData.events[sessionID] ?? DemoFinding.events[sessionID] ?? []
        // An approval answered in this run shows as resolved, as the server would send it.
        let answered = DemoData.answered.withLock { $0 }
        for case let (turn, .approval(approval)) in events.map({ ($0.turn, $0.data) })
        where answered.contains(approval.approvalId) {
            events.append(SessionEvent(seq: (events.map(\.seq).max() ?? 0) + 1, turn: turn, kind: .approvalResolved,
                                       time: .now, data: .approvalResolved(approvalId: approval.approvalId,
                                                                           outcome: "allowed")))
        }
        return EventPage(events: events, log: "demo")
    }

    func eventPage(sessionID: String, after: Int, log: String) async throws -> EventPage {
        EventPage(events: [], log: "demo")
    }

    func session(id: String, turns: Int) async throws -> SessionDetail {
        let all = DemoData.list + DemoData.twenty + DemoFinding.sessions
        guard let session = all.first(where: { $0.id == id }) else {
            throw APIError.server(status: 404, code: .unknownSession, message: "no such session")
        }
        if mode == "chat-long" { return DemoLong.detail(session) }
        let chat = mode == "chat-format" ? DemoChat.formatted(id) : mode == "chat-items" ? DemoChat.items(id)
            : isChat ? DemoChat.details[id] : nil
        return SessionDetail(id: id, title: session.title, project: session.project, lastActive: session.lastActive,
                             state: session.state, lastPrompt: session.lastPrompt, turns: chat?.turns ?? [],
                             stats: chat?.stats,
                             terminal: mode == "chat-terminal" || hasQueue ? DemoChat.terminal(id) : nil,
                             repo: DemoChat.repo(session.project), queue: hasQueue ? DemoQueue.status(id) : nil,
                             description: session.description, pinned: session.pinned)
    }

    func cancel(sessionID: String) async throws {}

    func answer(approvalID: String, optionID: String) async throws {
        try await Task.sleep(for: .milliseconds(300))
        DemoData.answered.withLock { _ = $0.insert(approvalID) }
    }

    func sendMessage(sessionID: String, text: String, key: String) async throws -> TurnAccepted {
        TurnAccepted(id: sessionID, turn: "demo-turn", after: 0, events: "/v1/sessions/\(sessionID)/events")
    }

    func terminalSend(sessionID: String, text: String, key: String) async throws {
        try await Task.sleep(for: .milliseconds(300))
    }

    func terminalPress(sessionID: String, promptID: String, option: String, key: String) async throws {
        try await Task.sleep(for: .milliseconds(300))
        DemoData.answered.withLock { _ = $0.insert(promptID) }
    }

    func queue(sessionID: String) async throws -> QueueStatus { DemoQueue.get(sessionID) }

    func addToQueue(sessionID: String, text: String, key: String) async throws -> QueueItem {
        try await Task.sleep(for: .milliseconds(200))
        return DemoQueue.add(sessionID, text: text)
    }

    func editQueueItem(sessionID: String, itemID: String, text: String) async throws -> QueueStatus {
        try DemoQueue.edit(sessionID, id: itemID, text: text)
    }

    func moveQueueItem(sessionID: String, itemID: String, position: Int) async throws -> QueueStatus {
        try DemoQueue.move(sessionID, id: itemID, position: position)
    }

    func deleteQueueItem(sessionID: String, itemID: String) async throws -> QueueStatus {
        DemoQueue.delete(sessionID, id: itemID)
    }

    func resumeQueue(sessionID: String) async throws -> QueueStatus { DemoQueue.resume(sessionID) }

    func health() async throws -> Health {
        if isFinding { return DemoFinding.health }
        let queue = hasQueue ? #","queue""# : ""
        let json = #"{"status":"ok","login":"demo","version":"demo","acpProtocolVersion":1,"# +
            #""capabilities":["sessions","search","new_sessions"\#(queue)],"# +
            #""links":{"jiraBase":"https://jira.example.com","jiraProjects":["EDGE","OPS"]}}"#
        return try APIDecoder.decode(Health.self, from: Data(json.utf8))
    }

    func search(_ query: String, limit: Int) async throws -> [SearchHit] {
        let rows = DemoData.list.suffix(3).map { session in
            #"{"id":"\#(session.id)-hit","provider":"claude","title":"\#(session.title)","# +
                #""project":"\#(session.project)","lastActive":"2026-10-20T10:00:00Z","state":"idle","# +
                #""snippet":"…we talked about [\#(query)] while fixing the [\#(query)] setup in this session…"}"#
        }
        let json = #"{"results":["# + rows.joined(separator: ",") + "]}"
        return try APIDecoder.decode(SearchResultsDemo.self, from: Data(json.utf8)).results
    }

    private struct SearchResultsDemo: Decodable { let results: [SearchHit] }
}

/// An open stream that sends nothing, so a demo chat stays live.
private struct DemoStream: EventStreaming {
    func events(sessionID: String, after: Int, log: String) -> AsyncThrowingStream<StreamEvent, Error> {
        AsyncThrowingStream { _ in }
    }
}

enum DemoData {
    static let start = Date.now
    /// Approvals answered since launch.
    static let answered = Mutex<Set<String>>([])

    static func ago(minutes: Double) -> Date { start.addingTimeInterval(-minutes * 60) }

    private static func make(_ id: String, _ title: String, _ project: String, minutesAgo: Double,
                             _ state: SessionState = .idle, _ prompt: String? = nil) -> Session {
        Session(id: id, title: title, project: "/Users/me/src/\(project)",
                lastActive: ago(minutes: minutesAgo), state: state, lastPrompt: prompt)
    }

    // MARK: Demo "list"

    static let list = [
        make("demo-13", "Database migration plan", "inventory", minutesAgo: 1, .busy, "plan the schema change"),
        make("demo-14", "Gateway certificate check", "edge-proxy", minutesAgo: 3, .idle,
             "apply the new gateway config"),
        make("demo-15", "Customer notes summary", "notes", minutesAgo: 6, .idle, "summarize the call"),
        make("demo-1", "Aimux iOS app structure", "aimux-ios", minutesAgo: 0.5, .openInTerminal,
             "Propose the app structure and the first screen to build"),
        make("demo-2", "Fix flaky SSE reconnect test in the remote client", "aimux", minutesAgo: 6, .busy,
             "run the remote client tests again and tell me which ones still fail"),
        make("demo-3", "Weekly report", "notes", minutesAgo: 90, .idle, "draft the platform section"),
        make("demo-4", "Gateway TLS rotation", "edge-proxy", minutesAgo: 180, .idle,
             "why does the gateway reject the new cert?"),
        make("demo-17", "Release checklist review", "release-tools", minutesAgo: 660, .openInTerminal,
             "check the open items"),
        make("demo-18", "Docs site navigation", "docs-site", minutesAgo: 672, .openInTerminal, "fix the sidebar"),
        make("demo-19", "Release notes draft", "release-tools", minutesAgo: 690, .openInTerminal,
             "write the notes"),
        make("demo-16", "ملاحظات الاجتماع الأسبوعي", "notes", minutesAgo: 720, .openInTerminal,
             "لخص النقاط الرئيسية"),
        make("demo-5", "Garden planner paths", "garden-planner-very-long-folder-name-for-testing",
             minutesAgo: 1200, .idle, "still running?"),
        make("demo-6", "Interview notes", "notes", minutesAgo: 1560, .idle, "summarize the call"),
        make("demo-7", "Sales dashboard charts", "web-shop", minutesAgo: 1800, .idle, "fix the chart taps"),
        make("demo-8", "New session", "scratch", minutesAgo: 3600, .unavailable),
        make("demo-9", "Sandbox driver gaps", "notes", minutesAgo: 4800, .idle, "compare the two drivers"),
        make("demo-10", "Session from a newer server", "aimux", minutesAgo: 60 * 24 * 12, .unknown("hibernating"),
             "this state is unknown to this app version"),
        make("demo-11", "Strategy narrative", "notes", minutesAgo: 60 * 24 * 20, .idle, "update the positioning"),
        make("demo-12", "Library release notes", "notes", minutesAgo: 60 * 24 * 33, .idle, "what shipped"),
        make("d12ce59e-17e0-4534-8027-eac22f02c34f", "Session d12ce59e-17e0-4534-8027-eac22f02c34f", "inventory",
             minutesAgo: 2, .idle, "check the nightly import")
    ]

    // MARK: Demo "twenty"

    static let twenty: [Session] = {
        var rows = [
            make("tw-a1", "Push notification setup", "aimux", minutesAgo: 1, .busy, "wire the notification key"),
            make("tw-a2", "Weekly status draft", "notes", minutesAgo: 2, .busy, "write the status page"),
            make("tw-f1", "Gateway certificate check", "edge-proxy", minutesAgo: 3, .idle,
                 "apply the new gateway config"),
            make("tw-d1", "Login page fixes", "web-shop", minutesAgo: 2, .idle, "fix the login redirect"),
            make("tw-d2", "Customer notes summary", "notes", minutesAgo: 6, .idle, "summarize the call")
        ]
        let working: [(String, [String])] = [
            ("research", ["Strategy narrative outline", "Identity and vault notes", "Gateway identity checks",
                          "Harness comparison", "Registry evidence", "Use-case discovery",
                          "ملاحظات الاجتماع الأسبوعي"]),
            ("aimux", ["Push server endpoints", "Event log paging", "Search ranking", "Session cache"]),
            ("edge-proxy", ["Schema downgrade", "Policy reload", "Driver matrix"]),
            ("aimux-ios", ["App structure", "Tab bar layout"]),
            ("mobile", ["Offline banner", "Widget sizes"]),
            ("garden-planner", ["Planting calendar"]),
            ("trips", ["Flight price alerts"])
        ]
        var minute = 2.5
        for (project, titles) in working {
            for (index, title) in titles.enumerated() {
                rows.append(make("tw-\(project)-\(index)", title, project, minutesAgo: minute, .busy))
                minute += 0.7
            }
        }
        rows += (1...4).map {
            make("tw-mac-\($0)", "Terminal session \($0)", "scratch", minutesAgo: 600, .openInTerminal)
        }
        return rows
    }()

    // MARK: Events

    static let events: [String: [SessionEvent]] = [
        "demo-13": approval("ap-demo-13", "Run the migration against the staging database?",
                            command: "make migrate ENV=staging", minutesAgo: 1),
        "demo-14": failed("kubectl apply exited 1", minutesAgo: 3),
        "demo-15": finished(minutesAgo: 6),
        "demo-2": working(tool: "go test ./remote/...", minutesAgo: 6),
        "tw-a1": approval("ap-tw-a1", "Read the signing key from the keychain?",
                          command: "security find-generic-password -s push-key", minutesAgo: 1),
        "tw-a2": approval("ap-tw-a2", "Write the status page to docs/status.md?", command: nil, minutesAgo: 2),
        "tw-f1": failed("kubectl apply exited 1", minutesAgo: 3),
        "tw-d1": finished(minutesAgo: 2),
        "tw-d2": finished(minutesAgo: 6),
        "tw-research-0": working(tool: "edit outline.md", minutesAgo: 2.5),
        "tw-research-1": thinking(minutesAgo: 3.2),
        "tw-research-2": working(tool: "rg identity", minutesAgo: 3.9),
        "tw-research-3": thinking(minutesAgo: 4.6),
        "tw-research-4": working(tool: "curl -s registry/v2", minutesAgo: 5.3)
    ]

    static let options = [
        PermissionOption(optionId: "allow-once", name: "Allow once", kind: "allow_once"),
        PermissionOption(optionId: "allow-always", name: "Always allow", kind: "allow_always"),
        PermissionOption(optionId: "reject", name: "Reject", kind: "reject_once")
    ]

    private static func event(_ seq: Int, _ kind: EventKind, _ data: EventData, minutesAgo: Double) -> SessionEvent {
        SessionEvent(seq: seq, turn: "t1", kind: kind, time: ago(minutes: minutesAgo), data: data)
    }

    private static func approval(_ id: String, _ title: String, command: String?,
                                 minutesAgo: Double) -> [SessionEvent] {
        [event(1, .turnStarted, .turnStarted(text: "go"), minutesAgo: minutesAgo + 1),
         event(2, .approval, .approval(Approval(approvalId: id, title: title, options: options, command: command)),
               minutesAgo: minutesAgo)]
    }

    private static func failed(_ error: String, minutesAgo: Double) -> [SessionEvent] {
        [event(1, .turnStarted, .turnStarted(text: "go"), minutesAgo: minutesAgo + 1),
         event(2, .turnError, .turnError(error: error), minutesAgo: minutesAgo)]
    }

    private static func finished(minutesAgo: Double) -> [SessionEvent] {
        [event(1, .turnStarted, .turnStarted(text: "go"), minutesAgo: minutesAgo + 1),
         event(2, .turnDone, .turnDone(stopReason: "end_turn"), minutesAgo: minutesAgo)]
    }

    static func workingEvents(tool: String, minutesAgo: Double) -> [SessionEvent] {
        working(tool: tool, minutesAgo: minutesAgo)
    }

    private static func working(tool: String, minutesAgo: Double) -> [SessionEvent] {
        [event(1, .turnStarted, .turnStarted(text: "go"), minutesAgo: minutesAgo + 1),
         event(2, .update, .update(sessionUpdate: "tool_call", title: tool), minutesAgo: minutesAgo)]
    }

    private static func thinking(minutesAgo: Double) -> [SessionEvent] {
        [event(1, .turnStarted, .turnStarted(text: "go"), minutesAgo: minutesAgo + 1),
         event(2, .update, .update(sessionUpdate: "agent_thought_chunk", title: nil), minutesAgo: minutesAgo)]
    }
}
#endif

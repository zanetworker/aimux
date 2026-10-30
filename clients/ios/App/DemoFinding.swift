import Foundation
import Synchronization
import AimuxKit

#if DEBUG
/// `-demo finding`: made-up sessions for Last on your Mac (fd-mac), Pinned (fd-pin1 idle, fd-pin2 working),
/// folders on Now (aimux with three sessions, notes with two, trips with one) and descriptions. Pins and owner
/// edits are kept in memory like the Mac would; Update summary answers after a second with a made-up summary.
enum DemoFinding {
    private struct Meta {
        var title: String?
        var description: String?
        var pinned: Bool?
    }

    private static let base: [Session] = [
        make("fd-mac", "Fix the login redirect", "web-shop", minutesAgo: 20, .openInTerminal),
        make("fd-pin1", "Plan the allotment layout", "notes", minutesAgo: 120, .idle),
        make("fd-pin2", "Release checklist", "release-tools", minutesAgo: 2, .busy),
        make("fd-a1", "Wire the share sheet", "aimux", minutesAgo: 1, .busy),
        make("fd-a2", "Fix the icon spacing", "aimux", minutesAgo: 3, .busy),
        make("fd-a3", "Event log paging", "aimux", minutesAgo: 5, .busy),
        make("fd-n1", "Outline the reading list", "notes", minutesAgo: 4, .busy),
        make("fd-n2", "Merge the duplicate notes", "notes", minutesAgo: 6, .busy),
        make("fd-t1", "Book the lake cabin", "trips", minutesAgo: 7, .busy),
        // Only its id as the title, as Claude Code leaves some sessions: shown and prefilled as "Session c0ffee12".
        make(oldID, oldID, "scratch", minutesAgo: 600, .idle)
    ]

    static let oldID = "c0ffee12-3c4d-4e5f-8a6b-7c8d9e0f1a2b"

    private static let descriptions = [
        "fd-mac": "Finds why the login page loops back to itself.",
        "fd-pin1": "Lays out twelve beds and the paths between them.",
        "fd-a1": "Adds the share sheet to the chat screen and checks it on a small phone.",
        "fd-a3": "Pages the event log by 200 entries.",
        "fd-n1": "Groups the books by season.",
        "fd-t1": "Compares three cabins for May."
    ]
    private static let pinned: Set = ["fd-pin1", "fd-pin2"]

    private static let edits = Mutex<[String: Meta]>([:])

    private static func make(_ id: String, _ title: String, _ project: String, minutesAgo: Double,
                             _ state: SessionState) -> Session {
        Session(id: id, title: title, project: "/Users/me/src/\(project)",
                lastActive: DemoData.ago(minutes: minutesAgo), state: state, lastPrompt: "carry on",
                description: descriptions[id], pinned: pinned.contains(id))
    }

    static var sessions: [Session] {
        let edits = edits.withLock { $0 }
        return base.map { session in
            guard let meta = edits[session.id] else { return session }
            return session.taking(title: meta.title ?? session.title,
                                  description: meta.description ?? session.description, pinned: meta.pinned)
        }
    }

    static var list: SessionList {
        SessionList(sessions: sessions, lastOnMac: LastOnMac(id: "fd-mac", at: DemoData.ago(minutes: 4)))
    }

    static let events: [String: [SessionEvent]] = [
        "fd-pin2": DemoData.workingEvents(tool: "make release-notes", minutesAgo: 2),
        "fd-a1": DemoData.workingEvents(tool: "xcodebuild test", minutesAgo: 1),
        "fd-n1": DemoData.workingEvents(tool: "rg season", minutesAgo: 4)
    ]

    static let health = Health(status: "ok", login: "demo", version: "demo", acpProtocolVersion: 1,
                               capabilities: ["sessions", "search", "new_sessions", "describe"],
                               naming: NamingStatus(today: 3, limit: 20, failing: false, reason: nil, paused: nil))

    /// "" clears the owner's value: the session shows its automatic one again.
    static func edit(_ id: String, _ change: MetaChange) throws -> SessionMeta {
        guard let original = base.first(where: { $0.id == id }) else {
            throw APIError.server(status: 404, code: .unknownSession, message: "no such session")
        }
        let meta = edits.withLock { all in
            var meta = all[id] ?? Meta()
            if let title = change.title { meta.title = title.isEmpty ? nil : title }
            if let description = change.description { meta.description = description.isEmpty ? nil : description }
            if let pinned = change.pinned { meta.pinned = pinned }
            all[id] = meta
            return meta
        }
        return SessionMeta(title: meta.title ?? original.title, description: meta.description ?? original.description,
                           pinned: meta.pinned ?? original.pinned)
    }

    static func describe(_ id: String) async throws -> DescribeResult {
        try await Task.sleep(for: .seconds(1))
        let title = "Summarised: " + (base.first { $0.id == id }?.title ?? id)
        let description = "A made-up summary of this demo session, written by the fake model."
        _ = try edit(id, MetaChange(title: title, description: description))
        return DescribeResult(title: title, description: description, changed: ["title", "description"], kept: [])
    }
}
#endif

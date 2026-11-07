import Foundation
import Synchronization
import AimuxKit

#if DEBUG
/// `-demo finding`: made-up sessions laid out as on a Mac. Window 1 has five tabs (research, openshell, aimux with
/// the pane last in front and a pinned open session, blog, scratch with 9 panes); Window 2 is renamed "Reviews".
/// Also one session open elsewhere (no pane), a pinned closed one, closed ones in repos, worktrees and Home, and
/// descriptions. Pins and owner edits are kept in memory like the Mac would; Update summary answers after a second
/// with a made-up summary.
enum DemoFinding {
    private struct Meta {
        var title: String?
        var description: String?
        var pinned: Bool?
    }

    private static let window1 = MacWindow(id: "w-1", index: 1, name: "Window 1")
    private static let window2 = MacWindow(id: "w-2", index: 2, name: "Reviews")

    private static func place(_ window: MacWindow, tab: Int, _ name: String, pane: Int, of panes: Int,
                              front: Bool = false) -> MacPlace {
        MacPlace(window: window, tab: MacTab(index: tab, name: name), pane: pane, panes: panes, lastInFront: front)
    }

    private static let aimux = "/src/aimux"
    private static func folder(_ repo: String, worktree: String? = nil, sub: String? = nil) -> Folder {
        let label = [repo, worktree].compactMap { $0 }.joined(separator: " / ") + (sub.map { " · " + $0 } ?? "")
        return Folder(repo: repo, repoPath: "/src/" + repo, worktree: worktree, sub: sub, label: label)
    }

    private static let base: [Session] = [
        // Window 1
        make("fd-r1", "Weekly report draft", folder("research"), minutesAgo: 50, .openInTerminal,
             mac: place(window1, tab: 1, "research", pane: 1, of: 2)),
        make("fd-o1", "Sandbox policy reload", folder("openshell"), minutesAgo: 1, .openInTerminal,
             mac: place(window1, tab: 2, "openshell", pane: 1, of: 2), working: true),
        // At a permission prompt in its pane: Needs you, "Waiting for permission".
        make("fd-o2", "Driver matrix notes", folder("openshell"), minutesAgo: 25, .openInTerminal,
             mac: place(window1, tab: 2, "openshell", pane: 2, of: 2),
             waiting: Waiting(message: "Claude needs your permission to use Bash", since: DemoData.ago(minutes: 2))),
        make("fd-a1", "Wire the share sheet", folder("aimux", worktree: "clients"), minutesAgo: 1, .busy,
             mac: place(window1, tab: 3, "aimux", pane: 1, of: 4, front: true)),
        make("fd-q1", "Chat queue polish", folder("aimux", worktree: "clients"), minutesAgo: 2, .openInTerminal,
             mac: place(window1, tab: 3, "aimux", pane: 2, of: 4)),
        make("fd-a2", "Fix the icon spacing", folder("aimux", worktree: "clients", sub: "ios"), minutesAgo: 4,
             .openInTerminal, mac: place(window1, tab: 3, "aimux", pane: 3, of: 4)),
        make("fd-a3", "Release notes", folder("aimux"), minutesAgo: 180, .openInTerminal,
             mac: place(window1, tab: 3, "aimux", pane: 4, of: 4)),
        make("fd-b1", "Draft the folders post", folder("blog"), minutesAgo: 300, .openInTerminal,
             mac: place(window1, tab: 4, "blog", pane: 1, of: 1)),
        make("fd-s1", "Try the new linter", folder("scratch"), minutesAgo: 3, .openInTerminal,
             mac: place(window1, tab: 5, "scratch", pane: 2, of: 9), working: true),
        make("fd-s2", "Rename the sample files", folder("scratch"), minutesAgo: 90, .openInTerminal,
             mac: place(window1, tab: 5, "scratch", pane: 5, of: 9)),
        // Window 2, renamed by the owner
        make("fd-v1", "Review queue PR", folder("openshell"), minutesAgo: 3, .openInTerminal,
             mac: place(window2, tab: 1, "reviews", pane: 1, of: 5), working: true),
        make("fd-v2", "Check the release branch", folder("openshell"), minutesAgo: 40, .openInTerminal,
             mac: place(window2, tab: 1, "reviews", pane: 2, of: 5)),
        // Open with no iTerm2 pane: started from the phone.
        make("fd-e1", "Draft the health check idea", folder("aimux"), minutesAgo: 1, .busy),
        // Closed
        make("fd-pin1", "Plan the allotment layout", folder("notes"), minutesAgo: 1500, .idle),
        make("fd-c1", "Chat queue design", folder("aimux", worktree: "clients"), minutesAgo: 2900, .idle),
        make("fd-fv", "Folder view in All", folder("aimux", worktree: "clients"), minutesAgo: 1400, .idle),
        make("fd-wt", "Worktrees explainer", folder("_experiments", worktree: "branches"), minutesAgo: 1450, .idle),
        make("fd-n2", "Merge the duplicate notes", folder("notes"), minutesAgo: 6, .idle),
        make("fd-t1", "Book the lake cabin", folder("trips"), minutesAgo: 700, .idle),
        make("fd-h1", "Tidy the downloads", Folder(repo: "Home", repoPath: "/home/me", label: "Home"),
             minutesAgo: 2000, .idle),
        // Only its id as the title, as Claude Code leaves some sessions: shown and prefilled as "Session c0ffee12".
        make(oldID, oldID, folder("scratch"), minutesAgo: 600, .idle)
    ]

    static let oldID = "c0ffee12-3c4d-4e5f-8a6b-7c8d9e0f1a2b"

    private static let descriptions = [
        "fd-pin1": "Lays out twelve beds and the paths between them.",
        "fd-a1": "Adds the share sheet to the chat screen and checks it on a small phone.",
        "fd-q1": "Folds the queue to one line under the composer.",
        "fd-t1": "Compares three cabins for May."
    ]
    private static let pinned: Set = ["fd-pin1", "fd-q1"]

    private static let edits = Mutex<[String: Meta]>([:])

    private static func make(_ id: String, _ title: String, _ folder: Folder, minutesAgo: Double,
                             _ state: SessionState, mac: MacPlace? = nil, working: Bool = false,
                             waiting: Waiting? = nil) -> Session {
        let project = folder.worktree.map { folder.repoPath + "-" + $0 } ?? folder.repoPath
        return Session(id: id, title: title, project: project + (folder.sub.map { "/" + $0 } ?? ""),
                       lastActive: DemoData.ago(minutes: minutesAgo), state: state, lastPrompt: "carry on",
                       description: descriptions[id], pinned: pinned.contains(id), folder: folder, mac: mac,
                       working: working, waiting: waiting)
    }

    static var sessions: [Session] {
        let edits = edits.withLock { $0 }
        return base.map { session in
            guard let meta = edits[session.id] else { return session }
            return session.taking(title: meta.title ?? session.title,
                                  description: meta.description ?? session.description, pinned: meta.pinned)
        }
    }

    static var list: SessionList { SessionList(sessions: sessions) }

    static let events: [String: [SessionEvent]] = [
        "fd-a1": DemoData.workingEvents(tool: "go test ./...", minutesAgo: 1),
        "fd-e1": DemoData.workingEvents(tool: "rg health", minutesAgo: 1)
    ]

    /// The server's conversation search: one closed session whose text mentions the query.
    static func hitsJSON(_ query: String) -> String {
        #"{"results":[{"id":"fd-fv","provider":"claude","title":"Folder view in All","# +
            #""project":"\#(aimux)-clients","lastActive":"2026-10-20T10:00:00Z","state":"idle","# +
            #""snippet":"…the [\#(query)] folds to one line under the composer…"}]}"#
    }

    static let health = Health(status: "ok", login: "demo", version: "demo", acpProtocolVersion: 1,
                               capabilities: ["sessions", "search", "new_sessions", "describe"],
                               naming: NamingStatus(today: 3, limit: 20, failing: false, reason: nil, paused: nil),
                               openOnMac: true)

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
